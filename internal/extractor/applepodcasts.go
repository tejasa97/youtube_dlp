package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	applePodcastsMaxURLBytes    = 4096
	applePodcastsMaxPathSegment = 256
	applePodcastsMaxHeaderItems = 64
	applePodcastsMaxTextBytes   = 64 << 10
	applePodcastsMaxTitleBytes  = 4096
	// applePodcastsMaxJSONDepth is a conservative repository-local nesting
	// budget applied before encoding/json decode of root and model payloads.
	applePodcastsMaxJSONDepth = 32
	applePodcastsScriptID     = "serialized-server-data"
)

var (
	applePodcastsEpisodeIDPattern = regexp.MustCompile(`^[0-9]{1,32}$`)
	applePodcastsOGImage          = regexp.MustCompile(`(?is)<meta\b[^>]*\bproperty\s*=\s*["']og:image["'][^>]*\bcontent\s*=\s*["']([^"']+)["']`)
	applePodcastsOGImageAlt       = regexp.MustCompile(`(?is)<meta\b[^>]*\bcontent\s*=\s*["']([^"']+)["'][^>]*\bproperty\s*=\s*["']og:image["']`)
	applePodcastsHTMLTag          = regexp.MustCompile(`(?s)<[^>]*>`)
)

type applePodcastsTarget struct {
	id         string
	webpageURL string
}

// ApplePodcasts extracts public Apple Podcasts episode pages using the pinned
// serialized-server-data EpisodeLockup share model. Show-only URLs without an
// episode query are unsupported.
type ApplePodcasts struct{}

func NewApplePodcasts() ApplePodcasts { return ApplePodcasts{} }
func (ApplePodcasts) Name() string    { return "applepodcasts" }

func (ApplePodcasts) Suitable(parsed *url.URL) bool {
	_, ok := parseApplePodcastsURL(parsed)
	return ok
}

func (ApplePodcasts) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseApplePodcastsURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	page, _, err := request.Transport.ReadPage(ctx, target.webpageURL)
	if err != nil {
		return Extraction{}, categorizeApplePodcastsError(err)
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	return parseApplePodcastsPage(page, target)
}

