package extractor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const brightcoveMaxPlaylistVideos = 10_000

var (
	brightcoveAccountID = regexp.MustCompile(`^[0-9]{1,32}$`)
	brightcovePlayerID  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)
	brightcoveEmbedID   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)
	brightcoveContentID = regexp.MustCompile(`^(?:[0-9]{1,32}|ref:[A-Za-z0-9_.-]{1,256})$`)
)

// Brightcove implements the current public Brightcove Player/Playback API
// flow. Legacy Flash-era /services URLs are intentionally not claimed: their
// runtime XML protocol is retired and does not share the authenticated Player
// API contract.
type Brightcove struct{}

func NewBrightcove() Brightcove { return Brightcove{} }

func (Brightcove) Name() string { return "brightcove" }

func (Brightcove) Suitable(parsed *url.URL) bool {
	_, ok := parseBrightcoveURL(parsed)
	return ok
}

func (Brightcove) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseBrightcoveURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if request.Transport == nil {
		return Extraction{}, fmt.Errorf("%w: missing transport", ErrInvalidMetadata)
	}
	policyKey, err := brightcovePolicyKey(ctx, request.Transport, target)
	if err != nil {
		return Extraction{}, err
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/json;pk="+policyKey)
	headers.Set("Referer", target.canonical)
	headers.Set("Origin", "https://players.brightcove.net")
	endpoint := "https://edge.api.brightcove.com/playback/v1/accounts/" + target.accountID + "/" + target.contentType + "s/" + url.PathEscape(target.contentID)
	var response brightcoveMedia
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, headers, &response); err != nil {
		return Extraction{}, err
	}
	if brightcoveHasGeoError(response.Errors) {
		return Extraction{}, ErrRegionRestricted
	}
	if brightcoveHasAuthError(response.Errors) {
		return Extraction{}, ErrAuthentication
	}
	if target.contentType == "playlist" {
		return normalizeBrightcovePlaylist(response, target)
	}
	return normalizeBrightcoveMedia(response, target.contentID, target.canonical, headers)
}

type brightcoveTarget struct {
	accountID   string
	playerID    string
	embed       string
	contentType string
	contentID   string
	canonical   string
}

func parseBrightcoveURL(parsed *url.URL) (brightcoveTarget, bool) {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Port() != "" || strings.ToLower(parsed.Hostname()) != "players.brightcove.net" || len(parsed.String()) > sharedHostingMaxURLBytes {
		return brightcoveTarget{}, false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) != 3 || segments[2] != "index.html" || !brightcoveAccountID.MatchString(segments[0]) {
		return brightcoveTarget{}, false
	}
	playerEmbed := strings.SplitN(segments[1], "_", 2)
	if len(playerEmbed) != 2 || !brightcovePlayerID.MatchString(playerEmbed[0]) || !brightcoveEmbedID.MatchString(playerEmbed[1]) {
		return brightcoveTarget{}, false
	}
	query := parsed.Query()
	videoID, playlistID := query.Get("videoId"), query.Get("playlistId")
	if (videoID == "" && playlistID == "") || (videoID != "" && playlistID != "") {
		return brightcoveTarget{}, false
	}
	contentType, contentID := "video", videoID
	if playlistID != "" {
		contentType, contentID = "playlist", playlistID
	}
	if !brightcoveContentID.MatchString(contentID) {
		return brightcoveTarget{}, false
	}
	canonicalQuery := url.Values{}
	canonicalQuery.Set(contentType+"Id", contentID)
	return brightcoveTarget{
		accountID: segments[0], playerID: playerEmbed[0], embed: playerEmbed[1], contentType: contentType, contentID: contentID,
		canonical: "https://players.brightcove.net/" + segments[0] + "/" + segments[1] + "/index.html?" + canonicalQuery.Encode(),
	}, true
}

func brightcovePolicyKey(ctx context.Context, transport Transport, target brightcoveTarget) (string, error) {
	var config struct {
		VideoCloud struct {
			PolicyKey string `json:"policy_key"`
		} `json:"video_cloud"`
	}
	configURL := "https://players.brightcove.net/" + target.accountID + "/" + target.playerID + "_" + target.embed + "/config.json"
	if err := hostedRequestJSON(ctx, transport, http.MethodGet, configURL, nil, make(http.Header), &config); err != nil {
		return "", err
	}
	// The policy key is a capability.  Never put it in a returned error or
	// result, and only accept a modest non-whitespace token.
	key := strings.TrimSpace(config.VideoCloud.PolicyKey)
	if key == "" || len(key) > 4096 || strings.ContainsAny(key, "\r\n") {
		return "", fmt.Errorf("%w: missing Brightcove playback policy", ErrInvalidMetadata)
	}
	return key, nil
}

