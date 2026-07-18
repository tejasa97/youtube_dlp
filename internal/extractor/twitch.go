package extractor

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	twitchGraphQLURL = "https://gql.twitch.tv/gql"
	twitchUsherBase  = "https://usher.ttvnw.net/api/channel/hls/"
	twitchVODBase    = "https://usher.ttvnw.net/vod/"
	twitchClientID   = "ue6666qo983tsx6so1t0vnawi233wa"
	twitchMaxURL     = 8 << 10
	twitchMaxMoments = 1000
	twitchMaxAssets  = 64
)

var (
	twitchChannelPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,25}$`)
	twitchVODPattern     = regexp.MustCompile(`^[0-9]{1,20}$`)
	twitchClipPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	twitchPreviewSize    = regexp.MustCompile(`\d+x\d+(\.[A-Za-z0-9]+)$`)
	twitchQualityHeight  = regexp.MustCompile(`^([0-9]{2,5})p?$`)
	twitchReservedPaths  = map[string]struct{}{
		"activate": {}, "bits": {}, "collections": {}, "directory": {}, "downloads": {},
		"drops": {}, "inventory": {}, "jobs": {}, "login": {}, "p": {}, "payments": {},
		"prime": {}, "products": {}, "search": {}, "settings": {}, "signup": {},
		"subscriptions": {}, "turbo": {}, "videos": {}, "wallet": {},
	}
)

var twitchOperationHashes = map[string]string{
	"StreamMetadata":                       "ad022ca32220d5523d03a23cbcb5beaa1e0999889c1f8f78f9f2520dafb5cae6",
	"ComscoreStreamingQuery":               "e1edae8122517d013405f237ffcc124515dc6ded82480a88daef69c83b53ac01",
	"VideoPreviewOverlay":                  "9515480dee68a77e667cb19de634739d33f243572b007e98e67184b1a5d8369f",
	"VideoMetadata":                        "45111672eea2e507f8ba44d101a61862f9c56b11dee09a15634cb75cb9b9084d",
	"VideoPlayer_ChapterSelectButtonVideo": "71835d5ef425e154bf282453a926d99b328cdc5e32f36d3a209d0f4778b41203",
	"VideoPlayer_VODSeekbarPreviewVideo":   "07e99e4d56c5a7c67117a154777b0baf85a5ffefa393b213f4bc712ccaf85dd6",
	"ShareClipRenderStatus":                "0a02bb974443b576f5579aab0fef1d4b7f44e58a8a256f0c5adfead0db70640f",
}

var ErrTwitchNetwork = errors.New("Twitch network request failed")

type Twitch struct{}

func NewTwitch() Twitch { return Twitch{} }

func (Twitch) Name() string { return "twitch" }

func (Twitch) Suitable(parsed *url.URL) bool {
	_, ok := classifyTwitchURL(parsed)
	return ok
}

func (Twitch) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyTwitchURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	switch target.kind {
	case twitchKindVOD:
		return extractTwitchVOD(ctx, request.Transport, target, parsed)
	case twitchKindClip:
		return extractTwitchClip(ctx, request.Transport, target)
	default:
		return extractTwitchLive(ctx, request.Transport, target.id)
	}
}

type twitchKind uint8

const (
	twitchKindLive twitchKind = iota + 1
	twitchKindVOD
	twitchKindClip
)

type twitchTarget struct {
	kind twitchKind
	id   string
}

func classifyTwitchURL(parsed *url.URL) (twitchTarget, bool) {
	if parsed == nil || len(parsed.String()) == 0 || len(parsed.String()) > twitchMaxURL ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Port() != "" {
		return twitchTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	decode := func(raw string, pattern *regexp.Regexp) (string, bool) {
		decoded, err := url.PathUnescape(raw)
		return decoded, err == nil && decoded == raw && pattern.MatchString(raw)
	}
	if host == "player.twitch.tv" {
		if strings.Trim(parsed.Path, "/") != "" {
			return twitchTarget{}, false
		}
		if raw := strings.TrimPrefix(parsed.Query().Get("video"), "v"); raw != "" {
			if id, ok := decode(raw, twitchVODPattern); ok {
				return twitchTarget{kind: twitchKindVOD, id: id}, true
			}
			return twitchTarget{}, false
		}
		if channel, ok := decode(parsed.Query().Get("channel"), twitchChannelPattern); ok {
			if _, reserved := twitchReservedPaths[strings.ToLower(channel)]; !reserved {
				return twitchTarget{kind: twitchKindLive, id: strings.ToLower(channel)}, true
			}
		}
		return twitchTarget{}, false
	}
	if host == "clips.twitch.tv" {
		var raw string
		if len(parts) == 1 {
			raw = parts[0]
		} else if len(parts) == 2 && parts[0] != "embed" {
			raw = parts[1]
		} else if len(parts) == 1 && parts[0] == "embed" {
			raw = parsed.Query().Get("clip")
		}
		// The embed endpoint has no slug in its path.
		if strings.Trim(parsed.Path, "/") == "embed" {
			raw = parsed.Query().Get("clip")
		}
		if slug, ok := decode(raw, twitchClipPattern); ok {
			return twitchTarget{kind: twitchKindClip, id: slug}, true
		}
		return twitchTarget{}, false
	}
	if host != "twitch.tv" && host != "www.twitch.tv" && host != "go.twitch.tv" && host != "m.twitch.tv" {
		return twitchTarget{}, false
	}
	if len(parts) == 2 && parts[0] == "videos" {
		if id, ok := decode(parts[1], twitchVODPattern); ok {
			return twitchTarget{kind: twitchKindVOD, id: id}, true
		}
	}
	if len(parts) == 3 && (parts[1] == "v" || parts[1] == "video") {
		if _, ok := decode(parts[0], twitchChannelPattern); ok {
			if id, ok := decode(parts[2], twitchVODPattern); ok {
				return twitchTarget{kind: twitchKindVOD, id: id}, true
			}
		}
	}
	if len(parts) == 2 && parts[1] == "schedule" {
		if _, ok := decode(parts[0], twitchChannelPattern); ok {
			if id, ok := decode(parsed.Query().Get("vodID"), twitchVODPattern); ok {
				return twitchTarget{kind: twitchKindVOD, id: id}, true
			}
		}
	}
	if (len(parts) == 3 && parts[1] == "clip") || (len(parts) == 2 && parts[0] == "clip") {
		raw := parts[len(parts)-1]
		if slug, ok := decode(raw, twitchClipPattern); ok {
			return twitchTarget{kind: twitchKindClip, id: slug}, true
		}
	}
	if channel, ok := twitchChannel(parsed); ok {
		return twitchTarget{kind: twitchKindLive, id: strings.ToLower(channel)}, true
	}
	return twitchTarget{}, false
}

func extractTwitchLive(ctx context.Context, transport Transport, channel string) (Extraction, error) {
	metadata, err := requestTwitchMetadata(ctx, transport, channel)
	if err != nil {
		return Extraction{}, err
	}
	if len(metadata) < 1 || metadata[0].Data.User == nil {
		return Extraction{}, ErrUnavailable
	}
	stream := metadata[0].Data.User.Stream
	if stream == nil {
		return Extraction{}, ErrUnavailable
	}

	token, err := requestTwitchAccessToken(ctx, transport, channel)
	if err != nil {
		return Extraction{}, err
	}
	if token.Value == "" || token.Signature == "" {
		return Extraction{}, ErrAuthentication
	}
	manifestURL := twitchManifestURL(channel, token)
	streamID := stream.ID
	if streamID == "" {
		streamID = channel
	}
	uploader := metadata[0].Data.User.DisplayName
	description := metadata[0].Data.User.BroadcastSettings.Title
	thumbnail := stream.PreviewImageURL
	if len(metadata) > 1 && metadata[1].Data.User != nil {
		if metadata[1].Data.User.DisplayName != "" {
			uploader = metadata[1].Data.User.DisplayName
		}
		if metadata[1].Data.User.BroadcastSettings.Title != "" {
			description = metadata[1].Data.User.BroadcastSettings.Title
		}
	}
	if len(metadata) > 2 && metadata[2].Data.User != nil && metadata[2].Data.User.Stream != nil && metadata[2].Data.User.Stream.PreviewImageURL != "" {
		thumbnail = metadata[2].Data.User.Stream.PreviewImageURL
	}
	title := uploader
	if title == "" {
		title = channel
	}
	streamType := strings.ToLower(stream.Type)
	if streamType == "live" || streamType == "rerun" {
		title += " (" + streamType + ")"
	}

	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(streamID)},
		value.Field{Key: "display_id", Value: value.String(channel)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "uploader_id", Value: value.String(channel)},
		value.Field{Key: "webpage_url", Value: value.String("https://www.twitch.tv/" + channel)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(manifestFormat("hls", manifestURL, "m3u8_native")))},
		value.Field{Key: "is_live", Value: value.Bool(streamType == "live")},
	)
	if description != "" {
		info.Set("description", value.String(description))
	}
	if uploader != "" {
		info.Set("uploader", value.String(uploader))
	}
	if streamType == "live" {
		info.Set("live_status", value.String("is_live"))
	} else if streamType == "rerun" {
		info.Set("live_status", value.String("not_live"))
	}
	if timestamp, parseErr := time.Parse(time.RFC3339, stream.CreatedAt); parseErr == nil {
		info.Set("timestamp", value.Int(timestamp.Unix()))
	}
	if stream.Viewers != nil && *stream.Viewers >= 0 {
		info.Set("view_count", value.Int(*stream.Viewers))
	}
	if validHTTPURL(thumbnail) {
		thumbnails := []value.Value{value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(twitchFullSizeThumbnail(thumbnail))}))}
		if twitchFullSizeThumbnail(thumbnail) != thumbnail {
			thumbnails = append(thumbnails, value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(thumbnail)})))
		}
		info.Set("thumbnails", value.List(thumbnails...))
	}
	return Media(value.NewInfo(info)), nil
}

func twitchChannel(parsed *url.URL) (string, bool) {
	if parsed == nil {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	var channel string
	switch host {
	case "twitch.tv", "www.twitch.tv", "go.twitch.tv", "m.twitch.tv":
		channel = strings.TrimPrefix(parsed.EscapedPath(), "/")
		channel = strings.TrimSuffix(channel, "/")
		decoded, err := url.PathUnescape(channel)
		if err != nil || decoded != channel || strings.Contains(channel, "/") {
			return "", false
		}
	case "player.twitch.tv":
		if strings.Trim(parsed.Path, "/") != "" {
			return "", false
		}
		channel = parsed.Query().Get("channel")
	default:
		return "", false
	}
	if !twitchChannelPattern.MatchString(channel) {
		return "", false
	}
	_, reserved := twitchReservedPaths[strings.ToLower(channel)]
	return channel, !reserved
}

type twitchMetadataResponse struct {
	Data struct {
		User *twitchUser `json:"user"`
	} `json:"data"`
	Errors []json.RawMessage `json:"errors"`
}

type twitchUser struct {
	DisplayName       string `json:"displayName"`
	Login             string `json:"login"`
	BroadcastSettings struct {
		Title string `json:"title"`
	} `json:"broadcastSettings"`
	Stream *twitchStream `json:"stream"`
}

type twitchStream struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	Viewers         *int64 `json:"viewers"`
	CreatedAt       string `json:"createdAt"`
	PreviewImageURL string `json:"previewImageURL"`
}

type twitchAccessToken struct {
	Value     string `json:"value"`
	Signature string `json:"signature"`
}

func requestTwitchMetadata(ctx context.Context, transport Transport, channel string) ([]twitchMetadataResponse, error) {
	operations := []map[string]any{
		{"operationName": "StreamMetadata", "variables": map[string]any{"channelLogin": channel, "includeIsDJ": true}},
		{"operationName": "ComscoreStreamingQuery", "variables": map[string]any{"channel": channel, "clipSlug": "", "isClip": false, "isLive": true, "isVodOrCollection": false, "vodID": ""}},
		{"operationName": "VideoPreviewOverlay", "variables": map[string]any{"login": channel}},
	}
	for _, operation := range operations {
		name := operation["operationName"].(string)
		operation["extensions"] = map[string]any{"persistedQuery": map[string]any{"version": 1, "sha256Hash": twitchOperationHashes[name]}}
	}
	body, err := json.Marshal(operations)
	if err != nil {
		return nil, fmt.Errorf("%w: Twitch metadata request", ErrInvalidMetadata)
	}
	var response []twitchMetadataResponse
	if err := RequestJSON(ctx, transport, http.MethodPost, twitchGraphQLURL, body, twitchHeaders(), &response); err != nil {
		return nil, categorizeTwitchHTTP(err)
	}
	if len(response) != len(operations) {
		return nil, fmt.Errorf("%w: Twitch metadata response", ErrInvalidMetadata)
	}
	for _, operation := range response {
		if len(operation.Errors) != 0 {
			return nil, fmt.Errorf("%w: Twitch metadata response", ErrInvalidMetadata)
		}
	}
	return response, nil
}

func requestTwitchAccessToken(ctx context.Context, transport Transport, channel string) (twitchAccessToken, error) {
	return requestTwitchPlaybackToken(ctx, transport, "stream", "channelName", channel)
}

func requestTwitchPlaybackToken(ctx context.Context, transport Transport, tokenKind, parameter, id string) (twitchAccessToken, error) {
	if (tokenKind != "stream" && tokenKind != "video") || (parameter != "channelName" && parameter != "id") {
		return twitchAccessToken{}, fmt.Errorf("%w: invalid Twitch token request", ErrInvalidMetadata)
	}
	method := tokenKind + "PlaybackAccessToken"
	query := fmt.Sprintf(`{ %s(%s: %q, params: { platform: "web", playerBackend: "mediaplayer", playerType: "site" }) { value signature } }`, method, parameter, id)
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return twitchAccessToken{}, fmt.Errorf("%w: Twitch token request", ErrInvalidMetadata)
	}
	var response struct {
		Data struct {
			Stream *twitchAccessToken `json:"streamPlaybackAccessToken"`
			Video  *twitchAccessToken `json:"videoPlaybackAccessToken"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := RequestJSON(ctx, transport, http.MethodPost, twitchGraphQLURL, body, twitchHeaders(), &response); err != nil {
		return twitchAccessToken{}, categorizeTwitchHTTP(err)
	}
	token := response.Data.Stream
	if tokenKind == "video" {
		token = response.Data.Video
	}
	if token == nil || len(response.Errors) != 0 {
		return twitchAccessToken{}, ErrAuthentication
	}
	return *token, nil
}