// parseApplePodcastsURL is the shared Suitable/Extract parser for the pinned
// podcasts.apple.com episode URL shapes.
func parseApplePodcastsURL(parsed *url.URL) (applePodcastsTarget, bool) {
	if parsed == nil {
		return applePodcastsTarget{}, false
	}
	if len(parsed.String()) == 0 || len(parsed.String()) > applePodcastsMaxURLBytes {
		return applePodcastsTarget{}, false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return applePodcastsTarget{}, false
	}
	if parsed.User != nil || parsed.Port() != "" {
		return applePodcastsTarget{}, false
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return applePodcastsTarget{}, false
	}
	if strings.ToLower(parsed.Hostname()) != "podcasts.apple.com" {
		return applePodcastsTarget{}, false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.ContainsAny(escaped, "\\\x00") ||
		strings.Contains(escaped, "%00") ||
		strings.Contains(escaped, "%2f") ||
		strings.Contains(escaped, "%5c") {
		return applePodcastsTarget{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return applePodcastsTarget{}, false
	}
	index := 0
	if parts[0] != "podcast" {
		if !applePodcastsValidPathSegment(parts[0]) {
			return applePodcastsTarget{}, false
		}
		index = 1
	}
	if index >= len(parts) || parts[index] != "podcast" {
		return applePodcastsTarget{}, false
	}
	index++
	segments := parts[index:]
	if len(segments) < 1 || len(segments) > 2 {
		return applePodcastsTarget{}, false
	}
	for _, segment := range segments {
		if !applePodcastsValidPathSegment(segment) {
			return applePodcastsTarget{}, false
		}
	}
	episodeID, ok := applePodcastsEpisodeQueryID(parsed.RawQuery)
	if !ok {
		return applePodcastsTarget{}, false
	}
	// Pinned reference behavior: always fetch the https canonical episode URL,
	// even when Suitable accepted an http input URL.
	canonical := "https://podcasts.apple.com/" + strings.Join(parts, "/") + "?i=" + episodeID
	return applePodcastsTarget{id: episodeID, webpageURL: canonical}, true
}

func applePodcastsValidPathSegment(segment string) bool {
	if segment == "" || len(segment) > applePodcastsMaxPathSegment || !utf8.ValidString(segment) {
		return false
	}
	for _, r := range segment {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func applePodcastsEpisodeQueryID(rawQuery string) (string, bool) {
	if rawQuery == "" || len(rawQuery) > applePodcastsMaxURLBytes {
		return "", false
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", false
	}
	ids, present := values["i"]
	if !present || len(ids) == 0 {
		return "", false
	}
	if len(ids) > 1 {
		for _, candidate := range ids[1:] {
			if candidate != ids[0] {
				return "", false
			}
		}
		// Duplicate identical values still count as duplicate per fail-closed policy.
		return "", false
	}
	id := ids[0]
	if !applePodcastsEpisodeIDPattern.MatchString(id) {
		return "", false
	}
	return id, true
}

func parseApplePodcastsPage(page []byte, target applePodcastsTarget) (Extraction, error) {
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, ErrJSONResponseTooLarge
	}
	script, err := findApplePodcastsSerializedData(page)
	if err != nil {
		return Extraction{}, err
	}
	model, err := decodeApplePodcastsEpisodeModel(script)
	if err != nil {
		return Extraction{}, err
	}
	title, err := applePodcastsRequiredTitle(model.Title)
	if err != nil {
		return Extraction{}, err
	}
	streamURL, ok := cleanApplePodcastsURL(model.PlayAction.EpisodeOffer.StreamURL)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: missing Apple Podcasts stream URL", ErrInvalidMetadata)
	}
	ext := applePodcastsExtension(streamURL)
	protocol := applePodcastsStreamProtocol(streamURL)
	format := value.NewObject(
		value.Field{Key: "format_id", Value: value.String(ext)},
		value.Field{Key: "url", Value: value.String(streamURL)},
		value.Field{Key: "ext", Value: value.String(ext)},
		value.Field{Key: "protocol", Value: value.String(protocol)},
		value.Field{Key: "vcodec", Value: value.String("none")},
	)
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "episode", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.webpageURL)},
		value.Field{Key: "ext", Value: value.String(ext)},
		value.Field{Key: "vcodec", Value: value.String("none")},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))},
	)
	if description := cleanApplePodcastsHTML(model.Summary); description != "" {
		info.Set("description", value.String(description))
	}
	if timestamp, ok := applePodcastsTimestamp(model.ReleaseDate); ok {
		info.Set("timestamp", value.Int(timestamp))
	}
	if duration, ok := applePodcastsPositiveNumber(model.Duration); ok {
		info.Set("duration", value.Int(duration))
	}
	if episodeNumber, ok := applePodcastsPositiveNumber(model.EpisodeNumber); ok {
		info.Set("episode_number", value.Int(episodeNumber))
	}
	if series, ok := applePodcastsOptionalTitle(model.ShowTitle); ok {
		info.Set("series", value.String(series))
	}
	if thumbnail, ok := applePodcastsOGThumbnail(page); ok {
		info.Set("thumbnail", value.String(thumbnail))
	}
	return Media(value.NewInfo(info)), nil
}

type applePodcastsEpisodeModel struct {
	Title         string          `json:"title"`
	Summary       string          `json:"summary"`
	ReleaseDate   string          `json:"releaseDate"`
	Duration      json.RawMessage `json:"duration"`
	EpisodeNumber json.RawMessage `json:"episodeNumber"`
	ShowTitle     string          `json:"showTitle"`
	PlayAction    struct {
		EpisodeOffer struct {
			StreamURL string `json:"streamUrl"`
		} `json:"episodeOffer"`
	} `json:"playAction"`
}

type applePodcastsHeaderItem struct {
	Kind      string          `json:"$kind"`
	ModelType string          `json:"modelType"`
	Model     json.RawMessage `json:"model"`
}

type applePodcastsPageData struct {
	HeaderButtonItems []applePodcastsHeaderItem `json:"headerButtonItems"`
}

type applePodcastsServerPage struct {
	Data applePodcastsPageData `json:"data"`
}

