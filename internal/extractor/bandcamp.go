package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// Bandcamp supports public track and album pages. Purchase/download hand-offs
// are deliberately not followed because they may contain short-lived customer
// URLs; the page's public stream map is sufficient for anonymous playback.
type Bandcamp struct{}

func NewBandcamp() Bandcamp   { return Bandcamp{} }
func (Bandcamp) Name() string { return "bandcamp" }

var bandcampSlug = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,200}$`)

func (Bandcamp) Suitable(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Port() != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".bandcamp.com") {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(parts) == 2 && (parts[0] == "track" || parts[0] == "album") && bandcampSlug.MatchString(parts[1])
}
func (Bandcamp) Extract(ctx context.Context, request Request) (Extraction, error) {
	u, err := url.Parse(request.URL)
	if err != nil || !NewBandcamp().Suitable(u) {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	page, _, err := request.Transport.ReadPage(ctx, request.URL)
	if err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, ErrJSONResponseTooLarge
	}
	tralbum, err := parseBandcampData(page, "tralbum")
	if err != nil {
		return Extraction{}, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if parts[0] == "album" {
		return normalizeBandcampAlbum(tralbum, u)
	}
	return normalizeBandcampTrack(tralbum, u)
}

func parseBandcampData(page []byte, name string) (bandcampTralbum, error) {
	pattern := regexp.MustCompile(`(?is)\bdata-` + regexp.QuoteMeta(name) + `\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	match := pattern.FindSubmatch(page)
	if len(match) != 3 {
		return bandcampTralbum{}, fmt.Errorf("%w: missing Bandcamp %s data", ErrInvalidMetadata, name)
	}
	raw := match[1]
	if len(raw) == 0 {
		raw = match[2]
	}
	decoded := html.UnescapeString(string(raw))
	if len(decoded) > int(maxExtractorJSONBytes) {
		return bandcampTralbum{}, ErrJSONResponseTooLarge
	}
	var result bandcampTralbum
	if err := json.Unmarshal([]byte(decoded), &result); err != nil {
		return bandcampTralbum{}, fmt.Errorf("%w: invalid Bandcamp %s data", ErrInvalidMetadata, name)
	}
	return result, nil
}

type bandcampTralbum struct {
	ID      json.Number `json:"id"`
	Artist  string      `json:"artist"`
	Current struct {
		Title       string `json:"title"`
		About       string `json:"about"`
		Artist      string `json:"artist"`
		PublishDate string `json:"publish_date"`
	} `json:"current"`
	AlbumTitle       string          `json:"album_title"`
	AlbumPublishDate string          `json:"album_publish_date"`
	TrackInfo        []bandcampTrack `json:"trackinfo"`
}
type bandcampTrack struct {
	TrackID      json.Number       `json:"track_id"`
	ID           json.Number       `json:"id"`
	Title        string            `json:"title"`
	TitleLink    string            `json:"title_link"`
	TrackNum     int64             `json:"track_num"`
	Duration     float64           `json:"duration"`
	File         map[string]string `json:"file"`
	StreamingURL string            `json:"streaming_url"`
}

