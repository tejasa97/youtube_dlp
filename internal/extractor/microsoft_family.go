package extractor

// Microsoft public media family. The pinned reference is
// yt_dlp/extractor/microsoftembed.py (commit aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8).
//
// Six extractor classes share:
//   * credential-isolated no-redirect request paths
//   * a strict route matrix so microsoft.com, medius.microsoft.com,
//     learn.microsoft.com, and build.microsoft.com URLs each route to
//     exactly one extractor
//   * role-specific attributable host policies for manifests, direct media,
//     captions, and thumbnails
//   * ISM, HLS, DASH, and direct HTTPS formats with explicit
//     credential-isolation flags
//
// This family never invents new downloaders or manifest parsers. It only
// adapts the upstream API shapes and routes them into the existing native
// product paths. Authenticated, DRM-protected, signed-cookie Medius
// playback, regional restrictions, Akamai CDN rotation, and any host not
// listed in conformance/extractors/risk/microsoft/PROVENANCE.md remain out
// of scope.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

// --- Microsoft public family configuration --------------------------------

const (
	microsoftMaxJSONBytes       int64 = 4 << 20
	microsoftMaxWebpage         int64 = 4 << 20
	microsoftMaxEntries               = 4096
	microsoftMaxPages                 = 256
	microsoftMaxFormats               = 64
	microsoftMaxSubtitles             = 64
	microsoftMaxThumbnails            = 32
	microsoftMaxCaptionURLBytes       = 1024
	microsoftMaxThumbURLBytes         = 1024

	microsoftAPIBase   = "https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/api/videos/"
	microsoftMediusWeb = "https://medius.microsoft.com/Embed/video-nc/"
	microsoftLearnAPI  = "https://learn.microsoft.com/api/contentbrowser/search/"
	microsoftLearnVID  = "https://learn.microsoft.com/api/video/public/v1/entries/"
	microsoftBuildAPI  = "https://api-v2.build.microsoft.com/api/session/all/en-US"
)

