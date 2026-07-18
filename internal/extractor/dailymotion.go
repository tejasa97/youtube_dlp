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
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// Dailymotion implements the public player-metadata route documented by the
// Dailymotion player. It deliberately does not mint or persist OAuth tokens:
// player metadata is the anonymous, bounded route used by this extractor.
type Dailymotion struct{}

func NewDailymotion() Dailymotion { return Dailymotion{} }
func (Dailymotion) Name() string  { return "dailymotion" }

var dailymotionID = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)

func (Dailymotion) Suitable(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "dai.ly" {
		return dailymotionID.MatchString(strings.Trim(u.Path, "/"))
	}
	if !strings.HasSuffix(host, ".dailymotion.com") && host != "dailymotion.com" {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 && (parts[0] == "video" || (parts[0] == "embed" && parts[1] == "video") || parts[0] == "swf") {
		id := parts[len(parts)-1]
		id = strings.Split(id, "_")[0]
		return dailymotionID.MatchString(id)
	}
	return strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "player/") &&
		(dailymotionID.MatchString(u.Query().Get("video")) || strings.HasPrefix(u.Query().Get("playlist"), "x"))
}

func (Dailymotion) Extract(ctx context.Context, request Request) (Extraction, error) {
	u, err := url.Parse(request.URL)
	if err != nil || !NewDailymotion().Suitable(u) {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if playlistID := u.Query().Get("playlist"); playlistID != "" && u.Query().Get("video") == "" {
		if !regexp.MustCompile(`^x[A-Za-z0-9]{1,128}$`).MatchString(playlistID) {
			return Extraction{}, fmt.Errorf("%w: invalid Dailymotion playlist ID", ErrInvalidPlaylist)
		}
		return extractDailymotionPlaylist(ctx, request.Transport, playlistID)
	}
	id := dailymotionVideoID(u)
	if id == "" {
		return Extraction{}, ErrUnsupported
	}
	var metadata dailymotionMetadata
	endpoint := "https://www.dailymotion.com/player/metadata/video/" + url.PathEscape(id)
	if err := RequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &metadata); err != nil {
		return Extraction{}, categorizeDailymotionError(err)
	}
	return normalizeDailymotion(metadata, id, "https://www.dailymotion.com/video/"+id)
}

func dailymotionVideoID(u *url.URL) string {
	if strings.EqualFold(u.Hostname(), "dai.ly") {
		return strings.Trim(u.Path, "/")
	}
	if id := u.Query().Get("video"); dailymotionID.MatchString(id) {
		return id
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	id := parts[len(parts)-1]
	if len(parts) >= 2 && parts[0] == "embed" {
		id = parts[len(parts)-1]
	}
	id = strings.Split(id, "_")[0]
	if !dailymotionID.MatchString(id) {
		return ""
	}
	return id
}

type dailymotionMetadata struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Duration    float64 `json:"duration"`
	CreatedTime int64   `json:"created_time"`
	PosterURL   string  `json:"poster_url"`
	Explicit    bool    `json:"explicit"`
	Live        bool    `json:"is_live"`
	Owner       struct {
		Screenname string `json:"screenname"`
		ID         string `json:"id"`
	} `json:"owner"`
	Error struct {
		Code  string `json:"code"`
		Title string `json:"title"`
		Raw   string `json:"raw_message"`
	} `json:"error"`
	Qualities map[string][]struct {
		URL     string `json:"url"`
		Type    string `json:"type"`
		Width   int64  `json:"width"`
		Height  int64  `json:"height"`
		Bitrate int64  `json:"bitrate"`
	} `json:"qualities"`
	Subtitles struct {
		Data map[string]struct {
			URLs []string `json:"urls"`
		} `json:"data"`
	} `json:"subtitles"`
}

