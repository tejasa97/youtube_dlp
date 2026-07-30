package extractor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	twitchGraphQLURL        = "https://gql.twitch.tv/gql"
	twitchGraphQLOrigin     = "https://gql.twitch.tv"
	twitchUsherBase         = "https://usher.ttvnw.net/api/channel/hls/"
	twitchVODBase           = "https://usher.ttvnw.net/vod/"
	twitchClientID          = "ue6666qo983tsx6so1t0vnawi233wa"
	twitchMaxURL            = 8 << 10
	twitchMaxMoments        = 1000
	twitchMaxAssets         = 64
	twitchMaxAuthTokenBytes = 512

	twitchVideosOperation   = "FilterableVideoTower_Videos"
	twitchVideosPageLimit   = 100
	twitchVideosMaxEdges    = 100
	twitchVideosMaxCursor   = 4 << 10
	twitchVideosMaxString   = 16 << 10
	twitchVideosStartToken  = "__twitch_videos_start__"
	twitchVideosDefaultSort = "Date"

	twitchCollectionDirectOperation    = "CollectionSideBar"
	twitchCollectionsOperation         = "ChannelCollectionsContent"
	twitchCollectionsPageLimit         = 100
	twitchCollectionsMaxEdges          = 100
	twitchCollectionsMaxCursor         = 4 << 10
	twitchCollectionsMaxString         = 16 << 10
	twitchCollectionsStartToken        = "__twitch_collections_start__"
	twitchCollectionsMaxEdgeArrayBytes = twitchCollectionsMaxEdges*(twitchMaxURL+1) + 2

	twitchClipsOperation         = "ClipsCards__User"
	twitchClipsPageLimit         = 20
	twitchClipsMaxEdges          = 20
	twitchClipsMaxCursor         = 4 << 10
	twitchClipsMaxString         = 16 << 10
	twitchClipsMaxLanguage       = 64
	twitchClipsMaxNumericText    = 64
	twitchClipsMaxTimestampText  = 128
	twitchClipsStartToken        = "__twitch_clips_start__"
	twitchClipsMaxEdgeArrayBytes = twitchClipsMaxEdges*(twitchMaxURL+1) + 2

	twitchStoryboardMaxJSONBytes = 1 << 20
	twitchStoryboardMaxSpecs     = 16
	twitchStoryboardMaxImages    = 1000
	twitchStoryboardMaxDimension = 10_000
	// twitchManifestRestrictionMaxBytes bounds the only error-body inspection
	// performed for a Twitch playback manifest. It is deliberately much smaller
	// than a media manifest and is sufficient for Twitch's JSON error envelope.
	twitchManifestRestrictionMaxBytes = 64 << 10
	// These structural limits make marker detection independent of adversarial
	// JSON nesting or token count within the capped response body.
	twitchManifestRestrictionMaxDepth  = 64
	twitchManifestRestrictionMaxTokens = 4096
)

type twitchVideosBroadcast struct {
	Type  string
	Label string
}

type twitchManifestRestrictionFrame struct {
	object     bool
	expectsKey bool
}

type twitchClipsRange struct {
	Filter string
	Label  string
}

var (
	twitchChannelPattern    = regexp.MustCompile(`^[A-Za-z0-9_]{1,25}$`)
	twitchVODPattern        = regexp.MustCompile(`^[0-9]{1,20}$`)
	twitchClipPattern       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	twitchCollectionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	twitchPreviewSize       = regexp.MustCompile(`\d+x\d+(\.[A-Za-z0-9]+)$`)
	twitchQualityHeight     = regexp.MustCompile(`^([0-9]{2,5})p?$`)
	twitchClipLegacyID      = regexp.MustCompile(`%7C(\d+)(?:-\d+)?\.mp4`)
	twitchReservedPaths     = map[string]struct{}{
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
	twitchVideosOperation:                  "67004f7881e65c297936f32c75246470629557a393788fb5a69d6d9a25a8fd5f",
	twitchCollectionDirectOperation:        "016e1e4ccee0eb4698eb3bf1a04dc1c077fb746c78c82bac9a8f0289658fbd1a",
	twitchCollectionsOperation:             "5247910a19b1cd2b760939bf4cba4dcbd3d13bdf8c266decd16956f6ef814077",
	twitchClipsOperation:                   "1cd671bfa12cec480499c087319f26d21925e9695d1f80225aae6a4354f23088",
}

var twitchVideosDefaultBroadcast = twitchVideosBroadcast{Type: "", Label: "All Videos"}

var twitchVideosBroadcasts = map[string]twitchVideosBroadcast{
	"archives":       {Type: "ARCHIVE", Label: "Past Broadcasts"},
	"highlights":     {Type: "HIGHLIGHT", Label: "Highlights"},
	"uploads":        {Type: "UPLOAD", Label: "Uploads"},
	"past_premieres": {Type: "PAST_PREMIERE", Label: "Past Premieres"},
	"all":            twitchVideosDefaultBroadcast,
}

var twitchVideosSortLabels = map[string]string{
	"time":  twitchVideosDefaultSort,
	"views": "Popular",
}

var twitchClipsDefaultRange = twitchClipsRange{Filter: "LAST_WEEK", Label: "Top 7D"}

var twitchClipsRanges = map[string]twitchClipsRange{
	"24hr": {Filter: "LAST_DAY", Label: "Top 24H"},
	"7d":   twitchClipsDefaultRange,
	"30d":  {Filter: "LAST_MONTH", Label: "Top 30D"},
	"all":  {Filter: "ALL_TIME", Label: "Top All"},
}

var (
	ErrTwitchNetwork     = errors.New("Twitch network request failed")
	ErrTwitchRateLimited = errors.New("Twitch rate limited")
	// ErrTwitchSubscriberOnly distinguishes a logged-in account without the
	// required entitlement from a missing login. It is consumed by the
	// follow-up restricted-manifest categorization.
	ErrTwitchSubscriberOnly = errors.New("Twitch subscriber-only content unavailable to this account")
)

// twitchAuthenticatedTransport is deliberately narrow: Twitch OAuth is made
// only from the operation cookie jar and only for a no-redirect GQL request.
// It is not a general-purpose credential transport.
type twitchAuthenticatedTransport interface {
	Transport
	Cookies(string) ([]*http.Cookie, error)
	DoNoRedirect(context.Context, *http.Request) (*http.Response, error)
}

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
	case twitchKindVideos:
		return extractTwitchVideos(ctx, request.Transport, target)
	case twitchKindCollection:
		return extractTwitchCollection(ctx, request.Transport, target)
	case twitchKindChannelCollections:
		return extractTwitchChannelCollections(ctx, request.Transport, target)
	case twitchKindChannelClips:
		return extractTwitchChannelClips(ctx, request.Transport, target)
	default:
		return extractTwitchLive(ctx, request.Transport, target.id)
	}
}

type twitchKind uint8

const (
	twitchKindLive twitchKind = iota + 1
	twitchKindVOD
	twitchKindClip
	twitchKindVideos
	twitchKindCollection
	twitchKindChannelCollections
	twitchKindChannelClips
)

type twitchVideosQuery struct {
	broadcastType  string
	broadcastLabel string
	videoSort      string
	sortLabel      string
}

type twitchClipsQuery struct {
	filter string
	label  string
}

