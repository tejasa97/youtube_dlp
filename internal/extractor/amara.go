package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	amaraMaxVideoIDLen                 = 64
	amaraMaxLangCodeLen                = 35
	amaraMaxLanguages                  = 256
	amaraMaxAllURLs                    = 64
	amaraMaxTitleBytes                 = 512
	amaraMaxDescriptionBytes           = 64 << 10
	amaraMaxThumbnailBytes             = 8 << 10
	amaraMaxCreatedBytes               = 64
	amaraMaxMediaURLBytes              = sharedHostingMaxURLBytes
	amaraMaxSubtitlesURIBytes          = sharedHostingMaxURLBytes
	amaraMaxJSONDepth                  = 32
	amaraMaxSubtitleEntries            = 256
	amaraMaxSubtitleEntriesPerLanguage = 32
	amaraMaxExtensionBytes             = 16
	amaraSubtitleFormats               = 3
)

var (
	amaraVideoIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	amaraLangPattern      = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)
	amaraInfoSlugPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	amaraExtensionPattern = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
	ErrAmaraRateLimited   = errors.New("Amara API rate limited")
	ErrAmaraNetwork       = errors.New("Amara API unavailable")
)

type amaraTarget struct {
	id, canonical string
}

// Amara extracts public Amara video pages through the documented JSON API.
// YouTube and Vimeo media URLs are emitted as transparent URL results while
// preserving Amara metadata; other validated HTTP(S) URLs are exposed directly.
type Amara struct{}

func NewAmara() Amara                       { return Amara{} }
func (Amara) Name() string                  { return "amara" }
func (Amara) Suitable(parsed *url.URL) bool { _, ok := parseAmaraURL(parsed); return ok }

func (Amara) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseAmaraURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	var payload amaraVideoResponse
	endpoint := "https://amara.org/api/videos/" + url.PathEscape(target.id) + "/?format=json"
	if err := amaraRequestJSON(ctx, request.Transport, endpoint, &payload); err != nil {
		return Extraction{}, err
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	return normalizeAmara(payload, target)
}

func parseAmaraURL(parsed *url.URL) (amaraTarget, bool) {
	if parsed == nil || len(parsed.String()) > sharedHostingMaxURLBytes {
		return amaraTarget{}, false
	}
	if hostedRejectUnsafeURL(parsed) {
		return amaraTarget{}, false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return amaraTarget{}, false
	}
	if !amaraHostOK(parsed.Hostname()) {
		return amaraTarget{}, false
	}
	segments, ok := amaraPathSegments(parsed.EscapedPath())
	if !ok || len(segments) == 0 || segments[0] == "api" {
		return amaraTarget{}, false
	}
	var id string
	var videoIndex int
	switch {
	case len(segments) >= 2 && segments[0] == "videos":
		id, videoIndex = segments[1], 1
	case len(segments) >= 3 && segments[1] == "videos":
		if !amaraLangPattern.MatchString(segments[0]) {
			return amaraTarget{}, false
		}
		id, videoIndex = segments[2], 2
	default:
		return amaraTarget{}, false
	}
	if !amaraVideoIDPattern.MatchString(id) || !amaraTrailingPathOK(segments, videoIndex) {
		return amaraTarget{}, false
	}
	canonical := "https://amara.org"
	if videoIndex == 2 {
		canonical += "/" + segments[0]
	}
	canonical += "/videos/" + id
	return amaraTarget{id: id, canonical: canonical}, true
}

func amaraTrailingPathOK(segments []string, videoIndex int) bool {
	rest := segments[videoIndex+1:]
	if len(rest) == 0 {
		return true
	}
	if rest[0] != "info" {
		return false
	}
	for _, part := range rest[1:] {
		if !amaraInfoSlugPattern.MatchString(part) {
			return false
		}
	}
	return true
}

func amaraHostOK(host string) bool {
	switch strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), ".")) {
	case "amara.org", "www.amara.org":
		return true
	default:
		return false
	}
}