func twitchHeaders() http.Header {
	headers := make(http.Header)
	headers.Set("Client-ID", twitchClientID)
	headers.Set("Content-Type", "text/plain;charset=UTF-8")
	return headers
}

func categorizeTwitchHTTP(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusUnavailableForLegalReasons:
			return ErrRegionRestricted
		default:
			return ErrTwitchNetwork
		}
	}
	return ErrTwitchNetwork
}

func twitchManifestURL(channel string, token twitchAccessToken) string {
	return twitchPlaybackManifestURL(twitchUsherBase, channel, token)
}

func twitchVODManifestURL(id string, token twitchAccessToken) string {
	return twitchPlaybackManifestURL(twitchVODBase, id, token)
}

func twitchPlaybackManifestURL(base, id string, token twitchAccessToken) string {
	query := make(url.Values)
	query.Set("allow_source", "true")
	query.Set("allow_audio_only", "true")
	query.Set("allow_spectre", "true")
	query.Set("p", twitchCacheBuster())
	query.Set("platform", "web")
	query.Set("player", "twitchweb")
	query.Set("supported_codecs", "av1,h265,h264")
	query.Set("playlist_include_framerate", "true")
	query.Set("sig", token.Signature)
	query.Set("token", token.Value)
	return base + id + ".m3u8?" + query.Encode()
}

