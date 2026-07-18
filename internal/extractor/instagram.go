package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	instagramImpersonationProfile = "chrome-133"
	instagramWebAppID             = "936619743392459"
	instagramMaxProfileEntries    = 50
)

var (
	instagramPostPath    = regexp.MustCompile(`^/(?:[^/?#]+/)?(?:p|tv|reels?)/([^/?#&]+)/?(?:embed/?)?$`)
	instagramStoryPath   = regexp.MustCompile(`^/stories/([^/?#]+)(?:/([0-9]+))?/?$`)
	instagramProfilePath = regexp.MustCompile(`^/([A-Za-z0-9_.]{2,30})/?$`)
	instagramScript      = regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script\s*>`)
	instagramShortcode   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

type Instagram struct{}

func NewInstagram() Instagram { return Instagram{} }

func (Instagram) Name() string { return "instagram" }

func (Instagram) Suitable(parsed *url.URL) bool {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Port() != "" || parsed.User != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "instagram.com" && host != "www.instagram.com" {
		return false
	}
	if instagramPostPath.MatchString(parsed.Path) || instagramStoryPath.MatchString(parsed.Path) {
		return true
	}
	match := instagramProfilePath.FindStringSubmatch(parsed.Path)
	return len(match) == 2 && !instagramReservedProfile(match[1])
}

func (Instagram) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewInstagram().Suitable(parsed) || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if match := instagramPostPath.FindStringSubmatch(parsed.Path); len(match) == 2 {
		return extractInstagramPost(ctx, request.Transport, match[1])
	}
	if match := instagramStoryPath.FindStringSubmatch(parsed.Path); len(match) != 0 {
		return extractInstagramStory(ctx, request.Transport, match[1], match[2], request.URL)
	}
	match := instagramProfilePath.FindStringSubmatch(parsed.Path)
	if len(match) != 2 || instagramReservedProfile(match[1]) {
		return Extraction{}, ErrUnsupported
	}
	return extractInstagramProfile(ctx, request.Transport, match[1])
}

func instagramReservedProfile(profile string) bool {
	switch strings.ToLower(profile) {
	case "about", "accounts", "api", "developer", "direct", "directory", "emails", "explore", "legal", "oauth", "p", "push", "reel", "reels", "stories", "tv", "web":
		return true
	default:
		return false
	}
}

func extractInstagramPost(ctx context.Context, transport Transport, shortcode string) (Extraction, error) {
	if !instagramShortcode.MatchString(shortcode) {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.instagram.com/p/" + shortcode + "/"
	page, _, err := ReadPageWithProfile(ctx, transport, canonical, instagramImpersonationProfile)
	if err != nil {
		return Extraction{}, categorizeInstagramError(err)
	}
	return parseInstagramPage(page, shortcode, canonical)
}

func extractInstagramStory(ctx context.Context, transport Transport, username, storyID, webpageURL string) (Extraction, error) {
	page, _, err := ReadPageWithProfile(ctx, transport, webpageURL, instagramImpersonationProfile)
	if err != nil {
		return Extraction{}, categorizeInstagramError(err)
	}
	result, err := parseInstagramPage(page, storyID, webpageURL)
	if err != nil && errors.Is(err, ErrInvalidMetadata) {
		return Extraction{}, ErrAuthentication
	}
	if err != nil {
		return Extraction{}, err
	}
	if result.IsPlaylist() {
		result.Info.Set("title", value.String("Story by "+username))
	}
	return result, nil
}

func extractInstagramProfile(ctx context.Context, transport Transport, username string) (Extraction, error) {
	endpoint := "https://www.instagram.com/api/v1/users/web_profile_info/?username=" + url.QueryEscape(username)
	var root instagramProfileResponse
	if err := requestRiskJSON(ctx, transport, http.MethodGet, endpoint, nil, instagramHeaders("https://www.instagram.com/"+username+"/"), instagramImpersonationProfile, &root); err != nil {
		return Extraction{}, categorizeInstagramError(err)
	}
	user := root.Data.User
	if user.Username == "" {
		return Extraction{}, fmt.Errorf("%w: missing Instagram profile", ErrInvalidMetadata)
	}
	if user.IsPrivate && len(user.Timeline.Edges) == 0 {
		return Extraction{}, ErrAuthentication
	}
	first := instagramTimelineEntries(user.Timeline.Edges)
	next := ""
	if user.Timeline.PageInfo.HasNextPage {
		next = user.Timeline.PageInfo.EndCursor
		if next == "" {
			return Extraction{}, fmt.Errorf("%w: missing Instagram cursor", ErrInvalidPlaylist)
		}
	}
	sequence, err := ContinuationEntries(first, next, func(ctx context.Context, cursor string) ([]Entry, string, error) {
		return fetchInstagramProfilePage(ctx, transport, user.ID, cursor)
	})
	if err != nil {
		return Extraction{}, err
	}
	title := user.FullName
	if title == "" {
		title = user.Username
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(user.Username)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String("https://www.instagram.com/" + user.Username + "/")},
	)
	riskString(info, "description", user.Biography)
	return Playlist(value.NewInfo(info), sequence)
}

func fetchInstagramProfilePage(ctx context.Context, transport Transport, userID, cursor string) ([]Entry, string, error) {
	if userID == "" || cursor == "" || len(cursor) > 2048 {
		return nil, "", fmt.Errorf("%w: invalid Instagram cursor", ErrInvalidPlaylist)
	}
	variables, _ := json.Marshal(map[string]any{"id": userID, "first": instagramMaxProfileEntries, "after": cursor})
	endpoint := "https://www.instagram.com/graphql/query/?query_hash=42323d64886122307be10013ad2dcc44&variables=" + url.QueryEscape(string(variables))
	var root struct {
		Data struct {
			User struct {
				Timeline instagramTimeline `json:"edge_owner_to_timeline_media"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := requestRiskJSON(ctx, transport, http.MethodGet, endpoint, nil, instagramHeaders("https://www.instagram.com/"), instagramImpersonationProfile, &root); err != nil {
		return nil, "", categorizeInstagramError(err)
	}
	next := ""
	if root.Data.User.Timeline.PageInfo.HasNextPage {
		next = root.Data.User.Timeline.PageInfo.EndCursor
		if next == "" {
			return nil, "", fmt.Errorf("%w: missing Instagram cursor", ErrInvalidPlaylist)
		}
	}
	return instagramTimelineEntries(root.Data.User.Timeline.Edges), next, nil
}

