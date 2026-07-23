package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const vimeoImpersonationProfile = "chrome-133"

const (
	vimeoMaxTextTracks = 128
	vimeoMaxTextURL    = 8192
	vimeoMaxTextLang   = 64
	vimeoMaxTextName   = 256
	vimeoMaxConfigURL  = 8192
)

var (
	vimeoURLPattern       = regexp.MustCompile(`^/(?:video/)?([0-9]+)(?:/)?$`)
	vimeoConfigURLPattern = regexp.MustCompile(`(?i)\bdata-config-url=["']([^"']+)`)
)

type Vimeo struct{}

func NewVimeo() Vimeo { return Vimeo{} }

func (Vimeo) Name() string { return "vimeo" }

func (Vimeo) Suitable(parsed *url.URL) bool {
	host := strings.ToLower(parsed.Hostname())
	return (host == "vimeo.com" || host == "www.vimeo.com" || host == "player.vimeo.com") && vimeoURLPattern.MatchString(parsed.Path)
}

func (Vimeo) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	match := vimeoURLPattern.FindStringSubmatch(parsed.Path)
	if len(match) != 2 {
		return Extraction{}, ErrUnsupported
	}
	videoID := match[1]
	// Never reflect caller query credentials into the webpage request or the
	// config Referer. The numeric identity is all this bounded route needs.
	webpageURL := "https://vimeo.com/" + videoID
	page, _, err := ReadPageWithProfile(ctx, request.Transport, webpageURL, vimeoImpersonationProfile)
	if err != nil {
		return Extraction{}, err
	}
	config, err := extractVimeoConfig(ctx, request.Transport, webpageURL, page)
	if err != nil {
		return Extraction{}, err
	}
	return parseVimeoConfigContext(ctx, config, videoID, webpageURL)
}

func extractVimeoConfig(ctx context.Context, transport Transport, webpageURL string, page []byte) (vimeoConfig, error) {
	if raw, err := extractJSONObject(page, "playerConfig"); err == nil {
		var config vimeoConfig
		if json.Unmarshal(raw, &config) != nil {
			return vimeoConfig{}, fmt.Errorf("%w: Vimeo player config", ErrInvalidMetadata)
		}
		return config, nil
	}
	configURL := ""
	if match := vimeoConfigURLPattern.FindSubmatch(page); len(match) == 2 {
		configURL = html.UnescapeString(string(match[1]))
	}
	if configURL == "" {
		for _, marker := range []string{"vimeo.clip_page_config", "vimeo.vod_title_page_config"} {
			raw, err := extractJSONObject(page, marker)
			if err != nil {
				continue
			}
			var pageConfig struct {
				Player struct {
					ConfigURL string `json:"config_url"`
				} `json:"player"`
			}
			if json.Unmarshal(raw, &pageConfig) == nil {
				configURL = pageConfig.Player.ConfigURL
			}
			break
		}
	}
	if configURL == "" {
		lower := strings.ToLower(string(page))
		if strings.Contains(lower, "privacy settings") || strings.Contains(lower, "password") || strings.Contains(lower, "log in") {
			return vimeoConfig{}, ErrAuthentication
		}
		return vimeoConfig{}, fmt.Errorf("%w: missing Vimeo config", ErrInvalidMetadata)
	}
	configURL, ok := normalizeVimeoConfigURL(configURL)
	if !ok {
		// Do not include the untrusted URL: config URLs commonly carry tokens.
		return vimeoConfig{}, fmt.Errorf("%w: unsafe Vimeo config URL", ErrInvalidMetadata)
	}
	headers := make(http.Header)
	headers.Set("Referer", webpageURL)
	var config vimeoConfig
	if err := RequestJSON(ctx, transport, http.MethodGet, configURL, nil, headers, &config); err != nil {
		var status *HTTPStatusError
		if errors.As(err, &status) {
			switch status.Code {
			case http.StatusUnauthorized, http.StatusForbidden:
				return vimeoConfig{}, ErrAuthentication
			case http.StatusNotFound, http.StatusGone:
				return vimeoConfig{}, ErrUnavailable
			}
		}
		return vimeoConfig{}, err
	}
	return config, nil
}