func twitchCacheBuster() string {
	value, err := rand.Int(rand.Reader, big.NewInt(9_000_001))
	if err != nil {
		// Entropy failure must not prevent playback; this remains within the
		// upstream cache-buster range and carries no security meaning.
		return "1000000"
	}
	return fmt.Sprint(1_000_000 + value.Int64())
}

func twitchFullSizeThumbnail(thumbnail string) string {
	parsed, err := url.Parse(thumbnail)
	if err != nil {
		return thumbnail
	}
	parsed.Path = twitchPreviewSize.ReplaceAllString(parsed.Path, "0x0$1")
	return parsed.String()
}

type twitchVODResponse struct {
	Data struct {
		Video *twitchVODVideo `json:"video"`
	} `json:"data"`
	Errors []json.RawMessage `json:"errors"`
}

type twitchVODVideo struct {
	ID                  string `json:"id"`
	Title               string `json:"title"`
	Description         string `json:"description"`
	LengthSeconds       int64  `json:"lengthSeconds"`
	PreviewThumbnailURL string `json:"previewThumbnailURL"`
	PublishedAt         string `json:"publishedAt"`
	ViewCount           int64  `json:"viewCount"`
	BroadcastType       string `json:"broadcastType"`
	Owner               struct {
		DisplayName string `json:"displayName"`
		Login       string `json:"login"`
	} `json:"owner"`
	Moments struct {
		Edges []twitchMomentEdge `json:"edges"`
	} `json:"moments"`
	SeekPreviewsURL string `json:"seekPreviewsURL"`
}