var (
	microsoftLocalePattern  = regexp.MustCompile(`(?i)^[a-z]{2}-[a-z]{2}$`)
	microsoftEmbedIDPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,16}$`)
	microsoftSlugPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,94}$`)
	microsoftUUIDPattern    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	microsoftLearnIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,95}$`)
	microsoftLangPattern    = regexp.MustCompile(`^[a-z]{2}(?:-[A-Z]{2})?$`)
	microsoftVTTPath        = regexp.MustCompile(`(?i)\.vtt(?:\?|$)`)
)

// --- Host policy ----------------------------------------------------------

type microsoftMediaRole uint8

const (
	microsoftRoleManifest microsoftMediaRole = iota + 1
	microsoftRoleMedia
	microsoftRoleCaption
	microsoftRoleThumbnail
)

func (role microsoftMediaRole) String() string {
	switch role {
	case microsoftRoleManifest:
		return "manifest"
	case microsoftRoleMedia:
		return "direct-media"
	case microsoftRoleCaption:
		return "caption"
	case microsoftRoleThumbnail:
		return "thumbnail"
	default:
		return "unknown"
	}
}

var microsoftProductionHosts = map[microsoftMediaRole][]string{
	microsoftRoleManifest: {
		"prod-video-cms-rt-microsoft-com.akamaized.net",
		"mediusimg.event.microsoft.com",
		"mediusdownload.event.microsoft.com",
		"learn.microsoft.com",
	},
	microsoftRoleMedia: {
		"mediusdownload.event.microsoft.com",
		"learn.microsoft.com",
		"prod-video-cms-rt-microsoft-com.akamaized.net",
	},
	microsoftRoleCaption: {
		"mediusimg.event.microsoft.com",
		"learn.microsoft.com",
	},
	microsoftRoleThumbnail: {
		"img-prod-cms-rt-microsoft-com.akamaized.net",
		"mediusimg.event.microsoft.com",
		"learn.microsoft.com",
	},
}

func microsoftURLCommon(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || strings.ContainsAny(host, " \x00\r\n\t/") ||
		looksLikeIPLiteralHost(host) || looksLikeLocalOrInternalHost(host) {
		return false
	}
	return !strictPathUnsafe(parsed.EscapedPath())
}

func microsoftHostAllowed(parsed *url.URL, role microsoftMediaRole) bool {
	if !microsoftURLCommon(parsed) {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, allowed := range microsoftProductionHosts[role] {
		if host == allowed || strings.HasSuffix(host, "."+allowed) {
			return true
		}
	}
	return false
}

func microsoftURLRole(rawURL string, role microsoftMediaRole) (*url.URL, error) {
	if len(rawURL) == 0 || len(rawURL) > microsoftMaxCaptionURLBytes*4 {
		return nil, fmt.Errorf("%w: %s URL too long or empty", ErrInvalidMetadata, role)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid %s URL", ErrInvalidMetadata, role)
	}
	if !microsoftHostAllowed(parsed, role) {
		return nil, fmt.Errorf("%w: %s host not attributable: %s", ErrInvalidMetadata, role, parsed.Hostname())
	}
	return parsed, nil
}

func microsoftExactHost(parsed *url.URL, host string) bool {
	if !microsoftURLCommon(parsed) {
		return false
	}
	return strings.ToLower(parsed.Hostname()) == host
}

// --- MicrosoftEmbed -------------------------------------------------------

type MicrosoftEmbed struct{}

func NewMicrosoftEmbed() MicrosoftEmbed              { return MicrosoftEmbed{} }
func (MicrosoftEmbed) Name() string                  { return "microsoft_embed" }
func (MicrosoftEmbed) Suitable(parsed *url.URL) bool { return microsoftEmbedSuitable(parsed) }

func (MicrosoftEmbed) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !microsoftEmbedSuitable(parsed) || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 3 && len(parts) != 4 {
		return Extraction{}, ErrUnsupported
	}
	if len(parts) == 3 && (parts[0] != "videoplayer" || parts[1] != "embed") {
		return Extraction{}, ErrUnsupported
	}
	if len(parts) == 4 && (!microsoftLocalePattern.MatchString(parts[0]) || parts[1] != "videoplayer" || parts[2] != "embed") {
		return Extraction{}, ErrUnsupported
	}
	videoID := parts[len(parts)-1]
	if !microsoftEmbedIDPattern.MatchString(videoID) {
		return Extraction{}, fmt.Errorf("%w: invalid microsoft_embed id", ErrInvalidMetadata)
	}
	if parsed.RawQuery != "" {
		return Extraction{}, fmt.Errorf("%w: microsoft_embed query must be empty", ErrInvalidMetadata)
	}
	endpoint := microsoftAPIBase + url.PathEscape(videoID)
	payload, err := microsoftIsolatedJSON(ctx, request.Transport, http.MethodGet, endpoint, microsoftMaxJSONBytes, nil)
	if err != nil {
		return Extraction{}, err
	}
	var root struct {
		Streams  map[string]map[string]json.RawMessage `json:"streams"`
		Captions map[string]map[string]json.RawMessage `json:"captions"`
		Snippet  map[string]json.RawMessage            `json:"snippet"`
	}
	if err := json.Unmarshal(payload, &root); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid microsoft_embed JSON", ErrInvalidMetadata)
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "extractor", Value: value.String("microsoft_embed")},
	)
	formats := microsoftEmbedFormats(root.Streams)
	info.Set("formats", value.List(formats...))
	subs := microsoftEmbedCaptions(root.Captions)
	if len(subs) > 0 {
		object := value.NewObject()
		for _, lang := range sortedLangKeys(subs) {
			object.Set(lang, value.List(subs[lang]...))
		}
		info.Set("subtitles", value.ObjectValue(object))
	}
	thumbnails := microsoftEmbedThumbnails(root.Snippet)
	if len(thumbnails) > 0 {
		info.Set("thumbnails", value.List(thumbnails...))
		if first, ok := thumbnails[0].Object(); ok {
			if thumbURL, ok := first.Lookup("url").StringValue(); ok {
				info.Set("thumbnail", value.String(thumbURL))
			}
		}
	}
	if title, ok := microsoftExtractString(root.Snippet, "title"); ok {
		info.Set("title", value.String(strings.TrimSpace(title)))
	}
	if ts, ok := microsoftExtractString(root.Snippet, "activeStartDate"); ok {
		if unix := microsoftAkamaiTimestamp(ts); unix > 0 {
			info.Set("timestamp", value.Int(unix))
			info.Set("upload_date", value.String(time.Unix(unix, 0).UTC().Format("20060102")))
		}
	}
	if rawAge, ok := root.Snippet["minimumAge"]; ok {
		if age := microsoftFlexibleInt(rawAge); age >= 0 {
			info.Set("age_limit", value.Int(age))
		}
	}
	if title, _ := info.Lookup("title").StringValue(); title == "" {
		return Extraction{}, fmt.Errorf("%w: missing microsoft_embed title", ErrInvalidMetadata)
	}
	return Media(value.NewInfo(info)), nil
}

func microsoftEmbedSuitable(parsed *url.URL) bool {
	if !microsoftExactHost(parsed, "www.microsoft.com") && !microsoftExactHost(parsed, "microsoft.com") {
		return false
	}
	if parsed.RawQuery != "" {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	switch len(parts) {
	case 3:
		return parts[0] == "videoplayer" && parts[1] == "embed" &&
			microsoftEmbedIDPattern.MatchString(parts[2])
	case 4:
		return microsoftLocalePattern.MatchString(parts[0]) && parts[1] == "videoplayer" &&
			parts[2] == "embed" && microsoftEmbedIDPattern.MatchString(parts[3])
	}
	return false
}

var microsoftEmbedStreamOrder = []string{
	"smooth_Streaming",
	"apple_HTTP_Live_Streaming",
	"mPEG_DASH",
	"high_bitrate_MP4",
	"low_bitrate_MP4",
}

func microsoftEmbedFormats(streams map[string]map[string]json.RawMessage) []value.Value {
	formats := make([]value.Value, 0, microsoftMaxFormats)
	seen := make(map[string]bool, microsoftMaxFormats)
	emit := func(kind string, raw map[string]json.RawMessage) {
		if kind == "" || len(formats) >= microsoftMaxFormats {
			return
		}
		sourceURL, ok := microsoftExtractString(raw, "url")
		if !ok {
			return
		}
		parsed, err := microsoftURLRole(sourceURL, microsoftRoleManifest)
		if err != nil || seen[parsed.String()] {
			return
		}
		seen[parsed.String()] = true
		switch kind {
		case "smooth_Streaming":
			format := manifestFormat("ism", parsed.String(), "ism")
			format.Set("_credential_isolated", value.Bool(true))
			formats = append(formats, value.ObjectValue(format))
		case "apple_HTTP_Live_Streaming":
			format := manifestFormat("hls", parsed.String(), "m3u8_native")
			format.Set("_credential_isolated", value.Bool(true))
			formats = append(formats, value.ObjectValue(format))
		case "mPEG_DASH":
			format := manifestFormat("dash", parsed.String(), "http_dash_segments")
			format.Set("_credential_isolated", value.Bool(true))
			formats = append(formats, value.ObjectValue(format))
		default:
			width, _ := microsoftExtractInt(raw, "widthPixels")
			height, _ := microsoftExtractInt(raw, "heightPixels")
			format := value.NewObject(
				value.Field{Key: "format_id", Value: value.String(kind)},
				value.Field{Key: "url", Value: value.String(parsed.String())},
				value.Field{Key: "ext", Value: value.String("mp4")},
				value.Field{Key: "protocol", Value: value.String("https")},
			)
			format.Set("_credential_isolated", value.Bool(true))
			riskPositiveInt(format, "width", width)
			riskPositiveInt(format, "height", height)
			formats = append(formats, value.ObjectValue(format))
		}
	}
	emitted := make(map[string]bool, len(streams))
	for _, kind := range microsoftEmbedStreamOrder {
		raw, ok := streams[kind]
		if !ok {
			continue
		}
		emit(kind, raw)
		emitted[kind] = true
	}
	extras := make([]string, 0)
	for kind := range streams {
		if !emitted[kind] {
			extras = append(extras, kind)
		}
	}
	sort.Strings(extras)
	for _, kind := range extras {
		emit(kind, streams[kind])
	}
	return formats
}

func microsoftEmbedCaptions(captions map[string]map[string]json.RawMessage) map[string][]value.Value {
	subs := make(map[string][]value.Value)
	if len(captions) > microsoftMaxSubtitles {
		return subs
	}
	for lang, raw := range captions {
		if !microsoftLangPattern.MatchString(lang) || len(subs) >= microsoftMaxSubtitles {
			continue
		}
		captionURL, ok := microsoftExtractString(raw, "url")
		if !ok {
			continue
		}
		parsed, err := microsoftURLRole(captionURL, microsoftRoleCaption)
		if err != nil || !microsoftVTTPath.MatchString(parsed.String()) {
			continue
		}
		subs[lang] = []value.Value{value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(parsed.String())},
			value.Field{Key: "ext", Value: value.String("vtt")},
			value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
		))}
	}
	return subs
}

func microsoftEmbedThumbnails(snippet map[string]json.RawMessage) []value.Value {
	thumbnails := make([]value.Value, 0, microsoftMaxThumbnails)
	rawThumbs, ok := snippet["thumbnails"]
	if !ok {
		return thumbnails
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(rawThumbs, &list); err != nil {
		return thumbnails
	}
	seen := make(map[string]bool, microsoftMaxThumbnails)
	for _, raw := range list {
		if len(thumbnails) >= microsoftMaxThumbnails {
			break
		}
		thumbURL, ok := microsoftExtractString(raw, "url")
		if !ok {
			continue
		}
		parsed, err := microsoftURLRole(thumbURL, microsoftRoleThumbnail)
		if err != nil || seen[parsed.String()] {
			continue
		}
		seen[parsed.String()] = true
		width, _ := microsoftExtractInt(raw, "width")
		height, _ := microsoftExtractInt(raw, "height")
		thumb := value.NewObject(value.Field{Key: "url", Value: value.String(parsed.String())})
		thumb.Set("_credential_isolated", value.Bool(true))
		riskPositiveInt(thumb, "width", width)
		riskPositiveInt(thumb, "height", height)
		thumbnails = append(thumbnails, value.ObjectValue(thumb))
	}
	return thumbnails
}

func sortedLangKeys(subs map[string][]value.Value) []string {
	keys := make([]string, 0, len(subs))
	for k := range subs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- MicrosoftMedius ------------------------------------------------------

type MicrosoftMedius struct{}

func NewMicrosoftMedius() MicrosoftMedius             { return MicrosoftMedius{} }
func (MicrosoftMedius) Name() string                  { return "microsoft_medius" }
func (MicrosoftMedius) Suitable(parsed *url.URL) bool { return microsoftMediusSuitable(parsed) }

func (MicrosoftMedius) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !microsoftMediusSuitable(parsed) || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	videoID, _, err := microsoftMediusID(parsed)
	if err != nil {
		return Extraction{}, err
	}
	webURL := microsoftMediusWeb + videoID
	headers := http.Header{
		"Accept": {"text/html,application/xhtml+xml"},
	}
	if referer := microsoftMediusScopedReferer(request.Referer); referer != "" {
		headers.Set("Referer", referer)
	}
	page, err := microsoftIsolatedPage(ctx, request.Transport, http.MethodGet, webURL, microsoftMaxWebpage, headers)
	if err != nil {
		return Extraction{}, err
	}
	ismURL, ok := microsoftMediusStreamURL(page)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: missing microsoft_medius manifest URL", ErrInvalidMetadata)
	}
	parsedISM, err := microsoftURLRole(ismURL, microsoftRoleManifest)
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "extractor", Value: value.String("microsoft_medius")},
		value.Field{Key: "webpage_url", Value: value.String(webURL)},
	)
	if title, ok := microsoftMediusMeta(page, "og:title"); ok {
		info.Set("title", value.String(strings.TrimSpace(title)))
	}
	if desc, ok := microsoftMediusMeta(page, "og:description"); ok {
		info.Set("description", value.String(strings.TrimSpace(desc)))
	}
	if thumb, ok := microsoftMediusMeta(page, "og:image"); ok {
		if parsedThumb, err := microsoftURLRole(thumb, microsoftRoleThumbnail); err == nil {
			info.Set("thumbnail", value.String(parsedThumb.String()))
			thumbObject := value.NewObject(
				value.Field{Key: "url", Value: value.String(parsedThumb.String())},
				value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
			)
			info.Set("thumbnails", value.List(value.ObjectValue(thumbObject)))
		}
	}
	if title, _ := info.Lookup("title").StringValue(); title == "" {
		return Extraction{}, fmt.Errorf("%w: missing microsoft_medius title", ErrInvalidMetadata)
	}
	format := manifestFormat("ism", parsedISM.String(), "ism")
	format.Set("_credential_isolated", value.Bool(true))
	info.Set("formats", value.List(value.ObjectValue(format)))
	subs := microsoftMediusSubtitles(page)
	if len(subs) > 0 {
		object := value.NewObject()
		for _, lang := range sortedLangKeys(subs) {
			object.Set(lang, value.List(subs[lang]...))
		}
		info.Set("subtitles", value.ObjectValue(object))
	}
	return Media(value.NewInfo(info)), nil
}

func microsoftMediusScopedReferer(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.String() != raw {
		return ""
	}
	if microsoftLearnSessionSuitable(parsed) || microsoftBuildSuitable(parsed) {
		return parsed.String()
	}
	return ""
}

func microsoftMediusSuitable(parsed *url.URL) bool {
	if !microsoftExactHost(parsed, "medius.microsoft.com") {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) < 2 || parts[0] != "Embed" {
		return false
	}
	switch {
	case len(parts) == 3 && parts[1] == "video-nc":
		return microsoftUUIDPattern.MatchString(parts[2]) && parsed.RawQuery == ""
	case len(parts) == 3 && parts[1] == "VideoDetails":
		return microsoftUUIDPattern.MatchString(parts[2]) && parsed.RawQuery == ""
	case len(parts) == 2 && parts[1] == "Video":
		return microsoftMediusVideoID(parsed) != ""
	}
	return false
}

func microsoftMediusID(parsed *url.URL) (string, string, error) {
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) < 2 || parts[0] != "Embed" {
		return "", "", fmt.Errorf("%w: invalid microsoft_medius path", ErrInvalidMetadata)
	}
	switch {
	case len(parts) == 3 && parts[1] == "video-nc":
		if parsed.RawQuery != "" || !microsoftUUIDPattern.MatchString(parts[2]) {
			return "", "", fmt.Errorf("%w: invalid microsoft_medius id", ErrInvalidMetadata)
		}
		return parts[2], parts[1], nil
	case len(parts) == 3 && parts[1] == "VideoDetails":
		if parsed.RawQuery != "" || !microsoftUUIDPattern.MatchString(parts[2]) {
			return "", "", fmt.Errorf("%w: invalid microsoft_medius id", ErrInvalidMetadata)
		}
		return parts[2], parts[1], nil
	case len(parts) == 2 && parts[1] == "Video":
		id := microsoftMediusVideoID(parsed)
		if id == "" {
			return "", "", fmt.Errorf("%w: invalid microsoft_medius Video?id= query", ErrInvalidMetadata)
		}
		return id, parts[1], nil
	}
	return "", "", fmt.Errorf("%w: unsupported microsoft_medius route", ErrInvalidMetadata)
}

func microsoftMediusVideoID(parsed *url.URL) string {
	if parsed.RawQuery == "" {
		return ""
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return ""
	}
	if len(values) != 1 {
		return ""
	}
	ids, ok := values["id"]
	if !ok || len(ids) != 1 {
		return ""
	}
	id := strings.TrimSpace(ids[0])
	if !microsoftUUIDPattern.MatchString(id) {
		return ""
	}
	return id
}

var microsoftMediusStreamPattern = regexp.MustCompile(`StreamUrl\s*=\s*"([^"]+manifest)"`)