// normalizeVimeoConfigURL permits only Vimeo's public player-config origin.
// It intentionally preserves the query because that is where public config
// tokens live, while rejecting every path encoding that could alter routing.
func normalizeVimeoConfigURL(rawURL string) (string, bool) {
	if len(rawURL) == 0 || len(rawURL) > vimeoMaxConfigURL || strings.ContainsAny(rawURL, "\\\x00\r\n") {
		return "", false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Path == "" || strings.ToLower(parsed.Hostname()) != "player.vimeo.com" {
		return "", false
	}
	if vimeoUnsafePath(parsed) {
		return "", false
	}
	parsed.Scheme = "https"
	parsed.Host = "player.vimeo.com"
	return parsed.String(), true
}

type vimeoConfig struct {
	View  int `json:"view"`
	Video struct {
		ID          json.Number       `json:"id"`
		Title       string            `json:"title"`
		Description string            `json:"description"`
		Duration    int64             `json:"duration"`
		Width       int64             `json:"width"`
		Height      int64             `json:"height"`
		Thumbs      map[string]string `json:"thumbs"`
		Owner       struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"owner"`
		LiveEvent struct {
			Status string `json:"status"`
		} `json:"live_event"`
		Files vimeoFiles `json:"files"`
	} `json:"video"`
	Request struct {
		Files      vimeoFiles       `json:"files"`
		TextTracks []vimeoTextTrack `json:"text_tracks"`
	} `json:"request"`
}

// vimeoTextTrack is the public player-config shape used for manually supplied
// captions. The pinned yt-dlp implementation uses lang and url; label/name and
// kind are accepted only to make the normalized result useful and safe.
type vimeoTextTrack struct {
	URL      string `json:"url"`
	Language string `json:"lang"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Name     string `json:"name"`
}

type vimeoFiles struct {
	Progressive []struct {
		URL     string `json:"url"`
		Quality string `json:"quality"`
		Width   int64  `json:"width"`
		Height  int64  `json:"height"`
		FPS     int64  `json:"fps"`
		Bitrate int64  `json:"bitrate"`
	} `json:"progressive"`
	HLS struct {
		CDNs map[string]struct {
			URL string `json:"url"`
		} `json:"cdns"`
	} `json:"hls"`
	DASH struct {
		CDNs map[string]struct {
			URL string `json:"url"`
		} `json:"cdns"`
	} `json:"dash"`
}

func parseVimeoConfig(config vimeoConfig, videoID, webpageURL string) (Extraction, error) {
	return parseVimeoConfigContext(context.Background(), config, videoID, webpageURL)
}

func parseVimeoConfigContext(ctx context.Context, config vimeoConfig, videoID, webpageURL string) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if config.View == 4 {
		return Extraction{}, ErrAuthentication
	}
	if config.Video.Title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Vimeo title", ErrInvalidMetadata)
	}
	files := config.Video.Files
	if len(files.Progressive) == 0 && len(files.HLS.CDNs) == 0 && len(files.DASH.CDNs) == 0 {
		files = config.Request.Files
	}
	formats := make([]value.Value, 0, len(files.Progressive)+len(files.HLS.CDNs)+len(files.DASH.CDNs))
	for _, format := range files.Progressive {
		if err := contextError(ctx); err != nil {
			return Extraction{}, err
		}
		if !validHTTPURL(format.URL) {
			continue
		}
		extension := strings.TrimPrefix(path.Ext(mustURLPath(format.URL)), ".")
		if extension == "" {
			extension = "mp4"
		}
		object := value.NewObject(
			value.Field{Key: "format_id", Value: value.String("http-" + format.Quality)},
			value.Field{Key: "url", Value: value.String(format.URL)},
			value.Field{Key: "ext", Value: value.String(extension)},
		)
		setPositiveInt(object, "width", format.Width)
		setPositiveInt(object, "height", format.Height)
		setPositiveInt(object, "fps", format.FPS)
		if format.Bitrate > 0 {
			object.Set("tbr", value.Float(float64(format.Bitrate)))
		}
		formats = append(formats, value.ObjectValue(object))
	}
	for _, name := range sortedVimeoCDNs(files.HLS.CDNs) {
		if err := contextError(ctx); err != nil {
			return Extraction{}, err
		}
		cdn := files.HLS.CDNs[name]
		if validHTTPURL(cdn.URL) {
			formats = append(formats, value.ObjectValue(manifestFormat("hls-"+name, cdn.URL, "m3u8_native")))
		}
	}
	for _, name := range sortedVimeoCDNs(files.DASH.CDNs) {
		if err := contextError(ctx); err != nil {
			return Extraction{}, err
		}
		cdn := files.DASH.CDNs[name]
		if validHTTPURL(cdn.URL) {
			manifestURL := strings.Replace(cdn.URL, "/master.json", "/master.mpd", 1)
			formats = append(formats, value.ObjectValue(manifestFormat("dash-"+name, manifestURL, "http_dash_segments")))
		}
	}
	liveStatus := map[string]string{"pending": "is_upcoming", "active": "is_upcoming", "started": "is_live", "ended": "post_live"}[config.Video.LiveEvent.Status]
	if len(formats) == 0 {
		if liveStatus == "is_upcoming" {
			return Extraction{}, ErrUnavailable
		}
		return Extraction{}, fmt.Errorf("%w: no Vimeo formats", ErrInvalidMetadata)
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "title", Value: value.String(config.Video.Title)},
		value.Field{Key: "description", Value: value.String(config.Video.Description)},
		value.Field{Key: "uploader", Value: value.String(config.Video.Owner.Name)},
		value.Field{Key: "uploader_url", Value: value.String(config.Video.Owner.URL)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	setPositiveInt(info, "duration", config.Video.Duration)
	setPositiveInt(info, "width", config.Video.Width)
	setPositiveInt(info, "height", config.Video.Height)
	if thumbnail := bestVimeoThumbnail(config.Video.Thumbs); thumbnail != "" {
		info.Set("thumbnail", value.String(thumbnail))
	}
	if liveStatus != "" {
		info.Set("live_status", value.String(liveStatus))
	}
	if subtitles, err := vimeoSubtitles(ctx, config.Request.TextTracks); err != nil {
		return Extraction{}, err
	} else if subtitles.Len() != 0 {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	return Media(value.NewInfo(info)), nil
}

func vimeoSubtitles(ctx context.Context, tracks []vimeoTextTrack) (*value.Object, error) {
	if len(tracks) > vimeoMaxTextTracks {
		return nil, fmt.Errorf("%w: Vimeo text-track limit", ErrInvalidMetadata)
	}
	grouped := make(map[string][]value.Value)
	order := make([]string, 0, len(tracks))
	seen := make(map[string]struct{}, len(tracks))
	for _, track := range tracks {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		language := boundedVimeoText(track.Language, vimeoMaxTextLang)
		if !validVimeoLanguage(language) || !validVimeoTextKind(track.Kind) {
			continue
		}
		trackURL := normalizeVimeoTextTrackURL(track.URL)
		if trackURL == "" {
			continue
		}
		name := boundedVimeoText(track.Label, vimeoMaxTextName)
		if name == "" {
			name = boundedVimeoText(track.Name, vimeoMaxTextName)
		}
		// A URL is the stable identity of a declared text format. Labels are
		// presentation metadata and must not manufacture duplicate downloads.
		key := language + "\x00" + trackURL
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		if _, exists := grouped[language]; !exists {
			order = append(order, language)
		}
		entry := value.NewObject(
			value.Field{Key: "url", Value: value.String(trackURL)},
			value.Field{Key: "ext", Value: value.String("vtt")},
		)
		if name != "" {
			entry.Set("name", value.String(name))
		}
		grouped[language] = append(grouped[language], value.ObjectValue(entry))
	}
	result := value.NewObject()
	for _, language := range order {
		result.Set(language, value.List(grouped[language]...))
	}
	return result, nil
}

func boundedVimeoText(input string, limit int) string {
	input = strings.TrimSpace(input)
	if input == "" || len(input) > limit || strings.ContainsRune(input, '\x00') {
		return ""
	}
	return input
}

func validVimeoLanguage(language string) bool {
	if language == "" || len(language) > vimeoMaxTextLang {
		return false
	}
	for index, character := range language {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || (index > 0 && (character == '.' || character == '_' || character == '-'))) {
			return false
		}
	}
	return true
}

func validVimeoTextKind(kind string) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	return kind == "" || kind == "subtitles" || kind == "captions"
}

