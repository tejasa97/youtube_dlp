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

var wistiaIDPattern = regexp.MustCompile(`^[a-z0-9]{10}$`)

// Wistia covers direct media embeds plus Wistia playlist and channel embeds.
// Videos are obtained from the documented embed JSON endpoint; channel and
// playlist entries stay lazy URL results rather than eagerly downloading every
// media JSON document.
type Wistia struct{}

func NewWistia() Wistia                      { return Wistia{} }
func (Wistia) Name() string                  { return "wistia" }
func (Wistia) Suitable(parsed *url.URL) bool { _, ok := parseWistiaURL(parsed); return ok }

func (Wistia) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseWistiaURL(parsed)
	if !ok || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	switch target.kind {
	case wistiaMedia:
		var response wistiaEmbedResponse
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, wistiaEndpoint("medias", target.id), nil, wistiaHeaders(target.referer), &response); err != nil {
			return Extraction{}, err
		}
		return normalizeWistiaMedia(response, target.id, target.canonical)
	case wistiaPlaylist:
		var response []wistiaPlaylistResponse
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, wistiaEndpoint("playlists", target.id), nil, wistiaHeaders(target.referer), &response); err != nil {
			return Extraction{}, err
		}
		return normalizeWistiaPlaylist(response, target)
	case wistiaChannel:
		var response wistiaChannelResponse
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, wistiaEndpoint("channel", target.id), nil, wistiaHeaders(target.referer), &response); err != nil {
			return Extraction{}, err
		}
		return normalizeWistiaChannel(response, target)
	default:
		return Extraction{}, ErrUnsupported
	}
}

type wistiaKind uint8

const (
	wistiaMedia wistiaKind = iota + 1
	wistiaPlaylist
	wistiaChannel
)

type wistiaTarget struct {
	kind                   wistiaKind
	id, canonical, referer string
}