func microsoftMediusStreamURL(page []byte) (string, bool) {
	match := microsoftMediusStreamPattern.FindSubmatch(page)
	if len(match) != 2 {
		return "", false
	}
	parsed, err := microsoftURLRole(string(match[1]), microsoftRoleManifest)
	if err != nil {
		return "", false
	}
	return parsed.String(), true
}

func microsoftMediusMeta(page []byte, property string) (string, bool) {
	for _, attr := range []string{"property", "name"} {
		pattern, err := regexp.Compile(`(?is)<meta[^>]+` + attr + `=["']` + regexp.QuoteMeta(property) + `["'][^>]+content=["']([^"']+)["']`)
		if err != nil {
			continue
		}
		match := pattern.FindSubmatch(page)
		if len(match) != 2 {
			continue
		}
		text := strings.TrimSpace(string(match[1]))
		if text != "" {
			return text, true
		}
	}
	return "", false
}

func microsoftMediusSubtitles(page []byte) map[string][]value.Value {
	subs := make(map[string][]value.Value)
	pattern := regexp.MustCompile(`(?is)const\s+captionsConfiguration\s*=\s*(\{.*?\})\s*;`)
	if match := pattern.FindSubmatch(page); len(match) == 2 {
		var cfg struct {
			LanguageList []struct {
				Src     string `json:"src"`
				SrcLang string `json:"srclang"`
				Kind    string `json:"kind"`
			} `json:"languageList"`
		}
		if err := json.Unmarshal(match[1], &cfg); err == nil {
			for _, item := range cfg.LanguageList {
				parsed, err := microsoftURLRole(item.Src, microsoftRoleCaption)
				if err != nil || !microsoftVTTPath.MatchString(parsed.String()) {
					continue
				}
				tag := strings.ToLower(item.SrcLang)
				if tag == "" || !microsoftLangPattern.MatchString(tag) {
					tag = "und"
				}
				if len(subs) >= microsoftMaxSubtitles {
					break
				}
				subs[tag] = append(subs[tag], value.ObjectValue(value.NewObject(
					value.Field{Key: "url", Value: value.String(parsed.String())},
					value.Field{Key: "ext", Value: value.String("vtt")},
					value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
				)))
			}
			if len(subs) > 0 {
				return subs
			}
		}
	}
	legacyOpen := regexp.MustCompile(`(?is)var\s+file\s*=\s*\{`)
	openIdx := legacyOpen.FindIndex(page)
	if openIdx == nil {
		return subs
	}
	contents := page[openIdx[1]:]
	closeIdx := bytes.IndexByte(contents, '}')
	if closeIdx < 0 {
		return subs
	}
	contents = contents[:closeIdx]
	urlPattern := regexp.MustCompile(`(?i)'(https://[^']+\.vtt\?[^']+)'`)
	matches := urlPattern.FindAllSubmatch(contents, microsoftMaxSubtitles+1)
	for _, match := range matches {
		if len(match) != 2 || len(subs) >= microsoftMaxSubtitles {
			continue
		}
		urlStr := string(match[1])
		parsed, err := microsoftURLRole(urlStr, microsoftRoleCaption)
		if err != nil {
			continue
		}
		bare := parsed.String()
		if i := strings.Index(bare, "?"); i >= 0 {
			bare = bare[:i]
		}
		basename := path.Base(bare)
		basename = strings.TrimSuffix(basename, ".vtt")
		basename = strings.TrimSuffix(basename, ".VTT")
		parts := strings.Split(basename, "_")
		tag := strings.ToLower(parts[len(parts)-1])
		if tag == "" || !microsoftLangPattern.MatchString(tag) {
			tag = "und"
		}
		subs[tag] = append(subs[tag], value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(parsed.String())},
			value.Field{Key: "ext", Value: value.String("vtt")},
			value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
		)))
	}
	return subs
}