type twitchTarget struct {
	kind   twitchKind
	id     string
	videos twitchVideosQuery
	clips  twitchClipsQuery
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
	if len(parts) == 2 && parts[0] == "collections" {
		if parsed.Fragment != "" {
			return twitchTarget{}, false
		}
		if !twitchRouteQuerySafe(parsed) {
			return twitchTarget{}, false
		}
		if id, ok := decode(parts[1], twitchCollectionPattern); ok {
			return twitchTarget{kind: twitchKindCollection, id: id}, true
		}
		return twitchTarget{}, false
	}
	if target, ok := classifyTwitchChannelCollectionsURL(parsed, parts, decode); ok {
		return target, true
	}
	if target, ok := classifyTwitchChannelClipsURL(parsed, parts, decode); ok {
		return target, true
	}
	if target, ok := classifyTwitchVideosURL(parsed, parts, decode); ok {
		return target, true
	}
	if channel, ok := twitchChannel(parsed); ok {
		return twitchTarget{kind: twitchKindLive, id: strings.ToLower(channel)}, true
	}
	return twitchTarget{}, false
}

func classifyTwitchVideosURL(parsed *url.URL, parts []string, decode func(string, *regexp.Regexp) (string, bool)) (twitchTarget, bool) {
	if parsed == nil || parsed.Fragment != "" {
		return twitchTarget{}, false
	}
	var channelRaw string
	switch {
	case len(parts) == 2 && (parts[1] == "videos" || parts[1] == "profile"):
		channelRaw = parts[0]
	case len(parts) == 3 && parts[1] == "videos" && parts[2] == "all":
		channelRaw = parts[0]
	default:
		return twitchTarget{}, false
	}
	channel, ok := decode(channelRaw, twitchChannelPattern)
	if !ok {
		return twitchTarget{}, false
	}
	if _, reserved := twitchReservedPaths[strings.ToLower(channel)]; reserved {
		return twitchTarget{}, false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return twitchTarget{}, false
	}
	filter := "all"
	if values, present := query["filter"]; present {
		if len(values) != 1 || !twitchVideosQueryValueOK(values[0]) {
			return twitchTarget{}, false
		}
		// Pinned parse_qs drops blank values, so filter= falls back to all.
		if values[0] != "" {
			filter = values[0]
		}
	}
	if filter == "clips" || filter == "collections" {
		return twitchTarget{}, false
	}
	sortKey := "time"
	if values, present := query["sort"]; present {
		if len(values) != 1 || !twitchVideosQueryValueOK(values[0]) {
			return twitchTarget{}, false
		}
		// Pinned parse_qs drops blank values, so sort= falls back to time.
		if values[0] != "" {
			sortKey = values[0]
		}
	}
	broadcast, found := twitchVideosBroadcasts[filter]
	if !found {
		// Pinned TwitchVideosIE falls back unknown filters to All Videos.
		broadcast = twitchVideosDefaultBroadcast
	}
	sortLabel, found := twitchVideosSortLabels[sortKey]
	if !found {
		sortLabel = twitchVideosDefaultSort
	}
	return twitchTarget{
		kind: twitchKindVideos,
		id:   strings.ToLower(channel),
		videos: twitchVideosQuery{
			broadcastType:  broadcast.Type,
			broadcastLabel: broadcast.Label,
			videoSort:      strings.ToUpper(sortKey),
			sortLabel:      sortLabel,
		},
	}, true
}

func classifyTwitchChannelCollectionsURL(parsed *url.URL, parts []string, decode func(string, *regexp.Regexp) (string, bool)) (twitchTarget, bool) {
	if parsed == nil || parsed.Fragment != "" {
		return twitchTarget{}, false
	}
	if len(parts) != 2 || parts[1] != "videos" {
		return twitchTarget{}, false
	}
	channel, ok := decode(parts[0], twitchChannelPattern)
	if !ok {
		return twitchTarget{}, false
	}
	if _, reserved := twitchReservedPaths[strings.ToLower(channel)]; reserved {
		return twitchTarget{}, false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return twitchTarget{}, false
	}
	if !twitchQueryValuesSafe(query) {
		return twitchTarget{}, false
	}
	if !twitchChannelCollectionsQueryOK(query) {
		return twitchTarget{}, false
	}
	return twitchTarget{kind: twitchKindChannelCollections, id: strings.ToLower(channel)}, true
}

func classifyTwitchChannelClipsURL(parsed *url.URL, parts []string, decode func(string, *regexp.Regexp) (string, bool)) (twitchTarget, bool) {
	if parsed == nil || parsed.Fragment != "" {
		return twitchTarget{}, false
	}
	if len(parts) != 2 || (parts[1] != "clips" && parts[1] != "videos") {
		return twitchTarget{}, false
	}
	channel, ok := decode(parts[0], twitchChannelPattern)
	if !ok {
		return twitchTarget{}, false
	}
	if _, reserved := twitchReservedPaths[strings.ToLower(channel)]; reserved {
		return twitchTarget{}, false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return twitchTarget{}, false
	}
	if !twitchQueryValuesSafe(query) {
		return twitchTarget{}, false
	}
	if parts[1] == "videos" {
		if !twitchChannelClipsVideosQueryOK(query) {
			return twitchTarget{}, false
		}
	} else if !twitchChannelClipsPathQueryOK(query) {
		return twitchTarget{}, false
	}
	clips, ok := twitchClipsQueryFromValues(query)
	if !ok {
		return twitchTarget{}, false
	}
	return twitchTarget{kind: twitchKindChannelClips, id: strings.ToLower(channel), clips: clips}, true
}

func twitchRouteQuerySafe(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	if parsed.RawQuery == "" {
		return true
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return false
	}
	return twitchQueryValuesSafe(query)
}

func twitchQueryValuesSafe(query url.Values) bool {
	for key, values := range query {
		if !twitchVideosQueryValueOK(key) {
			return false
		}
		for _, value := range values {
			if !twitchVideosQueryValueOK(value) {
				return false
			}
		}
	}
	return true
}

func twitchChannelCollectionsQueryOK(query url.Values) bool {
	filterValues, present := query["filter"]
	if !present || len(filterValues) != 1 || !twitchVideosQueryValueOK(filterValues[0]) || filterValues[0] != "collections" {
		return false
	}
	if sortValues, present := query["sort"]; present {
		if len(sortValues) != 1 || !twitchVideosQueryValueOK(sortValues[0]) {
			return false
		}
	}
	return true
}

func twitchChannelClipsVideosQueryOK(query url.Values) bool {
	filterValues, present := query["filter"]
	if !present || len(filterValues) != 1 || !twitchVideosQueryValueOK(filterValues[0]) || filterValues[0] != "clips" {
		return false
	}
	if rangeValues, present := query["range"]; present {
		if len(rangeValues) != 1 || !twitchVideosQueryValueOK(rangeValues[0]) {
			return false
		}
	}
	return true
}

func twitchChannelClipsPathQueryOK(query url.Values) bool {
	if filterValues, present := query["filter"]; present {
		if len(filterValues) != 1 || !twitchVideosQueryValueOK(filterValues[0]) {
			return false
		}
	}
	if rangeValues, present := query["range"]; present {
		if len(rangeValues) != 1 || !twitchVideosQueryValueOK(rangeValues[0]) {
			return false
		}
	}
	return true
}

func twitchClipsQueryFromValues(query url.Values) (twitchClipsQuery, bool) {
	rangeKey := "7d"
	if values, present := query["range"]; present {
		if len(values) != 1 || !twitchVideosQueryValueOK(values[0]) {
			return twitchClipsQuery{}, false
		}
		// Pinned parse_qs drops blank values, so range= falls back to 7d/LAST_WEEK.
		if values[0] != "" {
			rangeKey = values[0]
		}
	}
	selected, found := twitchClipsRanges[rangeKey]
	if !found {
		selected = twitchClipsDefaultRange
	}
	return twitchClipsQuery{filter: selected.Filter, label: selected.Label}, true
}