type twitchMomentEdge struct {
	Node twitchMoment `json:"node"`
}

type twitchMoment struct {
	PositionMilliseconds int64  `json:"positionMilliseconds"`
	DurationMilliseconds int64  `json:"durationMilliseconds"`
	Description          string `json:"description"`
}

func requestTwitchVODMetadata(ctx context.Context, transport Transport, id string) (twitchVODVideo, error) {
	operations := []map[string]any{
		{"operationName": "VideoMetadata", "variables": map[string]any{"channelLogin": "", "videoID": id}},
		{"operationName": "VideoPlayer_ChapterSelectButtonVideo", "variables": map[string]any{"includePrivate": false, "videoID": id}},
		{"operationName": "VideoPlayer_VODSeekbarPreviewVideo", "variables": map[string]any{"includePrivate": false, "videoID": id}},
	}
	for _, operation := range operations {
		name := operation["operationName"].(string)
		operation["extensions"] = map[string]any{"persistedQuery": map[string]any{"version": 1, "sha256Hash": twitchOperationHashes[name]}}
	}
	body, err := json.Marshal(operations)
	if err != nil {
		return twitchVODVideo{}, fmt.Errorf("%w: Twitch VOD metadata request", ErrInvalidMetadata)
	}
	var response []twitchVODResponse
	if err := RequestJSON(ctx, transport, http.MethodPost, twitchGraphQLURL, body, twitchHeaders(), &response); err != nil {
		return twitchVODVideo{}, categorizeTwitchHTTP(err)
	}
	if len(response) != len(operations) {
		return twitchVODVideo{}, fmt.Errorf("%w: Twitch VOD metadata response", ErrInvalidMetadata)
	}
	for _, operation := range response {
		if len(operation.Errors) != 0 {
			return twitchVODVideo{}, fmt.Errorf("%w: Twitch VOD metadata response", ErrInvalidMetadata)
		}
	}
	if response[0].Data.Video == nil {
		return twitchVODVideo{}, ErrUnavailable
	}
	video := *response[0].Data.Video
	if response[1].Data.Video != nil {
		video.Moments = response[1].Data.Video.Moments
	}
	if response[2].Data.Video != nil {
		video.SeekPreviewsURL = response[2].Data.Video.SeekPreviewsURL
	}
	return video, nil
}