func parseWistiaURL(parsed *url.URL) (wistiaTarget, bool) {
	if parsed == nil || len(parsed.String()) > sharedHostingMaxURLBytes {
		return wistiaTarget{}, false
	}
	if parsed.Scheme == "wistia" && wistiaIDPattern.MatchString(parsed.Opaque) {
		return wistiaTarget{kind: wistiaMedia, id: parsed.Opaque, canonical: "wistia:" + parsed.Opaque}, true
	}
	if parsed.Scheme == "wistiachannel" && wistiaIDPattern.MatchString(strings.Split(parsed.Opaque, "?")[0]) {
		id := strings.Split(parsed.Opaque, "?")[0]
		return wistiaTarget{kind: wistiaChannel, id: id, canonical: "wistiachannel:" + id}, true
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Port() != "" {
		return wistiaTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "fast.wistia.net" && host != "fast.wistia.com" && host != "wistia.net" && host != "wistia.com" {
		return wistiaTarget{}, false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 3 || segments[0] != "embed" {
		return wistiaTarget{}, false
	}
	kind := wistiaMedia
	switch segments[1] {
	case "iframe", "medias":
		kind = wistiaMedia
	case "playlists":
		kind = wistiaPlaylist
	case "channel":
		kind = wistiaChannel
	default:
		return wistiaTarget{}, false
	}
	id := strings.TrimSuffix(segments[2], ".json")
	if !wistiaIDPattern.MatchString(id) {
		return wistiaTarget{}, false
	}
	prefix := "wistia:"
	if kind == wistiaChannel {
		prefix = "wistiachannel:"
	}
	if kind == wistiaPlaylist {
		prefix = "https://fast.wistia.net/embed/playlists/"
	}
	canonical := prefix + id
	if kind == wistiaPlaylist {
		canonical += ".json"
	}
	return wistiaTarget{kind: kind, id: id, canonical: canonical, referer: parsed.String()}, true
}

func wistiaEndpoint(kind, id string) string {
	return "https://fast.wistia.net/embed/" + kind + "/" + url.PathEscape(id) + ".json"
}
func wistiaHeaders(referer string) http.Header {
	headers := make(http.Header)
	if validHostedHTTPURL(referer) {
		headers.Set("Referer", referer)
	}
	return headers
}

type wistiaEmbedResponse struct {
	Error string              `json:"error"`
	Media wistiaMediaResponse `json:"media"`
}
type wistiaMediaResponse struct {
	HashedID       string             `json:"hashedId"`
	Name           string             `json:"name"`
	SEODescription string             `json:"seoDescription"`
	Duration       hostingNumber      `json:"duration"`
	CreatedAt      hostingNumber      `json:"createdAt"`
	Assets         []wistiaAsset      `json:"assets"`
	Captions       []wistiaCaption    `json:"captions"`
	EmbedOptions   wistiaEmbedOptions `json:"embed_options"`
	EmbedOptionsV2 wistiaEmbedOptions `json:"embedOptions"`
}
type wistiaEmbedOptions struct {
	Plugin map[string]struct {
		On string `json:"on"`
	} `json:"plugin"`
}
type wistiaAsset struct {
	URL         string        `json:"url"`
	Type        string        `json:"type"`
	Ext         string        `json:"ext"`
	DisplayName string        `json:"display_name"`
	Container   string        `json:"container"`
	Codec       string        `json:"codec"`
	Status      hostingNumber `json:"status"`
	Width       hostingNumber `json:"width"`
	Height      hostingNumber `json:"height"`
	Bitrate     hostingNumber `json:"bitrate"`
	Size        hostingNumber `json:"size"`
}
type wistiaCaption struct {
	Language string `json:"language"`
}

func normalizeWistiaMedia(response wistiaEmbedResponse, requestedID, webpageURL string) (Extraction, error) {
	if response.Error != "" {
		return Extraction{}, ErrUnavailable
	}
	media := response.Media
	if media.HashedID != requestedID || strings.TrimSpace(media.Name) == "" {
		return Extraction{}, fmt.Errorf("%w: malformed Wistia media", ErrInvalidMetadata)
	}
	for _, options := range []wistiaEmbedOptions{media.EmbedOptions, media.EmbedOptionsV2} {
		for _, plugin := range options.Plugin {
			if strings.EqualFold(plugin.On, "true") {
				return Extraction{}, ErrAuthentication
			}
		}
	}
	formats, thumbnails := make([]value.Value, 0, len(media.Assets)), make([]value.Value, 0, 2)
	for index, asset := range media.Assets {
		if !validHostedHTTPURL(asset.URL) || (asset.Status.string() != "" && asset.Status.int64() != 2) || asset.Type == "preview" || asset.Type == "storyboard" {
			continue
		}
		if asset.Type == "still" || asset.Type == "still_image" {
			thumbnail := value.NewObject(value.Field{Key: "url", Value: value.String(wistiaImageURL(asset.URL, asset.Ext))})
			hostedSetInt(thumbnail, "width", asset.Width.int64())
			hostedSetInt(thumbnail, "height", asset.Height.int64())
			hostedSetInt(thumbnail, "filesize", asset.Size.int64())
			thumbnails = append(thumbnails, value.ObjectValue(thumbnail))
			continue
		}
		format := wistiaFormat(asset, index)
		if format == nil {
			continue
		}
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(media.HashedID)}, value.Field{Key: "title", Value: value.String(media.Name)}, value.Field{Key: "webpage_url", Value: value.String(webpageURL)}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "formats", Value: value.List(formats...)})
	hostedSetString(info, "description", media.SEODescription)
	hostedSetFloat(info, "duration", media.Duration.float64())
	hostedSetInt(info, "timestamp", media.CreatedAt.int64())
	if len(thumbnails) > 0 {
		info.Set("thumbnails", value.List(thumbnails...))
		first, _ := thumbnails[0].Object()
		info.Set("thumbnail", first.Lookup("url"))
	}
	if subtitles := wistiaSubtitles(media.Captions, media.HashedID); !subtitles.IsMissing() {
		info.Set("subtitles", subtitles)
	}
	return Media(value.NewInfo(info)), nil
}

func wistiaFormat(asset wistiaAsset, index int) *value.Object {
	formatID := asset.Type
	if formatID == "" {
		formatID = fmt.Sprintf("http-%d", index+1)
	}
	if strings.HasSuffix(formatID, "_video") && asset.DisplayName != "" {
		formatID = strings.TrimSuffix(formatID, "_video") + "-" + asset.DisplayName
	}
	if strings.EqualFold(asset.Container, "m3u8") || strings.EqualFold(asset.Ext, "m3u8") {
		format := manifestFormat(strings.Replace(formatID, "hls-", "", 1), asset.URL, "m3u8_native")
		format.Set("ext", value.String("mp4"))
		hostedSetInt(format, "width", asset.Width.int64())
		hostedSetInt(format, "height", asset.Height.int64())
		return format
	}
	format, ok := hostedURLFormat(formatID, asset.URL)
	if !ok {
		return nil
	}
	if asset.Ext != "" {
		format.Set("ext", value.String(strings.TrimPrefix(strings.ToLower(asset.Ext), ".")))
	}
	hostedSetInt(format, "width", asset.Width.int64())
	hostedSetInt(format, "height", asset.Height.int64())
	hostedSetInt(format, "filesize", asset.Size.int64())
	hostedSetInt(format, "tbr", asset.Bitrate.int64())
	hostedSetString(format, "vcodec", asset.Codec)
	hostedSetString(format, "container", asset.Container)
	if strings.EqualFold(asset.DisplayName, "Audio") {
		format.Set("vcodec", value.String("none"))
	}
	return format
}

func wistiaImageURL(rawURL, ext string) string {
	if ext == "" || !strings.HasSuffix(rawURL, ".bin") {
		return rawURL
	}
	return strings.TrimSuffix(rawURL, ".bin") + "." + strings.TrimPrefix(ext, ".")
}
func wistiaSubtitles(captions []wistiaCaption, videoID string) value.Value {
	if len(captions) == 0 {
		return value.Missing()
	}
	byLanguage := make(map[string][]value.Value)
	for _, caption := range captions {
		language := strings.ToLower(strings.TrimSpace(caption.Language))
		if language == "" {
			continue
		}
		subtitleURL := "https://fast.wistia.net/embed/captions/" + url.PathEscape(videoID) + ".vtt?language=" + url.QueryEscape(language)
		byLanguage[language] = append(byLanguage[language], value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(subtitleURL)})))
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

