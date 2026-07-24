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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	flickrBeaconURL      = "https://www.flickr.com/hermes_error_beacon.gne"
	flickrAPIURL         = "https://api.flickr.com/services/rest"
	flickrMaxURLBytes    = 4096
	flickrMaxStreams     = 128
	flickrMaxTags        = 256
	flickrMaxTextBytes   = 64 << 10
	flickrMaxAPIKeyBytes = 128
)

var (
	flickrIDPattern      = regexp.MustCompile(`^[0-9]{1,24}$`)
	flickrUserPattern    = regexp.MustCompile(`^[A-Za-z0-9_@-]{1,128}$`)
	flickrAPIKeyPattern  = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)
	flickrSecretPattern  = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)
	ErrFlickrNetwork     = errors.New("Flickr API request failed")
	ErrFlickrAPIResponse = errors.New("Flickr API rejected request")
)

type flickrTarget struct {
	id       string
	userPath string
}

// Flickr extracts public Flickr videos using the anonymous site key and
// documented REST response shapes. Photo-only items are unavailable.
type Flickr struct{}

func NewFlickr() Flickr     { return Flickr{} }
func (Flickr) Name() string { return "flickr" }
func (Flickr) Suitable(u *url.URL) bool {
	_, ok := classifyFlickrURL(u)
	return ok
}