type applePodcastsServerRoot struct {
	Data []applePodcastsServerPage `json:"data"`
}

func decodeApplePodcastsEpisodeModel(script []byte) (applePodcastsEpisodeModel, error) {
	if int64(len(script)) > maxExtractorJSONBytes {
		return applePodcastsEpisodeModel{}, ErrJSONResponseTooLarge
	}
	if !utf8.Valid(script) {
		return applePodcastsEpisodeModel{}, fmt.Errorf("%w: Apple Podcasts serialized data is not UTF-8", ErrInvalidMetadata)
	}
	pageData, err := decodeApplePodcastsPageData(script)
	if err != nil {
		return applePodcastsEpisodeModel{}, err
	}
	if len(pageData.HeaderButtonItems) > applePodcastsMaxHeaderItems {
		return applePodcastsEpisodeModel{}, fmt.Errorf("%w: Apple Podcasts header items overflow", ErrInvalidMetadata)
	}
	for _, item := range pageData.HeaderButtonItems {
		if item.Kind != "share" || item.ModelType != "EpisodeLockup" {
			continue
		}
		if len(item.Model) == 0 || item.Model[0] != '{' {
			return applePodcastsEpisodeModel{}, fmt.Errorf("%w: Apple Podcasts episode model missing", ErrInvalidMetadata)
		}
		if int64(len(item.Model)) > maxExtractorJSONBytes {
			return applePodcastsEpisodeModel{}, ErrJSONResponseTooLarge
		}
		if err := applePodcastsValidateJSONNesting(item.Model); err != nil {
			return applePodcastsEpisodeModel{}, err
		}
		var model applePodcastsEpisodeModel
		decoder := json.NewDecoder(bytes.NewReader(item.Model))
		decoder.UseNumber()
		if err := decoder.Decode(&model); err != nil {
			return applePodcastsEpisodeModel{}, fmt.Errorf("%w: invalid Apple Podcasts episode model", ErrInvalidMetadata)
		}
		if err := ensureJSONEOF(decoder); err != nil {
			return applePodcastsEpisodeModel{}, fmt.Errorf("%w: trailing Apple Podcasts episode model", ErrInvalidMetadata)
		}
		return model, nil
	}
	return applePodcastsEpisodeModel{}, fmt.Errorf("%w: missing Apple Podcasts EpisodeLockup share model", ErrInvalidMetadata)
}

func decodeApplePodcastsPageData(script []byte) (applePodcastsPageData, error) {
	trimmed := bytes.TrimSpace(script)
	if len(trimmed) == 0 {
		return applePodcastsPageData{}, fmt.Errorf("%w: empty Apple Podcasts serialized data", ErrInvalidMetadata)
	}
	switch trimmed[0] {
	case '{':
		var root applePodcastsServerRoot
		if err := applePodcastsDecodeJSON(trimmed, &root); err != nil {
			return applePodcastsPageData{}, err
		}
		if len(root.Data) == 0 {
			return applePodcastsPageData{}, fmt.Errorf("%w: missing Apple Podcasts server data page", ErrInvalidMetadata)
		}
		return root.Data[0].Data, nil
	case '[':
		var pages []applePodcastsServerPage
		if err := applePodcastsDecodeJSON(trimmed, &pages); err != nil {
			return applePodcastsPageData{}, err
		}
		if len(pages) == 0 {
			return applePodcastsPageData{}, fmt.Errorf("%w: missing Apple Podcasts server data page", ErrInvalidMetadata)
		}
		return pages[0].Data, nil
	default:
		return applePodcastsPageData{}, fmt.Errorf("%w: invalid Apple Podcasts serialized data", ErrInvalidMetadata)
	}
}

func applePodcastsDecodeJSON(raw []byte, target any) error {
	if err := applePodcastsValidateJSONNesting(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid Apple Podcasts serialized JSON", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: trailing Apple Podcasts serialized JSON", ErrInvalidMetadata)
	}
	return nil
}