// normalizeVimeoTextTrackURL mirrors the reference's player.vimeo.com URL
// join, but fails closed: subtitle tokens never leave the player origin.
func normalizeVimeoTextTrackURL(rawURL string) string {
	if len(rawURL) == 0 || len(rawURL) > vimeoMaxTextURL || strings.ContainsAny(rawURL, "\\\x00\r\n") {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || vimeoUnsafePath(parsed) {
		return ""
	}
	base, _ := url.Parse("https://player.vimeo.com/")
	parsed = base.ResolveReference(parsed)
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || strings.ToLower(parsed.Hostname()) != "player.vimeo.com" || vimeoUnsafePath(parsed) {
		return ""
	}
	parsed.Scheme = "https"
	parsed.Host = "player.vimeo.com"
	result := parsed.String()
	if len(result) > vimeoMaxTextURL {
		return ""
	}
	return result
}

func vimeoUnsafePath(parsed *url.URL) bool {
	if parsed == nil || parsed.RawPath != "" || parsed.Path == "" || path.Clean(parsed.Path) != parsed.Path {
		return true
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	return strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") || strings.Contains(escaped, "%2e") || strings.Contains(escaped, "%25") || strings.Contains(parsed.String(), "\x00")
}

func sortedVimeoCDNs(cdns map[string]struct {
	URL string `json:"url"`
}) []string {
	names := make([]string, 0, len(cdns))
	for name := range cdns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validHTTPURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func mustURLPath(rawURL string) string {
	parsed, _ := url.Parse(rawURL)
	return parsed.Path
}

func setPositiveInt(object *value.Object, key string, number int64) {
	if number > 0 {
		object.Set(key, value.Int(number))
	}
}

func bestVimeoThumbnail(thumbs map[string]string) string {
	bestWidth := -1
	bestURL := ""
	for width, rawURL := range thumbs {
		parsedWidth, err := strconv.Atoi(width)
		if err == nil && parsedWidth > bestWidth && validHTTPURL(rawURL) {
			bestWidth, bestURL = parsedWidth, rawURL
		}
	}
	return bestURL
}