func extractTwitchVOD(ctx context.Context, transport Transport, target twitchTarget, parsed *url.URL) (Extraction, error) {
	video, err := requestTwitchVODMetadata(ctx, transport, target.id)
	if err != nil {
		return Extraction{}, err
	}
	title := strings.TrimSpace(video.Title)
	if title == "" {
		title = "Untitled Broadcast"
	}
	if len(title) > 16<<10 || len(video.Moments.Edges) > twitchMaxMoments {
		return Extraction{}, fmt.Errorf("%w: Twitch VOD exceeds metadata limits", ErrInvalidMetadata)
	}
	token, err := requestTwitchPlaybackToken(ctx, transport, "video", "id", target.id)
	if err != nil {
		return Extraction{}, err
	}
	if token.Value == "" || token.Signature == "" {
		return Extraction{}, ErrAuthentication
	}
	videoID := video.ID
	if !twitchVODPattern.MatchString(videoID) {
		videoID = target.id
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String("v" + videoID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String("https://www.twitch.tv/videos/" + target.id)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(manifestFormat("hls", twitchVODManifestURL(target.id, token), "m3u8_native")))},
		value.Field{Key: "is_live", Value: value.Bool(false)},
		value.Field{Key: "was_live", Value: value.Bool(true)},
		value.Field{Key: "live_status", Value: value.String("was_live")},
	)
	twitchSetString(info, "description", video.Description)
	twitchSetPositiveInt(info, "duration", video.LengthSeconds)
	twitchSetString(info, "uploader", video.Owner.DisplayName)
	twitchSetString(info, "uploader_id", strings.ToLower(video.Owner.Login))
	twitchSetPositiveInt(info, "timestamp", twitchTimestamp(video.PublishedAt))
	twitchSetPositiveInt(info, "view_count", video.ViewCount)
	if validTwitchAssetURL(video.PreviewThumbnailURL) {
		info.Set("thumbnails", twitchThumbnails(video.PreviewThumbnailURL))
	}
	if chapters := twitchChapters(video.Moments.Edges, video.LengthSeconds); len(chapters) != 0 {
		info.Set("chapters", value.List(chapters...))
	}
	if start, ok := parseTwitchStartTime(parsed.Query().Get("t")); ok {
		info.Set("start_time", value.Int(start))
	}
	return Media(value.NewInfo(info)), nil
}