type brightcoveAPIError struct {
	ErrorCode    string `json:"error_code"`
	ErrorSubcode string `json:"error_subcode"`
}

type brightcoveMedia struct {
	ID          hostingNumber         `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Duration    hostingNumber         `json:"duration"`
	PublishedAt string                `json:"published_at"`
	AccountID   hostingNumber         `json:"account_id"`
	Poster      string                `json:"poster"`
	Thumbnail   string                `json:"thumbnail"`
	Tags        []string              `json:"tags"`
	Sources     []brightcoveSource    `json:"sources"`
	TextTracks  []brightcoveTextTrack `json:"text_tracks"`
	Videos      []brightcoveMedia     `json:"videos"`
	Errors      []brightcoveAPIError  `json:"errors"`
}

type brightcoveSource struct {
	Src          string        `json:"src"`
	StreamingSrc string        `json:"streaming_src"`
	Type         string        `json:"type"`
	Container    string        `json:"container"`
	Codec        string        `json:"codec"`
	Width        hostingNumber `json:"width"`
	Height       hostingNumber `json:"height"`
	AvgBitrate   hostingNumber `json:"avg_bitrate"`
	Size         hostingNumber `json:"size"`
}

type brightcoveTextTrack struct {
	Kind    string `json:"kind"`
	Src     string `json:"src"`
	SrcLang string `json:"srclang"`
	Label   string `json:"label"`
}

func normalizeBrightcoveMedia(media brightcoveMedia, requestedID, webpageURL string, requestHeaders http.Header) (Extraction, error) {
	mediaID := media.ID.string()
	if mediaID == "" {
		mediaID = requestedID
	}
	if mediaID == "" || (requestedID != "" && !strings.HasPrefix(requestedID, "ref:") && mediaID != requestedID) || strings.TrimSpace(media.Name) == "" {
		return Extraction{}, fmt.Errorf("%w: malformed Brightcove media", ErrInvalidMetadata)
	}
	formats := make([]value.Value, 0, len(media.Sources))
	for index, source := range media.Sources {
		if sourceHasBrightcoveDRM(source) {
			continue
		}
		mediaURL := source.Src
		if mediaURL == "" {
			mediaURL = source.StreamingSrc
		}
		format, ok := hostedURLFormat(brightcoveFormatID(source, index), mediaURL)
		if !ok {
			continue
		}
		if bitrate := source.AvgBitrate.float64(); bitrate > 0 {
			format.Set("tbr", value.Float(bitrate/1000))
		}
		hostedSetInt(format, "width", source.Width.int64())
		hostedSetInt(format, "height", source.Height.int64())
		hostedSetString(format, "vcodec", source.Codec)
		if size := source.Size.int64(); size > 0 {
			format.Set("filesize", value.Int(size))
		}
		format.Set("http_headers", hostedHeadersValue(requestHeaders))
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(mediaID)},
		value.Field{Key: "title", Value: value.String(media.Name)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	hostedSetString(info, "description", media.Description)
	hostedSetString(info, "uploader_id", media.AccountID.string())
	hostedSetFloat(info, "duration", media.Duration.float64()/1000)
	if timestamp := hostedUnixTimestamp(media.PublishedAt); timestamp > 0 {
		info.Set("timestamp", value.Int(timestamp))
	}
	if thumbnail := firstHostedURL(media.Poster, media.Thumbnail); thumbnail != "" {
		info.Set("thumbnail", value.String(thumbnail))
	}
	if len(media.Tags) > 0 {
		tags := make([]value.Value, 0, len(media.Tags))
		for _, tag := range media.Tags {
			if strings.TrimSpace(tag) != "" {
				tags = append(tags, value.String(tag))
			}
		}
		if len(tags) > 0 {
			info.Set("tags", value.List(tags...))
		}
	}
	if subtitles := brightcoveSubtitles(media.TextTracks); !subtitles.IsMissing() {
		info.Set("subtitles", subtitles)
	}
	return Media(value.NewInfo(info)), nil
}

func normalizeBrightcovePlaylist(playlist brightcoveMedia, target brightcoveTarget) (Extraction, error) {
	if len(playlist.Videos) > brightcoveMaxPlaylistVideos {
		return Extraction{}, ErrPlaylistLimit
	}
	entries := make([]Entry, 0, len(playlist.Videos))
	for _, video := range playlist.Videos {
		id := video.ID.string()
		if id == "" || !brightcoveContentID.MatchString(id) {
			continue
		}
		entryURL := "https://players.brightcove.net/" + target.accountID + "/" + target.playerID + "_" + target.embed + "/index.html?videoId=" + url.QueryEscape(id)
		entries = append(entries, Entry{URL: entryURL, ExtractorKey: "brightcove", ID: id, Title: video.Name})
	}
	if len(entries) == 0 {
		return Extraction{}, ErrUnavailable
	}
	playlistID := playlist.ID.string()
	if playlistID == "" {
		playlistID = target.contentID
	}
	title := playlist.Name
	if strings.TrimSpace(title) == "" {
		title = "Brightcove playlist " + playlistID
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(playlistID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
		value.Field{Key: "description", Value: value.String(playlist.Description)},
	))
	return Playlist(info, StaticEntries(entries...))
}

func sourceHasBrightcoveDRM(source brightcoveSource) bool {
	container := strings.ToUpper(source.Container)
	return container == "WVM" || strings.Contains(strings.ToLower(source.Type), "widevine") || strings.Contains(strings.ToLower(source.Type), "fairplay")
}

func brightcoveFormatID(source brightcoveSource, index int) string {
	parts := []string{"http"}
	extension := strings.ToLower(strings.TrimPrefix(pathExt(source.Src, source.StreamingSrc), "."))
	if extension == "m3u8" {
		parts[0] = "hls"
	} else if extension == "mpd" {
		parts[0] = "dash"
	}
	if bitrate := source.AvgBitrate.int64(); bitrate > 0 {
		parts = append(parts, strconv.FormatInt(bitrate/1000, 10)+"k")
	}
	if height := source.Height.int64(); height > 0 {
		parts = append(parts, strconv.FormatInt(height, 10)+"p")
	}
	if len(parts) == 1 {
		parts = append(parts, strconv.Itoa(index+1))
	}
	return strings.Join(parts, "-")
}

func pathExt(values ...string) string {
	for _, input := range values {
		if parsed, err := url.Parse(input); err == nil && parsed.Path != "" {
			if extension := path.Ext(parsed.Path); extension != "" {
				return extension
			}
		}
	}
	return ""
}

func firstHostedURL(urls ...string) string {
	for _, rawURL := range urls {
		if validHostedHTTPURL(rawURL) {
			return rawURL
		}
	}
	return ""
}

func brightcoveSubtitles(tracks []brightcoveTextTrack) value.Value {
	byLanguage := make(map[string][]value.Value)
	for _, track := range tracks {
		if strings.ToLower(track.Kind) != "captions" || !validHostedHTTPURL(track.Src) {
			continue
		}
		language := strings.ToLower(strings.TrimSpace(track.SrcLang))
		if language == "" {
			language = strings.ToLower(strings.TrimSpace(track.Label))
		}
		if language == "" {
			language = "en"
		}
		byLanguage[language] = append(byLanguage[language], value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(track.Src)})))
	}
	if len(byLanguage) == 0 {
		return value.Missing()
	}
	languages := make([]string, 0, len(byLanguage))
	for language := range byLanguage {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	object := value.NewObject()
	for _, language := range languages {
		object.Set(language, value.List(byLanguage[language]...))
	}
	return value.ObjectValue(object)
}

func brightcoveHasGeoError(errors []brightcoveAPIError) bool {
	for _, item := range errors {
		if strings.EqualFold(item.ErrorSubcode, "CLIENT_GEO") {
			return true
		}
	}
	return false
}

func brightcoveHasAuthError(errors []brightcoveAPIError) bool {
	for _, item := range errors {
		if strings.EqualFold(item.ErrorSubcode, "TVE_AUTH") || strings.Contains(strings.ToUpper(item.ErrorCode), "AUTH") {
			return true
		}
	}
	return false
}

var _ Extractor = Brightcove{}