// --- MicrosoftLearnPlaylist -----------------------------------------------

type MicrosoftLearnPlaylist struct{}

func NewMicrosoftLearnPlaylist() MicrosoftLearnPlaylist { return MicrosoftLearnPlaylist{} }
func (MicrosoftLearnPlaylist) Name() string             { return "microsoft_learn_playlist" }
func (MicrosoftLearnPlaylist) Suitable(parsed *url.URL) bool {
	return microsoftLearnPlaylistSuitable(parsed)
}

func (MicrosoftLearnPlaylist) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !microsoftLearnPlaylistSuitable(parsed) || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	locale := "en-us"
	var kind, slug string
	switch {
	case len(parts) == 2 && (parts[0] == "shows" || parts[0] == "events"):
		kind, slug = parts[0], parts[1]
	case len(parts) == 3 && microsoftLocalePattern.MatchString(parts[0]) && (parts[1] == "shows" || parts[1] == "events"):
		locale, kind, slug = parts[0], parts[1], parts[2]
	default:
		return Extraction{}, ErrUnsupported
	}
	if !microsoftSlugPattern.MatchString(slug) {
		return Extraction{}, fmt.Errorf("%w: invalid microsoft_learn_playlist slug", ErrInvalidMetadata)
	}
	if parsed.RawQuery != "" {
		return Extraction{}, fmt.Errorf("%w: microsoft_learn_playlist query must be empty", ErrInvalidMetadata)
	}
	page, err := microsoftIsolatedPage(ctx, request.Transport, http.MethodGet, parsed.String(), microsoftMaxWebpage, http.Header{
		"Accept": {"text/html,application/xhtml+xml"},
	})
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(slug)},
		value.Field{Key: "webpage_url", Value: value.String(parsed.String())},
	)
	if title, ok := microsoftMediusMeta(page, "og:title"); ok {
		info.Set("title", value.String(strings.TrimSpace(title)))
	}
	if desc, ok := microsoftMediusMeta(page, "og:description"); ok {
		info.Set("description", value.String(strings.TrimSpace(desc)))
	}
	subType := "episodes"
	if kind == "events" {
		subType = "sessions"
	}
	endpoint := microsoftLearnAPI + kind + "/" + slug + "/" + subType
	if title, _ := info.Lookup("title").StringValue(); title == "" {
		info.Set("title", value.String(slug))
	}
	return Playlist(value.NewInfo(info), microsoftLearnPlaylistEntries{
		transport: request.Transport,
		endpoint:  endpoint,
		locale:    locale,
		slug:      slug,
	})
}