func amaraPathSegments(escapedPath string) ([]string, bool) {
	lower := strings.ToLower(escapedPath)
	for _, banned := range []string{"%00", "%2f", "%5c", "%252f", "%255c", "%2500"} {
		if strings.Contains(lower, banned) {
			return nil, false
		}
	}
	trimmed := strings.Trim(escapedPath, "/")
	if trimmed == "" {
		return nil, false
	}
	raw := strings.Split(trimmed, "/")
	segments := make([]string, 0, len(raw))
	for _, segment := range raw {
		if segment == "" {
			return nil, false
		}
		decoded, err := url.PathUnescape(segment)
		if err != nil || strings.ContainsAny(decoded, "/\\\x00") {
			return nil, false
		}
		if decoded != segment && strings.Contains(strings.ToLower(segment), "%25") {
			return nil, false
		}
		segments = append(segments, decoded)
	}
	return segments, true
}

type amaraVideoResponse struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Thumbnail   string          `json:"thumbnail"`
	Duration    hostingNumber   `json:"duration"`
	Created     string          `json:"created"`
	AllURLs     []string        `json:"all_urls"`
	Languages   []amaraLanguage `json:"languages"`
}

type amaraLanguage struct {
	Code         string `json:"code"`
	Published    bool   `json:"published"`
	SubtitlesURI string `json:"subtitles_uri"`
}

func amaraRequestJSON(ctx context.Context, transport Transport, endpoint string, target any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if transport == nil || target == nil {
		return errors.New("invalid Amara JSON request")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("invalid Amara JSON request")
	}
	response, err := transport.Do(ctx, request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusTooManyRequests:
			return ErrAmaraRateLimited
		default:
			if response.StatusCode >= 500 {
				return ErrAmaraNetwork
			}
			return fmt.Errorf("Amara API: HTTP status %d", response.StatusCode)
		}
	}
	data, err := io.ReadAll(&io.LimitedReader{R: response.Body, N: maxExtractorJSONBytes + 1})
	if err != nil {
		return errors.New("read Amara JSON response failed")
	}
	if int64(len(data)) > maxExtractorJSONBytes {
		return ErrJSONResponseTooLarge
	}
	return amaraDecodeJSON(data, target)
}

func amaraDecodeJSON(raw []byte, target any) error {
	if err := amaraValidateJSONNesting(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid Amara JSON response", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: trailing Amara JSON response", ErrInvalidMetadata)
	}
	return nil
}

func amaraValidateJSONNesting(raw []byte) error {
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
			if depth > amaraMaxJSONDepth {
				return fmt.Errorf("%w: Amara JSON nesting exceeds limit", ErrInvalidMetadata)
			}
		case '}', ']':
			if depth > 0 {
				depth--
			}
		}
	}
	return nil
}

func normalizeAmara(payload amaraVideoResponse, target amaraTarget) (Extraction, error) {
	title, err := amaraRequiredString(payload.Title, amaraMaxTitleBytes, "title")
	if err != nil {
		return Extraction{}, err
	}
	if len(payload.AllURLs) > amaraMaxAllURLs {
		return Extraction{}, fmt.Errorf("%w: Amara media URL overflow", ErrInvalidMetadata)
	}
	if len(payload.Languages) > amaraMaxLanguages {
		return Extraction{}, fmt.Errorf("%w: Amara language overflow", ErrInvalidMetadata)
	}
	if err := amaraValidateOptionalStrings(payload); err != nil {
		return Extraction{}, err
	}
	mediaURL, ok := amaraFirstMediaURL(payload.AllURLs)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: missing Amara media URL", ErrInvalidMetadata)
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
	)
	description := amaraOptionalString(payload.Description, amaraMaxDescriptionBytes)
	if description != "" {
		info.Set("description", value.String(description))
	}
	thumbnail := amaraOptionalString(payload.Thumbnail, amaraMaxThumbnailBytes)
	if !strictValidHostedHTTPURL(thumbnail) {
		thumbnail = ""
	} else {
		info.Set("thumbnail", value.String(thumbnail))
	}
	duration := payload.Duration.float64()
	if duration > 0 {
		info.Set("duration", value.Float(duration))
	}
	timestamp := amaraTimestamp(payload.Created)
	if timestamp > 0 {
		info.Set("timestamp", value.Int(timestamp))
	}
	subtitles, err := amaraSubtitles(payload.Languages)
	if err != nil {
		return Extraction{}, err
	}
	if subtitles != nil {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	if entry, ok := amaraTransparentHandoff(mediaURL, title, thumbnail, duration, timestamp); ok {
		result, err := URLResult(entry)
		if err != nil {
			return Extraction{}, err
		}
		result.Info = value.NewInfo(info)
		return result, nil
	}
	format, ok := amaraDirectMediaFormat(mediaURL)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: unsafe Amara media URL", ErrInvalidMetadata)
	}
	info.Set("formats", value.List(value.ObjectValue(format)))
	if ext, ok := format.Lookup("ext").StringValue(); ok && ext != "" {
		info.Set("ext", value.String(ext))
	}
	return Media(value.NewInfo(info)), nil
}