func twitchVideosQueryValueOK(value string) bool {
	if len(value) > twitchVideosMaxString || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == 0 || unicode.IsControl(character) {
			return false
		}
	}
	return true
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
	if err := requestTwitchGQL(ctx, transport, body, &response); err != nil {
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
	if err := requestTwitchGQL(ctx, transport, body, &response); err != nil {
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

// twitchOAuthHeaders constructs the only credential-bearing Twitch headers.
// Cookie values are untrusted input even when they originate in the operation
// jar, so reject control characters and overlong values before header use.
func twitchOAuthHeaders(token string) (http.Header, error) {
	if !twitchSafeAuthToken(token) {
		return nil, ErrAuthentication
	}
	headers := twitchHeaders()
	headers.Set("Authorization", "OAuth "+token)
	return headers, nil
}

func twitchSafeAuthToken(token string) bool {
	return token != "" && len(token) <= twitchMaxAuthTokenBytes && !strings.ContainsAny(token, "\r\n\x00")
}

// twitchAuthToken reads only the cookie snapshot applicable to the exact GQL
// origin. A present but malformed token is an authentication failure rather
// than an opportunity to silently downgrade an authenticated operation.
func twitchAuthToken(cookies []*http.Cookie) (string, bool, error) {
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name != "auth-token" {
			continue
		}
		if !twitchSafeAuthToken(cookie.Value) {
			return "", false, ErrAuthentication
		}
		return cookie.Value, true, nil
	}
	return "", false, nil
}

// twitchHasAuthToken reports whether a usable operation-jar auth token exists
// for the one credential origin. It never reads cookies for media origins.
func twitchHasAuthToken(transport Transport) (bool, error) {
	authTransport, ok := transport.(twitchAuthenticatedTransport)
	if !ok {
		return false, nil
	}
	cookies, err := authTransport.Cookies(twitchGraphQLOrigin)
	if err != nil {
		return false, ErrTwitchNetwork
	}
	_, present, err := twitchAuthToken(cookies)
	if err != nil {
		return false, err
	}
	return present, nil
}

// requestTwitchGQL is the sole Twitch GraphQL transport path. It retains the
// anonymous request behavior when no auth-token cookie exists. When one does,
// it permits the jar cookie and derived OAuth header only on the fixed Twitch
// GQL origin, and refuses redirects before the request reaches the network.
func requestTwitchGQL(ctx context.Context, transport Transport, body []byte, target any) error {
	if transport == nil {
		return errors.New("invalid Twitch GQL request")
	}
	authTransport, ok := transport.(twitchAuthenticatedTransport)
	if !ok {
		return RequestJSON(ctx, transport, http.MethodPost, twitchGraphQLURL, body, twitchHeaders(), target)
	}
	cookies, err := authTransport.Cookies(twitchGraphQLOrigin)
	if err != nil {
		return ErrTwitchNetwork
	}
	token, present, err := twitchAuthToken(cookies)
	if err != nil {
		return err
	}
	if !present {
		return RequestJSON(ctx, transport, http.MethodPost, twitchGraphQLURL, body, twitchHeaders(), target)
	}
	headers, err := twitchOAuthHeaders(token)
	if err != nil {
		return ErrAuthentication
	}
	err = requestJSON(ctx, authTransport.DoNoRedirect, http.MethodPost, twitchGraphQLURL, body, headers, target)
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return err
	}
	// Do not let a transport error reflect a cookie-derived token.
	return ErrTwitchNetwork
}

func categorizeTwitchHTTP(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) ||
		errors.Is(err, ErrAuthentication) || errors.Is(err, ErrTwitchSubscriberOnly) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusTooManyRequests:
			return ErrTwitchRateLimited
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

// probeTwitchVODManifest inspects only a forbidden response, because that is
// the one point where Twitch exposes its subscriber-entitlement marker. The
// signed Usher URL is always requested through a credential-isolated,
// redirect-disabled transport: neither the operation jar nor OAuth headers can
// reach Usher, a redirect target, or any media origin.
func probeTwitchVODManifest(ctx context.Context, transport Transport, manifestURL string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	isolate, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return fmt.Errorf("%w: invalid Twitch manifest request", ErrInvalidMetadata)
	}
	response, err := isolate.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return ErrTwitchNetwork
	}
	if response == nil || response.Body == nil {
		return ErrTwitchNetwork
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return nil
	}
	if response.StatusCode != http.StatusForbidden {
		return categorizeTwitchHTTP(&HTTPStatusError{Code: response.StatusCode})
	}
	restricted, err := twitchManifestRestricted(response.Body)
	if err != nil {
		return err
	}
	if !restricted {
		return ErrAuthentication
	}
	authenticated, err := twitchHasAuthToken(transport)
	if err != nil {
		return err
	}
	if authenticated {
		return ErrTwitchSubscriberOnly
	}
	return ErrAuthentication
}

// twitchManifestRestricted parses a capped JSON error response with bounded
// token-stream state. It recognizes only string values exactly equal to
// Twitch's two documented restriction markers. Invalid, trailing, truncated,
// or structurally excessive JSON deliberately does not become an entitlement
// classification.
func twitchManifestRestricted(body io.Reader) (bool, error) {
	if body == nil {
		return false, nil
	}
	payload, err := io.ReadAll(io.LimitReader(body, twitchManifestRestrictionMaxBytes+1))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return false, ErrTwitchNetwork
	}
	if len(payload) > twitchManifestRestrictionMaxBytes {
		return false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	stack := make([]twitchManifestRestrictionFrame, 0, twitchManifestRestrictionMaxDepth)
	roots, tokens := 0, 0
	marker := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return roots == 1 && len(stack) == 0 && marker, nil
		}
		if err != nil {
			return false, nil
		}
		tokens++
		if tokens > twitchManifestRestrictionMaxTokens || roots > 1 {
			return false, nil
		}
		switch typed := token.(type) {
		case json.Delim:
			switch typed {
			case '{', '[':
				if len(stack) == 0 {
					roots++
				} else {
					twitchRestrictionValueComplete(stack)
				}
				if len(stack) == twitchManifestRestrictionMaxDepth {
					return false, nil
				}
				stack = append(stack, twitchManifestRestrictionFrame{object: typed == '{', expectsKey: typed == '{'})
			case '}', ']':
				if len(stack) == 0 || (typed == '}' && !stack[len(stack)-1].object) || (typed == ']' && stack[len(stack)-1].object) {
					return false, nil
				}
				stack = stack[:len(stack)-1]
				if len(stack) > 0 {
					twitchRestrictionValueComplete(stack)
				}
			}
		case string:
			if len(stack) == 0 {
				roots++
			} else if stack[len(stack)-1].object && stack[len(stack)-1].expectsKey {
				stack[len(stack)-1].expectsKey = false
				continue
			} else {
				if typed == "vod_manifest_restricted" || typed == "unauthorized_entitlements" {
					marker = true
				}
				twitchRestrictionValueComplete(stack)
			}
		default:
			if len(stack) == 0 {
				roots++
			} else {
				twitchRestrictionValueComplete(stack)
			}
		}
	}
}

