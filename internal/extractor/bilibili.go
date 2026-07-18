package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// Bilibili consumes the public page's INITIAL_STATE and playinfo hydration.
// This intentionally avoids the signed WBI API and its per-session signing
// material while retaining the normal public video, anthology, DASH, and durl
// playback paths.
type Bilibili struct{}

func NewBilibili() Bilibili   { return Bilibili{} }
func (Bilibili) Name() string { return "bilibili" }

var bilibiliID = regexp.MustCompile(`(?i)^(?:BV[0-9A-Za-z]{8,32}|av[0-9]{1,32})$`)

func (Bilibili) Suitable(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Port() != "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h != "bilibili.com" && h != "www.bilibili.com" {
		return false
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) >= 2 && p[0] == "video" {
		return bilibiliID.MatchString(p[1])
	}
	if len(p) >= 2 && p[0] == "festival" {
		return bilibiliID.MatchString(u.Query().Get("bvid"))
	}
	return false
}
func (Bilibili) Extract(ctx context.Context, request Request) (Extraction, error) {
	u, err := url.Parse(request.URL)
	if err != nil || !NewBilibili().Suitable(u) {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	id := bilibiliURLID(u)
	if id == "" {
		return Extraction{}, ErrUnsupported
	}
	part := 0
	if raw := u.Query().Get("p"); raw != "" {
		part, err = strconv.Atoi(raw)
		if err != nil || part < 1 || part > 10000 {
			return Extraction{}, fmt.Errorf("%w: invalid Bilibili page", ErrInvalidMetadata)
		}
	}
	pageURL := "https://www.bilibili.com/video/" + url.PathEscape(id)
	if part > 0 {
		pageURL += "?p=" + strconv.Itoa(part)
	}
	page, _, err := request.Transport.ReadPage(ctx, pageURL)
	if err != nil {
		return Extraction{}, err
	}
	return parseBilibiliPage(page, id, part, pageURL)
}
func bilibiliURLID(u *url.URL) string {
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) >= 2 && p[0] == "festival" {
		return u.Query().Get("bvid")
	}
	if len(p) >= 2 {
		return p[1]
	}
	return ""
}

type bilibiliState struct {
	Error struct {
		TrueCode int `json:"trueCode"`
	} `json:"error"`
	VideoData bilibiliVideoData `json:"videoData"`
	VideoInfo bilibiliVideoData `json:"videoInfo"`
	UpData    struct {
		Name string      `json:"name"`
		MID  json.Number `json:"mid"`
	} `json:"upData"`
	Tags []struct {
		TagName string `json:"tag_name"`
	} `json:"tags"`
}
type bilibiliVideoData struct {
	BVID     string      `json:"bvid"`
	Aid      json.Number `json:"aid"`
	Title    string      `json:"title"`
	Desc     string      `json:"desc"`
	PubDate  int64       `json:"pubdate"`
	Duration float64     `json:"duration"`
	Pic      string      `json:"pic"`
	Owner    struct {
		Name string      `json:"name"`
		MID  json.Number `json:"mid"`
	} `json:"owner"`
	Stat struct {
		View  int64 `json:"view"`
		Reply int64 `json:"reply"`
		Like  int64 `json:"like"`
	} `json:"stat"`
	Pages []struct {
		Page     int         `json:"page"`
		CID      json.Number `json:"cid"`
		Part     string      `json:"part"`
		Duration float64     `json:"duration"`
	} `json:"pages"`
}
type bilibiliPlayinfo struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Timelength float64 `json:"timelength"`
		Quality    int64   `json:"quality"`
		DASH       struct {
			Audio []bilibiliDash `json:"audio"`
			Video []bilibiliDash `json:"video"`
			Dolby struct {
				Audio []bilibiliDash `json:"audio"`
			} `json:"dolby"`
			Flac struct {
				Audio bilibiliDash `json:"audio"`
			} `json:"flac"`
		} `json:"dash"`
		DURL []struct {
			URL    string  `json:"url"`
			Size   int64   `json:"size"`
			Length float64 `json:"length"`
		} `json:"durl"`
	} `json:"data"`
}
type bilibiliDash struct {
	ID          int64  `json:"id"`
	BaseURL     string `json:"baseUrl"`
	BaseURLAlt  string `json:"base_url"`
	MimeType    string `json:"mimeType"`
	MimeTypeAlt string `json:"mime_type"`
	Bandwidth   int64  `json:"bandwidth"`
	Codecs      string `json:"codecs"`
	Width       int64  `json:"width"`
	Height      int64  `json:"height"`
	FrameRate   string `json:"frame_rate"`
	Size        int64  `json:"size"`
}