func amaraValidateOptionalStrings(payload amaraVideoResponse) error {
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"description", payload.Description, amaraMaxDescriptionBytes},
		{"thumbnail", payload.Thumbnail, amaraMaxThumbnailBytes},
		{"created timestamp", payload.Created, amaraMaxCreatedBytes},
	} {
		if _, ok := amaraBoundedString(field.value, field.limit); !ok {
			return fmt.Errorf("%w: Amara %s exceeds limit", ErrInvalidMetadata, field.name)
		}
	}
	for _, raw := range payload.AllURLs {
		if _, ok := amaraBoundedString(raw, amaraMaxMediaURLBytes); !ok {
			return fmt.Errorf("%w: Amara media URL exceeds limit", ErrInvalidMetadata)
		}
	}
	for _, language := range payload.Languages {
		if _, ok := amaraBoundedString(language.Code, amaraMaxLangCodeLen); !ok {
			return fmt.Errorf("%w: Amara language code exceeds limit", ErrInvalidMetadata)
		}
		if _, ok := amaraBoundedString(language.SubtitlesURI, amaraMaxSubtitlesURIBytes); !ok {
			return fmt.Errorf("%w: Amara subtitles URI exceeds limit", ErrInvalidMetadata)
		}
	}
	return nil
}

func amaraRequiredString(raw string, limit int, field string) (string, error) {
	trimmed, ok := amaraBoundedString(raw, limit)
	if !ok || trimmed == "" {
		return "", fmt.Errorf("%w: missing or invalid Amara %s", ErrInvalidMetadata, field)
	}
	return trimmed, nil
}

func amaraOptionalString(raw string, limit int) string {
	trimmed, ok := amaraBoundedString(raw, limit)
	if !ok {
		return ""
	}
	return trimmed
}

func amaraBoundedString(raw string, limit int) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	return trimmed, len(trimmed) <= limit
}

func amaraFirstMediaURL(urls []string) (string, bool) {
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" || len(raw) > amaraMaxMediaURLBytes {
			continue
		}
		if strictValidHostedHTTPURL(raw) {
			return raw, true
		}
	}
	return "", false
}

func amaraTransparentHandoff(mediaURL, title, thumbnail string, duration float64, timestamp int64) (Entry, bool) {
	if _, err := parseYouTubeTarget(mediaURL); err == nil {
		return amaraOverlayEntry(mediaURL, "youtube", title, thumbnail, duration, timestamp), true
	}
	parsed, err := url.Parse(mediaURL)
	if err != nil {
		return Entry{}, false
	}
	if kind, _ := classifyVimeoURL(parsed); kind == vimeoRouteVideo {
		return amaraOverlayEntry(mediaURL, "vimeo", title, thumbnail, duration, timestamp), true
	}
	return Entry{}, false
}

func amaraOverlayEntry(mediaURL, extractorKey, title, thumbnail string, duration float64, timestamp int64) Entry {
	entry := Entry{
		URL:          mediaURL,
		ExtractorKey: extractorKey,
		Title:        title,
		Thumbnail:    thumbnail,
		Transparent:  true,
	}
	if duration > 0 {
		entry.Duration = duration
		entry.HasDuration = true
	}
	if timestamp > 0 {
		entry.Timestamp = timestamp
		entry.HasTimestamp = true
	}
	return entry
}

