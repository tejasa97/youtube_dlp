package extractor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var jwPlatformID = regexp.MustCompile(`^[A-Za-z0-9]{8}$`)

// JWPlatform implements the public v2 media endpoint used by both the legacy
// content.jwplatform.com and current cdn.jwplayer.com player URLs.
type JWPlatform struct{}

func NewJWPlatform() JWPlatform { return JWPlatform{} }
func (JWPlatform) Name() string { return "jwplatform" }

func (JWPlatform) Suitable(parsed *url.URL) bool {
	_, _, ok := parseJWPlatformURL(parsed)
	return ok
}

func (JWPlatform) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	videoID, canonical, ok := parseJWPlatformURL(parsed)
	if !ok || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	var response jwPlatformResponse
	endpoint := "https://cdn.jwplayer.com/v2/media/" + url.PathEscape(videoID)
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &response); err != nil {
		return Extraction{}, err
	}
	return normalizeJWPlatform(response, videoID, canonical)
}

func parseJWPlatformURL(parsed *url.URL) (videoID, canonical string, ok bool) {
	if parsed == nil || len(parsed.String()) > sharedHostingMaxURLBytes {
		return "", "", false
	}
	if parsed.Scheme == "jwplatform" && parsed.Opaque != "" && jwPlatformID.MatchString(parsed.Opaque) {
		return parsed.Opaque, "jwplatform:" + parsed.Opaque, true
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Port() != "" {
		return "", "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "content.jwplatform.com" && host != "cdn.jwplayer.com" {
		return "", "", false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 2 {
		return "", "", false
	}
	candidate := segments[len(segments)-1]
	candidate = strings.TrimSuffix(candidate, ".json")
	candidate = strings.TrimSuffix(candidate, ".js")
	if dash := strings.IndexByte(candidate, '-'); dash > 0 {
		candidate = candidate[:dash]
	}
	if !jwPlatformID.MatchString(candidate) {
		return "", "", false
	}
	switch segments[0] {
	case "players", "feeds", "player", "thumbs", "previews", "manifests", "jw6", "v2":
	default:
		return "", "", false
	}
	return candidate, "https://cdn.jwplayer.com/players/" + candidate, true
}

type jwPlatformResponse struct {
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Image       string             `json:"image"`
	PubDate     hostingNumber      `json:"pubdate"`
	Duration    hostingNumber      `json:"duration"`
	Author      string             `json:"author"`
	Channel     string             `json:"channel"`
	MediaID     string             `json:"mediaid"`
	Playlist    []jwPlatformMedia  `json:"playlist"`
	Sources     []jwPlatformSource `json:"sources"`
	Tracks      []jwPlatformTrack  `json:"tracks"`
}
type jwPlatformMedia struct {
	Title       string             `json:"title"`
	Description string             `json:"description"`
	Image       string             `json:"image"`
	PubDate     hostingNumber      `json:"pubdate"`
	Duration    hostingNumber      `json:"duration"`
	Author      string             `json:"author"`
	Channel     string             `json:"channel"`
	MediaID     string             `json:"mediaid"`
	Sources     []jwPlatformSource `json:"sources"`
	Tracks      []jwPlatformTrack  `json:"tracks"`
}
type jwPlatformSource struct {
	File    string        `json:"file"`
	Type    string        `json:"type"`
	Label   string        `json:"label"`
	Width   hostingNumber `json:"width"`
	Height  hostingNumber `json:"height"`
	Bitrate hostingNumber `json:"bitrate"`
}
type jwPlatformTrack struct {
	File  string `json:"file"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

func normalizeJWPlatform(response jwPlatformResponse, requestedID, webpageURL string) (Extraction, error) {
	media := jwPlatformMedia{Title: response.Title, Description: response.Description, Image: response.Image, PubDate: response.PubDate, Duration: response.Duration, Author: response.Author, Channel: response.Channel, MediaID: response.MediaID, Sources: response.Sources, Tracks: response.Tracks}
	if len(response.Playlist) > 0 {
		media = response.Playlist[0]
	}
	if media.MediaID != "" && media.MediaID != requestedID {
		return Extraction{}, fmt.Errorf("%w: JW Platform media id mismatch", ErrInvalidMetadata)
	}
	if strings.TrimSpace(media.Title) == "" {
		return Extraction{}, fmt.Errorf("%w: missing JW Platform title", ErrInvalidMetadata)
	}
	formats := make([]value.Value, 0, len(media.Sources))
	for index, source := range media.Sources {
		format, ok := hostedURLFormat(jwPlatformFormatID(source, index), source.File)
		if !ok {
			continue
		}
		hostedSetInt(format, "width", source.Width.int64())
		hostedSetInt(format, "height", source.Height.int64())
		if bitrate := source.Bitrate.float64(); bitrate > 0 {
			format.Set("tbr", value.Float(bitrate))
		}
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(requestedID)}, value.Field{Key: "title", Value: value.String(media.Title)}, value.Field{Key: "webpage_url", Value: value.String(webpageURL)}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "formats", Value: value.List(formats...)})
	hostedSetString(info, "description", media.Description)
	hostedSetString(info, "thumbnail", firstHostedURL(media.Image))
	hostedSetString(info, "uploader", media.Author)
	hostedSetString(info, "channel", media.Channel)
	hostedSetFloat(info, "duration", media.Duration.float64())
	if timestamp := media.PubDate.int64(); timestamp > 0 {
		info.Set("timestamp", value.Int(timestamp))
	}
	if subtitles := jwPlatformSubtitles(media.Tracks); !subtitles.IsMissing() {
		info.Set("subtitles", subtitles)
	}
	return Media(value.NewInfo(info)), nil
}

func jwPlatformFormatID(source jwPlatformSource, index int) string {
	base := "http"
	lower := strings.ToLower(source.File + " " + source.Type)
	if strings.Contains(lower, "m3u8") || strings.Contains(lower, "hls") {
		base = "hls"
	} else if strings.Contains(lower, "mpd") || strings.Contains(lower, "dash") {
		base = "dash"
	}
	if source.Label != "" {
		return base + "-" + strings.ToLower(strings.ReplaceAll(source.Label, " ", "-"))
	}
	if height := source.Height.int64(); height > 0 {
		return fmt.Sprintf("%s-%dp", base, height)
	}
	return fmt.Sprintf("%s-%d", base, index+1)
}

func jwPlatformSubtitles(tracks []jwPlatformTrack) value.Value {
	byLanguage := make(map[string][]value.Value)
	for _, track := range tracks {
		if !strings.EqualFold(track.Kind, "captions") || !validHostedHTTPURL(track.File) {
			continue
		}
		language := strings.ToLower(strings.TrimSpace(track.Label))
		if language == "" {
			language = "en"
		}
		byLanguage[language] = append(byLanguage[language], value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(track.File)})))
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

var _ Extractor = JWPlatform{}
