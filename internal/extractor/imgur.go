package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	imgurClientID       = "546c25a59c58ad7"
	imgurMaxURLBytes    = 4096
	imgurMaxMediaItems  = 512
	imgurMaxTextBytes   = 64 << 10
	imgurMaxTitleBytes  = 4096
	imgurAPIBase        = "https://api.imgur.com/post/v1/"
	imgurIncludeQuery   = "client_id=" + imgurClientID + "&include=media%2Caccount"
	imgurGenericTagline = "Discover the magic of the internet at Imgur"
)

var (
	imgurIDPattern  = regexp.MustCompile(`^[A-Za-z0-9]{1,32}$`)
	imgurExtPattern = regexp.MustCompile(`^[a-z0-9]+$`)
	ErrImgurNetwork = errors.New("Imgur API request failed")
)

type imgurRouteKind uint8

const (
	imgurMediaRoute imgurRouteKind = iota
	imgurGalleryRoute
	imgurAlbumRoute
)

type imgurTarget struct {
	id   string
	kind imgurRouteKind
}

// Imgur extracts public videos, animated images, galleries, and albums through
// Imgur's bounded anonymous post API. Static-only posts are unavailable rather
// than being misrepresented as downloadable video.
type Imgur struct{}

func NewImgur() Imgur      { return Imgur{} }
func (Imgur) Name() string { return "imgur" }
func (Imgur) Suitable(u *url.URL) bool {
	_, ok := classifyImgurURL(u)
	return ok
}

func classifyImgurURL(parsed *url.URL) (imgurTarget, bool) {
	if parsed == nil || len(parsed.String()) == 0 || len(parsed.String()) > imgurMaxURLBytes ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil ||
		parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return imgurTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "imgur.com" && host != "www.imgur.com" && host != "i.imgur.com" {
		return imgurTarget{}, false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
		return imgurTarget{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return imgurTarget{}, false
	}
	kind := imgurMediaRoute
	rawID := ""
	switch {
	case parts[0] == "a" && len(parts) == 2:
		kind, rawID = imgurAlbumRoute, parts[1]
	case parts[0] == "gallery" && len(parts) == 2:
		kind, rawID = imgurGalleryRoute, parts[1]
	case (parts[0] == "t" || parts[0] == "topic" || parts[0] == "r") && len(parts) == 3:
		kind, rawID = imgurGalleryRoute, parts[2]
	case len(parts) == 1:
		switch parts[0] {
		case "a", "gallery", "t", "topic", "r":
			return imgurTarget{}, false
		}
		rawID = parts[0]
	default:
		return imgurTarget{}, false
	}
	rawID = strings.TrimSuffix(rawID, path.Ext(rawID))
	if separator := strings.LastIndexByte(rawID, '-'); separator >= 0 {
		rawID = rawID[separator+1:]
	}
	if !imgurIDPattern.MatchString(rawID) {
		return imgurTarget{}, false
	}
	return imgurTarget{id: rawID, kind: kind}, true
}

func (Imgur) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyImgurURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if target.kind == imgurMediaRoute {
		return imgurExtractMedia(ctx, request.Transport, target.id, request.URL, nil)
	}
	return imgurExtractCollection(ctx, request.Transport, target, request.URL)
}

type imgurAPIResponse struct {
	ID           any          `json:"id"`
	Title        string       `json:"title"`
	Description  string       `json:"description"`
	IsAlbum      bool         `json:"is_album"`
	IsMature     bool         `json:"is_mature"`
	AccountID    any          `json:"account_id"`
	Upvotes      any          `json:"upvote_count"`
	Downvotes    any          `json:"downvote_count"`
	CommentCount any          `json:"comment_count"`
	CreatedAt    string       `json:"created_at"`
	UpdatedAt    string       `json:"updated_at"`
	Account      imgurAccount `json:"account"`
	Media        []imgurMedia `json:"media"`
}

type imgurAccount struct {
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url"`
}

type imgurMedia struct {
	ID       any           `json:"id"`
	Type     string        `json:"type"`
	URL      string        `json:"url"`
	Ext      string        `json:"ext"`
	MIMEType string        `json:"mime_type"`
	Width    any           `json:"width"`
	Height   any           `json:"height"`
	Size     any           `json:"size"`
	Metadata imgurMetadata `json:"metadata"`
}