func twitchRestrictionValueComplete(stack []twitchManifestRestrictionFrame) {
	if len(stack) != 0 && stack[len(stack)-1].object {
		stack[len(stack)-1].expectsKey = true
	}
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
	if err := requestTwitchGQL(ctx, transport, body, &response); err != nil {
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
	manifestURL := twitchVODManifestURL(target.id, token)
	if err := probeTwitchVODManifest(ctx, transport, manifestURL); err != nil {
		return Extraction{}, err
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
		value.Field{Key: "formats", Value: value.List()},
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
	formats := []value.Value{value.ObjectValue(manifestFormat("hls", manifestURL, "m3u8_native"))}
	if storyboards, err := extractTwitchStoryboardFormats(ctx, transport, video.SeekPreviewsURL, video.LengthSeconds); err != nil {
		return Extraction{}, err
	} else if len(storyboards) != 0 {
		formats = append(formats, storyboards...)
	}
	info.Set("formats", value.List(formats...))
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
	if err := requestTwitchGQL(ctx, transport, body, &response); err != nil {
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
	finalFormatURL := ""
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
			if portrait {
				format.Set("quality", value.Int(-2))
			}
			formats = append(formats, value.ObjectValue(format))
			finalFormatURL = signedURL
		}
		if validTwitchAssetURL(asset.ThumbnailURL) {
			thumbnailID := "default"
			preference := int64(0)
			if portrait {
				thumbnailID = "portrait"
				preference = -1
			}
			thumbnails = append(thumbnails, value.ObjectValue(value.NewObject(
				value.Field{Key: "id", Value: value.String(thumbnailID)},
				value.Field{Key: "url", Value: value.String(asset.ThumbnailURL)},
				value.Field{Key: "preference", Value: value.Int(preference)},
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
			value.Field{Key: "preference", Value: value.Int(-2)},
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
	if legacyID := twitchClipLegacyArchiveID(finalFormatURL); legacyID != "" {
		info.Set("_old_archive_ids", value.List(value.String(legacyID)))
	}
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

type twitchStoryboardSpec struct {
	Width  int64    `json:"width"`
	Height int64    `json:"height"`
	Count  int64    `json:"count"`
	Rows   int64    `json:"rows"`
	Cols   int64    `json:"cols"`
	Images []string `json:"images"`
}

func extractTwitchStoryboardFormats(ctx context.Context, transport Transport, storyboardURL string, durationSeconds int64) ([]value.Value, error) {
	if durationSeconds <= 0 || strings.TrimSpace(storyboardURL) == "" {
		return nil, nil
	}
	if !validTwitchAssetURL(storyboardURL) {
		return nil, nil
	}
	specs, err := fetchTwitchStoryboardSpecs(ctx, transport, storyboardURL)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, nil
	}
	if len(specs) == 0 {
		return nil, nil
	}
	sort.Slice(specs, func(i, j int) bool {
		left := specs[i].Width
		if left <= 0 {
			left = 0
		}
		right := specs[j].Width
		if right <= 0 {
			right = 0
		}
		return left > right
	})
	base, err := url.Parse(storyboardURL)
	if err != nil {
		return nil, nil
	}
	duration := float64(durationSeconds)
	formats := make([]value.Value, 0, len(specs))
	for index, spec := range specs {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		if spec.Count <= 0 || len(spec.Images) == 0 || spec.Width <= 0 || spec.Height <= 0 ||
			spec.Width > twitchStoryboardMaxDimension || spec.Height > twitchStoryboardMaxDimension ||
			len(spec.Images) > twitchStoryboardMaxImages {
			continue
		}
		fragmentDuration := duration / float64(len(spec.Images))
		fragments := make([]value.Value, 0, len(spec.Images))
		seen := make(map[string]struct{}, len(spec.Images))
		for _, imagePath := range spec.Images {
			imageURL := resolveTwitchStoryboardImageURL(base, imagePath)
			if imageURL == "" {
				continue
			}
			if _, duplicate := seen[imageURL]; duplicate {
				continue
			}
			seen[imageURL] = struct{}{}
			fragment := value.NewObject(
				value.Field{Key: "url", Value: value.String(imageURL)},
				value.Field{Key: "duration", Value: value.Float(fragmentDuration)},
			)
			fragments = append(fragments, value.ObjectValue(fragment))
		}
		if len(fragments) == 0 {
			continue
		}
		firstURL, _ := fragments[0].Object()
		first, _ := firstURL.Lookup("url").StringValue()
		format := value.NewObject(
			value.Field{Key: "format_id", Value: value.String(fmt.Sprintf("sb%d", index))},
			value.Field{Key: "format_note", Value: value.String("storyboard")},
			value.Field{Key: "ext", Value: value.String("mhtml")},
			value.Field{Key: "protocol", Value: value.String("mhtml")},
			value.Field{Key: "acodec", Value: value.String("none")},
			value.Field{Key: "vcodec", Value: value.String("none")},
			value.Field{Key: "url", Value: value.String(first)},
			value.Field{Key: "width", Value: value.Int(spec.Width)},
			value.Field{Key: "height", Value: value.Int(spec.Height)},
			value.Field{Key: "fps", Value: value.Float(float64(spec.Count) / duration)},
			value.Field{Key: "fragments", Value: value.List(fragments...)},
		)
		if spec.Rows > 0 {
			format.Set("rows", value.Int(spec.Rows))
		}
		if spec.Cols > 0 {
			format.Set("columns", value.Int(spec.Cols))
		}
		formats = append(formats, value.ObjectValue(format))
	}
	return formats, nil
}

func fetchTwitchStoryboardSpecs(ctx context.Context, transport Transport, storyboardURL string) ([]twitchStoryboardSpec, error) {
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, storyboardURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Twitch storyboard request", ErrInvalidMetadata)
	}
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("%w: empty Twitch storyboard response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &HTTPStatusError{Code: response.StatusCode}
	}
	if location := strings.TrimSpace(response.Header.Get("Location")); location != "" {
		if !twitchStoryboardRedirectAllowed(storyboardURL, location) {
			return nil, fmt.Errorf("%w: hostile Twitch storyboard redirect", ErrInvalidMetadata)
		}
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, twitchStoryboardMaxJSONBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > twitchStoryboardMaxJSONBytes {
		return nil, fmt.Errorf("%w: Twitch storyboard response too large", ErrJSONResponseTooLarge)
	}
	return parseTwitchStoryboardSpecs(payload)
}

func parseTwitchStoryboardSpecs(payload []byte) ([]twitchStoryboardSpec, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var specs []twitchStoryboardSpec
	if err := decoder.Decode(&specs); err != nil || ensureJSONEOF(decoder) != nil {
		return nil, fmt.Errorf("%w: malformed Twitch storyboard JSON", ErrInvalidMetadata)
	}
	if len(specs) > twitchStoryboardMaxSpecs {
		return nil, fmt.Errorf("%w: Twitch storyboard specification limit", ErrInvalidMetadata)
	}
	for _, spec := range specs {
		if len(spec.Images) > twitchStoryboardMaxImages {
			return nil, fmt.Errorf("%w: Twitch storyboard image limit", ErrInvalidMetadata)
		}
	}
	return specs, nil
}

func resolveTwitchStoryboardImageURL(base *url.URL, imagePath string) string {
	if base == nil || imagePath == "" || len(imagePath) > twitchMaxURL || strings.ContainsAny(imagePath, "\\\x00\r\n") {
		return ""
	}
	parsed, err := url.Parse(imagePath)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(parsed).String()
	if !validTwitchAssetURL(resolved) {
		return ""
	}
	return resolved
}

func twitchStoryboardRedirectAllowed(originalURL, location string) bool {
	original, err := url.Parse(originalURL)
	if err != nil {
		return false
	}
	target, err := url.Parse(location)
	if err != nil {
		return false
	}
	if target.Scheme == "" {
		target = original.ResolveReference(target)
	}
	if !validTwitchAssetURL(target.String()) {
		return false
	}
	return strings.EqualFold(original.Hostname(), target.Hostname()) && original.Scheme == target.Scheme
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

// twitchClipLegacyArchiveID derives the historical TwitchClipsIE download-archive
// identity from the final accepted format URL, mirroring the pinned reference
// rule `%7C(\d+)(?:-\d+)?.mp4` applied to formats[-1]. Only the encoded URL
// before the signed query is inspected, without percent-decoding, so signed
// query parameters can never influence the captured numeric ID.
func twitchClipLegacyArchiveID(finalFormatURL string) string {
	candidate := finalFormatURL
	if index := strings.IndexByte(candidate, '?'); index >= 0 {
		candidate = candidate[:index]
	}
	if candidate == "" || len(candidate) > twitchMaxURL {
		return ""
	}
	match := twitchClipLegacyID.FindStringSubmatch(candidate)
	if len(match) != 2 {
		return ""
	}
	return "twitchclips " + match[1]
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

func extractTwitchVideos(ctx context.Context, transport Transport, target twitchTarget) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if transport == nil || target.kind != twitchKindVideos || target.id == "" {
		return Extraction{}, ErrUnsupported
	}
	channel := target.id
	query := target.videos
	sequence, err := ContinuationEntries(nil, twitchVideosStartToken, func(ctx context.Context, cursor string) ([]Entry, string, error) {
		return fetchTwitchVideosPage(ctx, transport, channel, query, cursor)
	})
	if err != nil {
		return Extraction{}, err
	}
	title := channel + " - " + query.broadcastLabel + " sorted by " + query.sortLabel
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(channel)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "uploader_id", Value: value.String(channel)},
		value.Field{Key: "webpage_url", Value: value.String("https://www.twitch.tv/" + channel + "/videos")},
	)
	return Playlist(value.NewInfo(info), sequence)
}

func fetchTwitchVideosPage(ctx context.Context, transport Transport, channel string, query twitchVideosQuery, cursor string) ([]Entry, string, error) {
	if err := contextError(ctx); err != nil {
		return nil, "", err
	}
	requestCursor := ""
	if cursor != twitchVideosStartToken {
		if cursor == "" || len(cursor) > twitchVideosMaxCursor {
			return nil, "", fmt.Errorf("%w: Twitch videos cursor", ErrInvalidPlaylist)
		}
		requestCursor = cursor
	}
	body, err := marshalTwitchVideosRequest(channel, query, requestCursor)
	if err != nil {
		return nil, "", err
	}
	var response []twitchVideosPageResponse
	if err := requestTwitchGQL(ctx, transport, body, &response); err != nil {
		return nil, "", categorizeTwitchHTTP(err)
	}
	return parseTwitchVideosPage(response)
}

func marshalTwitchVideosRequest(channel string, query twitchVideosQuery, cursor string) ([]byte, error) {
	if channel == "" || len(channel) > 25 || len(query.videoSort) > twitchVideosMaxString {
		return nil, fmt.Errorf("%w: Twitch videos request", ErrInvalidMetadata)
	}
	var broadcastType any
	if query.broadcastType != "" {
		broadcastType = query.broadcastType
	}
	variables := map[string]any{
		"channelOwnerLogin": channel,
		"broadcastType":     broadcastType,
		"videoSort":         query.videoSort,
		"limit":             twitchVideosPageLimit,
	}
	if cursor != "" {
		variables["cursor"] = cursor
	}
	hash := twitchOperationHashes[twitchVideosOperation]
	if hash == "" {
		return nil, fmt.Errorf("%w: Twitch videos operation hash", ErrInvalidMetadata)
	}
	payload := []map[string]any{{
		"operationName": twitchVideosOperation,
		"variables":     variables,
		"extensions": map[string]any{
			"persistedQuery": map[string]any{
				"version":    1,
				"sha256Hash": hash,
			},
		},
	}}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: Twitch videos request", ErrInvalidMetadata)
	}
	return body, nil
}

type twitchVideosPageResponse struct {
	Data struct {
		User *twitchVideosUser `json:"user"`
	} `json:"data"`
	Errors []json.RawMessage `json:"errors"`
}

type twitchVideosUser struct {
	ID     json.RawMessage `json:"id"`
	Videos *struct {
		Edges []json.RawMessage `json:"edges"`
	} `json:"videos"`
}

type twitchVideosEdge struct {
	Typename string          `json:"__typename"`
	Cursor   string          `json:"cursor"`
	Node     json.RawMessage `json:"node"`
}

type twitchVideosNode struct {
	Typename string `json:"__typename"`
	ID       string `json:"id"`
	Title    string `json:"title"`
}

func parseTwitchVideosPage(response []twitchVideosPageResponse) ([]Entry, string, error) {
	if len(response) != 1 {
		return nil, "", fmt.Errorf("%w: Twitch videos response", ErrInvalidMetadata)
	}
	page := response[0]
	if len(page.Errors) != 0 {
		return nil, "", fmt.Errorf("%w: Twitch videos response", ErrInvalidMetadata)
	}
	user := page.Data.User
	if user != nil && twitchVideosUserIDEmpty(user.ID) {
		return nil, "", ErrUnavailable
	}
	if user == nil || user.Videos == nil {
		return nil, "", nil
	}
	edges := user.Videos.Edges
	if edges == nil {
		return nil, "", nil
	}
	if len(edges) > twitchVideosMaxEdges {
		return nil, "", fmt.Errorf("%w: Twitch videos page exceeds edge bound", ErrInvalidPlaylist)
	}
	entries := make([]Entry, 0, len(edges))
	nextCursor := ""
	for _, rawEdge := range edges {
		if len(rawEdge) > twitchMaxURL {
			continue
		}
		var edge twitchVideosEdge
		if err := json.Unmarshal(rawEdge, &edge); err != nil {
			continue
		}
		if edge.Typename != "VideoEdge" || len(edge.Node) == 0 {
			continue
		}
		entry, ok := twitchVideosEntry(edge.Node)
		if !ok {
			continue
		}
		entries = append(entries, entry)
		if edge.Cursor != "" && len(edge.Cursor) <= twitchVideosMaxCursor {
			nextCursor = edge.Cursor
		} else {
			nextCursor = ""
		}
	}
	return entries, nextCursor, nil
}

func twitchVideosUserIDEmpty(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil {
		return false
	}
	return id == ""
}

func twitchVideosEntry(raw json.RawMessage) (Entry, bool) {
	if len(raw) == 0 || len(raw) > twitchMaxURL {
		return Entry{}, false
	}
	var node twitchVideosNode
	if err := json.Unmarshal(raw, &node); err != nil {
		return Entry{}, false
	}
	if node.Typename != "Video" || !twitchVODPattern.MatchString(node.ID) {
		return Entry{}, false
	}
	title := strings.TrimSpace(node.Title)
	if len(title) > twitchVideosMaxString {
		return Entry{}, false
	}
	return Entry{
		URL:          "https://www.twitch.tv/videos/" + node.ID,
		ExtractorKey: "twitch",
		ID:           "v" + node.ID,
		Title:        title,
		Transparent:  true,
	}, true
}

func extractTwitchCollection(ctx context.Context, transport Transport, target twitchTarget) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if transport == nil || target.kind != twitchKindCollection || target.id == "" {
		return Extraction{}, ErrUnsupported
	}
	entries, title, err := fetchTwitchCollection(ctx, transport, target.id)
	if err != nil {
		return Extraction{}, err
	}
	sequence, err := ContinuationEntries(entries, "", nil)
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String("https://www.twitch.tv/collections/" + target.id)},
	)
	return Playlist(value.NewInfo(info), sequence)
}

func fetchTwitchCollection(ctx context.Context, transport Transport, collectionID string) ([]Entry, string, error) {
	if err := contextError(ctx); err != nil {
		return nil, "", err
	}
	body, err := marshalTwitchCollectionRequest(collectionID)
	if err != nil {
		return nil, "", err
	}
	var response []twitchCollectionResponse
	if err := requestTwitchGQL(ctx, transport, body, &response); err != nil {
		return nil, "", categorizeTwitchHTTP(err)
	}
	return parseTwitchCollection(response)
}

func marshalTwitchCollectionRequest(collectionID string) ([]byte, error) {
	if !twitchCollectionPattern.MatchString(collectionID) {
		return nil, fmt.Errorf("%w: Twitch collection request", ErrInvalidMetadata)
	}
	hash := twitchOperationHashes[twitchCollectionDirectOperation]
	if hash == "" {
		return nil, fmt.Errorf("%w: Twitch collection operation hash", ErrInvalidMetadata)
	}
	payload := []map[string]any{{
		"operationName": twitchCollectionDirectOperation,
		"variables":     map[string]any{"collectionID": collectionID},
		"extensions": map[string]any{
			"persistedQuery": map[string]any{
				"version":    1,
				"sha256Hash": hash,
			},
		},
	}}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: Twitch collection request", ErrInvalidMetadata)
	}
	return body, nil
}