func normalizeBandcampTrack(data bandcampTralbum, u *url.URL) (Extraction, error) {
	if len(data.TrackInfo) == 0 {
		return Extraction{}, fmt.Errorf("%w: missing Bandcamp track", ErrInvalidMetadata)
	}
	track := data.TrackInfo[0]
	return bandcampTrackMedia(data, track, u.String())
}
func bandcampTrackMedia(data bandcampTralbum, track bandcampTrack, webpage string) (Extraction, error) {
	title := strings.TrimSpace(track.Title)
	artist := firstBandcampString(data.Current.Artist, data.Artist)
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Bandcamp track title", ErrInvalidMetadata)
	}
	formats := make([]value.Value, 0, len(track.File)+1)
	keys := make([]string, 0, len(track.File))
	for k := range track.File {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, id := range keys {
		raw := bandcampAbsoluteURL(track.File[id])
		if !validHTTPURL(raw) {
			continue
		}
		ext := strings.SplitN(id, "-", 2)[0]
		if ext == "" {
			ext = "mp3"
		}
		f := value.NewObject(value.Field{Key: "format_id", Value: value.String(id)}, value.Field{Key: "url", Value: value.String(raw)}, value.Field{Key: "ext", Value: value.String(ext)}, value.Field{Key: "vcodec", Value: value.String("none")}, value.Field{Key: "acodec", Value: value.String(ext)}, value.Field{Key: "protocol", Value: value.String("https")})
		if parts := strings.SplitN(id, "-", 2); len(parts) == 2 {
			if abr, err := strconv.ParseInt(parts[1], 10, 64); err == nil && abr > 0 {
				f.Set("abr", value.Int(abr))
			}
		}
		formats = append(formats, value.ObjectValue(f))
	}
	if len(formats) == 0 && validHTTPURL(bandcampAbsoluteURL(track.StreamingURL)) {
		formats = append(formats, value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("http")}, value.Field{Key: "url", Value: value.String(bandcampAbsoluteURL(track.StreamingURL))}, value.Field{Key: "ext", Value: value.String("mp3")}, value.Field{Key: "vcodec", Value: value.String("none")}, value.Field{Key: "protocol", Value: value.String("https")})))
	}
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: no public Bandcamp formats", ErrUnavailable)
	}
	id := track.TrackID.String()
	if id == "" {
		id = track.ID.String()
	}
	if id == "" {
		id = strings.Trim(strings.TrimPrefix(webpage, "https://"), "/")
	}
	fullTitle := title
	if artist != "" {
		fullTitle = artist + " - " + title
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "title", Value: value.String(fullTitle)}, value.Field{Key: "track", Value: value.String(title)}, value.Field{Key: "uploader", Value: value.String(artist)}, value.Field{Key: "artist", Value: value.String(artist)}, value.Field{Key: "webpage_url", Value: value.String(webpage)}, value.Field{Key: "ext", Value: value.String("mp3")}, value.Field{Key: "formats", Value: value.List(formats...)})
	if track.Duration > 0 {
		info.Set("duration", value.Float(track.Duration))
	}
	setPositiveInt(info, "track_number", track.TrackNum)
	if data.AlbumTitle != "" {
		info.Set("album", value.String(data.AlbumTitle))
	}
	return Media(value.NewInfo(info)), nil
}
func normalizeBandcampAlbum(data bandcampTralbum, u *url.URL) (Extraction, error) {
	if len(data.TrackInfo) == 0 {
		return Extraction{}, fmt.Errorf("%w: empty Bandcamp album", ErrInvalidPlaylist)
	}
	entries := make([]Entry, 0, len(data.TrackInfo))
	for i, t := range data.TrackInfo {
		if strings.TrimSpace(t.Title) == "" {
			continue
		}
		raw := bandcampAbsoluteURL(t.TitleLink)
		if raw == "" {
			raw = u.Scheme + "://" + u.Host + "/track/" + url.PathEscape(strings.ReplaceAll(strings.ToLower(t.Title), " ", "-"))
		}
		id := t.TrackID.String()
		if id == "" {
			id = t.ID.String()
		}
		entries = append(entries, Entry{URL: raw, ExtractorKey: "bandcamp", ID: id, Title: t.Title, Transparent: true})
		_ = i
	}
	if len(entries) == 0 {
		return Extraction{}, fmt.Errorf("%w: empty Bandcamp album", ErrInvalidPlaylist)
	}
	title := firstBandcampString(data.Current.Title, data.AlbumTitle)
	if title == "" {
		title := strings.Trim(strings.TrimPrefix(u.Path, "/album/"), "/")
		return Playlist(value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(title)}, value.Field{Key: "title", Value: value.String(title)}, value.Field{Key: "webpage_url", Value: value.String(u.String())})), StaticEntries(entries...))
	}
	return Playlist(value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(strings.Trim(u.Path, "/album/"))}, value.Field{Key: "title", Value: value.String(title)}, value.Field{Key: "uploader", Value: value.String(firstBandcampString(data.Current.Artist, data.Artist))}, value.Field{Key: "webpage_url", Value: value.String(u.String())})), StaticEntries(entries...))
}
func bandcampAbsoluteURL(raw string) string {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\/", "/"))
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	if strings.HasPrefix(raw, "/") {
		return "https://bandcamp.com" + raw
	}
	return raw
}
func firstBandcampString(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