type imgurMetadata struct {
	IsAnimated  bool   `json:"is_animated"`
	HasSound    bool   `json:"has_sound"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Duration    any    `json:"duration"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type imgurCollectionInfo struct {
	title       string
	description string
}

func imgurExtractCollection(ctx context.Context, transport Transport, target imgurTarget, webpageURL string) (Extraction, error) {
	response, err := imgurCallAPI(ctx, transport, "albums", target.id)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			return imgurExtractMedia(ctx, transport, target.id, webpageURL, nil)
		}
		return Extraction{}, err
	}
	info := imgurCollectionInfo{
		title:       imgurBoundedText(response.Title, imgurMaxTitleBytes),
		description: imgurDescription(response.Description),
	}
	if !response.IsAlbum {
		return imgurExtractMedia(ctx, transport, target.id, webpageURL, &info)
	}
	if len(response.Media) > imgurMaxMediaItems {
		return Extraction{}, fmt.Errorf("%w: Imgur collection exceeds media limit", ErrInvalidMetadata)
	}
	entries := make([]Entry, 0, len(response.Media))
	for _, media := range response.Media {
		if err := contextError(ctx); err != nil {
			return Extraction{}, err
		}
		if !imgurPlayable(media) {
			continue
		}
		id := imgurString(media.ID)
		if !imgurIDPattern.MatchString(id) {
			return Extraction{}, fmt.Errorf("%w: invalid Imgur media id", ErrInvalidMetadata)
		}
		entries = append(entries, Entry{
			URL:          "https://imgur.com/" + id,
			ExtractorKey: "imgur",
			ID:           id,
			Title:        imgurBoundedText(media.Metadata.Title, imgurMaxTitleBytes),
			Transparent:  true,
		})
	}
	if len(entries) == 0 {
		return Extraction{}, fmt.Errorf("%w: Imgur collection has no video or animated image", ErrUnavailable)
	}
	if target.kind == imgurGalleryRoute && len(entries) == 1 {
		return imgurExtractMedia(ctx, transport, entries[0].ID, webpageURL, &info)
	}
	playlist := value.NewObject(value.Field{Key: "id", Value: value.String(target.id)})
	if info.title != "" {
		playlist.Set("title", value.String(info.title))
	}
	if info.description != "" {
		playlist.Set("description", value.String(info.description))
	}
	return Playlist(value.NewInfo(playlist), StaticEntries(entries...))
}

func imgurExtractMedia(ctx context.Context, transport Transport, id, webpageURL string, collection *imgurCollectionInfo) (Extraction, error) {
	response, err := imgurCallAPI(ctx, transport, "media", id)
	if err != nil {
		return Extraction{}, err
	}
	if len(response.Media) == 0 {
		return Extraction{}, fmt.Errorf("%w: Imgur response has no media", ErrUnavailable)
	}
	if len(response.Media) > imgurMaxMediaItems {
		return Extraction{}, fmt.Errorf("%w: Imgur response exceeds media limit", ErrInvalidMetadata)
	}
	media := response.Media[0]
	if !imgurPlayable(media) {
		return Extraction{}, fmt.Errorf("%w: Imgur item is not video or animated", ErrUnavailable)
	}
	mediaURL, ok := normalizeImgurAssetURL(media.URL)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: unsafe Imgur media URL", ErrInvalidMetadata)
	}
	extension := imgurExtension(media.Ext, media.MIMEType, mediaURL)
	format := value.NewObject(
		value.Field{Key: "format_id", Value: value.String("api-" + extension)},
		value.Field{Key: "url", Value: value.String(mediaURL)},
		value.Field{Key: "ext", Value: value.String(extension)},
		value.Field{Key: "protocol", Value: value.String("https")},
	)
	setPositiveInt(format, "width", imgurInt(media.Width))
	setPositiveInt(format, "height", imgurInt(media.Height))
	setPositiveInt(format, "filesize", imgurInt(media.Size))
	if !media.Metadata.HasSound || strings.EqualFold(media.Type, "image") {
		format.Set("acodec", value.String("none"))
	}
	if strings.EqualFold(media.Type, "image") {
		format.Set("preference", value.Int(-10))
		if extension == "gif" {
			format.Set("vcodec", value.String("gif"))
		}
	}

	title := imgurBoundedText(media.Metadata.Title, imgurMaxTitleBytes)
	if title == "" {
		title = imgurBoundedText(response.Title, imgurMaxTitleBytes)
	}
	if title == "" && collection != nil {
		title = collection.title
	}
	if title == "" {
		title = "Imgur video " + id
	}
	description := imgurDescription(media.Metadata.Description)
	if description == "" {
		description = imgurDescription(response.Description)
	}
	if description == "" && collection != nil {
		description = collection.description
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "ext", Value: value.String(extension)},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))},
	)
	if description != "" {
		info.Set("description", value.String(description))
	}
	if duration := imgurFloat(media.Metadata.Duration); duration > 0 {
		info.Set("duration", value.Float(duration))
	}
	imgurSetCountsAndAccount(info, response)
	timestamp := imgurFirstTimestamp(media.Metadata.UpdatedAt, media.Metadata.CreatedAt, response.UpdatedAt, response.CreatedAt)
	if timestamp > 0 {
		info.Set("timestamp", value.Int(timestamp))
		info.Set("upload_date", value.String(time.Unix(timestamp, 0).UTC().Format("20060102")))
	}
	releaseTimestamp := imgurFirstTimestamp(media.Metadata.CreatedAt, response.CreatedAt)
	if releaseTimestamp > 0 {
		info.Set("release_timestamp", value.Int(releaseTimestamp))
		info.Set("release_date", value.String(time.Unix(releaseTimestamp, 0).UTC().Format("20060102")))
	}
	if response.IsMature {
		info.Set("age_limit", value.Int(18))
	}
	if thumbnail := imgurThumbnailURL(id); thumbnail != "" {
		info.Set("thumbnail", value.String(thumbnail))
	}
	return Media(value.NewInfo(info)), nil
}