type twitchCollectionResponse struct {
	Data struct {
		Collection *twitchCollectionData `json:"collection"`
	} `json:"data"`
	Errors []json.RawMessage `json:"errors"`
}

type twitchCollectionData struct {
	Title string `json:"title"`
	Items struct {
		Edges json.RawMessage `json:"edges"`
	} `json:"items"`
}

func parseTwitchCollection(response []twitchCollectionResponse) ([]Entry, string, error) {
	if len(response) != 1 {
		return nil, "", fmt.Errorf("%w: Twitch collection response", ErrInvalidMetadata)
	}
	page := response[0]
	if len(page.Errors) != 0 {
		return nil, "", fmt.Errorf("%w: Twitch collection response", ErrInvalidMetadata)
	}
	if page.Data.Collection == nil {
		return nil, "", ErrUnavailable
	}
	collection := page.Data.Collection
	title := strings.TrimSpace(collection.Title)
	if len(title) > twitchCollectionsMaxString {
		return nil, "", fmt.Errorf("%w: Twitch collection title exceeds bound", ErrInvalidMetadata)
	}
	edges, err := twitchBoundedJSONArray(collection.Items.Edges, twitchCollectionsMaxEdges)
	if err != nil {
		return nil, "", err
	}
	if edges == nil {
		return []Entry{}, title, nil
	}
	entries := make([]Entry, 0, len(edges))
	for _, rawEdge := range edges {
		if len(rawEdge) > twitchMaxURL {
			continue
		}
		var edge struct {
			Node json.RawMessage `json:"node"`
		}
		if err := json.Unmarshal(rawEdge, &edge); err != nil || len(edge.Node) == 0 {
			continue
		}
		entry, ok := twitchCollectionVideoEntry(edge.Node)
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries, title, nil
}

func twitchCollectionVideoEntry(raw json.RawMessage) (Entry, bool) {
	if len(raw) == 0 || len(raw) > twitchMaxURL {
		return Entry{}, false
	}
	var node struct {
		Typename string `json:"__typename"`
		ID       string `json:"id"`
		Title    string `json:"title"`
	}
	if err := json.Unmarshal(raw, &node); err != nil {
		return Entry{}, false
	}
	if node.Typename != "" && node.Typename != "Video" {
		return Entry{}, false
	}
	if !twitchVODPattern.MatchString(node.ID) {
		return Entry{}, false
	}
	title := strings.TrimSpace(node.Title)
	if len(title) > twitchVideosMaxString {
		return Entry{}, false
	}
	return Entry{
		URL:          "https://www.twitch.tv/videos/" + node.ID,
		ExtractorKey: "twitch",
		ID:           "v" + node.ID,
		Title:        title,
		Transparent:  true,
	}, true
}

func twitchBoundedJSONArray(raw json.RawMessage, maxEdges int) ([]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	maxBytes := maxEdges*(twitchMaxURL+1) + 2
	if len(raw) > maxBytes {
		return nil, fmt.Errorf("%w: Twitch response exceeds edge array payload bound", ErrInvalidPlaylist)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: Twitch response edge array", ErrInvalidMetadata)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '[' {
		return nil, fmt.Errorf("%w: Twitch response edge array", ErrInvalidMetadata)
	}
	edges := make([]json.RawMessage, 0, min(maxEdges, 8))
	for decoder.More() {
		var edge json.RawMessage
		if err := decoder.Decode(&edge); err != nil {
			return nil, fmt.Errorf("%w: Twitch response edge array", ErrInvalidMetadata)
		}
		if len(edges) >= maxEdges {
			return nil, fmt.Errorf("%w: Twitch page exceeds edge bound", ErrInvalidPlaylist)
		}
		edges = append(edges, edge)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("%w: Twitch response edge array", ErrInvalidMetadata)
	}
	return edges, nil
}

func extractTwitchChannelCollections(ctx context.Context, transport Transport, target twitchTarget) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if transport == nil || target.kind != twitchKindChannelCollections || target.id == "" {
		return Extraction{}, ErrUnsupported
	}
	channel := target.id
	sequence, err := ContinuationEntries(nil, twitchCollectionsStartToken, func(ctx context.Context, cursor string) ([]Entry, string, error) {
		return fetchTwitchChannelCollectionsPage(ctx, transport, channel, cursor)
	})
	if err != nil {
		return Extraction{}, err
	}
	title := channel + " - Collections"
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(channel)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "uploader_id", Value: value.String(channel)},
		value.Field{Key: "webpage_url", Value: value.String("https://www.twitch.tv/" + channel + "/videos?filter=collections")},
	)
	return Playlist(value.NewInfo(info), sequence)
}

