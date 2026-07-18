package extractor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var (
	sproutVideoPart = regexp.MustCompile(`^[0-9a-f]{6,128}$`)
	sproutHostPart  = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
	sproutVideoData = regexp.MustCompile(`(?is)(?:window\.|(?:var|const|let)\s+)(?:dat|(?:player|video)Info|)\s*=\s*["']([A-Za-z0-9+/=_-]+)["']`)
)

// SproutVideo decodes the public, base64-encoded player data and constructs
// its signed HLS and direct-download format URLs. Password submission and
// Vids.io account pages are intentionally outside this embed-only extractor.
type SproutVideo struct{}

func NewSproutVideo() SproutVideo                 { return SproutVideo{} }
func (SproutVideo) Name() string                  { return "sproutvideo" }
func (SproutVideo) Suitable(parsed *url.URL) bool { _, _, ok := parseSproutVideoURL(parsed); return ok }

func (SproutVideo) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	videoID, canonical, ok := parseSproutVideoURL(parsed)
	if !ok || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	return parseSproutVideoPage(page, videoID, canonical)
}

func parseSproutVideoURL(parsed *url.URL) (videoID, canonical string, ok bool) {
	if parsed == nil || len(parsed.String()) > sharedHostingMaxURLBytes || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Port() != "" || strings.ToLower(parsed.Hostname()) != "videos.sproutvideo.com" {
		return "", "", false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) != 3 || segments[0] != "embed" || !sproutVideoPart.MatchString(segments[1]) || !sproutVideoPart.MatchString(segments[2]) {
		return "", "", false
	}
	return segments[1], "https://videos.sproutvideo.com/embed/" + segments[1] + "/" + segments[2], true
}

type sproutVideoDataPayload struct {
	VideoUID     string                       `json:"videoUid"`
	Title        string                       `json:"title"`
	Duration     hostingNumber                `json:"duration"`
	Poster       string                       `json:"posterframe_url"`
	HLS          bool                         `json:"hls"`
	Base         string                       `json:"base"`
	S3UserHash   string                       `json:"s3_user_hash"`
	S3VideoHash  string                       `json:"s3_video_hash"`
	SessionID    string                       `json:"sessionID"`
	Signatures   map[string]map[string]string `json:"signatures"`
	Downloads    map[string]string            `json:"downloads"`
	HasAudio     *bool                        `json:"has_audio"`
	SubtitleData []struct {
		Src     string `json:"src"`
		SrcLang string `json:"srclang"`
	} `json:"subtitleData"`
}

func parseSproutVideoPage(page []byte, requestedID, webpageURL string) (Extraction, error) {
	match := sproutVideoData.FindSubmatch(page)
	if len(match) != 2 {
		lower := bytes.ToLower(page)
		if bytes.Contains(lower, []byte("password")) || bytes.Contains(lower, []byte("sign in")) {
			return Extraction{}, ErrAuthentication
		}
		if bytes.Contains(lower, []byte("not found")) || bytes.Contains(lower, []byte("account disabled")) {
			return Extraction{}, ErrUnavailable
		}
		return Extraction{}, fmt.Errorf("%w: missing SproutVideo player data", ErrInvalidMetadata)
	}
	encoded := match[1]
	if int64(len(encoded)) > maxExtractorJSONBytes*2 {
		return Extraction{}, ErrJSONResponseTooLarge
	}
	decoded, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(string(encoded))
	}
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid SproutVideo player encoding", ErrInvalidMetadata)
	}
	if int64(len(decoded)) > maxExtractorJSONBytes {
		return Extraction{}, ErrJSONResponseTooLarge
	}
	var payload sproutVideoDataPayload
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	if err := decoder.Decode(&payload); err != nil || ensureJSONEOF(decoder) != nil {
		return Extraction{}, fmt.Errorf("%w: invalid SproutVideo player JSON", ErrInvalidMetadata)
	}
	if payload.VideoUID != requestedID || strings.TrimSpace(payload.Title) == "" {
		return Extraction{}, fmt.Errorf("%w: SproutVideo media mismatch", ErrInvalidMetadata)
	}
	formats := make([]value.Value, 0, len(payload.Downloads)+1)
	headers := make(http.Header)
	headers.Set("Accept", "*/*")
	headers.Set("Origin", "https://videos.sproutvideo.com")
	headers.Set("Referer", webpageURL)
	if payload.HLS {
		hlsURL, query, segments, keys, ok := sproutHLSURLs(payload)
		if ok {
			format := manifestFormat("hls", hlsURL+"?"+query.Encode(), "m3u8_native")
			format.Set("http_headers", hostedHeadersValue(headers))
			if segments != "" {
				format.Set("extra_param_to_segment_url", value.String(segments))
			}
			if keys != "" {
				format.Set("extra_param_to_key_url", value.String(keys))
			}
			formats = append(formats, value.ObjectValue(format))
		}
	}
	qualities := []string{"hd", "uhd", "source", "sd"}
	seen := make(map[string]bool)
	for _, quality := range qualities {
		rawURL, exists := payload.Downloads[quality]
		if !exists {
			continue
		}
		seen[quality] = true
		format, ok := hostedURLFormat(quality, rawURL)
		if !ok {
			continue
		}
		format.Set("ext", value.String("mp4"))
		format.Set("http_headers", hostedHeadersValue(headers))
		if payload.HasAudio != nil && !*payload.HasAudio {
			format.Set("acodec", value.String("none"))
		}
		formats = append(formats, value.ObjectValue(format))
	}
	remaining := make([]string, 0, len(payload.Downloads))
	for quality := range payload.Downloads {
		if !seen[quality] {
			remaining = append(remaining, quality)
		}
	}
	sort.Strings(remaining)
	for _, quality := range remaining {
		format, ok := hostedURLFormat(quality, payload.Downloads[quality])
		if !ok {
			continue
		}
		format.Set("ext", value.String("mp4"))
		format.Set("http_headers", hostedHeadersValue(headers))
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(payload.VideoUID)}, value.Field{Key: "title", Value: value.String(payload.Title)}, value.Field{Key: "webpage_url", Value: value.String(webpageURL)}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "formats", Value: value.List(formats...)}, value.Field{Key: "http_headers", Value: hostedHeadersValue(headers)})
	hostedSetInt(info, "duration", payload.Duration.int64())
	hostedSetString(info, "thumbnail", firstHostedURL(payload.Poster))
	if subtitles := sproutSubtitles(payload.SubtitleData); !subtitles.IsMissing() {
		info.Set("subtitles", subtitles)
	}
	return Media(value.NewInfo(info)), nil
}