func parseBilibiliPage(page []byte, requestedID string, part int, webpage string) (Extraction, error) {
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, ErrJSONResponseTooLarge
	}
	lower := bytes.ToLower(page)
	if bytes.Contains(lower, []byte("login")) || bytes.Contains(lower, []byte(`truecode\":-403`)) {
		return Extraction{}, ErrAuthentication
	}
	if bytes.Contains(lower, []byte("geo-restricted")) || bytes.Contains(lower, []byte(`truecode\":-404`)) {
		return Extraction{}, ErrRegionRestricted
	}
	rawState, err := extractJSONObject(page, "window.__INITIAL_STATE__")
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: missing Bilibili initial state", ErrInvalidMetadata)
	}
	var state bilibiliState
	if json.Unmarshal(rawState, &state) != nil {
		return Extraction{}, fmt.Errorf("%w: invalid Bilibili initial state", ErrInvalidMetadata)
	}
	if state.Error.TrueCode == -403 {
		return Extraction{}, ErrAuthentication
	}
	if state.Error.TrueCode == -404 {
		return Extraction{}, ErrRegionRestricted
	}
	video := state.VideoData
	if video.BVID == "" {
		video = state.VideoInfo
	}
	if video.BVID == "" {
		video.BVID = requestedID
	}
	if video.Title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Bilibili title", ErrInvalidMetadata)
	}
	if len(video.Pages) > 1 && part == 0 {
		entries := make([]Entry, 0, len(video.Pages))
		for _, pageInfo := range video.Pages {
			if pageInfo.Page < 1 {
				continue
			}
			entries = append(entries, Entry{URL: "https://www.bilibili.com/video/" + url.PathEscape(video.BVID) + "?p=" + strconv.Itoa(pageInfo.Page), ExtractorKey: "bilibili", ID: video.BVID + "_p" + strconv.Itoa(pageInfo.Page), Title: video.Title + " p" + strconv.Itoa(pageInfo.Page) + " " + pageInfo.Part, Transparent: true})
		}
		if len(entries) == 0 {
			return Extraction{}, fmt.Errorf("%w: invalid Bilibili anthology", ErrInvalidPlaylist)
		}
		return Playlist(bilibiliInfo(state, video, requestedID, 0, webpage), StaticEntries(entries...))
	}
	if part == 0 {
		part = 1
	}
	if len(video.Pages) > 0 && part > len(video.Pages) {
		return Extraction{}, fmt.Errorf("%w: Bilibili page out of range", ErrInvalidMetadata)
	}
	rawPlay, playErr := extractJSONObject(page, "window.__playinfo__")
	if playErr != nil {
		if bytes.Contains(lower, []byte("rate limit")) {
			return Extraction{}, ErrUnavailable
		}
		return Extraction{}, fmt.Errorf("%w: missing Bilibili playinfo", ErrInvalidMetadata)
	}
	var play bilibiliPlayinfo
	if json.Unmarshal(rawPlay, &play) != nil {
		return Extraction{}, fmt.Errorf("%w: invalid Bilibili playinfo", ErrInvalidMetadata)
	}
	if play.Code == -403 {
		return Extraction{}, ErrAuthentication
	}
	if play.Code == -404 {
		return Extraction{}, ErrRegionRestricted
	}
	if play.Code != 0 {
		return Extraction{}, ErrUnavailable
	}
	formats := bilibiliFormats(play)
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: no Bilibili formats", ErrUnavailable)
	}
	info := bilibiliInfo(state, video, requestedID, part, webpage).Fields().Clone()
	if len(video.Pages) > 1 {
		entry := video.Pages[part-1]
		info.Set("title", value.String(video.Title+" p"+strconv.Itoa(part)+" "+entry.Part))
		if entry.Duration > 0 {
			info.Set("duration", value.Float(entry.Duration))
		}
	}
	if play.Data.Timelength > 0 {
		info.Set("duration", value.Float(play.Data.Timelength/1000))
	}
	info.Set("formats", value.List(formats...))
	info.Set("ext", value.String("mp4"))
	return Media(value.NewInfo(info)), nil
}
func bilibiliInfo(state bilibiliState, v bilibiliVideoData, requested string, part int, webpage string) value.Info {
	id := v.BVID
	if id == "" {
		id = requested
	}
	if part > 0 && len(v.Pages) > 1 {
		id += "_p" + strconv.Itoa(part)
	}
	uploader := firstBilibiliString(state.UpData.Name, v.Owner.Name)
	uploaderID := firstBilibiliString(state.UpData.MID.String(), v.Owner.MID.String())
	info := value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "title", Value: value.String(v.Title)}, value.Field{Key: "description", Value: value.String(v.Desc)}, value.Field{Key: "uploader", Value: value.String(uploader)}, value.Field{Key: "uploader_id", Value: value.String(uploaderID)}, value.Field{Key: "webpage_url", Value: value.String(webpage)})
	setPositiveInt(info, "timestamp", v.PubDate)
	setPositiveInt(info, "view_count", v.Stat.View)
	setPositiveInt(info, "comment_count", v.Stat.Reply)
	setPositiveInt(info, "like_count", v.Stat.Like)
	if validHTTPURL(v.Pic) {
		info.Set("thumbnail", value.String(v.Pic))
	}
	if v.Duration > 0 {
		info.Set("duration", value.Float(v.Duration))
	}
	tags := make([]value.Value, 0, len(state.Tags))
	for _, tag := range state.Tags {
		if tag.TagName != "" {
			tags = append(tags, value.String(tag.TagName))
		}
	}
	if len(tags) > 0 {
		info.Set("tags", value.List(tags...))
	}
	return value.NewInfo(info)
}
func bilibiliFormats(play bilibiliPlayinfo) []value.Value {
	out := make([]value.Value, 0)
	audio := append([]bilibiliDash(nil), play.Data.DASH.Audio...)
	audio = append(audio, play.Data.DASH.Dolby.Audio...)
	if validHTTPURL(bilibiliDashURL(play.Data.DASH.Flac.Audio)) {
		audio = append(audio, play.Data.DASH.Flac.Audio)
	}
	for _, a := range audio {
		raw := bilibiliDashURL(a)
		if !validHTTPURL(raw) {
			continue
		}
		f := value.NewObject(value.Field{Key: "format_id", Value: value.String("dash-audio-" + strconv.FormatInt(a.ID, 10))}, value.Field{Key: "url", Value: value.String(raw)}, value.Field{Key: "ext", Value: value.String(bilibiliMIMEExt(a.MimeType, a.MimeTypeAlt, "m4a"))}, value.Field{Key: "vcodec", Value: value.String("none")}, value.Field{Key: "protocol", Value: value.String("https")})
		if a.Bandwidth > 0 {
			f.Set("abr", value.Float(float64(a.Bandwidth)/1000))
		}
		out = append(out, value.ObjectValue(f))
	}
	for _, v := range play.Data.DASH.Video {
		raw := bilibiliDashURL(v)
		if !validHTTPURL(raw) {
			continue
		}
		f := value.NewObject(value.Field{Key: "format_id", Value: value.String("dash-video-" + strconv.FormatInt(v.ID, 10))}, value.Field{Key: "url", Value: value.String(raw)}, value.Field{Key: "ext", Value: value.String(bilibiliMIMEExt(v.MimeType, v.MimeTypeAlt, "mp4"))}, value.Field{Key: "acodec", Value: value.String("none")}, value.Field{Key: "protocol", Value: value.String("https")})
		setPositiveInt(f, "width", v.Width)
		setPositiveInt(f, "height", v.Height)
		if v.Bandwidth > 0 {
			f.Set("tbr", value.Float(float64(v.Bandwidth)/1000))
		}
		out = append(out, value.ObjectValue(f))
	}
	for i, d := range play.Data.DURL {
		if !validHTTPURL(d.URL) {
			continue
		}
		f := value.NewObject(value.Field{Key: "format_id", Value: value.String("http-" + strconv.Itoa(i+1))}, value.Field{Key: "url", Value: value.String(d.URL)}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "protocol", Value: value.String("https")})
		setPositiveInt(f, "filesize", d.Size)
		out = append(out, value.ObjectValue(f))
	}
	return out
}
func bilibiliDashURL(d bilibiliDash) string {
	if d.BaseURL != "" {
		return d.BaseURL
	}
	return d.BaseURLAlt
}
func bilibiliMIMEExt(primary, alternate, fallback string) string {
	m := strings.ToLower(firstBilibiliString(primary, alternate))
	if strings.Contains(m, "webm") {
		return "webm"
	}
	if strings.Contains(m, "flac") {
		return "flac"
	}
	if strings.Contains(m, "audio") {
		return "m4a"
	}
	return fallback
}
func firstBilibiliString(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