func fetchTwitchChannelCollectionsPage(ctx context.Context, transport Transport, channel, cursor string) ([]Entry, string, error) {
	if err := contextError(ctx); err != nil {
		return nil, "", err
	}
	requestCursor := ""
	if cursor != twitchCollectionsStartToken {
		if cursor == "" || len(cursor) > twitchCollectionsMaxCursor {
			return nil, "", fmt.Errorf("%w: Twitch collections cursor", ErrInvalidPlaylist)
		}
		requestCursor = cursor
	}
	body, err := marshalTwitchChannelCollectionsRequest(channel, requestCursor)
	if err != nil {
		return nil, "", err
	}
	var response []twitchChannelCollectionsPageResponse
	if err := requestTwitchGQL(ctx, transport, body, &response); err != nil {
		return nil, "", categorizeTwitchHTTP(err)
	}
	return parseTwitchChannelCollectionsPage(response)
}

func marshalTwitchChannelCollectionsRequest(channel, cursor string) ([]byte, error) {
	if !twitchChannelPattern.MatchString(channel) {
		return nil, fmt.Errorf("%w: Twitch collections request", ErrInvalidMetadata)
	}
	variables := map[string]any{
		"ownerLogin": channel,
		"limit":      twitchCollectionsPageLimit,
	}
	if cursor != "" {
		variables["cursor"] = cursor
	}
	hash := twitchOperationHashes[twitchCollectionsOperation]
	if hash == "" {
		return nil, fmt.Errorf("%w: Twitch collections operation hash", ErrInvalidMetadata)
	}
	payload := []map[string]any{{
		"operationName": twitchCollectionsOperation,
		"variables":     variables,
		"extensions": map[string]any{
			"persistedQuery": map[string]any{
				"version":    1,
				"sha256Hash": hash,
			},
		},
	}}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: Twitch collections request", ErrInvalidMetadata)
	}
	return body, nil
}

