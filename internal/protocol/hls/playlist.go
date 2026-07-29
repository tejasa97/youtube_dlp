// Package hls parses and downloads the Phase 1 HLS pilot subset.
package hls

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrInvalidPlaylist       = errors.New("invalid HLS playlist")
	ErrUnsupportedEncryption = errors.New("unsupported HLS encryption")
	ErrLivePollLimit         = errors.New("HLS live poll limit reached")
)

// EncryptionError describes an HLS encryption mode that the native
// downloader cannot consume. FFmpegEligible is deliberately narrow: callers
// may delegate only clear-key SAMPLE-AES with identity key delivery.
type EncryptionError struct {
	Method         string
	KeyFormat      string
	MediaURL       string
	FFmpegEligible bool
}

func (err *EncryptionError) Error() string {
	if err.FFmpegEligible {
		return ErrUnsupportedEncryption.Error() + ": clear-key SAMPLE-AES requires ffmpeg"
	}
	if err.KeyFormat != "" {
		return ErrUnsupportedEncryption.Error() + ": unsupported key delivery"
	}
	return ErrUnsupportedEncryption.Error() + ": unsupported method"
}

func (err *EncryptionError) Unwrap() error { return ErrUnsupportedEncryption }

const (
	maxPlaylistBytes   = 16 << 20
	maxPlaylistEntries = 100_000
)

type Playlist struct {
	Variants []Variant
	Media    *MediaPlaylist
}

type Variant struct {
	URL        string
	Bandwidth  int64
	Codecs     string
	Resolution string
}

type MediaPlaylist struct {
	Sequence              int64
	DiscontinuitySequence int64
	TargetDuration        time.Duration
	PartTarget            time.Duration
	CanBlockReload        bool
	CanSkipUntil          time.Duration
	PreloadHint           *PreloadHint
	RenditionReports      []RenditionReport
	Segments              []Segment
	EndList               bool
}

type Segment struct {
	URL                   string
	Sequence              int64
	DiscontinuitySequence int64
	Duration              time.Duration
	RangeStart            int64
	RangeLength           int64
	Map                   *Map
	MapDeclared           bool
	MapInherited          bool
	Key                   *Key
	KeyDeclared           bool
	Discontinuity         bool
	Partial               bool
	PartIndex             int
	Advertisement         bool
}

// PreloadHint describes the next low-latency part advertised by a playlist.
// It is continuation metadata, not a downloadable fragment: a server may
// replace or withdraw the hinted object before it becomes a playlist part.
type PreloadHint struct {
	URL         string
	RangeStart  int64
	RangeLength int64
}

// RenditionReport is bounded alternate-rendition progress metadata. The
// downloader never switches rendition from it; doing so can change codecs,
// authentication, and media alignment without product-level selection.
type RenditionReport struct {
	URL      string
	LastMSN  int64
	LastPart int
}

type Map struct {
	URL         string
	RangeStart  int64
	RangeLength int64
	Key         *Key
}

type Key struct {
	Method      string
	URL         string
	IV          []byte
	Declaration int64
	snapshot    uint64
	material    []byte
}