func classifyFlickrURL(parsed *url.URL) (flickrTarget, bool) {
	if parsed == nil || len(parsed.String()) == 0 || len(parsed.String()) > flickrMaxURLBytes ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Port() != "" {
		return flickrTarget{}, false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "flickr.com", "www.flickr.com", "secure.flickr.com":
	default:
		return flickrTarget{}, false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
		return flickrTarget{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "photos" || !flickrUserPattern.MatchString(parts[1]) || !flickrIDPattern.MatchString(parts[2]) {
		return flickrTarget{}, false
	}
	return flickrTarget{id: parts[2], userPath: parts[1]}, true
}

func (Flickr) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyFlickrURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	apiKey, err := flickrDiscoverAPIKey(ctx, request.Transport)
	if err != nil {
		return Extraction{}, err
	}
	infoResponse, err := flickrCallAPI(ctx, request.Transport, "photos.getInfo", target.id, apiKey, "")
	if err != nil {
		return Extraction{}, err
	}
	if !strings.EqualFold(infoResponse.Photo.Media, "video") {
		return Extraction{}, fmt.Errorf("%w: Flickr item is not a video", ErrUnavailable)
	}
	if !flickrSecretPattern.MatchString(infoResponse.Photo.Secret) {
		return Extraction{}, fmt.Errorf("%w: invalid Flickr video secret", ErrInvalidMetadata)
	}
	streamResponse, err := flickrCallAPI(ctx, request.Transport, "video.getStreamInfo", target.id, apiKey, infoResponse.Photo.Secret)
	if err != nil {
		return Extraction{}, err
	}
	webpageURL := "https://www.flickr.com/photos/" + target.userPath + "/" + target.id
	return normalizeFlickr(target, webpageURL, infoResponse.Photo, streamResponse.Streams.Stream)
}

type flickrBeaconResponse struct {
	SiteKey string `json:"site_key"`
}

type flickrAPIResponse struct {
	Stat    string           `json:"stat"`
	Message string           `json:"message"`
	Photo   flickrPhoto      `json:"photo"`
	Streams flickrStreamList `json:"streams"`
}

type flickrContent struct {
	Content string `json:"_content"`
}

type flickrPhoto struct {
	ID           string        `json:"id"`
	Secret       string        `json:"secret"`
	Media        string        `json:"media"`
	License      string        `json:"license"`
	DateUploaded any           `json:"dateuploaded"`
	Views        any           `json:"views"`
	Title        flickrContent `json:"title"`
	Description  flickrContent `json:"description"`
	Comments     flickrContent `json:"comments"`
	Owner        flickrOwner   `json:"owner"`
	Video        flickrVideo   `json:"video"`
	Tags         flickrTagList `json:"tags"`
}

type flickrOwner struct {
	NSID      string `json:"nsid"`
	RealName  string `json:"realname"`
	UserName  string `json:"username"`
	PathAlias string `json:"path_alias"`
}

type flickrVideo struct {
	Duration any `json:"duration"`
	Width    any `json:"width"`
	Height   any `json:"height"`
}

type flickrTagList struct {
	Tag []flickrTag `json:"tag"`
}

type flickrTag struct {
	Content string `json:"_content"`
}

type flickrStreamList struct {
	Stream []flickrStream `json:"stream"`
}

type flickrStream struct {
	Type    any    `json:"type"`
	Content string `json:"_content"`
}

func flickrDiscoverAPIKey(ctx context.Context, transport Transport) (string, error) {
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	var response flickrBeaconResponse
	if err := RequestJSONWithoutCookies(ctx, transport, http.MethodGet, flickrBeaconURL, nil, headers, &response); err != nil {
		return "", categorizeFlickrError(err)
	}
	key := strings.TrimSpace(response.SiteKey)
	if len(key) > flickrMaxAPIKeyBytes || !flickrAPIKeyPattern.MatchString(key) {
		return "", fmt.Errorf("%w: invalid Flickr site key", ErrInvalidMetadata)
	}
	return key, nil
}

func flickrCallAPI(ctx context.Context, transport Transport, method, videoID, apiKey, secret string) (flickrAPIResponse, error) {
	if !flickrIDPattern.MatchString(videoID) || !flickrAPIKeyPattern.MatchString(apiKey) {
		return flickrAPIResponse{}, ErrUnsupported
	}
	if method != "photos.getInfo" && method != "video.getStreamInfo" {
		return flickrAPIResponse{}, ErrUnsupported
	}
	query := make(url.Values)
	query.Set("photo_id", videoID)
	query.Set("method", "flickr."+method)
	query.Set("api_key", apiKey)
	query.Set("format", "json")
	query.Set("nojsoncallback", "1")
	if secret != "" {
		if !flickrSecretPattern.MatchString(secret) {
			return flickrAPIResponse{}, fmt.Errorf("%w: invalid Flickr secret", ErrInvalidMetadata)
		}
		query.Set("secret", secret)
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	var response flickrAPIResponse
	if err := RequestJSONWithoutCookies(ctx, transport, http.MethodGet, flickrAPIURL+"?"+query.Encode(), nil, headers, &response); err != nil {
		return flickrAPIResponse{}, categorizeFlickrError(err)
	}
	if response.Stat != "ok" {
		return flickrAPIResponse{}, ErrFlickrAPIResponse
	}
	return response, nil
}

func categorizeFlickrError(err error) error {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return ErrFlickrNetwork
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrJSONResponseTooLarge) || errors.Is(err, ErrInvalidMetadata) ||
		errors.Is(err, ErrTransportIsolation) {
		return err
	}
	return ErrFlickrNetwork
}

func normalizeFlickr(target flickrTarget, webpageURL string, photo flickrPhoto, streams []flickrStream) (Extraction, error) {
	if photo.ID != "" && photo.ID != target.id {
		return Extraction{}, fmt.Errorf("%w: mismatched Flickr id", ErrInvalidMetadata)
	}
	title := flickrText(photo.Title.Content)
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Flickr title", ErrInvalidMetadata)
	}
	if len(streams) == 0 || len(streams) > flickrMaxStreams {
		return Extraction{}, fmt.Errorf("%w: invalid Flickr stream inventory", ErrInvalidMetadata)
	}
	sort.SliceStable(streams, func(i, j int) bool {
		return flickrQuality(flickrString(streams[i].Type)) < flickrQuality(flickrString(streams[j].Type))
	})
	formats := make([]value.Value, 0, len(streams))
	seen := make(map[string]bool)
	for _, stream := range streams {
		formatID := flickrString(stream.Type)
		if formatID == "" || len(formatID) > 128 {
			continue
		}
		rawURL, ok := normalizeFlickrStreamURL(stream.Content)
		if !ok || seen[rawURL] {
			continue
		}
		seen[rawURL] = true
		extension := strings.TrimPrefix(strings.ToLower(path.Ext(mustURLPath(rawURL))), ".")
		if extension == "" {
			extension = "mp4"
		}
		protocol := "https"
		if extension == "m3u8" {
			protocol, extension = "m3u8_native", "mp4"
		}
		format := value.NewObject(
			value.Field{Key: "format_id", Value: value.String(formatID)},
			value.Field{Key: "url", Value: value.String(rawURL)},
			value.Field{Key: "ext", Value: value.String(extension)},
			value.Field{Key: "protocol", Value: value.String(protocol)},
			value.Field{Key: "quality", Value: value.Int(int64(flickrQuality(formatID)))},
		)
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: no safe Flickr video streams", ErrUnavailable)
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	if description := flickrText(photo.Description.Content); description != "" {
		info.Set("description", value.String(description))
	}
	setPositiveInt(info, "timestamp", flickrInt(photo.DateUploaded))
	if timestamp, ok := info.Lookup("timestamp").Int(); ok {
		info.Set("upload_date", value.String(time.Unix(timestamp, 0).UTC().Format("20060102")))
	}
	setPositiveInt(info, "duration", flickrInt(photo.Video.Duration))
	setPositiveInt(info, "width", flickrInt(photo.Video.Width))
	setPositiveInt(info, "height", flickrInt(photo.Video.Height))
	flickrSetNonNegativeInt(info, "comment_count", photo.Comments.Content)
	flickrSetNonNegativeInt(info, "view_count", photo.Views)
	if uploaderID := flickrText(photo.Owner.NSID); uploaderID != "" {
		info.Set("uploader_id", value.String(uploaderID))
	}
	uploader := flickrText(photo.Owner.RealName)
	if uploader == "" {
		uploader = flickrText(photo.Owner.UserName)
	}
	if uploader != "" {
		info.Set("uploader", value.String(uploader))
	}
	uploaderPath := flickrText(photo.Owner.PathAlias)
	if uploaderPath == "" {
		uploaderPath = flickrText(photo.Owner.NSID)
	}
	if flickrUserPattern.MatchString(uploaderPath) {
		info.Set("uploader_url", value.String("https://www.flickr.com/photos/"+uploaderPath+"/"))
	}
	if license := flickrLicense(photo.License); license != "" {
		info.Set("license", value.String(license))
	}
	tags := make([]value.Value, 0, min(len(photo.Tags.Tag), flickrMaxTags))
	seenTags := make(map[string]bool)
	for _, tag := range photo.Tags.Tag {
		text := flickrText(tag.Content)
		if text == "" || seenTags[text] {
			continue
		}
		seenTags[text] = true
		tags = append(tags, value.String(text))
		if len(tags) == flickrMaxTags {
			break
		}
	}
	info.Set("tags", value.List(tags...))
	return Media(value.NewInfo(info)), nil
}

func normalizeFlickrStreamURL(raw string) (string, bool) {
	if len(raw) == 0 || len(raw) > flickrMaxURLBytes || strings.ContainsAny(raw, "\\\x00\r\n") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Fragment != "" || parsed.Host == "" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "staticflickr.com" && !strings.HasSuffix(host, ".staticflickr.com") {
		return "", false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
		return "", false
	}
	parsed.Host = host
	return parsed.String(), true
}

func flickrQuality(formatID string) int {
	order := []string{"288p", "iphone_wifi", "100", "300", "700", "360p", "appletv", "720p", "1080p", "orig"}
	for index, candidate := range order {
		if formatID == candidate {
			return index
		}
	}
	return -1
}

func flickrLicense(id string) string {
	return map[string]string{
		"0":  "All Rights Reserved",
		"1":  "Attribution-NonCommercial-ShareAlike",
		"2":  "Attribution-NonCommercial",
		"3":  "Attribution-NonCommercial-NoDerivs",
		"4":  "Attribution",
		"5":  "Attribution-ShareAlike",
		"6":  "Attribution-NoDerivs",
		"7":  "No known copyright restrictions",
		"8":  "United States government work",
		"9":  "Public Domain Dedication (CC0)",
		"10": "Public Domain Work",
	}[id]
}

func flickrText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > flickrMaxTextBytes {
		return ""
	}
	return raw
}

func flickrString(input any) string {
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

func flickrInt(input any) int64 {
	switch typed := input.(type) {
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case float64:
		if typed > 0 && typed <= 1<<53 && typed == float64(int64(typed)) {
			return int64(typed)
		}
	}
	return 0
}

func flickrSetNonNegativeInt(object *value.Object, key string, input any) {
	raw := flickrString(input)
	if raw == "" {
		return
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err == nil && parsed >= 0 {
		object.Set(key, value.Int(parsed))
	}
}