func microsoftLearnPlaylistSuitable(parsed *url.URL) bool {
	if !microsoftExactHost(parsed, "learn.microsoft.com") {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	switch len(parts) {
	case 2:
		return (parts[0] == "shows" || parts[0] == "events") &&
			microsoftSlugPattern.MatchString(parts[1]) && parsed.RawQuery == ""
	case 3:
		return microsoftLocalePattern.MatchString(parts[0]) &&
			(parts[1] == "shows" || parts[1] == "events") &&
			microsoftSlugPattern.MatchString(parts[2]) && parsed.RawQuery == ""
	}
	return false
}

type microsoftLearnPlaylistEntries struct {
	transport Transport
	endpoint  string
	locale    string
	slug      string
}

func (entries microsoftLearnPlaylistEntries) Iterator() EntryIterator {
	return &microsoftLearnPlaylistIterator{entries: entries}
}

type microsoftLearnPlaylistIterator struct {
	entries    microsoftLearnPlaylistEntries
	skip       int
	pages      int
	seenURL    map[string]bool
	page       []Entry
	index      int
	terminated bool
	countTotal int
}

func (iterator *microsoftLearnPlaylistIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := contextError(ctx); err != nil {
		iterator.terminated = true
		return Entry{}, false, err
	}
	if iterator.seenURL == nil {
		iterator.seenURL = make(map[string]bool)
	}
	// Yield unyielded entries from a freshly materialized page before
	// checking the terminated flag. The flag is set during materialization
	// once skip has covered count, so we must drain the page first.
	for iterator.index >= len(iterator.page) {
		if iterator.terminated {
			return Entry{}, false, nil
		}
		if iterator.pages >= microsoftMaxPages {
			iterator.terminated = true
			return Entry{}, false, ErrPlaylistLimit
		}
		if err := contextError(ctx); err != nil {
			iterator.terminated = true
			return Entry{}, false, err
		}
		skip := iterator.skip
		endpoint, err := url.Parse(iterator.entries.endpoint)
		if err != nil {
			iterator.terminated = true
			return Entry{}, false, fmt.Errorf("%w: invalid microsoft_learn_playlist endpoint", ErrInvalidMetadata)
		}
		endpoint.RawQuery = "locale=" + url.QueryEscape(iterator.entries.locale) + "&%24skip=" + strconv.Itoa(skip)
		payload, err := microsoftIsolatedJSON(ctx, iterator.entries.transport, http.MethodGet, endpoint.String(), microsoftMaxJSONBytes, nil)
		if err != nil {
			iterator.terminated = true
			return Entry{}, false, err
		}
		var root struct {
			Count   int                          `json:"count"`
			Results []map[string]json.RawMessage `json:"results"`
		}
		if err := json.Unmarshal(payload, &root); err != nil {
			iterator.terminated = true
			return Entry{}, false, fmt.Errorf("%w: invalid microsoft_learn_playlist JSON", ErrInvalidMetadata)
		}
		if root.Count < 0 || root.Count > microsoftMaxEntries*2 {
			iterator.terminated = true
			return Entry{}, false, fmt.Errorf("%w: invalid microsoft_learn_playlist count", ErrInvalidMetadata)
		}
		rows := len(root.Results)
		iterator.pages++
		iterator.skip += rows
		iterator.countTotal = root.Count
		if rows == 0 {
			iterator.terminated = true
			return Entry{}, false, nil
		}
		iterator.page = iterator.page[:0]
		iterator.index = 0
		for _, raw := range root.Results {
			rawURL, ok := microsoftExtractString(raw, "url")
			if !ok || len(rawURL) == 0 || len(rawURL) > microsoftMaxThumbURLBytes {
				continue
			}
			var absolute string
			switch {
			case strings.HasPrefix(rawURL, "https://"):
				absolute = rawURL
			case strings.HasPrefix(rawURL, "http://"):
				absolute = ""
			case strings.HasPrefix(rawURL, "/"):
				absolute = "https://learn.microsoft.com" + rawURL
			default:
				continue
			}
			parsed, err := url.Parse(absolute)
			if err != nil {
				continue
			}
			if !microsoftLearnChildSuitable(parsed) {
				continue
			}
			canonical := parsed.String()
			if iterator.seenURL[canonical] {
				continue
			}
			iterator.seenURL[canonical] = true
			iterator.page = append(iterator.page, Entry{URL: canonical, Transparent: true})
			if len(iterator.page) >= microsoftMaxEntries {
				break
			}
		}
		if iterator.skip >= iterator.countTotal {
			iterator.terminated = true
		}
	}
	entry := iterator.page[iterator.index]
	iterator.index++
	return entry, true, nil
}

func microsoftLearnChildSuitable(parsed *url.URL) bool {
	if !microsoftExactHost(parsed, "learn.microsoft.com") {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	switch len(parts) {
	case 3:
		return (parts[0] == "shows" || parts[0] == "events") &&
			microsoftSlugPattern.MatchString(parts[1]) &&
			microsoftLearnIDPattern.MatchString(parts[2])
	case 4:
		return microsoftLocalePattern.MatchString(parts[0]) &&
			(parts[1] == "shows" || parts[1] == "events") &&
			microsoftSlugPattern.MatchString(parts[2]) && microsoftLearnIDPattern.MatchString(parts[3])
	}
	return false
}

// --- MicrosoftLearnEpisode -----------------------------------------------

type MicrosoftLearnEpisode struct{}

func NewMicrosoftLearnEpisode() MicrosoftLearnEpisode { return MicrosoftLearnEpisode{} }
func (MicrosoftLearnEpisode) Name() string            { return "microsoft_learn_episode" }
func (MicrosoftLearnEpisode) Suitable(parsed *url.URL) bool {
	return microsoftLearnEpisodeSuitable(parsed)
}

func (MicrosoftLearnEpisode) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !microsoftLearnEpisodeSuitable(parsed) || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	episodeID := parts[len(parts)-1]
	if !microsoftLearnIDPattern.MatchString(episodeID) {
		return Extraction{}, fmt.Errorf("%w: invalid microsoft_learn_episode id", ErrInvalidMetadata)
	}
	page, err := microsoftIsolatedPage(ctx, request.Transport, http.MethodGet, parsed.String(), microsoftMaxWebpage, http.Header{
		"Accept": {"text/html,application/xhtml+xml"},
	})
	if err != nil {
		return Extraction{}, err
	}
	entryID, ok := microsoftMediusMeta(page, "entryId")
	if !ok || !microsoftUUIDPattern.MatchString(entryID) {
		return Extraction{}, fmt.Errorf("%w: missing microsoft_learn_episode entryId", ErrInvalidMetadata)
	}
	videoURL := microsoftLearnVID + entryID
	payload, err := microsoftIsolatedJSON(ctx, request.Transport, http.MethodGet, videoURL, microsoftMaxJSONBytes, nil)
	if err != nil {
		return Extraction{}, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(payload, &root); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid microsoft_learn_episode JSON", ErrInvalidMetadata)
	}
	publicRaw, ok := root["publicVideo"]
	if !ok {
		return Extraction{}, fmt.Errorf("%w: missing microsoft_learn_episode publicVideo", ErrInvalidMetadata)
	}
	var publicVideo map[string]json.RawMessage
	if err := json.Unmarshal(publicRaw, &publicVideo); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid microsoft_learn_episode publicVideo", ErrInvalidMetadata)
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(entryID)},
		value.Field{Key: "extractor", Value: value.String("microsoft_learn_episode")},
		value.Field{Key: "webpage_url", Value: value.String(parsed.String())},
	)
	if title, ok := microsoftMediusMeta(page, "og:title"); ok {
		info.Set("title", value.String(strings.TrimSpace(title)))
	}
	if desc, ok := microsoftMediusMeta(page, "og:description"); ok {
		info.Set("description", value.String(strings.TrimSpace(desc)))
	}
	if title, _ := info.Lookup("title").StringValue(); title == "" {
		return Extraction{}, fmt.Errorf("%w: missing microsoft_learn_episode title", ErrInvalidMetadata)
	}
	formats := microsoftLearnEpisodeFormats(publicVideo)
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: missing microsoft_learn_episode formats", ErrInvalidMetadata)
	}
	info.Set("formats", value.List(formats...))
	if rawCaptions, ok := publicVideo["captions"]; ok {
		if object := microsoftLearnEpisodeCaptions(rawCaptions); object != nil {
			info.Set("subtitles", value.ObjectValue(object))
		}
	}
	if rawThumbs, ok := publicVideo["thumbnailOtherSizes"]; ok {
		if thumbs := microsoftLearnEpisodeThumbnails(rawThumbs); len(thumbs) > 0 {
			info.Set("thumbnails", value.List(thumbs...))
			if first, ok := thumbs[0].Object(); ok {
				if thumbURL, ok := first.Lookup("url").StringValue(); ok {
					info.Set("thumbnail", value.String(thumbURL))
				}
			}
		}
	}
	if rawTime, ok := root["createTime"]; ok {
		if ts := microsoftFlexibleTimestamp(rawTime); ts > 0 {
			info.Set("timestamp", value.Int(ts))
			info.Set("upload_date", value.String(time.Unix(ts, 0).UTC().Format("20060102")))
		}
	}
	return Media(value.NewInfo(info)), nil
}