type twitchClipResponse struct {
	Data struct {
		Clip *twitchClip `json:"clip"`
	} `json:"data"`
	Errors []json.RawMessage `json:"errors"`
}

type twitchClip struct {
	ID                  string            `json:"id"`
	Slug                string            `json:"slug"`
	Title               string            `json:"title"`
	DurationSeconds     int64             `json:"durationSeconds"`
	ViewCount           int64             `json:"viewCount"`
	CreatedAt           string            `json:"createdAt"`
	ThumbnailURL        string            `json:"thumbnailURL"`
	PlaybackAccessToken twitchAccessToken `json:"playbackAccessToken"`
	Broadcaster         twitchClipOwner   `json:"broadcaster"`
	Curator             twitchClipOwner   `json:"curator"`
	Game                struct {
		DisplayName string `json:"displayName"`
	} `json:"game"`
	Assets []twitchClipAsset `json:"assets"`
}

type twitchClipOwner struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	IsPartner   *bool  `json:"isPartner"`
	Followers   struct {
		TotalCount int64 `json:"totalCount"`
	} `json:"followers"`
}

type twitchClipAsset struct {
	AspectRatio    float64 `json:"aspectRatio"`
	ThumbnailURL   string  `json:"thumbnailURL"`
	VideoQualities []struct {
		SourceURL string  `json:"sourceURL"`
		Quality   string  `json:"quality"`
		FrameRate float64 `json:"frameRate"`
	} `json:"videoQualities"`
}

func requestTwitchClip(ctx context.Context, transport Transport, slug string) (twitchClip, error) {
	operation := map[string]any{
		"operationName": "ShareClipRenderStatus",
		"variables":     map[string]any{"slug": slug},
		"extensions": map[string]any{"persistedQuery": map[string]any{
			"version": 1, "sha256Hash": twitchOperationHashes["ShareClipRenderStatus"],
		}},
	}
	body, err := json.Marshal([]map[string]any{operation})
	if err != nil {
		return twitchClip{}, fmt.Errorf("%w: Twitch clip metadata request", ErrInvalidMetadata)
	}
	var response []twitchClipResponse
	if err := RequestJSON(ctx, transport, http.MethodPost, twitchGraphQLURL, body, twitchHeaders(), &response); err != nil {
		return twitchClip{}, categorizeTwitchHTTP(err)
	}
	if len(response) != 1 || len(response[0].Errors) != 0 {
		return twitchClip{}, fmt.Errorf("%w: Twitch clip metadata response", ErrInvalidMetadata)
	}
	if response[0].Data.Clip == nil {
		return twitchClip{}, ErrUnavailable
	}
	return *response[0].Data.Clip, nil
}

