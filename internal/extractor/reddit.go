package extractor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// Reddit supports the anonymous post-listing response. It only claims post
// URLs; community listings and comment pages are left to their own extractors.
type Reddit struct{}

func NewReddit() Reddit     { return Reddit{} }
func (Reddit) Name() string { return "reddit" }

var redditPostID = regexp.MustCompile(`^[a-z0-9]{3,16}$`)

func (Reddit) Suitable(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if !(host == "reddit.com" || strings.HasSuffix(host, ".reddit.com") || host == "redditmedia.com" || strings.HasSuffix(host, ".redditmedia.com")) {
		return false
	}
	_, id, ok := redditPostPath(u.Path)
	return ok && redditPostID.MatchString(id)
}

func redditPostPath(pathname string) (string, string, bool) {
	p := strings.Split(strings.Trim(pathname, "/"), "/")
	for i, segment := range p {
		if segment == "comments" && i+1 < len(p) {
			id := p[i+1]
			if strings.HasSuffix(id, ".json") {
				id = strings.TrimSuffix(id, ".json")
			}
			return strings.Join(p[:i+2], "/"), id, true
		}
	}
	return "", "", false
}

func (Reddit) Extract(ctx context.Context, request Request) (Extraction, error) {
	u, err := url.Parse(request.URL)
	if err != nil || !NewReddit().Suitable(u) {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	slug, displayID, _ := redditPostPath(u.Path)
	endpoint := &url.URL{Scheme: "https", Host: "www.reddit.com", Path: "/" + slug + "/.json"}
	q := endpoint.Query()
	q.Set("raw_json", "1")
	q.Set("limit", "1")
	endpoint.RawQuery = q.Encode()
	var listing []redditListing
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	if err := RequestJSON(ctx, request.Transport, http.MethodGet, endpoint.String(), nil, headers, &listing); err != nil {
		return Extraction{}, categorizeRedditError(err)
	}
	if len(listing) == 0 || len(listing[0].Data.Children) == 0 {
		return Extraction{}, ErrUnavailable
	}
	post := listing[0].Data.Children[0].Data
	return normalizeRedditPost(post, displayID, "https://www.reddit.com/"+slug+"/")
}

type redditListing struct {
	Data struct {
		Children []struct {
			Data redditPost `json:"data"`
		} `json:"children"`
	} `json:"data"`
}
type redditPost struct {
	ID            string                         `json:"id"`
	Name          string                         `json:"name"`
	Title         string                         `json:"title"`
	Selftext      string                         `json:"selftext"`
	Author        string                         `json:"author"`
	Subreddit     string                         `json:"subreddit"`
	Created       float64                        `json:"created_utc"`
	Over18        bool                           `json:"over_18"`
	IsVideo       bool                           `json:"is_video"`
	URL           string                         `json:"url"`
	Permalink     string                         `json:"permalink"`
	Thumbnail     string                         `json:"thumbnail"`
	Score         int64                          `json:"score"`
	NumComments   int64                          `json:"num_comments"`
	SecureMedia   *redditMedia                   `json:"secure_media"`
	Media         *redditMedia                   `json:"media"`
	Crossposts    []redditPost                   `json:"crosspost_parent_list"`
	MediaMetadata map[string]redditMediaMetadata `json:"media_metadata"`
}
type redditMedia struct {
	RedditVideo *redditVideo `json:"reddit_video"`
	OEmbed      *struct {
		Title        string `json:"title"`
		AuthorName   string `json:"author_name"`
		ThumbnailURL string `json:"thumbnail_url"`
	} `json:"oembed"`
}
type redditVideo struct {
	DASHURL     string  `json:"dash_url"`
	HLSURL      string  `json:"hls_url"`
	FallbackURL string  `json:"fallback_url"`
	ScrubberURL string  `json:"scrubber_media_url"`
	Width       int64   `json:"width"`
	Height      int64   `json:"height"`
	Duration    float64 `json:"duration"`
	IsGIF       bool    `json:"is_gif"`
}
type redditMediaMetadata struct {
	Status string `json:"status"`
	S      struct {
		U   string `json:"u"`
		MP4 string `json:"mp4"`
		GIF string `json:"gif"`
		X   int64  `json:"x"`
		Y   int64  `json:"y"`
	} `json:"s"`
}

func normalizeRedditPost(post redditPost, displayID, webpage string) (Extraction, error) {
	if len(post.Crossposts) > 0 && redditVideoFor(post) == nil {
		post = post.Crossposts[0]
	}
	if post.Title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Reddit title", ErrInvalidMetadata)
	}
	video := redditVideoFor(post)
	if video == nil {
		entries := redditEmbeddedEntries(post)
		if len(entries) > 0 {
			return Playlist(redditPostInfo(post, displayID, webpage, nil), StaticEntries(entries...))
		}
		if validHTTPURL(post.URL) && !strings.Contains(strings.ToLower(post.URL), "reddit.com") {
			return Playlist(redditPostInfo(post, displayID, webpage, nil), StaticEntries(Entry{URL: post.URL, Title: post.Title, Transparent: true}))
		}
		return Extraction{}, ErrUnavailable
	}
	formats := make([]value.Value, 0, 3)
	if validHTTPURL(video.DASHURL) {
		formats = append(formats, value.ObjectValue(manifestFormat("dash", redditCleanURL(video.DASHURL), "http_dash_segments")))
	}
	if validHTTPURL(video.HLSURL) {
		formats = append(formats, value.ObjectValue(manifestFormat("hls", redditCleanURL(video.HLSURL), "m3u8_native")))
	}
	if validHTTPURL(video.FallbackURL) {
		f := value.NewObject(value.Field{Key: "format_id", Value: value.String("http")}, value.Field{Key: "url", Value: value.String(redditCleanURL(video.FallbackURL))}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "protocol", Value: value.String("https")})
		setPositiveInt(f, "width", video.Width)
		setPositiveInt(f, "height", video.Height)
		formats = append(formats, value.ObjectValue(f))
	}
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: no Reddit video formats", ErrInvalidMetadata)
	}
	fields := redditPostInfo(post, displayID, webpage, video).Fields().Clone()
	fields.Set("id", value.String(redditVideoID(video, post.ID)))
	fields.Set("formats", value.List(formats...))
	fields.Set("ext", value.String("mp4"))
	if video.IsGIF {
		fields.Set("vcodec", value.String("none"))
	}
	return Media(value.NewInfo(fields)), nil
}