type instagramProfileResponse struct {
	Data struct {
		User struct {
			ID        string            `json:"id"`
			Username  string            `json:"username"`
			FullName  string            `json:"full_name"`
			Biography string            `json:"biography"`
			IsPrivate bool              `json:"is_private"`
			Timeline  instagramTimeline `json:"edge_owner_to_timeline_media"`
		} `json:"user"`
	} `json:"data"`
}

type instagramTimeline struct {
	Edges []struct {
		Node struct {
			Shortcode string `json:"shortcode"`
			ID        string `json:"id"`
			Title     string `json:"title"`
			IsVideo   bool   `json:"is_video"`
		} `json:"node"`
	} `json:"edges"`
	PageInfo struct {
		HasNextPage bool   `json:"has_next_page"`
		EndCursor   string `json:"end_cursor"`
	} `json:"page_info"`
}

func instagramTimelineEntries(edges []struct {
	Node struct {
		Shortcode string `json:"shortcode"`
		ID        string `json:"id"`
		Title     string `json:"title"`
		IsVideo   bool   `json:"is_video"`
	} `json:"node"`
}) []Entry {
	entries := make([]Entry, 0, len(edges))
	for _, edge := range edges {
		if !edge.Node.IsVideo || !instagramShortcode.MatchString(edge.Node.Shortcode) {
			continue
		}
		entries = append(entries, Entry{URL: "https://www.instagram.com/p/" + edge.Node.Shortcode + "/", ExtractorKey: "instagram", ID: edge.Node.Shortcode, Title: edge.Node.Title})
	}
	return entries
}

func instagramHeaders(referer string) http.Header {
	headers := make(http.Header)
	headers.Set("Accept", "*/*")
	headers.Set("X-IG-App-ID", instagramWebAppID)
	headers.Set("Referer", referer)
	return headers
}