func extractTwitchClip(ctx context.Context, transport Transport, target twitchTarget) (Extraction, error) {
	clip, err := requestTwitchClip(ctx, transport, target.id)
	if err != nil {
		return Extraction{}, err
	}
	if clip.PlaybackAccessToken.Value == "" || clip.PlaybackAccessToken.Signature == "" {
		return Extraction{}, ErrAuthentication
	}
	if len(clip.Assets) == 0 {
		return Extraction{}, ErrUnavailable
	}
	if len(clip.Assets) > twitchMaxAssets {
		return Extraction{}, fmt.Errorf("%w: Twitch clip exceeds asset limits", ErrInvalidMetadata)
	}
	formats := make([]value.Value, 0)
	thumbnails := make([]value.Value, 0, len(clip.Assets)+1)
	seen := make(map[string]bool)
	qualityCount := 0
	for assetIndex, asset := range clip.Assets {
		qualityCount += len(asset.VideoQualities)
		if qualityCount > twitchMaxAssets {
			return Extraction{}, fmt.Errorf("%w: Twitch clip exceeds quality limits", ErrInvalidMetadata)
		}
		portrait := assetIndex > 0
		for _, quality := range asset.VideoQualities {
			if !validTwitchAssetURL(quality.SourceURL) || seen[quality.SourceURL] {
				continue
			}
			seen[quality.SourceURL] = true
			signedURL := twitchSignedAssetURL(quality.SourceURL, clip.PlaybackAccessToken)
			formatID := strings.TrimSpace(quality.Quality)
			if len(formatID) > 128 {
				formatID = ""
			}
			if portrait {
				formatID = "portrait-" + formatID
			}
			if formatID == "" || formatID == "portrait-" {
				formatID = fmt.Sprintf("clip-%d", len(formats)+1)
			}
			format, ok := hostedURLFormat(formatID, signedURL)
			if !ok {
				continue
			}
			if match := twitchQualityHeight.FindStringSubmatch(quality.Quality); len(match) == 2 {
				height, _ := strconv.ParseInt(match[1], 10, 64)
				twitchSetPositiveInt(format, "height", height)
			}
			if quality.FrameRate > 0 && quality.FrameRate <= 1000 {
				format.Set("fps", value.Float(quality.FrameRate))
			}
			if asset.AspectRatio > 0 && asset.AspectRatio <= 100 {
				format.Set("aspect_ratio", value.Float(asset.AspectRatio))
			}
			formats = append(formats, value.ObjectValue(format))
		}
		if validTwitchAssetURL(asset.ThumbnailURL) {
			thumbnailID := "default"
			if portrait {
				thumbnailID = "portrait"
			}
			thumbnails = append(thumbnails, value.ObjectValue(value.NewObject(
				value.Field{Key: "id", Value: value.String(thumbnailID)},
				value.Field{Key: "url", Value: value.String(asset.ThumbnailURL)},
			)))
		}
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	if validTwitchAssetURL(clip.ThumbnailURL) && !seenTwitchThumbnail(thumbnails, clip.ThumbnailURL) {
		thumbnails = append(thumbnails, value.ObjectValue(value.NewObject(
			value.Field{Key: "id", Value: value.String("small")},
			value.Field{Key: "url", Value: value.String(clip.ThumbnailURL)},
		)))
	}
	title := strings.TrimSpace(clip.Title)
	if title == "" || len(title) > 16<<10 {
		return Extraction{}, fmt.Errorf("%w: missing Twitch clip title", ErrInvalidMetadata)
	}
	id := clip.ID
	if !twitchClipPattern.MatchString(id) {
		id = target.id
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "display_id", Value: value.String(target.id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String("https://clips.twitch.tv/" + target.id)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
		value.Field{Key: "is_live", Value: value.Bool(false)},
	)
	twitchSetPositiveInt(info, "duration", clip.DurationSeconds)
	twitchSetPositiveInt(info, "view_count", clip.ViewCount)
	twitchSetPositiveInt(info, "timestamp", twitchTimestamp(clip.CreatedAt))
	twitchSetString(info, "channel", clip.Broadcaster.DisplayName)
	twitchSetString(info, "channel_id", clip.Broadcaster.ID)
	twitchSetPositiveInt(info, "channel_follower_count", clip.Broadcaster.Followers.TotalCount)
	if clip.Broadcaster.IsPartner != nil {
		info.Set("channel_is_verified", value.Bool(*clip.Broadcaster.IsPartner))
	}
	twitchSetString(info, "uploader", clip.Curator.DisplayName)
	twitchSetString(info, "uploader_id", clip.Curator.ID)
	if creator := boundedTwitchString(clip.Broadcaster.DisplayName); creator != "" {
		info.Set("creators", value.List(value.String(creator)))
	}
	if category := boundedTwitchString(clip.Game.DisplayName); category != "" {
		info.Set("categories", value.List(value.String(category)))
	}
	if len(thumbnails) != 0 {
		info.Set("thumbnails", value.List(thumbnails...))
	}
	return Media(value.NewInfo(info)), nil
}

func twitchSetString(object *value.Object, key, input string) {
	if input = boundedTwitchString(input); input != "" {
		object.Set(key, value.String(input))
	}
}

func boundedTwitchString(input string) string {
	input = strings.TrimSpace(input)
	if input == "" || len(input) > 16<<10 {
		return ""
	}
	return input
}

func twitchSetPositiveInt(object *value.Object, key string, input int64) {
	if input > 0 {
		object.Set(key, value.Int(input))
	}
}

func twitchTimestamp(input string) int64 {
	parsed, err := time.Parse(time.RFC3339, input)
	if err != nil {
		return 0
	}
	return parsed.Unix()
}

func twitchThumbnails(thumbnail string) value.Value {
	full := twitchFullSizeThumbnail(thumbnail)
	thumbnails := []value.Value{value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(full)}))}
	if full != thumbnail {
		thumbnails = append(thumbnails, value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(thumbnail)})))
	}
	return value.List(thumbnails...)
}