func redditVideoFor(post redditPost) *redditVideo {
	if post.SecureMedia != nil && post.SecureMedia.RedditVideo != nil {
		return post.SecureMedia.RedditVideo
	}
	if post.Media != nil {
		return post.Media.RedditVideo
	}
	return nil
}
func redditVideoID(video *redditVideo, fallback string) string {
	for _, raw := range []string{video.DASHURL, video.HLSURL, video.FallbackURL} {
		u, err := url.Parse(raw)
		if err == nil {
			p := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(p) > 0 && p[0] != "" {
				return p[0]
			}
		}
	}
	return fallback
}
func redditCleanURL(raw string) string { return strings.ReplaceAll(raw, "&amp;", "&") }

func redditPostInfo(post redditPost, displayID, webpage string, video *redditVideo) value.Info {
	info := value.NewObject(value.Field{Key: "id", Value: value.String(post.ID)}, value.Field{Key: "display_id", Value: value.String(displayID)}, value.Field{Key: "title", Value: value.String(post.Title)}, value.Field{Key: "description", Value: value.String(post.Selftext)}, value.Field{Key: "uploader", Value: value.String(post.Author)}, value.Field{Key: "channel_id", Value: value.String(post.Subreddit)}, value.Field{Key: "webpage_url", Value: value.String(webpage)}, value.Field{Key: "age_limit", Value: value.Int(0)})
	if post.Over18 {
		info.Set("age_limit", value.Int(18))
	}
	if post.Created > 0 {
		info.Set("timestamp", value.Float(post.Created))
	}
	setPositiveInt(info, "like_count", post.Score)
	setPositiveInt(info, "comment_count", post.NumComments)
	if validHTTPURL(post.Thumbnail) {
		info.Set("thumbnail", value.String(post.Thumbnail))
	}
	if video != nil && video.Duration > 0 {
		info.Set("duration", value.Float(video.Duration))
	}
	return value.NewInfo(info)
}
func redditEmbeddedEntries(post redditPost) []Entry {
	keys := make([]string, 0, len(post.MediaMetadata))
	for k := range post.MediaMetadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entries := make([]Entry, 0, len(keys))
	for _, k := range keys {
		m := post.MediaMetadata[k]
		raw := m.S.MP4
		if raw == "" {
			raw = m.S.GIF
		}
		raw = redditCleanURL(raw)
		if validHTTPURL(raw) {
			entries = append(entries, Entry{URL: raw, ID: k, Title: post.Title})
		}
	}
	return entries
}
func categorizeRedditError(err error) error {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusUnavailableForLegalReasons:
			return ErrRegionRestricted
		case http.StatusTooManyRequests:
			return ErrUnavailable
		}
	}
	return err
}

// Kept separate from the route parser so fuzzers can exercise identifiers and
// hostile percent-encoding without making a request.
func parseRedditTimestamp(raw string) int64 { n, _ := strconv.ParseInt(raw, 10, 64); return n }