func microsoftLearnEpisodeSuitable(parsed *url.URL) bool {
	if !microsoftExactHost(parsed, "learn.microsoft.com") {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	switch len(parts) {
	case 3:
		return parts[0] == "shows" && microsoftSlugPattern.MatchString(parts[1]) &&
			microsoftLearnIDPattern.MatchString(parts[2]) && parsed.RawQuery == ""
	case 4:
		return microsoftLocalePattern.MatchString(parts[0]) && parts[1] == "shows" &&
			microsoftSlugPattern.MatchString(parts[2]) && microsoftLearnIDPattern.MatchString(parts[3]) &&
			parsed.RawQuery == ""
	}
	return false
}

func microsoftLearnEpisodeFormats(publicVideo map[string]json.RawMessage) []value.Value {
	formats := make([]value.Value, 0, microsoftMaxFormats)
	seen := make(map[string]bool, microsoftMaxFormats)
	emitManifest := func(key, formatID, protocol string, role microsoftMediaRole) {
		if key == "" {
			return
		}
		rawURL, ok := microsoftExtractString(publicVideo, key)
		if !ok {
			return
		}
		parsed, err := microsoftURLRole(rawURL, role)
		if err != nil || seen[parsed.String()] {
			return
		}
		seen[parsed.String()] = true
		format := manifestFormat(formatID, parsed.String(), protocol)
		format.Set("_credential_isolated", value.Bool(true))
		formats = append(formats, value.ObjectValue(format))
	}
	emitManifest("adaptiveVideoUrl", "ism", "ism", microsoftRoleManifest)
	emitManifest("adaptiveVideoHLSUrl", "hls", "m3u8_native", microsoftRoleManifest)
	emitManifest("adaptiveVideoDashUrl", "dash", "http_dash_segments", microsoftRoleManifest)
	for _, tier := range []string{"low", "medium", "high"} {
		key := tier + "QualityVideoUrl"
		rawURL, ok := microsoftExtractString(publicVideo, key)
		if !ok {
			continue
		}
		parsed, err := microsoftURLRole(rawURL, microsoftRoleMedia)
		if err != nil || seen[parsed.String()] {
			continue
		}
		seen[parsed.String()] = true
		format := value.NewObject(
			value.Field{Key: "format_id", Value: value.String("video-http-" + tier)},
			value.Field{Key: "url", Value: value.String(parsed.String())},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "protocol", Value: value.String("https")},
			value.Field{Key: "acodec", Value: value.String("none")},
			value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
		)
		formats = append(formats, value.ObjectValue(format))
	}
	if rawURL, ok := microsoftExtractString(publicVideo, "audioUrl"); ok {
		if parsed, err := microsoftURLRole(rawURL, microsoftRoleMedia); err == nil && !seen[parsed.String()] {
			seen[parsed.String()] = true
			format := value.NewObject(
				value.Field{Key: "format_id", Value: value.String("audio-http")},
				value.Field{Key: "url", Value: value.String(parsed.String())},
				value.Field{Key: "ext", Value: value.String("mp3")},
				value.Field{Key: "protocol", Value: value.String("https")},
				value.Field{Key: "vcodec", Value: value.String("none")},
				value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
			)
			formats = append(formats, value.ObjectValue(format))
		}
	}
	return formats
}

func microsoftLearnEpisodeCaptions(raw json.RawMessage) *value.Object {
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 || len(list) > microsoftMaxSubtitles {
		return nil
	}
	object := value.NewObject()
	emitted := 0
	for _, raw := range list {
		lang, _ := microsoftExtractString(raw, "language")
		lang = strings.ToLower(lang)
		if !microsoftLangPattern.MatchString(lang) {
			lang = "und"
		}
		captionURL, _ := microsoftExtractString(raw, "url")
		parsed, err := microsoftURLRole(captionURL, microsoftRoleCaption)
		if err != nil || !microsoftVTTPath.MatchString(parsed.String()) {
			continue
		}
		object.Set(lang, value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(parsed.String())},
			value.Field{Key: "ext", Value: value.String("vtt")},
			value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
		))))
		emitted++
	}
	if emitted == 0 {
		return nil
	}
	return object
}