func twitchChapters(edges []twitchMomentEdge, duration int64) []value.Value {
	if len(edges) == 0 {
		return nil
	}
	type chapter struct {
		start, end int64
		title      string
	}
	chapters := make([]chapter, 0, len(edges))
	for _, edge := range edges {
		position := edge.Node.PositionMilliseconds / 1000
		chapterDuration := edge.Node.DurationMilliseconds / 1000
		title := strings.TrimSpace(edge.Node.Description)
		if edge.Node.PositionMilliseconds < 0 || chapterDuration <= 0 || title == "" || len(title) > 1024 {
			continue
		}
		end := position + chapterDuration
		if end < position {
			continue
		}
		if duration > 0 && end > duration {
			end = duration
		}
		if end <= position {
			continue
		}
		chapters = append(chapters, chapter{start: position, end: end, title: title})
	}
	sort.Slice(chapters, func(i, j int) bool { return chapters[i].start < chapters[j].start })
	result := make([]value.Value, 0, len(chapters))
	var previousEnd int64
	for _, chapter := range chapters {
		if chapter.start < previousEnd {
			continue
		}
		result = append(result, value.ObjectValue(value.NewObject(
			value.Field{Key: "start_time", Value: value.Int(chapter.start)},
			value.Field{Key: "end_time", Value: value.Int(chapter.end)},
			value.Field{Key: "title", Value: value.String(chapter.title)},
		)))
		previousEnd = chapter.end
	}
	return result
}

func parseTwitchStartTime(input string) (int64, bool) {
	if input == "" || len(input) > 32 {
		return 0, false
	}
	if seconds, err := strconv.ParseInt(input, 10, 64); err == nil && seconds >= 0 && seconds <= 30*24*60*60 {
		return seconds, true
	}
	remaining := strings.ToLower(input)
	var total int64
	seen := false
	for _, unit := range []struct {
		suffix string
		factor int64
	}{
		{"h", 3600}, {"m", 60}, {"s", 1},
	} {
		index := strings.Index(remaining, unit.suffix)
		if index < 0 {
			continue
		}
		if index == 0 {
			return 0, false
		}
		number, err := strconv.ParseInt(remaining[:index], 10, 64)
		if err != nil || number < 0 {
			return 0, false
		}
		if number > (30*24*60*60-total)/unit.factor {
			return 0, false
		}
		total += number * unit.factor
		remaining = remaining[index+1:]
		seen = true
	}
	return total, seen && remaining == ""
}

func validTwitchAssetURL(rawURL string) bool {
	if len(rawURL) == 0 || len(rawURL) > twitchMaxURL {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if net.ParseIP(host) != nil || host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	for _, suffix := range []string{".twitchcdn.net", ".ttvnw.net", ".jtvnw.net", ".twitch.tv", ".example.test"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func twitchSignedAssetURL(rawURL string, token twitchAccessToken) string {
	parsed, _ := url.Parse(rawURL)
	query := parsed.Query()
	query.Set("sig", token.Signature)
	query.Set("token", token.Value)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func seenTwitchThumbnail(thumbnails []value.Value, rawURL string) bool {
	for _, entry := range thumbnails {
		object, _ := entry.Object()
		if existing, _ := object.Lookup("url").StringValue(); existing == rawURL {
			return true
		}
	}
	return false
}