type twitchChannelCollectionsPageResponse struct {
	Data struct {
		User *twitchChannelCollectionsUser `json:"user"`
	} `json:"data"`
	Errors []json.RawMessage `json:"errors"`
}

type twitchChannelCollectionsUser struct {
	ID          json.RawMessage `json:"id"`
	Collections *struct {
		Edges json.RawMessage `json:"edges"`
	} `json:"collections"`
}

type twitchCollectionsItemEdge struct {
	Typename string          `json:"__typename"`
	Cursor   string          `json:"cursor"`
	Node     json.RawMessage `json:"node"`
}

type twitchCollectionNode struct {
	Typename string `json:"__typename"`
	ID       string `json:"id"`
	Title    string `json:"title"`
}

func parseTwitchChannelCollectionsPage(response []twitchChannelCollectionsPageResponse) ([]Entry, string, error) {
	if len(response) != 1 {
		return nil, "", fmt.Errorf("%w: Twitch collections response", ErrInvalidMetadata)
	}
	page := response[0]
	if len(page.Errors) != 0 {
		return nil, "", fmt.Errorf("%w: Twitch collections response", ErrInvalidMetadata)
	}
	user := page.Data.User
	if user != nil && twitchVideosUserIDEmpty(user.ID) {
		return nil, "", ErrUnavailable
	}
	if user == nil || user.Collections == nil {
		return nil, "", nil
	}
	edges, err := twitchBoundedJSONArray(user.Collections.Edges, twitchCollectionsMaxEdges)
	if err != nil {
		return nil, "", err
	}
	if edges == nil {
		return nil, "", nil
	}
	entries := make([]Entry, 0, len(edges))
	nextCursor := ""
	for _, rawEdge := range edges {
		if len(rawEdge) > twitchMaxURL {
			continue
		}
		var edge twitchCollectionsItemEdge
		if err := json.Unmarshal(rawEdge, &edge); err != nil {
			continue
		}
		if edge.Typename != "CollectionsItemEdge" || len(edge.Node) == 0 {
			continue
		}
		entry, ok := twitchChannelCollectionEntry(edge.Node)
		if !ok {
			continue
		}
		entries = append(entries, entry)
		if edge.Cursor != "" && len(edge.Cursor) <= twitchCollectionsMaxCursor {
			nextCursor = edge.Cursor
		} else {
			nextCursor = ""
		}
	}
	return entries, nextCursor, nil
}

func twitchChannelCollectionEntry(raw json.RawMessage) (Entry, bool) {
	if len(raw) == 0 || len(raw) > twitchMaxURL {
		return Entry{}, false
	}
	var node twitchCollectionNode
	if err := json.Unmarshal(raw, &node); err != nil {
		return Entry{}, false
	}
	if node.Typename != "Collection" || !twitchCollectionPattern.MatchString(node.ID) {
		return Entry{}, false
	}
	title := strings.TrimSpace(node.Title)
	if len(title) > twitchCollectionsMaxString {
		return Entry{}, false
	}
	return Entry{
		URL:          "https://www.twitch.tv/collections/" + node.ID,
		ExtractorKey: "twitch",
		ID:           node.ID,
		Title:        title,
		Transparent:  true,
	}, true
}

func extractTwitchChannelClips(ctx context.Context, transport Transport, target twitchTarget) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if transport == nil || target.kind != twitchKindChannelClips || target.id == "" {
		return Extraction{}, ErrUnsupported
	}
	channel := target.id
	query := target.clips
	sequence, err := ContinuationEntries(nil, twitchClipsStartToken, func(ctx context.Context, cursor string) ([]Entry, string, error) {
		return fetchTwitchChannelClipsPage(ctx, transport, channel, query, cursor)
	})
	if err != nil {
		return Extraction{}, err
	}
	title := channel + " - Clips " + query.label
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(channel)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "uploader_id", Value: value.String(channel)},
		value.Field{Key: "webpage_url", Value: value.String("https://www.twitch.tv/" + channel + "/clips")},
	)
	return Playlist(value.NewInfo(info), sequence)
}

func fetchTwitchChannelClipsPage(ctx context.Context, transport Transport, channel string, query twitchClipsQuery, cursor string) ([]Entry, string, error) {
	if err := contextError(ctx); err != nil {
		return nil, "", err
	}
	requestCursor := ""
	if cursor != twitchClipsStartToken {
		if cursor == "" || len(cursor) > twitchClipsMaxCursor {
			return nil, "", fmt.Errorf("%w: Twitch clips cursor", ErrInvalidPlaylist)
		}
		requestCursor = cursor
	}
	body, err := marshalTwitchChannelClipsRequest(channel, query, requestCursor)
	if err != nil {
		return nil, "", err
	}
	var response []twitchChannelClipsPageResponse
	if err := requestTwitchGQL(ctx, transport, body, &response); err != nil {
		return nil, "", categorizeTwitchHTTP(err)
	}
	return parseTwitchChannelClipsPage(response)
}

func marshalTwitchChannelClipsRequest(channel string, query twitchClipsQuery, cursor string) ([]byte, error) {
	if !twitchChannelPattern.MatchString(channel) || query.filter == "" || len(query.filter) > twitchClipsMaxString {
		return nil, fmt.Errorf("%w: Twitch clips request", ErrInvalidMetadata)
	}
	variables := map[string]any{
		"login": channel,
		"criteria": map[string]any{
			"filter": query.filter,
		},
		"limit": twitchClipsPageLimit,
	}
	if cursor != "" {
		variables["cursor"] = cursor
	}
	hash := twitchOperationHashes[twitchClipsOperation]
	if hash == "" {
		return nil, fmt.Errorf("%w: Twitch clips operation hash", ErrInvalidMetadata)
	}
	payload := []map[string]any{{
		"operationName": twitchClipsOperation,
		"variables":     variables,
		"extensions": map[string]any{
			"persistedQuery": map[string]any{
				"version":    1,
				"sha256Hash": hash,
			},
		},
	}}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: Twitch clips request", ErrInvalidMetadata)
	}
	return body, nil
}

type twitchChannelClipsPageResponse struct {
	Data struct {
		User *twitchChannelClipsUser `json:"user"`
	} `json:"data"`
	Errors []json.RawMessage `json:"errors"`
}

type twitchChannelClipsUser struct {
	ID    json.RawMessage `json:"id"`
	Clips *struct {
		Edges json.RawMessage `json:"edges"`
	} `json:"clips"`
}

type twitchClipsItemEdge struct {
	Typename string          `json:"__typename"`
	Cursor   string          `json:"cursor"`
	Node     json.RawMessage `json:"node"`
}

type twitchClipPlaylistNode struct {
	Typename        string          `json:"__typename"`
	ID              string          `json:"id"`
	URL             string          `json:"url"`
	Title           string          `json:"title"`
	ThumbnailURL    string          `json:"thumbnailURL"`
	DurationSeconds json.RawMessage `json:"durationSeconds"`
	CreatedAt       json.RawMessage `json:"createdAt"`
	ViewCount       json.RawMessage `json:"viewCount"`
	Language        json.RawMessage `json:"language"`
}