func normalizeDailymotion(m dailymotionMetadata, id, webpage string) (Extraction, error) {
	if m.Error.Code != "" || m.Error.Title != "" || m.Error.Raw != "" {
		if strings.EqualFold(m.Error.Code, "DM007") {
			return Extraction{}, ErrRegionRestricted
		}
		if strings.Contains(strings.ToLower(m.Error.Title+" "+m.Error.Raw), "private") {
			return Extraction{}, ErrAuthentication
		}
		return Extraction{}, ErrUnavailable
	}
	if strings.TrimSpace(m.Title) == "" {
		return Extraction{}, fmt.Errorf("%w: missing Dailymotion title", ErrInvalidMetadata)
	}
	qualities := make([]string, 0, len(m.Qualities))
	for quality := range m.Qualities {
		qualities = append(qualities, quality)
	}
	sort.Strings(qualities)
	formats := make([]value.Value, 0)
	for _, quality := range qualities {
		for _, f := range m.Qualities[quality] {
			if !validHTTPURL(f.URL) {
				continue
			}
			kind := strings.ToLower(f.Type)
			var format *value.Object
			switch {
			case strings.Contains(kind, "mpegurl") || strings.HasSuffix(strings.ToLower(mustURLPath(f.URL)), ".m3u8"):
				format = manifestFormat("hls-"+quality, strings.Split(f.URL, "#")[0], "m3u8_native")
			case strings.Contains(kind, "dash") || strings.HasSuffix(strings.ToLower(mustURLPath(f.URL)), ".mpd"):
				format = manifestFormat("dash-"+quality, strings.Split(f.URL, "#")[0], "http_dash_segments")
			default:
				ext := strings.TrimPrefix(path.Ext(mustURLPath(f.URL)), ".")
				if ext == "" {
					ext = "mp4"
				}
				format = value.NewObject(value.Field{Key: "format_id", Value: value.String("http-" + quality)}, value.Field{Key: "url", Value: value.String(strings.Split(f.URL, "#")[0])}, value.Field{Key: "ext", Value: value.String(ext)}, value.Field{Key: "protocol", Value: value.String("https")})
			}
			setPositiveInt(format, "width", f.Width)
			setPositiveInt(format, "height", f.Height)
			if f.Bitrate > 0 {
				format.Set("tbr", value.Float(float64(f.Bitrate)/1000))
			}
			formats = append(formats, value.ObjectValue(format))
		}
	}
	if len(formats) == 0 {
		if m.Live {
			return Extraction{}, ErrUnavailable
		}
		return Extraction{}, fmt.Errorf("%w: no Dailymotion formats", ErrInvalidMetadata)
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "title", Value: value.String(m.Title)}, value.Field{Key: "description", Value: value.String(m.Description)}, value.Field{Key: "webpage_url", Value: value.String(webpage)}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "formats", Value: value.List(formats...)}, value.Field{Key: "is_live", Value: value.Bool(m.Live)})
	setPositiveInt(info, "timestamp", m.CreatedTime)
	if m.Duration > 0 {
		info.Set("duration", value.Float(m.Duration))
	}
	if validHTTPURL(m.PosterURL) {
		info.Set("thumbnail", value.String(m.PosterURL))
	}
	if m.Owner.Screenname != "" {
		info.Set("uploader", value.String(m.Owner.Screenname))
	}
	if m.Owner.ID != "" {
		info.Set("uploader_id", value.String(m.Owner.ID))
	}
	if m.Explicit {
		info.Set("age_limit", value.Int(18))
	} else {
		info.Set("age_limit", value.Int(0))
	}
	subs := value.NewObject()
	for lang, sub := range m.Subtitles.Data {
		if len(sub.URLs) > 0 && validHTTPURL(sub.URLs[0]) {
			subs.Set(lang, value.List(value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(sub.URLs[0])}))))
		}
	}
	if len(subs.Fields()) > 0 {
		info.Set("subtitles", value.ObjectValue(subs))
	}
	return Media(value.NewInfo(info)), nil
}

type dailymotionPlaylistMetadata struct {
	Title  string `json:"title"`
	Videos []struct {
		ID    string `json:"id"`
		XID   string `json:"xid"`
		URL   string `json:"url"`
		Title string `json:"title"`
	} `json:"videos"`
}

func extractDailymotionPlaylist(ctx context.Context, transport Transport, id string) (Extraction, error) {
	var page dailymotionPlaylistMetadata
	endpoint := "https://www.dailymotion.com/player/metadata/playlist/" + url.PathEscape(id)
	if err := RequestJSON(ctx, transport, http.MethodGet, endpoint, nil, make(http.Header), &page); err != nil {
		return Extraction{}, categorizeDailymotionError(err)
	}
	entries := make([]Entry, 0, len(page.Videos))
	for _, item := range page.Videos {
		videoID := item.XID
		if videoID == "" {
			videoID = item.ID
		}
		raw := item.URL
		if raw == "" && dailymotionID.MatchString(videoID) {
			raw = "https://www.dailymotion.com/video/" + videoID
		}
		if raw != "" && dailymotionID.MatchString(videoID) {
			entries = append(entries, Entry{URL: raw, ExtractorKey: "dailymotion", ID: videoID, Title: item.Title, Transparent: true})
		}
	}
	if len(entries) == 0 {
		return Extraction{}, fmt.Errorf("%w: empty Dailymotion playlist", ErrInvalidPlaylist)
	}
	title := page.Title
	if title == "" {
		title = id
	}
	return Playlist(value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "title", Value: value.String(title)}, value.Field{Key: "webpage_url", Value: value.String("https://www.dailymotion.com/playlist/" + id)})), StaticEntries(entries...))
}

func categorizeDailymotionError(err error) error {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusUnavailableForLegalReasons:
			return ErrRegionRestricted
		}
	}
	return err
}
