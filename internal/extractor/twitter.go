package extractor

import (
	"context"
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

// Twitter is intentionally built on the public syndication endpoint instead
// of embedding a web bearer token. The endpoint returns Tweet media variants
// without persisting account data or leaking caller query parameters.
type Twitter struct{}

func NewTwitter() Twitter    { return Twitter{} }
func (Twitter) Name() string { return "twitter" }

var twitterStatusID = regexp.MustCompile(`^[0-9]{5,32}$`)

func (Twitter) Suitable(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Port() != "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h != "twitter.com" && h != "www.twitter.com" && h != "mobile.twitter.com" && h != "x.com" && h != "www.x.com" {
		return false
	}
	_, id, ok := twitterStatusPath(u.Path)
	return ok && twitterStatusID.MatchString(id)
}
func twitterStatusPath(p string) (string, string, bool) {
	segments := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range segments {
		if s == "status" && i+1 < len(segments) {
			return strings.Join(segments[:i+2], "/"), segments[i+1], true
		}
	}
	return "", "", false
}

func (Twitter) Extract(ctx context.Context, request Request) (Extraction, error) {
	u, err := url.Parse(request.URL)
	if err != nil || !NewTwitter().Suitable(u) {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	_, id, _ := twitterStatusPath(u.Path)
	endpoint := &url.URL{Scheme: "https", Host: "cdn.syndication.twimg.com", Path: "/tweet-result"}
	q := endpoint.Query()
	q.Set("id", id)
	q.Set("lang", "en")
	endpoint.RawQuery = q.Encode()
	var tweet twitterSyndicationTweet
	if err := RequestJSON(ctx, request.Transport, http.MethodGet, endpoint.String(), nil, make(http.Header), &tweet); err != nil {
		return Extraction{}, categorizeTwitterError(err)
	}
	mediaIndex := 0
	if requested, parseErr := strconv.Atoi(u.Query().Get("media")); parseErr == nil && requested > 0 {
		mediaIndex = requested
	}
	return normalizeTwitter(tweet, id, "https://twitter.com/i/web/status/"+id, mediaIndex)
}

type twitterSyndicationTweet struct {
	IDStr             string                 `json:"id_str"`
	Text              string                 `json:"text"`
	FullText          string                 `json:"full_text"`
	CreatedAt         string                 `json:"created_at"`
	FavoriteCount     int64                  `json:"favorite_count"`
	RetweetCount      int64                  `json:"retweet_count"`
	ReplyCount        int64                  `json:"reply_count"`
	PossiblySensitive bool                   `json:"possibly_sensitive"`
	User              twitterSyndicationUser `json:"user"`
	Entities          struct {
		Media []twitterSyndicationMedia `json:"media"`
	} `json:"entities"`
	ExtendedEntities struct {
		Media []twitterSyndicationMedia `json:"media"`
	} `json:"extended_entities"`
	Media []twitterSyndicationMedia `json:"media"`
}
type twitterSyndicationUser struct {
	IDStr        string `json:"id_str"`
	Name         string `json:"name"`
	ScreenName   string `json:"screen_name"`
	ProfileImage string `json:"profile_image_url_https"`
}
type twitterSyndicationMedia struct {
	IDStr     string `json:"id_str"`
	Type      string `json:"type"`
	MediaURL  string `json:"media_url_https"`
	URL       string `json:"url"`
	VideoInfo struct {
		DurationMS int64            `json:"duration_millis"`
		Variants   []twitterVariant `json:"variants"`
	} `json:"video_info"`
}
type twitterVariant struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Bitrate     int64  `json:"bitrate"`
}