// applePodcastsValidateJSONNesting enforces an explicit nesting-depth budget
// with string/escape awareness before encoding/json decoding.
func applePodcastsValidateJSONNesting(raw []byte) error {
	depth := 0
	inString := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > applePodcastsMaxJSONDepth {
				return fmt.Errorf("%w: Apple Podcasts JSON nesting exceeds limit", ErrInvalidMetadata)
			}
		case '}', ']':
			if depth > 0 {
				depth--
			}
		}
	}
	return nil
}

func findApplePodcastsSerializedData(page []byte) ([]byte, error) {
	lower := bytes.ToLower(page)
	searchFrom := 0
	for searchFrom < len(page) {
		startRel := bytes.Index(lower[searchFrom:], []byte("<script"))
		if startRel < 0 {
			break
		}
		start := searchFrom + startRel
		nameEnd := start + len("<script")
		if !applePodcastsScriptTagNameBoundary(lower, nameEnd) {
			searchFrom = start + 1
			continue
		}
		tagEndRel := bytes.IndexByte(page[start:], '>')
		if tagEndRel < 0 {
			break
		}
		tagEnd := start + tagEndRel
		openTag := page[start : tagEnd+1]
		if !applePodcastsScriptHasID(openTag, applePodcastsScriptID) {
			searchFrom = tagEnd + 1
			continue
		}
		bodyStart := tagEnd + 1
		for {
			closeRel := bytes.Index(lower[bodyStart:], []byte("</script"))
			if closeRel < 0 {
				return nil, fmt.Errorf("%w: unclosed Apple Podcasts serialized-server-data script", ErrInvalidMetadata)
			}
			closeStart := bodyStart + closeRel
			closeNameEnd := closeStart + len("</script")
			if !applePodcastsScriptTagNameBoundary(lower, closeNameEnd) {
				bodyStart = closeStart + 1
				continue
			}
			body := bytes.TrimSpace(page[tagEnd+1 : closeStart])
			if int64(len(body)) > maxExtractorJSONBytes {
				return nil, ErrJSONResponseTooLarge
			}
			return body, nil
		}
	}
	return nil, fmt.Errorf("%w: missing Apple Podcasts serialized-server-data", ErrInvalidMetadata)
}

// applePodcastsScriptTagNameBoundary requires a valid HTML tag-name terminator
// immediately after "script" so prefixes like scripture/scriptx do not match.
func applePodcastsScriptTagNameBoundary(lower []byte, nameEnd int) bool {
	if nameEnd > len(lower) {
		return false
	}
	if nameEnd == len(lower) {
		return true
	}
	c := lower[nameEnd]
	if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
		return false
	}
	return true
}

func applePodcastsScriptHasID(openTag []byte, want string) bool {
	lower := bytes.ToLower(openTag)
	for i := 0; i+2 < len(lower); i++ {
		if lower[i] != 'i' || lower[i+1] != 'd' {
			continue
		}
		if i > 0 {
			prev := lower[i-1]
			if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') || prev == '-' || prev == '_' {
				continue
			}
		}
		j := i + 2
		for j < len(lower) && (lower[j] == ' ' || lower[j] == '\t' || lower[j] == '\n' || lower[j] == '\r') {
			j++
		}
		if j >= len(lower) || lower[j] != '=' {
			continue
		}
		j++
		for j < len(lower) && (lower[j] == ' ' || lower[j] == '\t' || lower[j] == '\n' || lower[j] == '\r') {
			j++
		}
		if j >= len(lower) {
			continue
		}
		quote := lower[j]
		var value []byte
		switch quote {
		case '"', '\'':
			j++
			end := bytes.IndexByte(lower[j:], quote)
			if end < 0 {
				return false
			}
			value = lower[j : j+end]
		default:
			end := j
			for end < len(lower) && lower[end] != '>' && lower[end] != ' ' && lower[end] != '\t' &&
				lower[end] != '\n' && lower[end] != '\r' {
				end++
			}
			value = lower[j:end]
		}
		if string(value) == want {
			return true
		}
	}
	return false
}

func cleanApplePodcastsURL(raw string) (string, bool) {
	return cleanPodcastMediaURL(raw, applePodcastsMaxURLBytes)
}