func sproutHLSURLs(payload sproutVideoDataPayload) (string, url.Values, string, string, bool) {
	if !sproutHostPart.MatchString(payload.Base) || !sproutVideoPart.MatchString(payload.S3UserHash) || !sproutVideoPart.MatchString(payload.S3VideoHash) || payload.SessionID == "" || len(payload.SessionID) > 2048 {
		return "", nil, "", "", false
	}
	manifest, okM := sproutPolicy(payload, "m")
	fragments, okT := sproutPolicy(payload, "t")
	keys, okK := sproutPolicy(payload, "k")
	if !okM || !okT || !okK {
		return "", nil, "", "", false
	}
	manifest.Set("sessionID", payload.SessionID)
	fragments.Set("sessionID", payload.SessionID)
	keys.Set("sessionID", payload.SessionID)
	hlsURL := "https://" + payload.Base + ".videos.sproutvideo.com/" + payload.S3UserHash + "/" + payload.S3VideoHash + "/video/index.m3u8"
	return hlsURL, manifest, fragments.Encode(), keys.Encode(), true
}
func sproutPolicy(payload sproutVideoDataPayload, key string) (url.Values, bool) {
	values, ok := payload.Signatures[key]
	if !ok || len(values) == 0 || len(values) > 16 {
		return nil, false
	}
	output := make(url.Values)
	for name, input := range values {
		name = strings.TrimPrefix(name, "CloudFront-")
		if name == "" || len(name) > 128 || len(input) == 0 || len(input) > 4096 || strings.ContainsAny(input, "\r\n") {
			return nil, false
		}
		output.Set(name, input)
	}
	return output, true
}
func sproutSubtitles(subtitles []struct {
	Src     string `json:"src"`
	SrcLang string `json:"srclang"`
}) value.Value {
	byLanguage := make(map[string][]value.Value)
	for _, subtitle := range subtitles {
		if !validHostedHTTPURL(subtitle.Src) {
			continue
		}
		language := strings.ToLower(strings.TrimSpace(subtitle.SrcLang))
		if language == "" {
			language = "en"
		}
		byLanguage[language] = append(byLanguage[language], value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(subtitle.Src)})))
	}
	if len(byLanguage) == 0 {
		return value.Missing()
	}
	languages := make([]string, 0, len(byLanguage))
	for language := range byLanguage {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	output := value.NewObject()
	for _, language := range languages {
		output.Set(language, value.List(byLanguage[language]...))
	}
	return value.ObjectValue(output)
}

var _ Extractor = SproutVideo{}
