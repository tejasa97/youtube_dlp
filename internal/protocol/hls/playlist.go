// Package hls parses and downloads the Phase 1 HLS pilot subset.
package hls

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
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
	Sequence       int64
	TargetDuration time.Duration
	Segments       []Segment
	EndList        bool
}

type Segment struct {
	URL           string
	Sequence      int64
	Duration      time.Duration
	RangeStart    int64
	RangeLength   int64
	Map           *Map
	Key           *Key
	Discontinuity bool
}

type Map struct {
	URL         string
	RangeStart  int64
	RangeLength int64
}

type Key struct {
	Method string
	URL    string
	IV     []byte
}

func Parse(rawURL string, input []byte) (Playlist, error) {
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
	var currentKey *Key
	var discontinuity bool
	sequence := int64(0)

	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
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
				bandwidth, _ := strconv.ParseInt(pendingVariant["BANDWIDTH"], 10, 64)
				playlist.Variants = append(playlist.Variants, Variant{
					URL: resolved, Bandwidth: bandwidth, Codecs: pendingVariant["CODECS"], Resolution: pendingVariant["RESOLUTION"],
				})
				pendingVariant = nil
				continue
			}
			segment := Segment{
				URL: resolved, Sequence: sequence, Duration: pendingDuration,
				RangeStart: pendingRangeStart, RangeLength: pendingRangeLength,
				Map: cloneMap(currentMap), Key: cloneKey(currentKey), Discontinuity: discontinuity,
			}
			media.Segments = append(media.Segments, segment)
			sequence++
			pendingDuration = 0
			if pendingRangeLength > 0 {
				nextRangeStart = pendingRangeStart + pendingRangeLength
			}
			pendingRangeLength = 0
			pendingRangeStart = 0
			discontinuity = false
			continue
		}

		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			pendingVariant, err = parseAttributes(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			sequence, err = strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
			media.Sequence = sequence
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			var seconds int64
			seconds, err = strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"), 10, 64)
			media.TargetDuration = time.Duration(seconds) * time.Second
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
				currentMap, err = parseMap(base, attributes)
			}
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			var attributes map[string]string
			attributes, err = parseAttributes(strings.TrimPrefix(line, "#EXT-X-KEY:"))
			if err == nil {
				currentKey, err = parseKey(base, attributes)
			}
		case line == "#EXT-X-DISCONTINUITY":
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

func parseMap(base *url.URL, attributes map[string]string) (*Map, error) {
	rawURI := attributes["URI"]
	if rawURI == "" {
		return nil, errors.New("map URI is missing")
	}
	resolved, err := resolveURL(base, rawURI)
	if err != nil {
		return nil, err
	}
	result := &Map{URL: resolved}
	if rawRange := attributes["BYTERANGE"]; rawRange != "" {
		parts := strings.SplitN(rawRange, "@", 2)
		result.RangeLength, err = strconv.ParseInt(parts[0], 10, 64)
		if err == nil && len(parts) == 2 {
			result.RangeStart, err = strconv.ParseInt(parts[1], 10, 64)
		}
	}
	return result, err
}

func parseKey(base *url.URL, attributes map[string]string) (*Key, error) {
	method := attributes["METHOD"]
	if method == "NONE" {
		return nil, nil
	}
	if method != "AES-128" {
		return nil, fmt.Errorf("%w: method %q", ErrUnsupportedEncryption, method)
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

func resolveURL(base *url.URL, raw string) (string, error) {
	if raw == "" {
		return "", errors.New("URI is missing")
	}
	reference, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(reference).String(), nil
}

func cloneMap(input *Map) *Map {
	if input == nil {
		return nil
	}
	copy := *input
	return &copy
}

func cloneKey(input *Key) *Key {
	if input == nil {
		return nil
	}
	copy := *input
	copy.IV = append([]byte(nil), input.IV...)
	return &copy
}