func categorizeInstagramError(err error) error {
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

type instagramMedia struct {
	PK                json.RawMessage `json:"pk"`
	ID                string          `json:"id"`
	Code              string          `json:"code"`
	Shortcode         string          `json:"shortcode"`
	Title             string          `json:"title"`
	VideoURL          string          `json:"video_url"`
	VideoDuration     float64         `json:"video_duration"`
	TakenAt           int64           `json:"taken_at"`
	TakenAtTimestamp  int64           `json:"taken_at_timestamp"`
	ViewCount         int64           `json:"view_count"`
	VideoViewCount    int64           `json:"video_view_count"`
	LikeCount         int64           `json:"like_count"`
	CommentCount      int64           `json:"comment_count"`
	DisplayURL        string          `json:"display_url"`
	ThumbnailSrc      string          `json:"thumbnail_src"`
	IsVideo           bool            `json:"is_video"`
	Typename          string          `json:"__typename"`
	VideoDashManifest string          `json:"video_dash_manifest"`
	VideoVersions     []struct {
		ID     string `json:"id"`
		Type   int64  `json:"type"`
		URL    string `json:"url"`
		Width  int64  `json:"width"`
		Height int64  `json:"height"`
	} `json:"video_versions"`
	Caption struct {
		Text string `json:"text"`
	} `json:"caption"`
	User struct {
		PK       json.RawMessage `json:"pk"`
		ID       string          `json:"id"`
		Username string          `json:"username"`
		FullName string          `json:"full_name"`
	} `json:"user"`
	Owner struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		FullName string `json:"full_name"`
	} `json:"owner"`
	Dimensions struct {
		Width  int64 `json:"width"`
		Height int64 `json:"height"`
	} `json:"dimensions"`
	CarouselMedia []instagramMedia `json:"carousel_media"`
}

func parseInstagramPage(page []byte, expectedID, webpageURL string) (Extraction, error) {
	if int64(len(page)) > riskExtractorMaxJSONBytes {
		return Extraction{}, ErrJSONResponseTooLarge
	}
	media, ok := findInstagramMediaInPage(page, expectedID)
	if !ok {
		lower := bytes.ToLower(page)
		switch {
		case bytes.Contains(lower, []byte("challenge_required")), bytes.Contains(lower, []byte("please wait")), bytes.Contains(lower, []byte("checkpoint_required")):
			return Extraction{}, ErrChallengeSolver
		case bytes.Contains(lower, []byte("login_required")), bytes.Contains(lower, []byte("accounts/login")), bytes.Contains(lower, []byte("restricted video")), bytes.Contains(lower, []byte("private account")):
			return Extraction{}, ErrAuthentication
		case bytes.Contains(lower, []byte("content isn't available")), bytes.Contains(lower, []byte("page isn't available")):
			return Extraction{}, ErrUnavailable
		}
		return Extraction{}, fmt.Errorf("%w: missing Instagram media", ErrInvalidMetadata)
	}
	if len(media.CarouselMedia) != 0 {
		entries := make([]Entry, 0, len(media.CarouselMedia))
		for index, item := range media.CarouselMedia {
			mediaURL := instagramBestMediaURL(item)
			if mediaURL == "" {
				continue
			}
			id := instagramMediaID(item, strconv.Itoa(index+1))
			entries = append(entries, Entry{URL: mediaURL, ExtractorKey: "generic", ID: id, Title: fmt.Sprintf("Video %d", index+1)})
		}
		if len(entries) == 0 {
			return Extraction{}, ErrUnavailable
		}
		info := instagramBaseInfo(media, expectedID, webpageURL)
		channel, _ := info.Lookup("channel").StringValue()
		if channel != "" {
			info.Set("title", value.String("Post by "+channel))
		}
		return Playlist(value.NewInfo(info), StaticEntries(entries...))
	}
	return normalizeInstagramMedia(media, expectedID, webpageURL)
}

func findInstagramMediaInPage(page []byte, expectedID string) (instagramMedia, bool) {
	for _, match := range instagramScript.FindAllSubmatch(page, -1) {
		if len(match) != 2 {
			continue
		}
		candidate := bytes.TrimSpace([]byte(html.UnescapeString(string(match[1]))))
		if len(candidate) == 0 || (candidate[0] != '{' && candidate[0] != '[') {
			continue
		}
		var root any
		decoder := json.NewDecoder(bytes.NewReader(candidate))
		decoder.UseNumber()
		if decoder.Decode(&root) != nil {
			continue
		}
		if media, ok := findInstagramMedia(root, expectedID, 0); ok {
			return media, true
		}
	}
	return instagramMedia{}, false
}

func findInstagramMedia(node any, expectedID string, depth int) (instagramMedia, bool) {
	if depth > 64 {
		return instagramMedia{}, false
	}
	switch node := node.(type) {
	case map[string]any:
		_, versions := node["video_versions"]
		_, videoURL := node["video_url"]
		_, carousel := node["carousel_media"]
		isVideo, _ := node["is_video"].(bool)
		typename, _ := node["__typename"].(string)
		if versions || videoURL || carousel || isVideo || typename == "GraphVideo" || typename == "GraphSidecar" {
			encoded, err := json.Marshal(node)
			if err == nil {
				var media instagramMedia
				if json.Unmarshal(encoded, &media) == nil {
					id := instagramMediaID(media, "")
					if expectedID == "" || id == expectedID || media.ID == expectedID || media.Shortcode == expectedID || media.Code == expectedID || carousel {
						return media, true
					}
				}
			}
		}
		for _, child := range node {
			if media, ok := findInstagramMedia(child, expectedID, depth+1); ok {
				return media, true
			}
		}
	case []any:
		for _, child := range node {
			if media, ok := findInstagramMedia(child, expectedID, depth+1); ok {
				return media, true
			}
		}
	}
	return instagramMedia{}, false
}