func microsoftLearnEpisodeThumbnails(raw json.RawMessage) []value.Value {
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil || len(list) == 0 {
		return nil
	}
	thumbs := make([]value.Value, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, raw := range list {
		if len(thumbs) >= microsoftMaxThumbnails {
			break
		}
		thumbURL, _ := microsoftExtractString(raw, "url")
		parsed, err := microsoftURLRole(thumbURL, microsoftRoleThumbnail)
		if err != nil || seen[parsed.String()] {
			continue
		}
		seen[parsed.String()] = true
		thumb := value.NewObject(value.Field{Key: "url", Value: value.String(parsed.String())})
		thumb.Set("_credential_isolated", value.Bool(true))
		thumbs = append(thumbs, value.ObjectValue(thumb))
	}
	return thumbs
}

// --- MicrosoftLearnSession -----------------------------------------------

type MicrosoftLearnSession struct{}

func NewMicrosoftLearnSession() MicrosoftLearnSession { return MicrosoftLearnSession{} }
func (MicrosoftLearnSession) Name() string            { return "microsoft_learn_session" }
func (MicrosoftLearnSession) Suitable(parsed *url.URL) bool {
	return microsoftLearnSessionSuitable(parsed)
}

func (MicrosoftLearnSession) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !microsoftLearnSessionSuitable(parsed) || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	sessionID := parts[len(parts)-1]
	if !microsoftLearnIDPattern.MatchString(sessionID) {
		return Extraction{}, fmt.Errorf("%w: invalid microsoft_learn_session id", ErrInvalidMetadata)
	}
	page, err := microsoftIsolatedPage(ctx, request.Transport, http.MethodGet, parsed.String(), microsoftMaxWebpage, http.Header{
		"Accept": {"text/html,application/xhtml+xml"},
	})
	if err != nil {
		return Extraction{}, err
	}
	externalRaw, ok := microsoftMediusMeta(page, "externalVideoUrl")
	if !ok {
		return Extraction{}, fmt.Errorf("%w: missing microsoft_learn_session externalVideoUrl", ErrInvalidMetadata)
	}
	externalParsed, err := url.Parse(externalRaw)
	if err != nil || !microsoftMediusSuitable(externalParsed) {
		return Extraction{}, fmt.Errorf("%w: microsoft_learn_session externalVideoUrl does not satisfy microsoft_medius route", ErrInvalidMetadata)
	}
	externalID, _, err := microsoftMediusID(externalParsed)
	if err != nil {
		return Extraction{}, err
	}
	entry := Entry{
		URL: externalParsed.String(), ExtractorKey: "microsoft_medius", Transparent: true,
		ID: externalID, Title: sessionID,
		Referer: parsed.String(),
	}
	if startDate, ok := microsoftMediusMeta(page, "startDate"); ok {
		if ts := riskTimestamp(startDate); ts > 0 {
			entry.Timestamp = ts
			entry.HasTimestamp = true
		}
	}
	return URLResult(entry)
}

func microsoftLearnSessionSuitable(parsed *url.URL) bool {
	if !microsoftExactHost(parsed, "learn.microsoft.com") {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	switch len(parts) {
	case 3:
		return parts[0] == "events" && microsoftSlugPattern.MatchString(parts[1]) &&
			microsoftLearnIDPattern.MatchString(parts[2]) && parsed.RawQuery == ""
	case 4:
		return microsoftLocalePattern.MatchString(parts[0]) && parts[1] == "events" &&
			microsoftSlugPattern.MatchString(parts[2]) && microsoftLearnIDPattern.MatchString(parts[3]) &&
			parsed.RawQuery == ""
	}
	return false
}

// --- MicrosoftBuild ------------------------------------------------------

type MicrosoftBuild struct{}

func NewMicrosoftBuild() MicrosoftBuild              { return MicrosoftBuild{} }
func (MicrosoftBuild) Name() string                  { return "microsoft_build" }
func (MicrosoftBuild) Suitable(parsed *url.URL) bool { return microsoftBuildSuitable(parsed) }

func (MicrosoftBuild) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !microsoftBuildSuitable(parsed) || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	tail, err := microsoftBuildTail(parsed)
	if err != nil {
		return Extraction{}, err
	}
	payload, err := microsoftIsolatedJSON(ctx, request.Transport, http.MethodGet, microsoftBuildAPI, microsoftMaxJSONBytes, nil)
	if err != nil {
		return Extraction{}, err
	}
	var sessions []map[string]json.RawMessage
	if err := json.Unmarshal(payload, &sessions); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid microsoft_build JSON", ErrInvalidMetadata)
	}
	if len(sessions) > microsoftMaxEntries {
		return Extraction{}, fmt.Errorf("%w: too many microsoft_build sessions", ErrInvalidMetadata)
	}
	if tail == "sessions" {
		return microsoftBuildPlaylist(parsed, sessions)
	}
	return microsoftBuildSingle(parsed, sessions, tail)
}

func microsoftBuildSuitable(parsed *url.URL) bool {
	if !microsoftExactHost(parsed, "build.microsoft.com") {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	switch len(parts) {
	case 2:
		return microsoftLocalePattern.MatchString(parts[0]) && parts[1] == "sessions" &&
			microsoftBuildQueryOK(parsed)
	case 3:
		return microsoftLocalePattern.MatchString(parts[0]) && parts[1] == "sessions" &&
			microsoftUUIDPattern.MatchString(parts[2]) && microsoftBuildQueryOK(parsed)
	}
	return false
}

func microsoftBuildQueryOK(parsed *url.URL) bool {
	if parsed.RawQuery == "" {
		return true
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return false
	}
	if len(values) != 1 {
		return false
	}
	sources, ok := values["source"]
	if !ok || len(sources) != 1 || sources[0] != "sessions" {
		return false
	}
	return true
}

func microsoftBuildTail(parsed *url.URL) (string, error) {
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) == 2 {
		return parts[1], nil
	}
	return parts[2], nil
}

func microsoftBuildPlaylist(parsed *url.URL, sessions []map[string]json.RawMessage) (Extraction, error) {
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String("sessions")},
		value.Field{Key: "webpage_url", Value: value.String(parsed.String())},
	)
	entries := make([]Entry, 0, len(sessions))
	seen := make(map[string]bool, len(sessions))
	for _, raw := range sessions {
		if len(entries) >= microsoftMaxEntries {
			break
		}
		sessionID, ok := microsoftExtractString(raw, "sessionId")
		if !ok || !microsoftUUIDPattern.MatchString(sessionID) || seen[sessionID] {
			continue
		}
		ondemandRaw, ok := microsoftExtractString(raw, "onDemand")
		if !ok {
			continue
		}
		ondemandParsed, err := url.Parse(ondemandRaw)
		if err != nil || !microsoftMediusSuitable(ondemandParsed) {
			continue
		}
		mediusID, _, err := microsoftMediusID(ondemandParsed)
		if err != nil {
			continue
		}
		seen[sessionID] = true
		title, _ := microsoftExtractString(raw, "title")
		entry := Entry{
			URL: ondemandParsed.String(), ExtractorKey: "microsoft_medius", Transparent: true,
			ID: mediusID, Title: strings.TrimSpace(title),
			Referer: parsed.String(),
		}
		if ts := microsoftBuildSessionTimestamp(raw); ts > 0 {
			entry.Timestamp = ts
			entry.HasTimestamp = true
		}
		entries = append(entries, entry)
	}
	if len(entries) == 0 {
		return Extraction{}, fmt.Errorf("%w: no valid microsoft_build sessions", ErrInvalidMetadata)
	}
	return Playlist(value.NewInfo(info), StaticEntries(entries...))
}

