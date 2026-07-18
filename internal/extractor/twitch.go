package extractor

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	twitchGraphQLURL = "https://gql.twitch.tv/gql"
	twitchUsherBase  = "https://usher.ttvnw.net/api/channel/hls/"
	twitchClientID   = "ue6666qo983tsx6so1t0vnawi233wa"
)

var (
	twitchChannelPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,25}$`)
	twitchPreviewSize    = regexp.MustCompile(`\d+x\d+(\.[A-Za-z0-9]+)$`)
	twitchReservedPaths  = map[string]struct{}{
		"activate": {}, "bits": {}, "collections": {}, "directory": {}, "downloads": {},
		"drops": {}, "inventory": {}, "jobs": {}, "login": {}, "p": {}, "payments": {},
		"prime": {}, "products": {}, "search": {}, "settings": {}, "signup": {},
		"subscriptions": {}, "turbo": {}, "videos": {}, "wallet": {},
	}
)

var twitchOperationHashes = map[string]string{
	"StreamMetadata":         "ad022ca32220d5523d03a23cbcb5beaa1e0999889c1f8f78f9f2520dafb5cae6",
	"ComscoreStreamingQuery": "e1edae8122517d013405f237ffcc124515dc6ded82480a88daef69c83b53ac01",
	"VideoPreviewOverlay":    "9515480dee68a77e667cb19de634739d33f243572b007e98e67184b1a5d8369f",
}

type Twitch struct{}

func NewTwitch() Twitch { return Twitch{} }

func (Twitch) Name() string { return "twitch" }

func (Twitch) Suitable(parsed *url.URL) bool {
	_, ok := twitchChannel(parsed)
	return ok
}

func (Twitch) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	channel, ok := twitchChannel(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	channel = strings.ToLower(channel)

	metadata, err := requestTwitchMetadata(ctx, request.Transport, channel)
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

	token, err := requestTwitchAccessToken(ctx, request.Transport, channel)
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
	query := fmt.Sprintf(`{ streamPlaybackAccessToken(channelName: %q, params: { platform: "web", playerBackend: "mediaplayer", playerType: "site" }) { value signature } }`, channel)
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return twitchAccessToken{}, fmt.Errorf("%w: Twitch token request", ErrInvalidMetadata)
	}
	var response struct {
		Data struct {
			Token *twitchAccessToken `json:"streamPlaybackAccessToken"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := RequestJSON(ctx, transport, http.MethodPost, twitchGraphQLURL, body, twitchHeaders(), &response); err != nil {
		return twitchAccessToken{}, categorizeTwitchHTTP(err)
	}
	if response.Data.Token == nil || len(response.Errors) != 0 {
		return twitchAccessToken{}, ErrAuthentication
	}
	return *response.Data.Token, nil
}

func twitchHeaders() http.Header {
	headers := make(http.Header)
	headers.Set("Client-ID", twitchClientID)
	headers.Set("Content-Type", "text/plain;charset=UTF-8")
	return headers
}

func categorizeTwitchHTTP(err error) error {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		}
	}
	return err
}

func twitchManifestURL(channel string, token twitchAccessToken) string {
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
	return twitchUsherBase + channel + ".m3u8?" + query.Encode()
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