func Parse(rawURL string, input []byte) (Playlist, error) {
	if len(input) > maxPlaylistBytes {
		return Playlist{}, fmt.Errorf("%w: playlist exceeds %d bytes", ErrInvalidPlaylist, maxPlaylistBytes)
	}
	base, err := url.Parse(rawURL)
	if err != nil {
		return Playlist{}, fmt.Errorf("%w: base URL: %v", ErrInvalidPlaylist, err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(input)))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	lineNumber := 0
	seenHeader := false
	playlist := Playlist{}
	media := &MediaPlaylist{}
	var pendingVariant map[string]string
	var pendingDuration time.Duration
	var pendingRangeLength int64
	var pendingRangeStart int64
	var nextRangeStart int64
	var currentMap *Map
	mapDeclared := false
	var currentKey *Key
	keyDeclared := false
	keyDeclaration := int64(0)
	var discontinuity bool
	advertisement := false
	partIndex := 0
	nextPartRangeStart := int64(0)
	sequence := int64(0)
	discontinuitySequence := int64(0)

	for scanner.Scan() {
		lineNumber++
		rawLine := scanner.Text()
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if !seenHeader {
			if line != "#EXTM3U" {
				return Playlist{}, fmt.Errorf("%w: line 1 must be #EXTM3U", ErrInvalidPlaylist)
			}
			seenHeader = true
			continue
		}
		if !strings.HasPrefix(line, "#") {
			resolved, err := resolveURL(base, line)
			if err != nil {
				return Playlist{}, fmt.Errorf("%w at line %d: %v", ErrInvalidPlaylist, lineNumber, err)
			}
			if pendingVariant != nil {
				if len(playlist.Variants) >= maxPlaylistEntries {
					return Playlist{}, fmt.Errorf("%w: variant count exceeds %d", ErrInvalidPlaylist, maxPlaylistEntries)
				}
				bandwidth, _ := strconv.ParseInt(pendingVariant["BANDWIDTH"], 10, 64)
				playlist.Variants = append(playlist.Variants, Variant{
					URL: resolved, Bandwidth: bandwidth, Codecs: pendingVariant["CODECS"], Resolution: pendingVariant["RESOLUTION"],
				})
				pendingVariant = nil
				continue
			}
			if len(media.Segments) >= maxPlaylistEntries {
				return Playlist{}, fmt.Errorf("%w: segment count exceeds %d", ErrInvalidPlaylist, maxPlaylistEntries)
			}
			if sequence == math.MaxInt64 {
				return Playlist{}, fmt.Errorf("%w: media sequence overflow", ErrInvalidPlaylist)
			}
			segment := Segment{
				URL: resolved, Sequence: sequence, Duration: pendingDuration,
				RangeStart: pendingRangeStart, RangeLength: pendingRangeLength,
				Map: cloneMap(currentMap), MapDeclared: mapDeclared, Key: cloneKey(currentKey), KeyDeclared: keyDeclared,
				DiscontinuitySequence: discontinuitySequence, Discontinuity: discontinuity,
				Advertisement: advertisement,
			}
			media.Segments = append(media.Segments, segment)
			sequence++
			partIndex = 0
			nextPartRangeStart = 0
			pendingDuration = 0
			if pendingRangeLength > 0 {
				nextRangeStart = pendingRangeStart + pendingRangeLength
			}
			pendingRangeLength = 0
			pendingRangeStart = 0
			discontinuity = false
			mapDeclared = false
			continue
		}

		// EXT-X-DATERANGE SCTE-35 attributes require the raw tag at byte zero.
		// Leading whitespace is a rejected pseudo-tag and does not change state.
		// Invalid directional payloads fail closed; ordinary dateranges are ignored.
		if daterangeStart, daterangeEnd, handled, daterangeErr := applyDaterangeSCTE35(rawLine); daterangeErr != nil {
			return Playlist{}, fmt.Errorf("%w at line %d: %w", ErrInvalidPlaylist, lineNumber, daterangeErr)
		} else if handled {
			if daterangeStart {
				advertisement = true
			} else if daterangeEnd {
				advertisement = false
			}
			continue
		}

		// Provider markers use the trimmed line (pinned yt-dlp grammar).
		// Cue tags require the raw line to begin with the tag at byte zero.
		// Start is intentionally tested before end so an Anvato line containing
		// both tokens starts an ad. Cue payloads after ':' are ignored.
		if isAdvertisementStart(line, rawLine) {
			advertisement = true
			continue
		} else if isAdvertisementEnd(line, rawLine) {
			advertisement = false
			continue
		}

		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			pendingVariant, err = parseAttributes(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			sequence, err = strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
			if err == nil && sequence < 0 {
				err = errors.New("media sequence must not be negative")
			}
			media.Sequence = sequence
		case strings.HasPrefix(line, "#EXT-X-DISCONTINUITY-SEQUENCE:"):
			discontinuitySequence, err = strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-DISCONTINUITY-SEQUENCE:"), 10, 64)
			if err == nil && discontinuitySequence < 0 {
				err = errors.New("discontinuity sequence must not be negative")
			}
			media.DiscontinuitySequence = discontinuitySequence
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			var seconds int64
			seconds, err = strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"), 10, 64)
			media.TargetDuration = time.Duration(seconds) * time.Second
		case strings.HasPrefix(line, "#EXT-X-PART-INF:"):
			var attributes map[string]string
			attributes, err = parseAttributes(strings.TrimPrefix(line, "#EXT-X-PART-INF:"))
			if err == nil {
				var seconds float64
				seconds, err = strconv.ParseFloat(attributes["PART-TARGET"], 64)
				if err == nil && seconds <= 0 {
					err = errors.New("part target must be positive")
				}
				media.PartTarget = time.Duration(seconds * float64(time.Second))
			}
		case strings.HasPrefix(line, "#EXT-X-SERVER-CONTROL:"):
			var attributes map[string]string
			attributes, err = parseAttributes(strings.TrimPrefix(line, "#EXT-X-SERVER-CONTROL:"))
			if err == nil {
				err = parseServerControl(media, attributes)
			}
		case strings.HasPrefix(line, "#EXT-X-PRELOAD-HINT:"):
			var attributes map[string]string
			attributes, err = parseAttributes(strings.TrimPrefix(line, "#EXT-X-PRELOAD-HINT:"))
			if err == nil {
				media.PreloadHint, err = parsePreloadHint(base, attributes)
			}
		case strings.HasPrefix(line, "#EXT-X-RENDITION-REPORT:"):
			var attributes map[string]string
			attributes, err = parseAttributes(strings.TrimPrefix(line, "#EXT-X-RENDITION-REPORT:"))
			if err == nil {
				var report RenditionReport
				report, err = parseRenditionReport(base, attributes)
				if err == nil {
					if len(media.RenditionReports) >= maxPlaylistEntries {
						err = fmt.Errorf("rendition report count exceeds %d", maxPlaylistEntries)
					} else {
						media.RenditionReports = append(media.RenditionReports, report)
					}
				}
			}
		case strings.HasPrefix(line, "#EXT-X-SKIP:"):
			var attributes map[string]string
			attributes, err = parseAttributes(strings.TrimPrefix(line, "#EXT-X-SKIP:"))
			if err == nil {
				var skipped int64
				skipped, err = strconv.ParseInt(attributes["SKIPPED-SEGMENTS"], 10, 64)
				if err == nil && (skipped < 0 || skipped > math.MaxInt64-sequence) {
					err = errors.New("skipped segment count is invalid")
				}
				if err == nil {
					sequence += skipped
				}
			}
		case strings.HasPrefix(line, "#EXT-X-PART:"):
			var attributes map[string]string
			attributes, err = parseAttributes(strings.TrimPrefix(line, "#EXT-X-PART:"))
			if err == nil {
				var part Segment
				part, nextPartRangeStart, err = parsePart(base, attributes, sequence, discontinuitySequence, partIndex, nextPartRangeStart, currentMap, mapDeclared, currentKey, keyDeclared, discontinuity, advertisement)
				if err == nil {
					if len(media.Segments) >= maxPlaylistEntries {
						err = fmt.Errorf("segment count exceeds %d", maxPlaylistEntries)
					} else {
						media.Segments = append(media.Segments, part)
						partIndex++
						discontinuity = false
						mapDeclared = false
					}
				}
			}
		case strings.HasPrefix(line, "#EXTINF:"):
			value := strings.SplitN(strings.TrimPrefix(line, "#EXTINF:"), ",", 2)[0]
			var seconds float64
			seconds, err = strconv.ParseFloat(value, 64)
			pendingDuration = time.Duration(seconds * float64(time.Second))
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			value := strings.TrimPrefix(line, "#EXT-X-BYTERANGE:")
			parts := strings.SplitN(value, "@", 2)
			pendingRangeLength, err = strconv.ParseInt(parts[0], 10, 64)
			pendingRangeStart = nextRangeStart
			if err == nil && len(parts) == 2 {
				pendingRangeStart, err = strconv.ParseInt(parts[1], 10, 64)
			}
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			var attributes map[string]string
			attributes, err = parseAttributes(strings.TrimPrefix(line, "#EXT-X-MAP:"))
			if err == nil {
				currentMap, err = parseMap(base, attributes, currentKey)
				mapDeclared = err == nil
			}
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			var attributes map[string]string
			attributes, err = parseAttributes(strings.TrimPrefix(line, "#EXT-X-KEY:"))
			if err == nil {
				currentKey, err = parseKey(base, attributes)
				keyDeclared = true
				if currentKey != nil {
					if keyDeclaration == math.MaxInt64 {
						err = errors.New("key declaration overflow")
					} else {
						keyDeclaration++
						currentKey.Declaration = keyDeclaration
					}
				}
			}
		case strings.HasPrefix(line, "#EXT-X-SESSION-KEY:"):
			var attributes map[string]string
			attributes, err = parseAttributes(strings.TrimPrefix(line, "#EXT-X-SESSION-KEY:"))
			if err == nil {
				err = validateSessionKey(base, attributes)
			}
		case strings.HasPrefix(line, "#EXT-X-FAXS-CM:"):
			err = &EncryptionError{Method: "ADOBE-FAXS"}
		case line == "#EXT-X-DISCONTINUITY":
			if discontinuitySequence == math.MaxInt64 {
				err = errors.New("discontinuity sequence overflow")
			} else {
				discontinuitySequence++
			}
			discontinuity = true
		case line == "#EXT-X-ENDLIST":
			media.EndList = true
		}
		if err != nil {
			return Playlist{}, fmt.Errorf("%w at line %d: %w", ErrInvalidPlaylist, lineNumber, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return Playlist{}, fmt.Errorf("%w: scan: %v", ErrInvalidPlaylist, err)
	}
	if !seenHeader {
		return Playlist{}, ErrInvalidPlaylist
	}
	if pendingVariant != nil {
		return Playlist{}, fmt.Errorf("%w: stream variant has no URI", ErrInvalidPlaylist)
	}
	if len(playlist.Variants) == 0 {
		playlist.Media = media
	}
	return playlist, nil
}

func isAdvertisementStart(trimmed, raw string) bool {
	return isProviderAdvertisementStart(trimmed) || isCueAdvertisementStart(raw)
}

func isAdvertisementEnd(trimmed, raw string) bool {
	return isProviderAdvertisementEnd(trimmed) || isCueAdvertisementEnd(raw)
}

func isProviderAdvertisementStart(line string) bool {
	return (strings.HasPrefix(line, "#ANVATO-SEGMENT-INFO") && strings.Contains(line, "type=ad")) ||
		(strings.HasPrefix(line, "#UPLYNK-SEGMENT") && strings.HasSuffix(line, ",ad"))
}

func isProviderAdvertisementEnd(line string) bool {
	return (strings.HasPrefix(line, "#ANVATO-SEGMENT-INFO") && strings.Contains(line, "type=master")) ||
		(strings.HasPrefix(line, "#UPLYNK-SEGMENT") && strings.HasSuffix(line, ",segment"))
}

// cueRawLine returns the cue-matching form of a raw playlist line. Cue tags
// must begin at byte zero: a leading space or tab is a rejected pseudo-tag.
// Trailing ASCII spaces and tabs are ignored so a packager-trailing-padded
// cue line matches the same exact tag/colon grammar as the bare tag.
func cueRawLine(raw string) (string, bool) {
	if raw == "" || raw[0] == ' ' || raw[0] == '\t' {
		return "", false
	}
	return strings.TrimRight(raw, " \t"), true
}

// isCueTagName reports an exact uppercase HLS tag or the same tag with a
// conventional colon payload. Lookalikes that merely share a prefix without
// an immediate ':' (for example #EXT-X-CUE-OUT-CONT vs #EXT-X-CUE-OUT) are
// rejected by requiring equality or name+":".
func isCueTagName(line, name string) bool {
	return line == name || strings.HasPrefix(line, name+":")
}

func isCueAdvertisementStart(raw string) bool {
	line, ok := cueRawLine(raw)
	if !ok {
		return false
	}
	// OUT-CONT is checked explicitly; OUT uses name+":"/equality so it cannot
	// swallow the longer OUT-CONT tag.
	return isCueTagName(line, "#EXT-X-CUE-OUT-CONT") || isCueTagName(line, "#EXT-X-CUE-OUT")
}

func isCueAdvertisementEnd(raw string) bool {
	line, ok := cueRawLine(raw)
	if !ok {
		return false
	}
	return isCueTagName(line, "#EXT-X-CUE-IN")
}

func parseAttributes(input string) (map[string]string, error) {
	result := make(map[string]string)
	for index := 0; index < len(input); {
		start := index
		for index < len(input) && input[index] != '=' {
			index++
		}
		if index == len(input) {
			return nil, fmt.Errorf("attribute %q has no value", input[start:])
		}
		key := strings.TrimSpace(input[start:index])
		index++
		var value string
		if index < len(input) && input[index] == '"' {
			index++
			start = index
			for index < len(input) && input[index] != '"' {
				index++
			}
			if index == len(input) {
				return nil, errors.New("unterminated quoted attribute")
			}
			value = input[start:index]
			index++
		} else {
			start = index
			for index < len(input) && input[index] != ',' {
				index++
			}
			value = strings.TrimSpace(input[start:index])
		}
		if key == "" {
			return nil, errors.New("empty attribute name")
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate attribute %q", key)
		}
		result[key] = value
		if index < len(input) {
			if input[index] != ',' {
				return nil, fmt.Errorf("unexpected attribute character %q", input[index])
			}
			index++
		}
	}
	return result, nil
}

func parseMap(base *url.URL, attributes map[string]string, currentKey *Key) (*Map, error) {
	rawURI := attributes["URI"]
	if rawURI == "" {
		return nil, errors.New("map URI is missing")
	}
	resolved, err := resolveURL(base, rawURI)
	if err != nil {
		return nil, err
	}
	result := &Map{URL: resolved, Key: cloneKey(currentKey)}
	if result.Key != nil && len(result.Key.IV) == 0 {
		return nil, errors.New("AES-128 map encryption requires an explicit IV")
	}
	if rawRange := attributes["BYTERANGE"]; rawRange != "" {
		parts := strings.SplitN(rawRange, "@", 2)
		result.RangeLength, err = strconv.ParseInt(parts[0], 10, 64)
		if err == nil && len(parts) == 2 {
			result.RangeStart, err = strconv.ParseInt(parts[1], 10, 64)
		}
	}
	return result, err
}

func parsePart(base *url.URL, attributes map[string]string, sequence, discontinuitySequence int64, partIndex int, inferredStart int64, currentMap *Map, mapDeclared bool, currentKey *Key, keyDeclared, discontinuity, advertisement bool) (Segment, int64, error) {
	rawURI := attributes["URI"]
	resolved, err := resolveURL(base, rawURI)
	if err != nil {
		return Segment{}, inferredStart, err
	}
	seconds, err := strconv.ParseFloat(attributes["DURATION"], 64)
	if err != nil || seconds <= 0 {
		return Segment{}, inferredStart, errors.New("part duration must be positive")
	}
	part := Segment{
		URL: resolved, Sequence: sequence, Duration: time.Duration(seconds * float64(time.Second)),
		Map: cloneMap(currentMap), MapDeclared: mapDeclared, Key: cloneKey(currentKey), KeyDeclared: keyDeclared,
		DiscontinuitySequence: discontinuitySequence, Discontinuity: discontinuity,
		Partial: true, PartIndex: partIndex, Advertisement: advertisement,
	}
	nextStart := int64(0)
	if rawRange := attributes["BYTERANGE"]; rawRange != "" {
		pieces := strings.SplitN(rawRange, "@", 2)
		part.RangeLength, err = strconv.ParseInt(pieces[0], 10, 64)
		if err != nil || part.RangeLength <= 0 {
			return Segment{}, inferredStart, errors.New("part byte range must be positive")
		}
		part.RangeStart = inferredStart
		if len(pieces) == 2 {
			part.RangeStart, err = strconv.ParseInt(pieces[1], 10, 64)
			if err != nil || part.RangeStart < 0 {
				return Segment{}, inferredStart, errors.New("part byte range start is invalid")
			}
		}
		nextStart = part.RangeStart + part.RangeLength
	}
	return part, nextStart, nil
}

func parseServerControl(media *MediaPlaylist, attributes map[string]string) error {
	if value := attributes["CAN-BLOCK-RELOAD"]; value != "" {
		switch value {
		case "YES":
			media.CanBlockReload = true
		case "NO":
			media.CanBlockReload = false
		default:
			return errors.New("CAN-BLOCK-RELOAD must be YES or NO")
		}
	}
	if value := attributes["CAN-SKIP-UNTIL"]; value != "" {
		seconds, err := strconv.ParseFloat(value, 64)
		if err != nil || !isFinitePositive(seconds) {
			return errors.New("CAN-SKIP-UNTIL must be positive")
		}
		media.CanSkipUntil = time.Duration(seconds * float64(time.Second))
	}
	return nil
}

func parsePreloadHint(base *url.URL, attributes map[string]string) (*PreloadHint, error) {
	// A preload hint may describe a map or part. Maps cannot be safely emitted
	// before a declared media segment, and other types are not download input.
	if attributes["TYPE"] != "PART" {
		return nil, nil
	}
	if attributes["URI"] == "" {
		return nil, errors.New("preload hint PART URI is missing")
	}
	resolved, err := resolveURL(base, attributes["URI"])
	if err != nil {
		return nil, err
	}
	hint := &PreloadHint{URL: resolved}
	if start := attributes["BYTERANGE-START"]; start != "" {
		hint.RangeStart, err = strconv.ParseInt(start, 10, 64)
		if err != nil || hint.RangeStart < 0 {
			return nil, errors.New("preload hint byte range start is invalid")
		}
	}
	if length := attributes["BYTERANGE-LENGTH"]; length != "" {
		hint.RangeLength, err = strconv.ParseInt(length, 10, 64)
		if err != nil || hint.RangeLength <= 0 {
			return nil, errors.New("preload hint byte range length is invalid")
		}
	}
	return hint, nil
}

func parseRenditionReport(base *url.URL, attributes map[string]string) (RenditionReport, error) {
	var result RenditionReport
	var err error
	result.LastPart = -1
	rawURI := attributes["URI"]
	if rawURI == "" {
		return RenditionReport{}, errors.New("rendition report URI is missing")
	}
	result.URL, err = resolveURL(base, rawURI)
	if err != nil {
		return RenditionReport{}, err
	}
	if rawMSN := attributes["LAST-MSN"]; rawMSN != "" {
		result.LastMSN, err = strconv.ParseInt(rawMSN, 10, 64)
		if err != nil || result.LastMSN < 0 {
			return RenditionReport{}, errors.New("rendition report LAST-MSN is invalid")
		}
	}
	if rawPart := attributes["LAST-PART"]; rawPart != "" {
		result.LastPart, err = strconv.Atoi(rawPart)
		if err != nil || result.LastPart < 0 {
			return RenditionReport{}, errors.New("rendition report LAST-PART is invalid")
		}
	}
	return result, nil
}

func isFinitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func parseKey(base *url.URL, attributes map[string]string) (*Key, error) {
	method := attributes["METHOD"]
	if method == "NONE" {
		return nil, nil
	}
	keyFormat := attributes["KEYFORMAT"]
	if keyURI, _ := url.Parse(attributes["URI"]); keyURI != nil && strings.EqualFold(keyURI.Scheme, "skd") {
		return nil, &EncryptionError{Method: method, KeyFormat: keyFormat}
	}
	if method == "SAMPLE-AES" {
		if attributes["URI"] == "" {
			return nil, errors.New("SAMPLE-AES key URI is missing")
		}
		eligible := (keyFormat == "" || keyFormat == "identity") && attributes["URI"] != ""
		if eligible {
			parsed, err := url.Parse(attributes["URI"])
			if err != nil {
				eligible = false
			} else {
				parsed = base.ResolveReference(parsed)
				if parsed.User != nil || parsed.Hostname() == "" ||
					(parsed.Scheme != "http" && parsed.Scheme != "https") {
					eligible = false
				}
			}
		}
		return nil, &EncryptionError{
			Method: method, KeyFormat: keyFormat, FFmpegEligible: eligible,
		}
	}
	if method != "AES-128" {
		return nil, &EncryptionError{Method: method, KeyFormat: keyFormat}
	}
	if keyFormat != "" && keyFormat != "identity" {
		return nil, &EncryptionError{Method: method, KeyFormat: keyFormat}
	}
	resolved, err := resolveURL(base, attributes["URI"])
	if err != nil {
		return nil, err
	}
	key := &Key{Method: method, URL: resolved}
	if rawIV := attributes["IV"]; rawIV != "" {
		rawIV = strings.TrimPrefix(strings.TrimPrefix(rawIV, "0x"), "0X")
		if len(rawIV) > 32 {
			return nil, errors.New("AES-128 IV exceeds 128 bits")
		}
		rawIV = strings.Repeat("0", 32-len(rawIV)) + rawIV
		key.IV, err = hex.DecodeString(rawIV)
	}
	return key, err
}

func validateSessionKey(base *url.URL, attributes map[string]string) error {
	method := attributes["METHOD"]
	keyFormat := attributes["KEYFORMAT"]
	if method == "NONE" {
		return errors.New("session key method NONE is invalid")
	}
	rawURI := attributes["URI"]
	parsed, err := url.Parse(rawURI)
	if err != nil {
		return err
	}
	if strings.EqualFold(parsed.Scheme, "skd") ||
		(keyFormat != "" && keyFormat != "identity") {
		return &EncryptionError{Method: method, KeyFormat: keyFormat}
	}
	resolved, err := resolveURL(base, rawURI)
	if err != nil {
		return &EncryptionError{Method: method, KeyFormat: keyFormat}
	}
	keyURI, err := url.Parse(resolved)
	if err != nil {
		return err
	}
	if keyURI.User != nil || keyURI.Hostname() == "" ||
		(keyURI.Scheme != "http" && keyURI.Scheme != "https") {
		return &EncryptionError{Method: method, KeyFormat: keyFormat}
	}
	switch method {
	case "AES-128", "SAMPLE-AES":
		return nil
	default:
		return &EncryptionError{Method: method, KeyFormat: keyFormat}
	}
}

func resolveURL(base *url.URL, raw string) (string, error) {
	if raw == "" {
		return "", errors.New("URI is missing")
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	resolved := base.ResolveReference(reference)
	if resolved.User != nil || resolved.Hostname() == "" ||
		(resolved.Scheme != "http" && resolved.Scheme != "https") {
		return "", errors.New("URI must resolve to credential-free HTTP(S)")
	}
	return resolved.String(), nil
}

func cloneMap(input *Map) *Map {
	if input == nil {
		return nil
	}
	copy := *input
	copy.Key = cloneKey(input.Key)
	return &copy
}

func cloneKey(input *Key) *Key {
	if input == nil {
		return nil
	}
	copy := *input
	copy.IV = append([]byte(nil), input.IV...)
	copy.material = append([]byte(nil), input.material...)
	return &copy
}