func normalizeInstagramMedia(media instagramMedia, fallbackID, webpageURL string) (Extraction, error) {
	formats := make([]value.Value, 0, len(media.VideoVersions)+1)
	seen := make(map[string]bool)
	for _, version := range media.VideoVersions {
		if seen[version.URL] {
			continue
		}
		formatID := version.ID
		if formatID == "" && version.Type != 0 {
			formatID = strconv.FormatInt(version.Type, 10)
		}
		format, ok := riskFormat(version.URL, formatID)
		if !ok {
			continue
		}
		seen[version.URL] = true
		riskPositiveInt(format, "width", version.Width)
		riskPositiveInt(format, "height", version.Height)
		formats = append(formats, value.ObjectValue(format))
	}
	if !seen[media.VideoURL] {
		if format, ok := riskFormat(media.VideoURL, "direct"); ok {
			seen[media.VideoURL] = true
			riskPositiveInt(format, "width", media.Dimensions.Width)
			riskPositiveInt(format, "height", media.Dimensions.Height)
			formats = append(formats, value.ObjectValue(format))
		}
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	info := instagramBaseInfo(media, fallbackID, webpageURL)
	info.Set("formats", value.List(formats...))
	info.Set("ext", value.String("mp4"))
	riskFloat(info, "duration", media.VideoDuration)
	return Media(value.NewInfo(info)), nil
}

func instagramBaseInfo(media instagramMedia, fallbackID, webpageURL string) *value.Object {
	id := instagramMediaID(media, fallbackID)
	channel := media.User.Username
	uploader := media.User.FullName
	uploaderID := instagramRawString(media.User.PK)
	if channel == "" {
		channel = media.Owner.Username
	}
	if uploader == "" {
		uploader = media.Owner.FullName
	}
	if uploaderID == "" {
		uploaderID = media.Owner.ID
	}
	title := media.Title
	if title == "" && channel != "" {
		title = "Video by " + channel
	}
	if title == "" {
		title = "Instagram video " + id
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(value.Field{Key: "Referer", Value: value.String("https://www.instagram.com/")}))},
	)
	riskString(info, "description", media.Caption.Text)
	riskString(info, "channel", channel)
	riskString(info, "uploader", uploader)
	riskString(info, "uploader_id", uploaderID)
	timestamp := media.TakenAt
	if timestamp == 0 {
		timestamp = media.TakenAtTimestamp
	}
	riskPositiveInt(info, "timestamp", timestamp)
	viewCount := media.ViewCount
	if viewCount == 0 {
		viewCount = media.VideoViewCount
	}
	riskPositiveInt(info, "view_count", viewCount)
	riskPositiveInt(info, "like_count", media.LikeCount)
	riskPositiveInt(info, "comment_count", media.CommentCount)
	thumbnail := media.DisplayURL
	if thumbnail == "" {
		thumbnail = media.ThumbnailSrc
	}
	riskString(info, "thumbnail", thumbnail)
	return info
}

func instagramBestMediaURL(media instagramMedia) string {
	for _, version := range media.VideoVersions {
		if validHTTPURL(version.URL) {
			return version.URL
		}
	}
	if validHTTPURL(media.VideoURL) {
		return media.VideoURL
	}
	return ""
}

func instagramMediaID(media instagramMedia, fallback string) string {
	for _, candidate := range []string{media.Code, media.Shortcode} {
		if instagramShortcode.MatchString(candidate) {
			return candidate
		}
	}
	if pk := instagramRawString(media.PK); pk != "" {
		if shortcode := instagramPKToShortcode(pk); shortcode != "" {
			return shortcode
		}
	}
	if media.ID != "" {
		return strings.Split(media.ID, "_")[0]
	}
	return fallback
}

func instagramRawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

func instagramPKToShortcode(pk string) string {
	pk = strings.Split(pk, "_")[0]
	number, err := strconv.ParseUint(pk, 10, 64)
	if err != nil {
		return ""
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	if number == 0 {
		return "A"
	}
	var encoded [11]byte
	index := len(encoded)
	for number != 0 {
		index--
		encoded[index] = alphabet[number%64]
		number /= 64
	}
	return string(encoded[index:])
}
