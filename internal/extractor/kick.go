package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const kickImpersonationProfile = "chrome-133"

var (
	kickSlugPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	kickUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)
	kickClipPattern = regexp.MustCompile(`^clip_[A-Za-z0-9_-]{1,128}$`)
)

type Kick struct{}

func NewKick() Kick { return Kick{} }

func (Kick) Name() string { return "kick" }

func (Kick) Suitable(parsed *url.URL) bool {
	_, ok := classifyKickURL(parsed)
	return ok
}

func (Kick) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyKickURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	switch target.kind {
	case kickLive:
		return extractKickLive(ctx, request.Transport, target.id)
	case kickVOD:
		return extractKickVOD(ctx, request.Transport, target.id, target.channel)
	case kickClip:
		return extractKickClip(ctx, request.Transport, target.id, target.channel)
	default:
		return Extraction{}, ErrUnsupported
	}
}

type kickTargetKind uint8

const (
	kickLive kickTargetKind = iota + 1
	kickVOD
	kickClip
)

type kickTarget struct {
	kind    kickTargetKind
	id      string
	channel string
}

func classifyKickURL(parsed *url.URL) (kickTarget, bool) {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Port() != "" || parsed.User != nil {
		return kickTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "kick.com" && host != "www.kick.com" {
		return kickTarget{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 || !kickSlugPattern.MatchString(parts[0]) {
		return kickTarget{}, false
	}
	channel := parts[0]
	if len(parts) == 1 {
		if clip := parsed.Query().Get("clip"); kickClipPattern.MatchString(clip) {
			return kickTarget{kind: kickClip, id: clip, channel: channel}, true
		}
		switch strings.ToLower(channel) {
		case "auth", "categories", "category", "search", "video":
			return kickTarget{}, false
		}
		return kickTarget{kind: kickLive, id: channel, channel: channel}, true
	}
	if len(parts) == 3 && parts[1] == "videos" && kickUUIDPattern.MatchString(parts[2]) {
		return kickTarget{kind: kickVOD, id: strings.ToLower(parts[2]), channel: channel}, true
	}
	if len(parts) == 3 && parts[1] == "clips" && kickClipPattern.MatchString(parts[2]) {
		return kickTarget{kind: kickClip, id: parts[2], channel: channel}, true
	}
	return kickTarget{}, false
}

type kickLiveResponse struct {
	ID          json.RawMessage `json:"id"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	UserID      json.RawMessage `json:"user_id"`
	PlaybackURL string          `json:"playback_url"`
	User        struct {
		ID       json.RawMessage `json:"id"`
		Username string          `json:"username"`
		Bio      string          `json:"bio"`
	} `json:"user"`
	Livestream *struct {
		ID           json.RawMessage `json:"id"`
		Slug         string          `json:"slug"`
		ChannelID    json.RawMessage `json:"channel_id"`
		SessionTitle string          `json:"session_title"`
		CreatedAt    string          `json:"created_at"`
		StartTime    string          `json:"start_time"`
		ViewerCount  int64           `json:"viewer_count"`
		IsMature     bool            `json:"is_mature"`
		Thumbnail    struct {
			URL string `json:"url"`
		} `json:"thumbnail"`
	} `json:"livestream"`
	RecentCategories []struct {
		Name string `json:"name"`
	} `json:"recent_categories"`
	Message string `json:"message"`
}

func extractKickLive(ctx context.Context, transport Transport, channel string) (Extraction, error) {
	endpoint := "https://kick.com/api/v2/channels/" + url.PathEscape(channel)
	var response kickLiveResponse
	if err := requestRiskJSON(ctx, transport, http.MethodGet, endpoint, nil, kickHeaders(channel), kickImpersonationProfile, &response); err != nil {
		return Extraction{}, categorizeKickError(err)
	}
	if kickAuthMessage(response.Message) {
		return Extraction{}, ErrAuthentication
	}
	if response.Livestream == nil || response.PlaybackURL == "" {
		return Extraction{}, ErrUnavailable
	}
	format, ok := riskFormat(response.PlaybackURL, "hls")
	if !ok {
		return Extraction{}, fmt.Errorf("%w: invalid Kick playback URL", ErrInvalidMetadata)
	}
	id := response.Livestream.Slug
	if id == "" {
		id = instagramRawString(response.Livestream.ID)
	}
	if id == "" || response.Livestream.SessionTitle == "" {
		return Extraction{}, fmt.Errorf("%w: incomplete Kick livestream", ErrInvalidMetadata)
	}
	uploader := response.Name
	if uploader == "" {
		uploader = response.User.Username
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(response.Livestream.SessionTitle)},
		value.Field{Key: "channel", Value: value.String(channel)},
		value.Field{Key: "webpage_url", Value: value.String("https://kick.com/" + channel)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))},
		value.Field{Key: "is_live", Value: value.Bool(true)},
		value.Field{Key: "live_status", Value: value.String("is_live")},
		value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(value.Field{Key: "Referer", Value: value.String("https://kick.com/")}))},
	)
	riskString(info, "description", response.User.Bio)
	riskString(info, "channel_id", firstRiskRaw(response.ID, response.Livestream.ChannelID))
	riskString(info, "uploader", uploader)
	riskString(info, "uploader_id", firstRiskRaw(response.UserID, response.User.ID))
	riskPositiveInt(info, "timestamp", riskTimestamp(response.Livestream.CreatedAt))
	riskPositiveInt(info, "release_timestamp", riskTimestamp(response.Livestream.StartTime))
	riskString(info, "thumbnail", response.Livestream.Thumbnail.URL)
	riskInt(info, "concurrent_view_count", response.Livestream.ViewerCount)
	age := int64(0)
	if response.Livestream.IsMature {
		age = 18
	}
	riskInt(info, "age_limit", age)
	categoryNames := make([]string, 0, len(response.RecentCategories))
	for _, category := range response.RecentCategories {
		categoryNames = append(categoryNames, category.Name)
	}
	if categories := kickCategories(categoryNames); len(categories) != 0 {
		info.Set("categories", value.List(categories...))
	}
	return Media(value.NewInfo(info)), nil
}

type kickVODResponse struct {
	Source     string `json:"source"`
	CreatedAt  string `json:"created_at"`
	Views      int64  `json:"views"`
	Message    string `json:"message"`
	Livestream struct {
		SessionTitle string  `json:"session_title"`
		Slug         string  `json:"slug"`
		Duration     float64 `json:"duration"`
		Thumbnail    string  `json:"thumbnail"`
		IsMature     bool    `json:"is_mature"`
		IsLive       bool    `json:"is_live"`
		Channel      struct {
			ID     json.RawMessage `json:"id"`
			Slug   string          `json:"slug"`
			UserID json.RawMessage `json:"user_id"`
			User   struct {
				Username string `json:"username"`
				Bio      string `json:"bio"`
			} `json:"user"`
		} `json:"channel"`
		Categories []struct {
			Name string `json:"name"`
		} `json:"categories"`
	} `json:"livestream"`
}

func extractKickVOD(ctx context.Context, transport Transport, id, channel string) (Extraction, error) {
	var response kickVODResponse
	endpoint := "https://kick.com/api/v1/video/" + url.PathEscape(id)
	if err := requestRiskJSON(ctx, transport, http.MethodGet, endpoint, nil, kickHeaders(channel), kickImpersonationProfile, &response); err != nil {
		return Extraction{}, categorizeKickError(err)
	}
	if kickAuthMessage(response.Message) {
		return Extraction{}, ErrAuthentication
	}
	format, ok := riskFormat(response.Source, "hls")
	if !ok {
		return Extraction{}, ErrUnavailable
	}
	title := response.Livestream.SessionTitle
	if title == "" {
		title = response.Livestream.Slug
	}
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Kick VOD title", ErrInvalidMetadata)
	}
	if response.Livestream.Channel.Slug != "" {
		channel = response.Livestream.Channel.Slug
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String("https://kick.com/" + channel + "/videos/" + id)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))},
		value.Field{Key: "is_live", Value: value.Bool(response.Livestream.IsLive)},
	)
	riskString(info, "channel", channel)
	riskString(info, "channel_id", instagramRawString(response.Livestream.Channel.ID))
	riskString(info, "uploader", response.Livestream.Channel.User.Username)
	riskString(info, "uploader_id", instagramRawString(response.Livestream.Channel.UserID))
	riskString(info, "description", response.Livestream.Channel.User.Bio)
	riskPositiveInt(info, "timestamp", riskTimestamp(response.CreatedAt))
	riskPositiveInt(info, "duration", int64(response.Livestream.Duration/1000))
	riskString(info, "thumbnail", response.Livestream.Thumbnail)
	riskInt(info, "view_count", response.Views)
	age := int64(0)
	if response.Livestream.IsMature {
		age = 18
	}
	riskInt(info, "age_limit", age)
	categoryNames := make([]string, 0, len(response.Livestream.Categories))
	for _, category := range response.Livestream.Categories {
		categoryNames = append(categoryNames, category.Name)
	}
	if categories := kickCategories(categoryNames); len(categories) != 0 {
		info.Set("categories", value.List(categories...))
	}
	if response.Livestream.IsLive {
		info.Set("live_status", value.String("is_live"))
	}
	return Media(value.NewInfo(info)), nil
}

type kickClipResponse struct {
	Clip *struct {
		ID           string  `json:"id"`
		ClipURL      string  `json:"clip_url"`
		Title        string  `json:"title"`
		ThumbnailURL string  `json:"thumbnail_url"`
		Duration     float64 `json:"duration"`
		CreatedAt    string  `json:"created_at"`
		Views        int64   `json:"views"`
		Likes        int64   `json:"likes"`
		IsMature     bool    `json:"is_mature"`
		Channel      struct {
			ID   json.RawMessage `json:"id"`
			Slug string          `json:"slug"`
		} `json:"channel"`
		Creator struct {
			ID       json.RawMessage `json:"id"`
			Username string          `json:"username"`
		} `json:"creator"`
		Category struct {
			Name string `json:"name"`
		} `json:"category"`
	} `json:"clip"`
	Message string `json:"message"`
}

func extractKickClip(ctx context.Context, transport Transport, id, channel string) (Extraction, error) {
	var response kickClipResponse
	endpoint := "https://kick.com/api/v2/clips/" + url.PathEscape(id) + "/play"
	if err := requestRiskJSON(ctx, transport, http.MethodGet, endpoint, nil, kickHeaders(channel), kickImpersonationProfile, &response); err != nil {
		return Extraction{}, categorizeKickError(err)
	}
	if kickAuthMessage(response.Message) {
		return Extraction{}, ErrAuthentication
	}
	if response.Clip == nil {
		return Extraction{}, ErrUnavailable
	}
	format, ok := riskFormat(response.Clip.ClipURL, "clip")
	if !ok {
		return Extraction{}, ErrUnavailable
	}
	if response.Clip.Title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Kick clip title", ErrInvalidMetadata)
	}
	if response.Clip.Channel.Slug != "" {
		channel = response.Clip.Channel.Slug
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(response.Clip.Title)},
		value.Field{Key: "webpage_url", Value: value.String("https://kick.com/" + channel + "/clips/" + id)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))},
	)
	riskString(info, "channel", channel)
	riskString(info, "channel_id", instagramRawString(response.Clip.Channel.ID))
	riskString(info, "uploader", response.Clip.Creator.Username)
	riskString(info, "uploader_id", instagramRawString(response.Clip.Creator.ID))
	riskString(info, "thumbnail", response.Clip.ThumbnailURL)
	riskFloat(info, "duration", response.Clip.Duration)
	riskPositiveInt(info, "timestamp", riskTimestamp(response.Clip.CreatedAt))
	riskInt(info, "view_count", response.Clip.Views)
	riskInt(info, "like_count", response.Clip.Likes)
	age := int64(0)
	if response.Clip.IsMature {
		age = 18
	}
	riskInt(info, "age_limit", age)
	if response.Clip.Category.Name != "" {
		info.Set("categories", value.List(value.String(response.Clip.Category.Name)))
	}
	return Media(value.NewInfo(info)), nil
}

func kickHeaders(channel string) http.Header {
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	headers.Set("Referer", "https://kick.com/"+channel)
	return headers
}

func categorizeKickError(err error) error {
	switch riskHTTPStatus(err) {
	case http.StatusUnauthorized:
		return ErrAuthentication
	case http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return ErrChallengeSolver
	case http.StatusNotFound, http.StatusGone:
		return ErrUnavailable
	case http.StatusUnavailableForLegalReasons:
		return ErrRegionRestricted
	default:
		return err
	}
}

func kickAuthMessage(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "log in") || strings.Contains(lower, "login") || strings.Contains(lower, "authentication") || strings.Contains(lower, "private")
}

func firstRiskRaw(values ...json.RawMessage) string {
	for _, raw := range values {
		if text := instagramRawString(raw); text != "" {
			return text
		}
	}
	return ""
}

func kickCategories(categories []string) []value.Value {
	result := make([]value.Value, 0, len(categories))
	for _, category := range categories {
		if category != "" {
			result = append(result, value.String(category))
		}
	}
	return result
}