func applePodcastsOGThumbnail(page []byte) (string, bool) {
	match := applePodcastsOGImage.FindSubmatch(page)
	if len(match) != 2 {
		match = applePodcastsOGImageAlt.FindSubmatch(page)
		if len(match) != 2 {
			return "", false
		}
	}
	raw := html.UnescapeString(string(match[1]))
	if len(raw) > applePodcastsMaxURLBytes {
		return "", false
	}
	cleaned, ok := cleanApplePodcastsURL(raw)
	if !ok {
		return "", false
	}
	return cleaned, true
}

func cleanApplePodcastsHTML(input string) string {
	if input == "" {
		return ""
	}
	if len(input) > applePodcastsMaxTextBytes {
		input = input[:applePodcastsMaxTextBytes]
	}
	stripped := applePodcastsHTMLTag.ReplaceAllString(input, " ")
	return strings.Join(strings.Fields(html.UnescapeString(stripped)), " ")
}

func applePodcastsRequiredTitle(input string) (string, error) {
	title := strings.TrimSpace(input)
	if title == "" {
		return "", fmt.Errorf("%w: missing Apple Podcasts title", ErrInvalidMetadata)
	}
	if !utf8.ValidString(title) || len(title) > applePodcastsMaxTitleBytes {
		// Reject rather than silently byte-truncate required titles.
		return "", fmt.Errorf("%w: invalid Apple Podcasts title", ErrInvalidMetadata)
	}
	return title, nil
}

func applePodcastsOptionalTitle(input string) (string, bool) {
	title := strings.TrimSpace(input)
	if title == "" || !utf8.ValidString(title) || len(title) > applePodcastsMaxTitleBytes {
		// Omit oversized optional titles instead of truncating/altering them.
		return "", false
	}
	return title, true
}

func applePodcastsStreamProtocol(streamURL string) string {
	parsed, err := url.Parse(streamURL)
	if err != nil {
		return "https"
	}
	switch scheme := strings.ToLower(parsed.Scheme); scheme {
	case "http", "https":
		return scheme
	default:
		return "https"
	}
}

func applePodcastsTimestamp(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 64 {
		return 0, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			unix := parsed.Unix()
			return unix, unix > 0
		}
	}
	return 0, false
}

func applePodcastsPositiveNumber(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, false
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		if asInt, err := number.Int64(); err == nil {
			return asInt, asInt > 0
		}
		if asFloat, err := number.Float64(); err == nil && asFloat > 0 && asFloat <= float64(1<<53) {
			return int64(asFloat), true
		}
		return 0, false
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err != nil {
		return 0, false
	}
	asString = strings.TrimSpace(asString)
	if asString == "" || len(asString) > 32 {
		return 0, false
	}
	for _, r := range asString {
		if !unicode.IsDigit(r) && r != '.' {
			return 0, false
		}
	}
	if strings.Contains(asString, ".") {
		asFloat, err := strconv.ParseFloat(asString, 64)
		if err != nil || asFloat <= 0 || asFloat > float64(1<<53) {
			return 0, false
		}
		return int64(asFloat), true
	}
	asInt, err := strconv.ParseInt(asString, 10, 64)
	return asInt, err == nil && asInt > 0
}

func applePodcastsExtension(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "m4a"
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(parsed.Path), "."))
	switch ext {
	case "m4a", "mp3", "aac", "mp4", "wav", "ogg", "opus", "flac":
		return ext
	default:
		return "m4a"
	}
}

func categorizeApplePodcastsError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrUnavailable) ||
		errors.Is(err, ErrJSONResponseTooLarge) || errors.Is(err, ErrUnsupported) ||
		errors.Is(err, ErrAuthentication) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%w: Apple Podcasts denied access", ErrAuthentication)
		case http.StatusNotFound, http.StatusGone:
			return fmt.Errorf("%w: Apple Podcasts episode unavailable", ErrUnavailable)
		default:
			// Preserve unhandled HTTPStatusError so product maps to ErrorNetwork.
			return status
		}
	}
	// Preserve ErrorNetwork categorization without wrapping ErrInvalidMetadata
	// and without embedding underlying secret-bearing transport text.
	return errors.New("Apple Podcasts request failed")
}