func imgurCallAPI(ctx context.Context, transport Transport, endpoint, id string) (imgurAPIResponse, error) {
	if !imgurIDPattern.MatchString(id) || (endpoint != "media" && endpoint != "albums") {
		return imgurAPIResponse{}, ErrUnsupported
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	var response imgurAPIResponse
	rawURL := imgurAPIBase + endpoint + "/" + url.PathEscape(id) + "?" + imgurIncludeQuery
	if err := RequestJSON(ctx, transport, http.MethodGet, rawURL, nil, headers, &response); err != nil {
		return imgurAPIResponse{}, categorizeImgurError(err)
	}
	return response, nil
}

func categorizeImgurError(err error) error {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return ErrImgurNetwork
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrJSONResponseTooLarge) || errors.Is(err, ErrInvalidMetadata) {
		return err
	}
	return ErrImgurNetwork
}

func imgurPlayable(media imgurMedia) bool {
	return strings.EqualFold(media.Type, "video") || media.Metadata.IsAnimated
}

func normalizeImgurAssetURL(raw string) (string, bool) {
	if len(raw) == 0 || len(raw) > imgurMaxURLBytes || strings.ContainsAny(raw, "\\\x00\r\n") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil ||
		parsed.Port() != "" || parsed.Fragment != "" || !strings.EqualFold(parsed.Hostname(), "i.imgur.com") {
		return "", false
	}
	if strings.Contains(strings.ToLower(parsed.EscapedPath()), "%2f") || strings.Contains(strings.ToLower(parsed.EscapedPath()), "%5c") {
		return "", false
	}
	parsed.Scheme = "https"
	parsed.Host = "i.imgur.com"
	return parsed.String(), true
}

func imgurExtension(explicit, mime, rawURL string) string {
	explicit = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(explicit)), ".")
	if explicit != "" && len(explicit) <= 8 && imgurExtPattern.MatchString(explicit) {
		return explicit
	}
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "video/mp4":
		return "mp4"
	case "video/webm":
		return "webm"
	case "image/gif":
		return "gif"
	}
	extension := strings.TrimPrefix(strings.ToLower(path.Ext(mustURLPath(rawURL))), ".")
	if extension != "" && len(extension) <= 8 {
		return extension
	}
	return "mp4"
}

func imgurThumbnailURL(id string) string {
	if !imgurIDPattern.MatchString(id) {
		return ""
	}
	return "https://i.imgur.com/" + id + "h.jpg"
}

func imgurSetCountsAndAccount(info *value.Object, response imgurAPIResponse) {
	imgurSetNonNegativeInt(info, "like_count", response.Upvotes)
	imgurSetNonNegativeInt(info, "dislike_count", response.Downvotes)
	imgurSetNonNegativeInt(info, "comment_count", response.CommentCount)
	if uploader := strings.TrimSpace(response.Account.Username); uploader != "" && len(uploader) <= 256 {
		info.Set("uploader", value.String(uploader))
	}
	if accountID := imgurInt(response.AccountID); accountID > 0 {
		info.Set("uploader_id", value.String(strconv.FormatInt(accountID, 10)))
	}
	if avatar, ok := normalizeImgurAssetURL(response.Account.AvatarURL); ok {
		info.Set("uploader_url", value.String(avatar))
	}
}

func imgurDescription(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, imgurGenericTagline) || len(raw) > imgurMaxTextBytes {
		return ""
	}
	return raw
}

func imgurBoundedText(raw string, limit int) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > limit {
		return ""
	}
	return raw
}

func imgurSetNonNegativeInt(object *value.Object, key string, input any) {
	parsed, ok := imgurIntOK(input)
	if ok && parsed >= 0 {
		object.Set(key, value.Int(parsed))
	}
}

func imgurString(input any) string {
	switch typed := input.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		if typed >= 0 && typed <= 1<<53 && typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
	}
	return ""
}

func imgurInt(input any) int64 {
	parsed, _ := imgurIntOK(input)
	return parsed
}

func imgurIntOK(input any) (int64, bool) {
	switch typed := input.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case float64:
		if typed >= 0 && typed <= 1<<53 && typed == float64(int64(typed)) {
			return int64(typed), true
		}
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	}
	return 0, false
}

func imgurFloat(input any) float64 {
	switch typed := input.(type) {
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	case float64:
		return typed
	case string:
		parsed, _ := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed
	}
	return 0
}

func imgurFirstTimestamp(inputs ...string) int64 {
	for _, input := range inputs {
		if input == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, input)
		if err == nil {
			return parsed.Unix()
		}
	}
	return 0
}