func microsoftBuildSingle(parsed *url.URL, sessions []map[string]json.RawMessage, tail string) (Extraction, error) {
	for _, raw := range sessions {
		sessionID, ok := microsoftExtractString(raw, "sessionId")
		if !ok || sessionID != tail {
			continue
		}
		ondemandRaw, ok := microsoftExtractString(raw, "onDemand")
		if !ok {
			return Extraction{}, fmt.Errorf("%w: microsoft_build onDemand missing", ErrInvalidMetadata)
		}
		ondemandParsed, err := url.Parse(ondemandRaw)
		if err != nil || !microsoftMediusSuitable(ondemandParsed) {
			return Extraction{}, fmt.Errorf("%w: microsoft_build onDemand does not satisfy microsoft_medius route", ErrInvalidMetadata)
		}
		mediusID, _, err := microsoftMediusID(ondemandParsed)
		if err != nil {
			return Extraction{}, err
		}
		title, _ := microsoftExtractString(raw, "title")
		entry := Entry{
			URL: ondemandParsed.String(), ExtractorKey: "microsoft_medius", Transparent: true,
			ID: mediusID, Title: strings.TrimSpace(title),
			Referer: parsed.String(),
		}
		if ts := microsoftBuildSessionTimestamp(raw); ts > 0 {
			entry.Timestamp = ts
			entry.HasTimestamp = true
		}
		return URLResult(entry)
	}
	return Extraction{}, fmt.Errorf("%w: microsoft_build session not found", ErrInvalidMetadata)
}

func microsoftBuildSessionTimestamp(raw map[string]json.RawMessage) int64 {
	ts, ok := microsoftExtractString(raw, "startDateTime")
	if !ok {
		return 0
	}
	return riskTimestamp(ts)
}

// --- Transport helpers ----------------------------------------------------

func microsoftIsolatedJSON(ctx context.Context, transport Transport, method, rawURL string, maxBytes int64, headers http.Header) ([]byte, error) {
	if transport == nil {
		return nil, ErrTransportIsolation
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid microsoft request", ErrInvalidMetadata)
	}
	if headers != nil {
		request.Header = headers.Clone()
	}
	execute := isolated.DoWithoutCredentialsNoRedirect
	if request.Header.Get("Referer") != "" {
		refererIsolated, ok := transport.(RefererCredentialIsolatedNoRedirectTransport)
		if !ok {
			return nil, ErrTransportIsolation
		}
		execute = refererIsolated.DoWithoutCredentialsNoRedirectWithReferer
	}
	response, err := execute(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("%w: empty microsoft response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	if _, err := microsoftCategorizeStatus(response.StatusCode); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read microsoft response failed", ErrInvalidMetadata)
	}
	if int64(len(body)) > maxBytes {
		return nil, ErrJSONResponseTooLarge
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%w: empty microsoft response body", ErrInvalidMetadata)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var probe any
	if err := decoder.Decode(&probe); err != nil {
		return nil, fmt.Errorf("%w: invalid microsoft JSON", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%w: trailing microsoft JSON", ErrInvalidMetadata)
	}
	return trimmed, nil
}

func microsoftIsolatedPage(ctx context.Context, transport Transport, method, rawURL string, maxBytes int64, headers http.Header) ([]byte, error) {
	if transport == nil {
		return nil, ErrTransportIsolation
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid microsoft page request", ErrInvalidMetadata)
	}
	if headers != nil {
		request.Header = headers.Clone()
	}
	execute := isolated.DoWithoutCredentialsNoRedirect
	if request.Header.Get("Referer") != "" {
		refererIsolated, ok := transport.(RefererCredentialIsolatedNoRedirectTransport)
		if !ok {
			return nil, ErrTransportIsolation
		}
		execute = refererIsolated.DoWithoutCredentialsNoRedirectWithReferer
	}
	response, err := execute(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("%w: empty microsoft page response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	if _, err := microsoftCategorizeStatus(response.StatusCode); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read microsoft page failed", ErrInvalidMetadata)
	}
	if int64(len(body)) > maxBytes {
		return nil, ErrJSONResponseTooLarge
	}
	return body, nil
}

func microsoftCategorizeStatus(status int) (string, error) {
	switch {
	case status == http.StatusUnauthorized:
		return "auth", ErrAuthentication
	case status == http.StatusForbidden:
		return "region", ErrRegionRestricted
	case status == http.StatusNotFound, status == http.StatusGone:
		return "unavailable", ErrUnavailable
	case status == http.StatusUnavailableForLegalReasons:
		return "legal", ErrRegionRestricted
	case status >= 300 && status < 400:
		// The no-redirect contract requires the server to never return a
		// 3xx redirect for microsoft.com, medius.microsoft.com,
		// learn.microsoft.com, or build.microsoft.com. A redirect would
		// either leak ambient credentials to an unrelated host or signal
		// a contract violation. Fail closed with a typed, secret-safe
		// sentinel so callers can distinguish transport drift.
		return "redirect", fmt.Errorf("%w: microsoft media API returned redirect", ErrInvalidMetadata)
	}
	if status >= 400 {
		return "transport", fmt.Errorf("microsoft media API: HTTP status %d", status)
	}
	return "", nil
}

// --- JSON helpers ---------------------------------------------------------

func microsoftExtractString(raw map[string]json.RawMessage, key string) (string, bool) {
	if raw == nil {
		return "", false
	}
	value, ok := raw[key]
	if !ok || len(value) == 0 || string(value) == "null" {
		return "", false
	}
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		trimmed := strings.TrimSpace(text)
		if trimmed != "" {
			return trimmed, true
		}
	}
	return "", false
}

func microsoftExtractInt(raw map[string]json.RawMessage, key string) (int64, bool) {
	if raw == nil {
		return 0, false
	}
	value, ok := raw[key]
	if !ok || len(value) == 0 || string(value) == "null" {
		return 0, false
	}
	var number json.Number
	if err := json.Unmarshal(value, &number); err == nil {
		if n, err := number.Int64(); err == nil {
			return n, true
		}
		if f, err := number.Float64(); err == nil {
			return int64(f), true
		}
	}
	var text string
	if err := json.Unmarshal(value, &text); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func microsoftFlexibleInt(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		if n, err := number.Int64(); err == nil {
			return n
		}
		if f, err := number.Float64(); err == nil {
			return int64(f)
		}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func microsoftAkamaiTimestamp(text string) int64 {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	if strings.HasPrefix(text, "/Date(") && strings.HasSuffix(text, ")/") {
		body := strings.TrimSuffix(strings.TrimPrefix(text, "/Date("), ")/")
		if n, err := strconv.ParseInt(strings.TrimSpace(body), 10, 64); err == nil {
			return n / 1000
		}
	}
	if ts := riskTimestamp(text); ts > 0 {
		return ts
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil && n > 0 {
		return n
	}
	return 0
}

func microsoftFlexibleTimestamp(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return riskTimestamp(text)
	}
	return 0
}