func normalizeTwitter(tweet twitterSyndicationTweet, requestedID, webpage string, mediaIndex int) (Extraction, error) {
	if tweet.IDStr == "" {
		tweet.IDStr = requestedID
	}
	title := strings.TrimSpace(tweet.FullText)
	if title == "" {
		title = strings.TrimSpace(tweet.Text)
	}
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Tweet text", ErrInvalidMetadata)
	}
	media := tweet.ExtendedEntities.Media
	if len(media) == 0 {
		media = tweet.Entities.Media
	}
	if len(media) == 0 {
		media = tweet.Media
	}
	if len(media) == 0 {
		return Extraction{}, ErrUnavailable
	}
	// A Tweet can contain several videos. Preserve their order as a lazy URL
	// result only when there is more than one playable item.
	playable := make([]twitterSyndicationMedia, 0, len(media))
	for _, m := range media {
		if len(m.VideoInfo.Variants) > 0 {
			playable = append(playable, m)
		}
	}
	if len(playable) == 0 {
		return Extraction{}, ErrUnavailable
	}
	if mediaIndex > 0 {
		if mediaIndex > len(playable) {
			return Extraction{}, fmt.Errorf("%w: Tweet media index", ErrInvalidMetadata)
		}
		formats := twitterFormats(playable[mediaIndex-1])
		if len(formats) == 0 {
			return Extraction{}, fmt.Errorf("%w: no Tweet formats", ErrInvalidMetadata)
		}
		fields := twitterInfo(tweet, requestedID, webpage, title, &playable[mediaIndex-1]).Fields().Clone()
		fields.Set("id", value.String(twitterMediaID(playable[mediaIndex-1], tweet.IDStr, mediaIndex-1)))
		fields.Set("formats", value.List(formats...))
		fields.Set("ext", value.String("mp4"))
		return Media(value.NewInfo(fields)), nil
	}
	if len(playable) > 1 {
		entries := make([]Entry, 0, len(playable))
		for i, m := range playable {
			formats := twitterFormats(m)
			if len(formats) == 0 {
				continue
			}
			entryURL := "https://twitter.com/i/web/status/" + tweet.IDStr + "?media=" + strconv.Itoa(i+1)
			entries = append(entries, Entry{URL: entryURL, ExtractorKey: "twitter", ID: twitterMediaID(m, tweet.IDStr, i), Title: title, Transparent: true})
		}
		if len(entries) == 0 {
			return Extraction{}, fmt.Errorf("%w: no Tweet formats", ErrInvalidMetadata)
		}
		return Playlist(twitterInfo(tweet, requestedID, webpage, title, nil), StaticEntries(entries...))
	}
	formats := twitterFormats(playable[0])
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: no Tweet formats", ErrInvalidMetadata)
	}
	fields := twitterInfo(tweet, requestedID, webpage, title, &playable[0]).Fields().Clone()
	fields.Set("id", value.String(twitterMediaID(playable[0], tweet.IDStr, 0)))
	fields.Set("formats", value.List(formats...))
	fields.Set("ext", value.String("mp4"))
	return Media(value.NewInfo(fields)), nil
}
func twitterMediaID(m twitterSyndicationMedia, tweet string, index int) string {
	if m.IDStr != "" {
		return m.IDStr
	}
	if index == 0 {
		return tweet
	}
	return tweet + "_" + strconv.Itoa(index+1)
}
func twitterFormats(media twitterSyndicationMedia) []value.Value {
	variants := append([]twitterVariant(nil), media.VideoInfo.Variants...)
	sort.SliceStable(variants, func(i, j int) bool { return variants[i].Bitrate < variants[j].Bitrate })
	out := make([]value.Value, 0, len(variants))
	for _, v := range variants {
		if !validHTTPURL(v.URL) {
			continue
		}
		ct := strings.ToLower(v.ContentType)
		if strings.Contains(ct, "mpegurl") || strings.HasSuffix(strings.ToLower(mustURLPath(v.URL)), ".m3u8") {
			out = append(out, value.ObjectValue(manifestFormat("hls", v.URL, "m3u8_native")))
			continue
		}
		ext := strings.TrimPrefix(path.Ext(mustURLPath(v.URL)), ".")
		if ext == "" {
			ext = "mp4"
		}
		f := value.NewObject(value.Field{Key: "format_id", Value: value.String("http-" + strconv.FormatInt(v.Bitrate, 10))}, value.Field{Key: "url", Value: value.String(v.URL)}, value.Field{Key: "ext", Value: value.String(ext)}, value.Field{Key: "protocol", Value: value.String("https")})
		if v.Bitrate > 0 {
			f.Set("tbr", value.Float(float64(v.Bitrate)/1000))
		}
		out = append(out, value.ObjectValue(f))
	}
	return out
}
func twitterInfo(tweet twitterSyndicationTweet, displayID, webpage, title string, media *twitterSyndicationMedia) value.Info {
	info := value.NewObject(value.Field{Key: "id", Value: value.String(tweet.IDStr)}, value.Field{Key: "display_id", Value: value.String(displayID)}, value.Field{Key: "title", Value: value.String(title)}, value.Field{Key: "description", Value: value.String(title)}, value.Field{Key: "webpage_url", Value: value.String(webpage)}, value.Field{Key: "uploader", Value: value.String(tweet.User.Name)}, value.Field{Key: "uploader_id", Value: value.String(tweet.User.ScreenName)}, value.Field{Key: "uploader_url", Value: value.String("https://twitter.com/" + tweet.User.ScreenName)}, value.Field{Key: "age_limit", Value: value.Int(0)})
	if tweet.PossiblySensitive {
		info.Set("age_limit", value.Int(18))
	}
	if validHTTPURL(tweet.User.ProfileImage) {
		info.Set("uploader_url", value.String("https://twitter.com/"+tweet.User.ScreenName))
	}
	for k, n := range map[string]int64{"like_count": tweet.FavoriteCount, "repost_count": tweet.RetweetCount, "comment_count": tweet.ReplyCount} {
		setPositiveInt(info, k, n)
	}
	if created, err := time.Parse(time.RubyDate, tweet.CreatedAt); err == nil {
		info.Set("timestamp", value.Int(created.Unix()))
	}
	if media != nil && media.VideoInfo.DurationMS > 0 {
		info.Set("duration", value.Float(float64(media.VideoInfo.DurationMS)/1000))
	}
	return value.NewInfo(info)
}
func categorizeTwitterError(err error) error {
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