type wistiaPlaylistResponse struct {
	Medias []struct {
		EmbedConfig wistiaEmbedResponse `json:"embed_config"`
		HashedID    string              `json:"hashedId"`
		Name        string              `json:"name"`
	} `json:"medias"`
}

func normalizeWistiaPlaylist(response []wistiaPlaylistResponse, target wistiaTarget) (Extraction, error) {
	if len(response) == 0 {
		return Extraction{}, ErrUnavailable
	}
	medias := response[0].Medias
	if len(medias) == 0 {
		return Extraction{}, ErrUnavailable
	}
	if len(medias) > defaultMaxPlaylistEntries {
		return Extraction{}, ErrPlaylistLimit
	}
	entries := make([]Entry, 0, len(medias))
	for _, media := range medias {
		id, title := media.HashedID, media.Name
		if media.EmbedConfig.Media.HashedID != "" {
			id, title = media.EmbedConfig.Media.HashedID, media.EmbedConfig.Media.Name
		}
		if wistiaIDPattern.MatchString(id) {
			entries = append(entries, Entry{URL: "wistia:" + id, ExtractorKey: "wistia", ID: id, Title: title})
		}
	}
	if len(entries) == 0 {
		return Extraction{}, ErrUnavailable
	}
	return Playlist(value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(target.id)}, value.Field{Key: "title", Value: value.String("Wistia playlist " + target.id)}, value.Field{Key: "webpage_url", Value: value.String(target.canonical)})), StaticEntries(entries...))
}

type wistiaChannelResponse struct {
	Series []struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Sections    []struct {
			Videos []struct {
				HashedID string `json:"hashedId"`
				Name     string `json:"name"`
			} `json:"videos"`
			Episodes []struct {
				HashedID string `json:"hashedId"`
				Name     string `json:"name"`
			} `json:"episodes"`
		} `json:"sections"`
	} `json:"series"`
}

func normalizeWistiaChannel(response wistiaChannelResponse, target wistiaTarget) (Extraction, error) {
	if len(response.Series) == 0 {
		return Extraction{}, ErrUnavailable
	}
	series := response.Series[0]
	entries := make([]Entry, 0)
	for _, section := range series.Sections {
		for _, media := range append(section.Videos, section.Episodes...) {
			if wistiaIDPattern.MatchString(media.HashedID) {
				entries = append(entries, Entry{URL: "wistia:" + media.HashedID, ExtractorKey: "wistia", ID: media.HashedID, Title: media.Name})
			}
		}
	}
	if len(entries) == 0 {
		return Extraction{}, ErrUnavailable
	}
	if len(entries) > defaultMaxPlaylistEntries {
		return Extraction{}, ErrPlaylistLimit
	}
	title := series.Title
	if title == "" {
		title = "Wistia channel " + target.id
	}
	return Playlist(value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(target.id)}, value.Field{Key: "title", Value: value.String(title)}, value.Field{Key: "description", Value: value.String(series.Description)}, value.Field{Key: "webpage_url", Value: value.String(target.canonical)})), StaticEntries(entries...))
}

var _ Extractor = Wistia{}