func amaraSubtitles(languages []amaraLanguage) (*value.Object, error) {
	grouped := make(map[string][]value.Value)
	perLanguage := make(map[string]int)
	totalEntries := 0
	for _, language := range languages {
		if !language.Published {
			continue
		}
		baseURI := strings.TrimSpace(language.SubtitlesURI)
		if baseURI == "" || len(baseURI) > amaraMaxSubtitlesURIBytes {
			continue
		}
		code := strings.TrimSpace(language.Code)
		if code == "" {
			code = "en"
		}
		if len(code) > amaraMaxLangCodeLen || !validAmaraLanguage(code) {
			continue
		}
		perLanguage[code]++
		if perLanguage[code] > amaraMaxSubtitleEntriesPerLanguage {
			return nil, fmt.Errorf("%w: Amara subtitle language overflow", ErrInvalidMetadata)
		}
		entries, ok := amaraSubtitleFormatsFor(baseURI)
		if !ok {
			continue
		}
		totalEntries += len(entries)
		if totalEntries > amaraMaxSubtitleEntries {
			return nil, fmt.Errorf("%w: Amara subtitle overflow", ErrInvalidMetadata)
		}
		grouped[code] = append(grouped[code], entries...)
	}
	if len(grouped) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(grouped))
	for language := range grouped {
		keys = append(keys, language)
	}
	sort.Strings(keys)
	result := value.NewObject()
	for _, language := range keys {
		result.Set(language, value.List(grouped[language]...))
	}
	return result, nil
}

func amaraSubtitleFormatsFor(baseURI string) ([]value.Value, bool) {
	parsed, err := url.Parse(baseURI)
	if err != nil || !strictValidHostedHTTPURL(baseURI) {
		return nil, false
	}
	entries := make([]value.Value, 0, amaraSubtitleFormats)
	for _, extension := range []string{"json", "srt", "vtt"} {
		clone := *parsed
		query := clone.Query()
		query.Set("format", extension)
		clone.RawQuery = query.Encode()
		raw := clone.String()
		if !strictValidHostedHTTPURL(raw) {
			continue
		}
		entries = append(entries, value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(raw)},
			value.Field{Key: "ext", Value: value.String(extension)},
		)))
	}
	if len(entries) == 0 {
		return nil, false
	}
	return entries, true
}

func amaraDirectMediaFormat(mediaURL string) (*value.Object, bool) {
	if !strictValidHostedHTTPURL(mediaURL) {
		return nil, false
	}
	parsed, err := url.Parse(mediaURL)
	if err != nil {
		return nil, false
	}
	extension := strings.ToLower(strings.TrimPrefix(path.Ext(parsed.Path), "."))
	if extension == "" || len(extension) > amaraMaxExtensionBytes || !amaraExtensionPattern.MatchString(extension) {
		extension = "mp4"
	}
	return value.NewObject(
		value.Field{Key: "format_id", Value: value.String("http")},
		value.Field{Key: "url", Value: value.String(mediaURL)},
		value.Field{Key: "ext", Value: value.String(extension)},
		value.Field{Key: "protocol", Value: value.String(parsed.Scheme)},
	), true
}

func amaraTimestamp(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > amaraMaxCreatedBytes {
		return 0
	}
	if unix := hostedUnixTimestamp(raw); unix > 0 {
		return unix
	}
	return 0
}

func validAmaraLanguage(language string) bool {
	if language == "" || len(language) > amaraMaxLangCodeLen {
		return false
	}
	for _, part := range strings.Split(language, "-") {
		if part == "" || len(part) > 8 {
			return false
		}
		for index := 0; index < len(part); index++ {
			character := part[index]
			isLetter := character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
			isDigit := character >= '0' && character <= '9'
			if !isLetter && !isDigit {
				return false
			}
		}
	}
	return true
}

var _ Extractor = Amara{}