func parseTwitchChannelClipsPage(response []twitchChannelClipsPageResponse) ([]Entry, string, error) {
	if len(response) != 1 {
		return nil, "", fmt.Errorf("%w: Twitch clips response", ErrInvalidMetadata)
	}
	page := response[0]
	if len(page.Errors) != 0 {
		return nil, "", fmt.Errorf("%w: Twitch clips response", ErrInvalidMetadata)
	}
	user := page.Data.User
	if user != nil && twitchVideosUserIDEmpty(user.ID) {
		return nil, "", ErrUnavailable
	}
	if user == nil || user.Clips == nil {
		return nil, "", nil
	}
	edges, err := twitchBoundedJSONArray(user.Clips.Edges, twitchClipsMaxEdges)
	if err != nil {
		return nil, "", err
	}
	if edges == nil {
		return nil, "", nil
	}
	entries := make([]Entry, 0, len(edges))
	seenIDs := make(map[string]struct{}, len(edges))
	seenURLs := make(map[string]struct{}, len(edges))
	nextCursor := ""
	for _, rawEdge := range edges {
		if len(rawEdge) > twitchMaxURL {
			continue
		}
		var edge twitchClipsItemEdge
		if err := json.Unmarshal(rawEdge, &edge); err != nil {
			continue
		}
		if edge.Typename != "ClipEdge" || len(edge.Node) == 0 {
			continue
		}
		entry, ok := twitchChannelClipEntry(edge.Node)
		if !ok {
			continue
		}
		if edge.Cursor != "" && len(edge.Cursor) <= twitchClipsMaxCursor {
			nextCursor = edge.Cursor
		} else {
			nextCursor = ""
		}
		if _, exists := seenIDs[entry.ID]; exists {
			continue
		}
		if _, exists := seenURLs[entry.URL]; exists {
			continue
		}
		seenIDs[entry.ID] = struct{}{}
		seenURLs[entry.URL] = struct{}{}
		entries = append(entries, entry)
	}
	return entries, nextCursor, nil
}

func twitchChannelClipEntry(raw json.RawMessage) (Entry, bool) {
	if len(raw) == 0 || len(raw) > twitchMaxURL {
		return Entry{}, false
	}
	var node twitchClipPlaylistNode
	if err := json.Unmarshal(raw, &node); err != nil {
		return Entry{}, false
	}
	if node.Typename != "Clip" {
		return Entry{}, false
	}
	title := strings.TrimSpace(node.Title)
	if len(title) > twitchClipsMaxString {
		return Entry{}, false
	}
	clipURL := strings.TrimSpace(node.URL)
	if len(clipURL) == 0 || len(clipURL) > twitchMaxURL {
		return Entry{}, false
	}
	parsed, err := url.Parse(clipURL)
	if err != nil {
		return Entry{}, false
	}
	target, ok := classifyTwitchURL(parsed)
	if !ok || target.kind != twitchKindClip || target.id == "" {
		return Entry{}, false
	}
	id := node.ID
	if !twitchClipPattern.MatchString(id) {
		id = target.id
	}
	if !twitchClipPattern.MatchString(id) {
		return Entry{}, false
	}
	entry := Entry{
		URL:          clipURL,
		ExtractorKey: "twitch",
		ID:           id,
		Title:        title,
		Transparent:  true,
	}
	if thumb := strings.TrimSpace(node.ThumbnailURL); validTwitchAssetURL(thumb) {
		entry.Thumbnail = thumb
	}
	if duration, present := twitchOptionalFloat(node.DurationSeconds, float64(30*24*60*60)); present {
		entry.Duration = duration
		entry.HasDuration = true
	}
	if timestamp, present := twitchOptionalRFC3339Timestamp(node.CreatedAt); present {
		entry.Timestamp = timestamp
		entry.HasTimestamp = true
	}
	if views, present := twitchOptionalInt(node.ViewCount, math.MaxInt64); present {
		entry.ViewCount = views
		entry.HasViewCount = true
	}
	if language, present := twitchOptionalLanguage(node.Language); present {
		entry.Language = language
	}
	return entry, true
}

func twitchOptionalFloat(raw json.RawMessage, max float64) (float64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return twitchBoundedFloat(number, max)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil || text == "" || len(text) > twitchClipsMaxNumericText {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, false
	}
	return twitchBoundedFloat(parsed, max)
}

func twitchBoundedFloat(number float64, max float64) (float64, bool) {
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > max {
		return 0, false
	}
	return number, true
}

func twitchOptionalInt(raw json.RawMessage, max int64) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return 0, false
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil || text == "" || len(text) > twitchClipsMaxNumericText {
			return 0, false
		}
		return twitchParseBoundedIntText(text, max)
	}
	if len(trimmed) > twitchClipsMaxNumericText {
		return 0, false
	}
	return twitchParseBoundedIntText(string(trimmed), max)
}

// twitchParseBoundedIntText implements overflow-safe int_or_none semantics for
// JSON number tokens and numeric strings without float64 round-trip hazards.
func twitchParseBoundedIntText(text string, max int64) (int64, bool) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > twitchClipsMaxNumericText || max < 0 {
		return 0, false
	}
	lower := strings.ToLower(text)
	switch lower {
	case "nan", "inf", "+inf", "-inf", "infinity", "+infinity", "-infinity":
		return 0, false
	}

	digits := text
	switch {
	case digits[0] == '-':
		return 0, false
	case digits[0] == '+':
		digits = digits[1:]
		if digits == "" {
			return 0, false
		}
	}
	if twitchDecimalDigits(digits) {
		number := new(big.Int)
		if _, ok := number.SetString(digits, 10); !ok {
			return 0, false
		}
		if number.Sign() < 0 || number.Cmp(big.NewInt(max)) > 0 || !number.IsInt64() {
			return 0, false
		}
		value := number.Int64()
		if value < 0 || value > max {
			return 0, false
		}
		return value, true
	}

	if strings.Contains(text, ".") && !strings.ContainsAny(lower, "e") {
		ratio := new(big.Rat)
		if _, ok := ratio.SetString(text); !ok || !ratio.IsInt() {
			return 0, false
		}
		number := ratio.Num()
		if number.Sign() < 0 || number.Cmp(big.NewInt(max)) > 0 || !number.IsInt64() {
			return 0, false
		}
		value := number.Int64()
		if value < 0 || value > max {
			return 0, false
		}
		return value, true
	}

	// Scientific notation and other float tokens: only accept values that are
	// exactly representable as integers within the float64 mantissa.
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
		return 0, false
	}
	if parsed != math.Trunc(parsed) {
		return 0, false
	}
	const maxExactFloatInt = 1 << 53
	if parsed > float64(maxExactFloatInt) {
		return 0, false
	}
	value := int64(parsed)
	if value < 0 || value > max {
		return 0, false
	}
	return value, true
}

func twitchDecimalDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, character := range text {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func twitchOptionalRFC3339Timestamp(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, false
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > twitchClipsMaxTimestampText {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return 0, false
	}
	return parsed.Unix(), true
}

func twitchOptionalLanguage(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "", false
	}
	text = strings.TrimSpace(text)
	if text == "" || len(text) > twitchClipsMaxLanguage || !utf8.ValidString(text) {
		return "", false
	}
	for _, character := range text {
		if character == 0 || unicode.IsControl(character) {
			return "", false
		}
	}
	return text, true
}
