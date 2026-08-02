package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	bilibiliPageMaxBytes    = 5 << 20
	bilibiliListMaxPages    = 10_000
	bilibiliListPageSize    = 30
	bilibiliDomesticReferer = "https://www.bilibili.com/"
	bilibiliIntlReferer     = "https://www.bilibili.tv/"
)

var (
	// These sentinels deliberately contain no URL, response body, query, or
	// transport detail. They are the only status details exposed by this
	// extractor's HTTP boundary.
	ErrBilibiliRedirect    = errors.New("bilibili redirect response")
	ErrBilibiliClient      = errors.New("bilibili client response")
	ErrBilibiliRateLimited = errors.New("bilibili rate limited")
	ErrBilibiliServer      = errors.New("bilibili server response")
)

var bilibiliID = regexp.MustCompile(`(?i)^(?:BV[0-9A-Za-z]{8,32}|av[0-9]{1,32})$`)
var bilibiliNumericID = regexp.MustCompile(`^[0-9]{1,32}$`)
var bilibiliSafeHostLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type bilibiliURLRole uint8

const (
	bilibiliDomesticMedia bilibiliURLRole = iota
	bilibiliInternationalMedia
	bilibiliLiveMedia
	bilibiliDomesticThumbnail
	bilibiliInternationalThumbnail
	bilibiliDomesticSubtitle
	bilibiliInternationalSubtitle
)

func bilibiliHostSuffix(host, suffix string) bool {
	host = strings.ToLower(host)
	suffix = strings.ToLower(suffix)
	if host != suffix && !strings.HasSuffix(host, "."+suffix) {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !bilibiliSafeHostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func bilibiliRoleHost(host string, role bilibiliURLRole) bool {
	switch role {
	case bilibiliDomesticMedia:
		return bilibiliHostSuffix(host, "bilivideo.com") || host == "upos-hz-mirrorakam.akamaized.net"
	case bilibiliInternationalMedia:
		return bilibiliHostSuffix(host, "bstarstatic.com") || bilibiliHostSuffix(host, "biliintl.com")
	case bilibiliLiveMedia:
		return bilibiliHostSuffix(host, "bilivideo.com") || bilibiliHostSuffix(host, "hdslb.com")
	case bilibiliDomesticThumbnail:
		return bilibiliHostSuffix(host, "hdslb.com") || bilibiliHostSuffix(host, "biliimg.com")
	case bilibiliInternationalThumbnail:
		return bilibiliHostSuffix(host, "bstarstatic.com")
	case bilibiliDomesticSubtitle:
		return bilibiliHostSuffix(host, "bilibili.com") || bilibiliHostSuffix(host, "hdslb.com") || bilibiliHostSuffix(host, "bilivideo.com")
	case bilibiliInternationalSubtitle:
		return bilibiliHostSuffix(host, "bstarstatic.com")
	default:
		return false
	}
}

func validBilibiliRoleURL(raw string, role bilibiliURLRole) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.Hostname() == "" {
		return false
	}
	return bilibiliRoleHost(parsed.Hostname(), role)
}

func bilibiliUniqueQuery(parsed *url.URL) (url.Values, bool) {
	if parsed == nil || parsed.ForceQuery {
		return nil, false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return nil, false
	}
	for _, values := range query {
		if len(values) != 1 {
			return nil, false
		}
	}
	return query, true
}

func bilibiliStrictURL(parsed *url.URL, hosts ...string) bool {
	if parsed == nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Hostname() == "" {
		return false
	}
	if _, ok := bilibiliUniqueQuery(parsed); !ok {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if len(hosts) == 0 {
		return true
	}
	for _, allowed := range hosts {
		if host == allowed {
			return true
		}
	}
	return false
}

func bilibiliExactPath(parsed *url.URL, path string, trailingSlash bool) bool {
	if parsed == nil || parsed.Path == path {
		return parsed != nil && parsed.Path == path
	}
	return trailingSlash && parsed.Path == path+"/"
}

func bilibiliStatusError(status int) error {
	switch {
	case status >= 300 && status < 400:
		return fmt.Errorf("%w: status %d", ErrBilibiliRedirect, status)
	case status == http.StatusTooManyRequests:
		return ErrBilibiliRateLimited
	case status == http.StatusUnavailableForLegalReasons:
		return ErrRegionRestricted
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrAuthentication
	case status >= 400 && status < 500:
		return fmt.Errorf("%w: status %d", ErrBilibiliClient, status)
	case status >= 500:
		return fmt.Errorf("%w: status %d", ErrBilibiliServer, status)
	default:
		return fmt.Errorf("%w: status %d", ErrUnavailable, status)
	}
}

func sanitizeBilibiliRequestError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return bilibiliStatusError(status.Code)
	}
	if errors.Is(err, ErrTransportIsolation) || errors.Is(err, ErrJSONResponseTooLarge) || errors.Is(err, ErrInvalidMetadata) {
		return err
	}
	return ErrUnavailable
}

func bilibiliIsolatedReadPage(ctx context.Context, transport Transport, rawURL string) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, ErrInvalidMetadata
	}
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, sanitizeBilibiliRequestError(ctx, err)
	}
	if response == nil {
		return nil, ErrUnavailable
	}
	if response.Body == nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, bilibiliStatusError(response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, bilibiliPageMaxBytes+1))
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrUnavailable
	}
	if len(data) > bilibiliPageMaxBytes {
		return nil, ErrJSONResponseTooLarge
	}
	return data, nil
}

func bilibiliIsolatedRequestJSON(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	err := requestJSON(ctx, isolated.DoWithoutCredentialsNoRedirect, method, rawURL, body, headers, target)
	return sanitizeBilibiliRequestError(ctx, err)
}

func bilibiliIsolatedRequestJSONWithReferer(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	isolated, ok := transport.(RefererCredentialIsolatedNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	err := requestJSON(ctx, isolated.DoWithoutCredentialsNoRedirectWithReferer, method, rawURL, body, headers, target)
	return sanitizeBilibiliRequestError(ctx, err)
}

func bilibiliAPIError(code int) error {
	switch code {
	case 0:
		return nil
	case -404, 10004001:
		return ErrRegionRestricted
	case -403, 10004004, 10004005, 10023006:
		return ErrAuthentication
	case -10403:
		return ErrAuthentication
	case 352, 412:
		return ErrBilibiliRateLimited
	default:
		return ErrUnavailable
	}
}

// Bilibili consumes the public page's INITIAL_STATE and playinfo hydration.
// It intentionally avoids signed WBI API calls while retaining the normal
// public video, anthology, DASH, and durl playback paths.
type Bilibili struct{}

func NewBilibili() Bilibili   { return Bilibili{} }
func (Bilibili) Name() string { return "bilibili" }

func (Bilibili) Suitable(parsed *url.URL) bool {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Opaque != "" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return false
	}
	if _, ok := bilibiliUniqueQuery(parsed); !ok {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "bilibili.com" && host != "www.bilibili.com" {
		return false
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	switch parts[0] {
	case "video":
		return bilibiliID.MatchString(parts[1])
	case "festival":
		query, _ := bilibiliUniqueQuery(parsed)
		values, ok := query["bvid"]
		return ok && len(values) == 1 && bilibiliID.MatchString(values[0])
	default:
		return false
	}
}

func (Bilibili) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewBilibili().Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrTransportIsolation
	}
	id := bilibiliURLID(parsed)
	part := 0
	if raw := parsed.Query().Get("p"); raw != "" {
		part, err = strconv.Atoi(raw)
		if err != nil || part < 1 || part > 10000 {
			return Extraction{}, fmt.Errorf("%w: invalid Bilibili page", ErrInvalidMetadata)
		}
	}
	pageURL := "https://www.bilibili.com/video/" + url.PathEscape(id)
	if part > 0 {
		pageURL += "?p=" + strconv.Itoa(part)
	}
	page, err := bilibiliIsolatedReadPage(ctx, request.Transport, pageURL)
	if err != nil {
		return Extraction{}, err
	}
	return parseBilibiliPageWithPlaylistChoice(page, id, part, pageURL, request.NoPlaylist)
}

func bilibiliURLID(parsed *url.URL) string {
	path := strings.TrimSuffix(parsed.Path, "/")
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 2 {
		return ""
	}
	if parts[0] == "festival" {
		return parsed.Query().Get("bvid")
	}
	return parts[1]
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
	Code int `json:"code"`
	Data struct {
		Timelength float64              `json:"timelength"`
		DASH       bilibiliDashManifest `json:"dash"`
		DURL       []struct {
			URL  string `json:"url"`
			Size int64  `json:"size"`
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
	FrameRate   string `json:"frameRate"`
	FrameAlt    string `json:"frame_rate"`
	Size        int64  `json:"size"`
}

func parseBilibiliPage(page []byte, requestedID string, part int, webpage string) (Extraction, error) {
	return parseBilibiliPageWithPlaylistChoice(page, requestedID, part, webpage, false)
}

func parseBilibiliPageWithPlaylistChoice(page []byte, requestedID string, part int, webpage string, noPlaylist bool) (Extraction, error) {
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, ErrJSONResponseTooLarge
	}
	lower := bytes.ToLower(page)
	if bytes.Contains(lower, []byte("login")) || bytes.Contains(lower, []byte(`"truecode":-403`)) {
		return Extraction{}, ErrAuthentication
	}
	if bytes.Contains(lower, []byte("geo-restricted")) || bytes.Contains(lower, []byte(`"truecode":-404`)) {
		return Extraction{}, ErrRegionRestricted
	}
	rawState, err := extractJSONObject(page, "window.__INITIAL_STATE__")
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: missing Bilibili initial state", ErrInvalidMetadata)
	}
	var state bilibiliState
	if err := json.Unmarshal(rawState, &state); err != nil {
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
	// Bilibili anthologies use the same video URL for their playlist and first
	// child. Match the pinned _yes_playlist(video_id, video_id) choice only
	// when the URL did not explicitly select a page.
	if len(video.Pages) > 1 && part == 0 && !noPlaylist {
		entries := make([]Entry, 0, len(video.Pages))
		for _, pageInfo := range video.Pages {
			if pageInfo.Page < 1 {
				continue
			}
			entries = append(entries, Entry{
				URL: "https://www.bilibili.com/video/" + url.PathEscape(video.BVID) + "?p=" + strconv.Itoa(pageInfo.Page), ExtractorKey: "bilibili", ID: video.BVID + "_p" + strconv.Itoa(pageInfo.Page), Title: video.Title + " p" + strconv.Itoa(pageInfo.Page) + " " + pageInfo.Part, Transparent: true,
			})
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
		return Extraction{}, fmt.Errorf("%w: missing Bilibili playinfo", ErrInvalidMetadata)
	}
	var play bilibiliPlayinfo
	if err := json.Unmarshal(rawPlay, &play); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid Bilibili playinfo", ErrInvalidMetadata)
	}
	if err := bilibiliAPIError(play.Code); err != nil {
		return Extraction{}, err
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

func bilibiliInfo(state bilibiliState, video bilibiliVideoData, requested string, part int, webpage string) value.Info {
	id := video.BVID
	if id == "" {
		id = requested
	}
	if part > 0 && len(video.Pages) > 1 {
		id += "_p" + strconv.Itoa(part)
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "title", Value: value.String(video.Title)}, value.Field{Key: "description", Value: value.String(video.Desc)}, value.Field{Key: "uploader", Value: value.String(firstBilibiliString(state.UpData.Name, video.Owner.Name))}, value.Field{Key: "uploader_id", Value: value.String(firstBilibiliString(state.UpData.MID.String(), video.Owner.MID.String()))}, value.Field{Key: "webpage_url", Value: value.String(webpage)})
	setPositiveInt(info, "timestamp", video.PubDate)
	setPositiveInt(info, "view_count", video.Stat.View)
	setPositiveInt(info, "comment_count", video.Stat.Reply)
	setPositiveInt(info, "like_count", video.Stat.Like)
	setBilibiliThumbnail(info, video.Pic, bilibiliDomesticThumbnail)
	if video.Duration > 0 {
		info.Set("duration", value.Float(video.Duration))
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

func setBilibiliThumbnail(info *value.Object, raw string, role bilibiliURLRole) {
	if info == nil || !validBilibiliRoleURL(raw, role) {
		return
	}
	info.Set("thumbnail", value.String(raw))
	info.Set("thumbnails", value.List(value.ObjectValue(value.NewObject(
		value.Field{Key: "url", Value: value.String(raw)},
		value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
	))))
}

func bilibiliFormats(play bilibiliPlayinfo) []value.Value {
	out := make([]value.Value, 0)
	audio := append([]bilibiliDash(nil), play.Data.DASH.Audio...)
	audio = append(audio, play.Data.DASH.Dolby.Audio...)
	if validBilibiliRoleURL(bilibiliDashURL(play.Data.DASH.Flac.Audio), bilibiliDomesticMedia) {
		audio = append(audio, play.Data.DASH.Flac.Audio)
	}
	for _, track := range audio {
		raw := bilibiliDashURL(track)
		if !validBilibiliRoleURL(raw, bilibiliDomesticMedia) {
			continue
		}
		format := value.NewObject(value.Field{Key: "format_id", Value: value.String("dash-audio-" + strconv.FormatInt(track.ID, 10))}, value.Field{Key: "url", Value: value.String(raw)}, value.Field{Key: "ext", Value: value.String(bilibiliMIMEExt(track.MimeType, track.MimeTypeAlt, "m4a"))}, value.Field{Key: "vcodec", Value: value.String("none")}, value.Field{Key: "acodec", Value: value.String(bilibiliCodec(track.Codecs))}, value.Field{Key: "protocol", Value: value.String("https")}, value.Field{Key: "_credential_isolated", Value: value.Bool(true)})
		if track.Bandwidth > 0 {
			format.Set("abr", value.Float(float64(track.Bandwidth)/1000))
		}
		out = append(out, value.ObjectValue(format))
	}
	for _, track := range play.Data.DASH.Video {
		raw := bilibiliDashURL(track)
		if !validBilibiliRoleURL(raw, bilibiliDomesticMedia) {
			continue
		}
		format := value.NewObject(value.Field{Key: "format_id", Value: value.String("dash-video-" + strconv.FormatInt(track.ID, 10))}, value.Field{Key: "url", Value: value.String(raw)}, value.Field{Key: "ext", Value: value.String(bilibiliMIMEExt(track.MimeType, track.MimeTypeAlt, "mp4"))}, value.Field{Key: "vcodec", Value: value.String(bilibiliCodec(track.Codecs))}, value.Field{Key: "acodec", Value: value.String("none")}, value.Field{Key: "protocol", Value: value.String("https")}, value.Field{Key: "_credential_isolated", Value: value.Bool(true)})
		setPositiveInt(format, "width", track.Width)
		setPositiveInt(format, "height", track.Height)
		if track.Bandwidth > 0 {
			format.Set("tbr", value.Float(float64(track.Bandwidth)/1000))
		}
		out = append(out, value.ObjectValue(format))
	}
	for index, durl := range play.Data.DURL {
		if !validBilibiliRoleURL(durl.URL, bilibiliDomesticMedia) {
			continue
		}
		format := value.NewObject(value.Field{Key: "format_id", Value: value.String("http-" + strconv.Itoa(index+1))}, value.Field{Key: "url", Value: value.String(durl.URL)}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "protocol", Value: value.String("https")}, value.Field{Key: "_credential_isolated", Value: value.Bool(true)})
		setPositiveInt(format, "filesize", durl.Size)
		out = append(out, value.ObjectValue(format))
	}
	return out
}

func bilibiliCodec(codec string) string {
	if strings.TrimSpace(codec) == "" {
		return "unknown"
	}
	return codec
}

func bilibiliDashURL(track bilibiliDash) string {
	return firstBilibiliString(track.BaseURL, track.BaseURLAlt)
}

func bilibiliMIMEExt(primary, alternate, fallback string) string {
	mime := strings.ToLower(firstBilibiliString(primary, alternate))
	switch {
	case strings.Contains(mime, "webm"):
		return "webm"
	case strings.Contains(mime, "flac"):
		return "flac"
	case strings.Contains(mime, "audio"):
		return "m4a"
	default:
		return fallback
	}
}

func firstBilibiliString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// BilibiliBangumi extracts a public, non-premium episode through the unsigned
// PGC playurl endpoint. WBI signing is intentionally not used here.
type BilibiliBangumi struct{}

func NewBilibiliBangumi() BilibiliBangumi { return BilibiliBangumi{} }
func (BilibiliBangumi) Name() string      { return "bilibili_bangumi" }

var bilibiliBangumiEPPattern = regexp.MustCompile(`^/bangumi/play/ep([0-9]{1,32})$`)

func (BilibiliBangumi) Suitable(parsed *url.URL) bool {
	return bilibiliStrictURL(parsed, "bilibili.com", "www.bilibili.com") && bilibiliBangumiEPPattern.MatchString(parsed.Path)
}

type bilibiliBangumiVideoInfo struct {
	AID      int64   `json:"aid"`
	BVID     string  `json:"bvid"`
	CID      int64   `json:"cid"`
	Title    string  `json:"title"`
	Cover    string  `json:"cover"`
	Duration float64 `json:"duration"`
	PubDate  int64   `json:"pubdate"`
}

type bilibiliBangumiState struct {
	VideoInfo bilibiliBangumiVideoInfo `json:"videoInfo"`
	EpInfo    struct {
		AID        int64  `json:"aid"`
		BVID       string `json:"bvid"`
		CID        int64  `json:"cid"`
		Title      string `json:"title"`
		LongTitle  string `json:"long_title"`
		ShareTitle string `json:"share_title"`
		Cover      string `json:"cover"`
	} `json:"epInfo"`
	SeasonInfo struct {
		SeasonID int64  `json:"season_id"`
		Title    string `json:"title"`
		Evaluate string `json:"evaluate"`
		Cover    string `json:"cover"`
	} `json:"seasonInfo"`
}

func (BilibiliBangumi) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewBilibiliBangumi().Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	page, err := bilibiliIsolatedReadPage(ctx, request.Transport, request.URL)
	if err != nil {
		return Extraction{}, err
	}
	if bytes.Contains(page, []byte("您所在的地区无法观看本片")) || bytes.Contains(page, []byte("AreaLimitPanel")) {
		return Extraction{}, ErrRegionRestricted
	}
	if bytes.Contains(page, []byte("正在观看预览，大会员免费看全片")) || bytes.Contains(page, []byte("开通大会员观看")) {
		return Extraction{}, ErrAuthentication
	}
	raw, err := extractJSONObject(page, "window.__INITIAL_STATE__")
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: missing bangumi state", ErrInvalidMetadata)
	}
	var state bilibiliBangumiState
	if err := json.Unmarshal(raw, &state); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid bangumi state", ErrInvalidMetadata)
	}
	video := state.VideoInfo
	if video.AID == 0 {
		video.AID, video.BVID, video.CID, video.Title, video.Cover = state.EpInfo.AID, state.EpInfo.BVID, state.EpInfo.CID, firstBilibiliString(state.EpInfo.Title, state.EpInfo.LongTitle, state.EpInfo.ShareTitle), state.EpInfo.Cover
	}
	if video.AID == 0 || video.CID == 0 || firstBilibiliString(video.Title, state.EpInfo.Title, state.EpInfo.LongTitle) == "" {
		return Extraction{}, fmt.Errorf("%w: incomplete bangumi episode", ErrInvalidMetadata)
	}
	play, err := fetchBilibiliBangumiPlayURL(ctx, request.Transport, parsed.Path[len("/bangumi/play/ep"):])
	if err != nil {
		return Extraction{}, err
	}
	formats := bilibiliFormats(play)
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: bangumi episode has no public formats", ErrUnavailable)
	}
	id := state.EpInfo.BVID
	if id == "" {
		id = video.BVID
	}
	if id == "" {
		id = "av" + strconv.FormatInt(video.AID, 10)
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "title", Value: value.String(firstBilibiliString(video.Title, state.EpInfo.Title, state.EpInfo.LongTitle))}, value.Field{Key: "webpage_url", Value: value.String(request.URL)}, value.Field{Key: "extractor", Value: value.String("bilibili_bangumi")}, value.Field{Key: "extractor_key", Value: value.String("BilibiliBangumi")}, value.Field{Key: "formats", Value: value.List(formats...)}))
	if video.Duration > 0 {
		info.Set("duration", value.Float(video.Duration))
	}
	setBilibiliThumbnail(info.Fields(), video.Cover, bilibiliDomesticThumbnail)
	if state.SeasonInfo.Title != "" {
		info.Set("series", value.String(state.SeasonInfo.Title))
		info.Set("series_id", value.String(strconv.FormatInt(state.SeasonInfo.SeasonID, 10)))
	}
	if state.SeasonInfo.Evaluate != "" {
		info.Set("description", value.String(state.SeasonInfo.Evaluate))
	}
	return Media(info), nil
}

type bilibiliPGCPlayResponse struct {
	Code int `json:"code"`
	Data struct {
		Result struct {
			VideoInfo bilibiliPlayinfoData `json:"video_info"`
		} `json:"result"`
		VideoInfo bilibiliPlayinfoData `json:"video_info"`
	} `json:"data"`
	Result struct {
		VideoInfo bilibiliPlayinfoData `json:"video_info"`
	} `json:"result"`
	Raw struct {
		Data struct {
			VideoInfo bilibiliPlayinfoData `json:"video_info"`
		} `json:"data"`
	} `json:"raw"`
}

type bilibiliPlayinfoData struct {
	Timelength float64              `json:"timelength"`
	Dash       bilibiliDashManifest `json:"dash"`
	DURL       []struct {
		URL  string `json:"url"`
		Size int64  `json:"size"`
	} `json:"durl"`
}

type bilibiliDashManifest struct {
	Video []bilibiliDash `json:"video"`
	Audio []bilibiliDash `json:"audio"`
	Dolby struct {
		Audio []bilibiliDash `json:"audio"`
	} `json:"dolby"`
	Flac struct {
		Audio bilibiliDash `json:"audio"`
	} `json:"flac"`
}

func fetchBilibiliBangumiPlayURL(ctx context.Context, transport Transport, episodeID string) (bilibiliPlayinfo, error) {
	endpoint := "https://api.bilibili.com/pgc/player/web/v2/playurl?ep_id=" + episodeID + "&fnval=12240"
	var response bilibiliPGCPlayResponse
	headers := make(http.Header)
	headers.Set("Referer", bilibiliDomesticReferer)
	if err := bilibiliIsolatedRequestJSONWithReferer(ctx, transport, http.MethodGet, endpoint, nil, headers, &response); err != nil {
		return bilibiliPlayinfo{}, err
	}
	if err := bilibiliAPIError(response.Code); err != nil {
		return bilibiliPlayinfo{}, err
	}
	data := response.Data.Result.VideoInfo
	if len(data.Dash.Video) == 0 && len(data.Dash.Audio) == 0 && len(data.DURL) == 0 {
		data = response.Data.VideoInfo
	}
	if len(data.Dash.Video) == 0 && len(data.Dash.Audio) == 0 && len(data.DURL) == 0 {
		data = response.Result.VideoInfo
	}
	if len(data.Dash.Video) == 0 && len(data.Dash.Audio) == 0 && len(data.DURL) == 0 {
		data = response.Raw.Data.VideoInfo
	}
	var play bilibiliPlayinfo
	play.Code = 0
	play.Data.Timelength = data.Timelength
	play.Data.DASH = data.Dash
	play.Data.DURL = data.DURL
	return play, nil
}

type bilibiliBangumiEntry struct {
	ID         int64   `json:"id"`
	AID        int64   `json:"aid"`
	BVID       string  `json:"bvid"`
	CID        int64   `json:"cid"`
	Title      string  `json:"title"`
	LongTitle  string  `json:"long_title"`
	IndexTitle string  `json:"index_title"`
	Cover      string  `json:"cover"`
	Duration   float64 `json:"duration"`
}

func bilibiliBangumiEntries(raw []bilibiliBangumiEntry) []Entry {
	entries := make([]Entry, 0, len(raw))
	for _, episode := range raw {
		if episode.ID == 0 || episode.CID == 0 {
			continue
		}
		id := episode.BVID
		if id == "" && episode.AID != 0 {
			id = "av" + strconv.FormatInt(episode.AID, 10)
		}
		entry := Entry{URL: "https://www.bilibili.com/bangumi/play/ep" + strconv.FormatInt(episode.ID, 10), ExtractorKey: "bilibili_bangumi", ID: strconv.FormatInt(episode.ID, 10), Title: firstBilibiliString(episode.LongTitle, episode.Title, episode.IndexTitle), Duration: episode.Duration, HasDuration: episode.Duration > 0}
		if validBilibiliRoleURL(episode.Cover, bilibiliDomesticThumbnail) {
			entry.Thumbnail = episode.Cover
		}
		entries = append(entries, entry)
	}
	return entries
}

func bilibiliBangumiPlaylist(ctx context.Context, request Request, seasonID int64, title, cover, description string) (Extraction, error) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(strconv.FormatInt(seasonID, 10))}, value.Field{Key: "title", Value: value.String(title)}, value.Field{Key: "webpage_url", Value: value.String(request.URL)}, value.Field{Key: "extractor", Value: value.String("bilibili_bangumi_season")}, value.Field{Key: "extractor_key", Value: value.String("BilibiliBangumiSeason")}))
	setBilibiliThumbnail(info.Fields(), cover, bilibiliDomesticThumbnail)
	if description != "" {
		info.Set("description", value.String(description))
	}
	sequence, err := LazyFirstPageEntries(5000, func(ctx context.Context) ([]Entry, error) {
		endpoint := "https://api.bilibili.com/pgc/web/season/section?season_id=" + strconv.FormatInt(seasonID, 10)
		var response struct {
			Code   int `json:"code"`
			Result struct {
				MainSection struct {
					Episodes []bilibiliBangumiEntry `json:"episodes"`
				} `json:"main_section"`
				Main struct {
					Episodes []bilibiliBangumiEntry `json:"episodes"`
				} `json:"main"`
			} `json:"result"`
		}
		if err := bilibiliIsolatedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, nil, &response); err != nil {
			return nil, err
		}
		if err := bilibiliAPIError(response.Code); err != nil {
			return nil, err
		}
		episodes := response.Result.MainSection.Episodes
		if len(episodes) == 0 {
			episodes = response.Result.Main.Episodes
		}
		return bilibiliBangumiEntries(episodes), nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

type BilibiliBangumiMedia struct{}

func NewBilibiliBangumiMedia() BilibiliBangumiMedia { return BilibiliBangumiMedia{} }
func (BilibiliBangumiMedia) Name() string           { return "bilibili_bangumi_media" }

var bilibiliBangumiMediaPattern = regexp.MustCompile(`^/bangumi/media/md([0-9]{1,32})$`)

func (BilibiliBangumiMedia) Suitable(parsed *url.URL) bool {
	return bilibiliStrictURL(parsed, "bilibili.com", "www.bilibili.com") && bilibiliBangumiMediaPattern.MatchString(parsed.Path)
}

func (BilibiliBangumiMedia) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewBilibiliBangumiMedia().Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	page, err := bilibiliIsolatedReadPage(ctx, request.Transport, request.URL)
	if err != nil {
		return Extraction{}, err
	}
	raw, err := extractJSONObject(page, "window.__INITIAL_STATE__")
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: missing bangumi media state", ErrInvalidMetadata)
	}
	var state struct {
		MediaInfo struct {
			SeasonID int64  `json:"season_id"`
			Title    string `json:"title"`
			Cover    string `json:"cover"`
			Evaluate string `json:"evaluate"`
		} `json:"mediaInfo"`
	}
	if err := json.Unmarshal(raw, &state); err != nil || state.MediaInfo.SeasonID == 0 {
		return Extraction{}, fmt.Errorf("%w: invalid bangumi media state", ErrInvalidMetadata)
	}
	return bilibiliBangumiPlaylist(ctx, request, state.MediaInfo.SeasonID, state.MediaInfo.Title, state.MediaInfo.Cover, state.MediaInfo.Evaluate)
}

type BilibiliBangumiSeason struct{}

func NewBilibiliBangumiSeason() BilibiliBangumiSeason { return BilibiliBangumiSeason{} }
func (BilibiliBangumiSeason) Name() string            { return "bilibili_bangumi_season" }

var bilibiliBangumiSeasonPattern = regexp.MustCompile(`^/bangumi/play/ss([0-9]{1,32})$`)

func (BilibiliBangumiSeason) Suitable(parsed *url.URL) bool {
	return bilibiliStrictURL(parsed, "bilibili.com", "www.bilibili.com") && bilibiliBangumiSeasonPattern.MatchString(parsed.Path)
}

func (BilibiliBangumiSeason) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewBilibiliBangumiSeason().Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	page, err := bilibiliIsolatedReadPage(ctx, request.Transport, request.URL)
	if err != nil {
		return Extraction{}, err
	}
	matches := bilibiliBangumiSeasonPattern.FindStringSubmatch(parsed.Path)
	seasonID, _ := strconv.ParseInt(matches[1], 10, 64)
	title := ""
	if raw, stateErr := extractJSONObject(page, "window.__INITIAL_STATE__"); stateErr == nil {
		var state struct {
			MediaInfo struct {
				Title string `json:"title"`
			} `json:"mediaInfo"`
		}
		if json.Unmarshal(raw, &state) == nil {
			title = state.MediaInfo.Title
		}
	}
	return bilibiliBangumiPlaylist(ctx, request, seasonID, title, "", "")
}

// bilibiliPageSequence is a lazy, reusable page-list implementation. Each
// iterator owns its page counter and seen-entry set; a repeated row or a
// server that claims more rows without progress fails closed.
type bilibiliPageSequence struct {
	fetch func(context.Context, int) ([]Entry, int, bool, error)
}

func (sequence bilibiliPageSequence) Iterator() EntryIterator {
	return &bilibiliPageIterator{fetch: sequence.fetch, page: 1, seen: make(map[string]bool)}
}

type bilibiliPageIterator struct {
	fetch       func(context.Context, int) ([]Entry, int, bool, error)
	page        int
	entries     []Entry
	index       int
	seen        map[string]bool
	seenEntries int
	done        bool
	pages       int
}

func (iterator *bilibiliPageIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := contextError(ctx); err != nil {
		iterator.done = true
		return Entry{}, false, err
	}
	for iterator.index >= len(iterator.entries) && !iterator.done {
		if iterator.pages >= bilibiliListMaxPages {
			iterator.done = true
			return Entry{}, false, ErrPlaylistLimit
		}
		entries, total, hasMore, err := iterator.fetch(ctx, iterator.page)
		if err != nil {
			iterator.done = true
			return Entry{}, false, err
		}
		iterator.pages++
		for _, entry := range entries {
			keys := []string{entry.ID}
			if entry.URL != entry.ID {
				keys = append(keys, entry.URL)
			}
			for _, key := range keys {
				if iterator.seen[key] {
					iterator.done = true
					return Entry{}, false, fmt.Errorf("%w: repeated Bilibili playlist entry", ErrInvalidPlaylist)
				}
				iterator.seen[key] = true
			}
			iterator.seenEntries++
		}
		iterator.entries, iterator.index = entries, 0
		if total > 0 {
			iterator.done = iterator.seenEntries >= total
		} else {
			iterator.done = !hasMore
		}
		if len(entries) == 0 && !iterator.done {
			iterator.page++
			continue
		}
		if !iterator.done {
			iterator.page++
		}
	}
	if iterator.index >= len(iterator.entries) {
		return Entry{}, false, nil
	}
	entry := iterator.entries[iterator.index]
	iterator.index++
	return entry, true, nil
}

func bilibiliVideoEntry(id, title, thumbnail string, duration float64, viewCount int64) Entry {
	entry := Entry{URL: "https://www.bilibili.com/video/" + id, ExtractorKey: "bilibili", ID: id, Title: title, Duration: duration, HasDuration: duration > 0, ViewCount: viewCount, HasViewCount: viewCount > 0}
	if validBilibiliRoleURL(thumbnail, bilibiliDomesticThumbnail) {
		entry.Thumbnail = thumbnail
	}
	return entry
}

type BilibiliCollectionList struct{}

func NewBilibiliCollectionList() BilibiliCollectionList { return BilibiliCollectionList{} }
func (BilibiliCollectionList) Name() string             { return "bilibili_collection" }

var bilibiliCollectionPath = regexp.MustCompile(`^/([0-9]{1,32})/lists/([0-9]{1,32})$`)

func bilibiliSpaceListTarget(parsed *url.URL, wantSeries bool) (string, string, bool) {
	if !bilibiliStrictURL(parsed, "space.bilibili.com") {
		return "", "", false
	}
	query, _ := bilibiliUniqueQuery(parsed)
	pathMatches := bilibiliCollectionPath.FindStringSubmatch(parsed.Path)
	if len(pathMatches) == 3 {
		typeValue := ""
		if values := query["type"]; len(values) == 1 {
			typeValue = values[0]
		}
		if typeValue != "" && typeValue != "series" && typeValue != "season" {
			return "", "", false
		}
		if wantSeries != (typeValue == "series") {
			return "", "", false
		}
		return pathMatches[1], pathMatches[2], true
	}
	var pattern *regexp.Regexp
	if wantSeries {
		pattern = regexp.MustCompile(`^/([0-9]{1,32})/channel/seriesdetail$`)
	} else {
		pattern = regexp.MustCompile(`^/([0-9]{1,32})/channel/collectiondetail$`)
	}
	matches := pattern.FindStringSubmatch(parsed.Path)
	values := query["sid"]
	if len(matches) != 2 || len(values) != 1 || !bilibiliNumericID.MatchString(values[0]) {
		return "", "", false
	}
	return matches[1], values[0], true
}

func (BilibiliCollectionList) Suitable(parsed *url.URL) bool {
	_, _, ok := bilibiliSpaceListTarget(parsed, false)
	return ok
}

func (BilibiliCollectionList) Extract(ctx context.Context, request Request) (Extraction, error) {
	return extractBilibiliSpaceList(ctx, request, false)
}

type BilibiliSeriesList struct{}

func NewBilibiliSeriesList() BilibiliSeriesList { return BilibiliSeriesList{} }
func (BilibiliSeriesList) Name() string         { return "bilibili_series" }
func (BilibiliSeriesList) Suitable(parsed *url.URL) bool {
	_, _, ok := bilibiliSpaceListTarget(parsed, true)
	return ok
}
func (BilibiliSeriesList) Extract(ctx context.Context, request Request) (Extraction, error) {
	return extractBilibiliSpaceList(ctx, request, true)
}

type bilibiliSpaceMeta struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Cover       string          `json:"cover"`
	CoverURL    string          `json:"cover_url"`
	Uploader    string          `json:"uploader"`
	UName       string          `json:"uname"`
	MID         json.RawMessage `json:"mid"`
	PTime       json.RawMessage `json:"ptime"`
	CTime       json.RawMessage `json:"ctime"`
	MTime       json.RawMessage `json:"mtime"`
	Upper       struct {
		Name string `json:"name"`
	} `json:"upper"`
}

type bilibiliSpaceListResponse struct {
	Code int `json:"code"`
	Data struct {
		Meta     bilibiliSpaceMeta `json:"meta"`
		Archives []struct {
			AID      int64   `json:"aid"`
			BVID     string  `json:"bvid"`
			Title    string  `json:"title"`
			Pic      string  `json:"pic"`
			Duration float64 `json:"duration"`
			Stat     struct {
				View int64 `json:"view"`
			} `json:"stat"`
		} `json:"archives"`
		Page struct {
			Total    int `json:"total"`
			Size     int `json:"size"`
			PageSize int `json:"page_size"`
		} `json:"page"`
	} `json:"data"`
}

type bilibiliSpacePage struct {
	entries []Entry
	meta    bilibiliSpaceMeta
	total   int
	hasMore bool
}

func bilibiliJSONInt(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		if parsed, err := strconv.ParseInt(string(number), 10, 64); err == nil {
			return parsed
		}
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		parsed, _ := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		return parsed
	}
	return 0
}

func mergeBilibiliSpaceMeta(primary, fallback bilibiliSpaceMeta) bilibiliSpaceMeta {
	if primary.Name == "" {
		primary.Name = fallback.Name
	}
	if primary.Description == "" {
		primary.Description = fallback.Description
	}
	if primary.Cover == "" {
		primary.Cover = fallback.Cover
	}
	if primary.CoverURL == "" {
		primary.CoverURL = fallback.CoverURL
	}
	if primary.Uploader == "" {
		primary.Uploader = fallback.Uploader
	}
	if primary.UName == "" {
		primary.UName = fallback.UName
	}
	if len(primary.MID) == 0 {
		primary.MID = fallback.MID
	}
	if len(primary.PTime) == 0 {
		primary.PTime = fallback.PTime
	}
	if len(primary.CTime) == 0 {
		primary.CTime = fallback.CTime
	}
	if len(primary.MTime) == 0 {
		primary.MTime = fallback.MTime
	}
	if primary.Upper.Name == "" {
		primary.Upper.Name = fallback.Upper.Name
	}
	return primary
}

func applyBilibiliSpaceMeta(info *value.Info, meta bilibiliSpaceMeta) error {
	if info == nil || meta.Name == "" {
		return fmt.Errorf("%w: missing Bilibili playlist title", ErrInvalidMetadata)
	}
	info.Set("title", value.String(meta.Name))
	if meta.Description != "" {
		info.Set("description", value.String(meta.Description))
	}
	if uploaderID := bilibiliJSONInt(meta.MID); uploaderID > 0 {
		info.Set("uploader_id", value.String(strconv.FormatInt(uploaderID, 10)))
	}
	if uploader := firstBilibiliString(meta.Uploader, meta.UName, meta.Upper.Name); uploader != "" {
		info.Set("uploader", value.String(uploader))
	}
	if timestamp := firstPositiveBilibiliInt(bilibiliJSONInt(meta.PTime), bilibiliJSONInt(meta.CTime)); timestamp > 0 {
		info.Set("timestamp", value.Int(timestamp))
	}
	if modified := bilibiliJSONInt(meta.MTime); modified > 0 {
		info.Set("modified_timestamp", value.Int(modified))
	}
	setBilibiliThumbnail(info.Fields(), firstBilibiliString(meta.Cover, meta.CoverURL), bilibiliDomesticThumbnail)
	return nil
}

func firstPositiveBilibiliInt(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func extractBilibiliSpaceList(ctx context.Context, request Request, series bool) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	mid, sid, ok := bilibiliSpaceListTarget(parsed, series)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	referer := request.URL
	headers := make(http.Header)
	headers.Set("Referer", referer)
	name := "collection"
	if series {
		name = "series"
	}
	fetchPage := func(ctx context.Context, page int) (bilibiliSpacePage, error) {
		var endpoint string
		if series {
			endpoint = fmt.Sprintf("https://api.bilibili.com/x/series/archives?mid=%s&series_id=%s&pn=%d&ps=%d", mid, sid, page, bilibiliListPageSize)
		} else {
			endpoint = fmt.Sprintf("https://api.bilibili.com/x/polymer/web-space/seasons_archives_list?mid=%s&season_id=%s&page_num=%d&page_size=%d", mid, sid, page, bilibiliListPageSize)
		}
		var response bilibiliSpaceListResponse
		if err := bilibiliIsolatedRequestJSONWithReferer(ctx, request.Transport, http.MethodGet, endpoint, nil, headers, &response); err != nil {
			return bilibiliSpacePage{}, err
		}
		if err := bilibiliAPIError(response.Code); err != nil {
			return bilibiliSpacePage{}, err
		}
		entries := make([]Entry, 0, len(response.Data.Archives))
		for _, archive := range response.Data.Archives {
			id := archive.BVID
			if id == "" && archive.AID != 0 {
				id = "av" + strconv.FormatInt(archive.AID, 10)
			}
			if bilibiliID.MatchString(id) {
				entries = append(entries, bilibiliVideoEntry(id, archive.Title, archive.Pic, archive.Duration, archive.Stat.View))
			}
		}
		size := response.Data.Page.Size
		if size == 0 {
			size = response.Data.Page.PageSize
		}
		if size == 0 {
			size = bilibiliListPageSize
		}
		total := response.Data.Page.Total
		hasMore := total == 0 && len(entries) >= size
		return bilibiliSpacePage{entries: entries, meta: response.Data.Meta, total: total, hasMore: hasMore}, nil
	}
	metadata := bilibiliSpaceMeta{}
	if series {
		var response struct {
			Code int `json:"code"`
			Data struct {
				Meta bilibiliSpaceMeta `json:"meta"`
			} `json:"data"`
		}
		endpoint := "https://api.bilibili.com/x/series/series?series_id=" + sid
		if err := bilibiliIsolatedRequestJSONWithReferer(ctx, request.Transport, http.MethodGet, endpoint, nil, headers, &response); err != nil {
			return Extraction{}, err
		}
		if err := bilibiliAPIError(response.Code); err != nil {
			return Extraction{}, err
		}
		metadata = response.Data.Meta
	}
	firstPage, err := fetchPage(ctx, 1)
	if err != nil {
		return Extraction{}, err
	}
	metadata = mergeBilibiliSpaceMeta(metadata, firstPage.meta)
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(mid + "_" + sid)}, value.Field{Key: "webpage_url", Value: value.String(request.URL)}, value.Field{Key: "extractor", Value: value.String("bilibili_" + name)}, value.Field{Key: "extractor_key", Value: value.String(map[bool]string{false: "BilibiliCollectionList", true: "BilibiliSeriesList"}[series])}))
	if err := applyBilibiliSpaceMeta(&info, metadata); err != nil {
		return Extraction{}, err
	}
	sequence := bilibiliPageSequence{fetch: func(ctx context.Context, page int) ([]Entry, int, bool, error) {
		if page == 1 {
			return firstPage.entries, firstPage.total, firstPage.hasMore, nil
		}
		pageData, err := fetchPage(ctx, page)
		if err != nil {
			return nil, 0, false, err
		}
		return pageData.entries, pageData.total, pageData.hasMore, nil
	}}
	return Playlist(info, sequence)
}

type BilibiliCategory struct{}

func NewBilibiliCategory() BilibiliCategory { return BilibiliCategory{} }
func (BilibiliCategory) Name() string       { return "bilibili_category" }

var bilibiliCategoryPath = regexp.MustCompile(`^/v/([a-z]+)/([a-z]+)$`)
var bilibiliCategoryRID = map[string]int{"kichiku/mad": 26, "kichiku/manual_vocaloid": 126, "kichiku/guide": 22, "kichiku/theatre": 216, "kichiku/course": 127}

func (BilibiliCategory) Suitable(parsed *url.URL) bool {
	if !bilibiliStrictURL(parsed, "bilibili.com", "www.bilibili.com") {
		return false
	}
	matches := bilibiliCategoryPath.FindStringSubmatch(parsed.Path)
	if len(matches) != 3 {
		return false
	}
	_, ok := bilibiliCategoryRID[matches[1]+"/"+matches[2]]
	return ok
}

func (BilibiliCategory) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewBilibiliCategory().Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	matches := bilibiliCategoryPath.FindStringSubmatch(parsed.Path)
	category := matches[1] + "/" + matches[2]
	rid := bilibiliCategoryRID[category]
	searchKey := matches[1] + ": " + matches[2]
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(category)}, value.Field{Key: "title", Value: value.String(category)}, value.Field{Key: "webpage_url", Value: value.String(request.URL)}, value.Field{Key: "extractor", Value: value.String("bilibili_category")}, value.Field{Key: "extractor_key", Value: value.String("BilibiliCategory")}))
	sequence := bilibiliPageSequence{fetch: func(ctx context.Context, page int) ([]Entry, int, bool, error) {
		endpoint := fmt.Sprintf("https://api.bilibili.com/x/web-interface/newlist?rid=%d&type=1&ps=20&jsonp=jsonp&Search_key=%s&pn=%d", rid, url.QueryEscape(searchKey), page)
		var response struct {
			Code int `json:"code"`
			Data struct {
				Page struct {
					Count int `json:"count"`
					Size  int `json:"size"`
				} `json:"page"`
				Archives []struct {
					AID      int64   `json:"aid"`
					BVID     string  `json:"bvid"`
					Title    string  `json:"title"`
					Pic      string  `json:"pic"`
					Duration float64 `json:"duration"`
					Stat     struct {
						View int64 `json:"view"`
					} `json:"stat"`
				} `json:"archives"`
			} `json:"data"`
		}
		if err := bilibiliIsolatedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, nil, &response); err != nil {
			return nil, 0, false, err
		}
		if err := bilibiliAPIError(response.Code); err != nil {
			return nil, 0, false, err
		}
		entries := make([]Entry, 0, len(response.Data.Archives))
		for _, archive := range response.Data.Archives {
			id := archive.BVID
			if id == "" && archive.AID != 0 {
				id = "av" + strconv.FormatInt(archive.AID, 10)
			}
			if bilibiliID.MatchString(id) {
				entries = append(entries, bilibiliVideoEntry(id, archive.Title, archive.Pic, archive.Duration, archive.Stat.View))
			}
		}
		total := response.Data.Page.Count
		size := response.Data.Page.Size
		if size == 0 {
			size = bilibiliListPageSize
		}
		return entries, total, total == 0 && len(entries) >= size, nil
	}}
	return Playlist(info, sequence)
}

type BilibiliAudio struct{}

func NewBilibiliAudio() BilibiliAudio { return BilibiliAudio{} }
func (BilibiliAudio) Name() string    { return "bilibili_audio" }

var bilibiliAudioPath = regexp.MustCompile(`^/audio/au([0-9]{1,32})$`)

func (BilibiliAudio) Suitable(parsed *url.URL) bool {
	return bilibiliStrictURL(parsed, "bilibili.com", "www.bilibili.com") && bilibiliAudioPath.MatchString(parsed.Path)
}

type bilibiliAudioSong struct {
	Title     string `json:"title"`
	Author    string `json:"author"`
	Cover     string `json:"cover"`
	Duration  int64  `json:"duration"`
	Intro     string `json:"intro"`
	Lyric     string `json:"lyric"`
	Statistic struct {
		Play    int64 `json:"play"`
		Comment int64 `json:"comment"`
	} `json:"statistic"`
}

func (BilibiliAudio) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewBilibiliAudio().Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	id := bilibiliAudioPath.FindStringSubmatch(parsed.Path)[1]
	var urlResponse struct {
		Code int `json:"code"`
		Data struct {
			CDNs []string `json:"cdns"`
			Size int64    `json:"size"`
		} `json:"data"`
	}
	if err := bilibiliIsolatedRequestJSON(ctx, request.Transport, http.MethodGet, "https://www.bilibili.com/audio/music-service-c/web/url?sid="+id, nil, nil, &urlResponse); err != nil {
		return Extraction{}, err
	}
	if err := bilibiliAPIError(urlResponse.Code); err != nil {
		return Extraction{}, err
	}
	var songResponse struct {
		Code int               `json:"code"`
		Data bilibiliAudioSong `json:"data"`
	}
	if err := bilibiliIsolatedRequestJSON(ctx, request.Transport, http.MethodGet, "https://www.bilibili.com/audio/music-service-c/web/song/info?sid="+id, nil, nil, &songResponse); err != nil {
		return Extraction{}, err
	}
	if err := bilibiliAPIError(songResponse.Code); err != nil {
		return Extraction{}, err
	}
	formats := make([]value.Value, 0, len(urlResponse.Data.CDNs))
	audioReferer := "https://www.bilibili.com/audio/au" + id
	for index, raw := range urlResponse.Data.CDNs {
		if !validBilibiliRoleURL(raw, bilibiliDomesticMedia) {
			continue
		}
		format := value.NewObject(value.Field{Key: "format_id", Value: value.String("audio-" + strconv.Itoa(index))}, value.Field{Key: "url", Value: value.String(raw)}, value.Field{Key: "ext", Value: value.String("m4a")}, value.Field{Key: "protocol", Value: value.String("https")}, value.Field{Key: "vcodec", Value: value.String("none")}, value.Field{Key: "acodec", Value: value.String("unknown")}, value.Field{Key: "_credential_isolated", Value: value.Bool(true)}, value.Field{Key: "_credential_isolated_referer", Value: value.String(audioReferer)})
		setPositiveInt(format, "filesize", urlResponse.Data.Size)
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 || songResponse.Data.Title == "" {
		return Extraction{}, fmt.Errorf("%w: incomplete Bilibili audio", ErrInvalidMetadata)
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "title", Value: value.String(songResponse.Data.Title)}, value.Field{Key: "formats", Value: value.List(formats...)}, value.Field{Key: "webpage_url", Value: value.String(request.URL)}, value.Field{Key: "extractor", Value: value.String("bilibili_audio")}, value.Field{Key: "extractor_key", Value: value.String("BilibiliAudio")}))
	if songResponse.Data.Duration > 0 {
		info.Set("duration", value.Float(float64(songResponse.Data.Duration)))
	}
	if songResponse.Data.Author != "" {
		info.Set("uploader", value.String(songResponse.Data.Author))
	}
	if songResponse.Data.Intro != "" {
		info.Set("description", value.String(songResponse.Data.Intro))
	}
	setBilibiliThumbnail(info.Fields(), songResponse.Data.Cover, bilibiliDomesticThumbnail)
	setPositiveInt(info.Fields(), "view_count", songResponse.Data.Statistic.Play)
	return Media(info), nil
}

type BilibiliAudioAlbum struct{}

func NewBilibiliAudioAlbum() BilibiliAudioAlbum { return BilibiliAudioAlbum{} }
func (BilibiliAudioAlbum) Name() string         { return "bilibili_audio_album" }

var bilibiliAudioAlbumPath = regexp.MustCompile(`^/audio/am([0-9]{1,32})$`)

func (BilibiliAudioAlbum) Suitable(parsed *url.URL) bool {
	return bilibiliStrictURL(parsed, "bilibili.com", "www.bilibili.com") && bilibiliAudioAlbumPath.MatchString(parsed.Path)
}

func (BilibiliAudioAlbum) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewBilibiliAudioAlbum().Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	id := bilibiliAudioAlbumPath.FindStringSubmatch(parsed.Path)[1]
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "title", Value: value.String("album " + id)}, value.Field{Key: "webpage_url", Value: value.String(request.URL)}, value.Field{Key: "extractor", Value: value.String("bilibili_audio_album")}, value.Field{Key: "extractor_key", Value: value.String("BilibiliAudioAlbum")}))
	sequence, err := LazyFirstPageEntries(100, func(ctx context.Context) ([]Entry, error) {
		endpoint := "https://www.bilibili.com/audio/music-service-c/web/song/of-menu?sid=" + id + "&pn=1&ps=100"
		var response struct {
			Code int `json:"code"`
			Data struct {
				Songs []struct {
					ID    int64  `json:"id"`
					Title string `json:"title"`
				} `json:"data"`
			} `json:"data"`
		}
		if err := bilibiliIsolatedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, nil, &response); err != nil {
			return nil, err
		}
		if err := bilibiliAPIError(response.Code); err != nil {
			return nil, err
		}
		entries := make([]Entry, 0, len(response.Data.Songs))
		for _, song := range response.Data.Songs {
			if song.ID > 0 {
				entries = append(entries, Entry{URL: "https://www.bilibili.com/audio/au" + strconv.FormatInt(song.ID, 10), ExtractorKey: "bilibili_audio", ID: strconv.FormatInt(song.ID, 10), Title: song.Title})
			}
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

type BilibiliPlayer struct{}

func NewBilibiliPlayer() BilibiliPlayer { return BilibiliPlayer{} }
func (BilibiliPlayer) Name() string     { return "bilibili_player" }

func (BilibiliPlayer) Suitable(parsed *url.URL) bool {
	if !bilibiliStrictURL(parsed, "player.bilibili.com") || !bilibiliExactPath(parsed, "/player.html", false) {
		return false
	}
	query, _ := bilibiliUniqueQuery(parsed)
	for key := range query {
		if key != "aid" && key != "cid" && key != "page" {
			return false
		}
	}
	values := query["aid"]
	return len(values) == 1 && bilibiliNumericID.MatchString(values[0])
}

func (BilibiliPlayer) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewBilibiliPlayer().Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	aid := parsed.Query().Get("aid")
	return URLResult(Entry{URL: "https://www.bilibili.com/video/av" + aid, ExtractorKey: "bilibili", ID: "av" + aid, Transparent: true})
}

type BilibiliDynamic struct{}

func NewBilibiliDynamic() BilibiliDynamic { return BilibiliDynamic{} }
func (BilibiliDynamic) Name() string      { return "bilibili_dynamic" }

var bilibiliDynamicTPath = regexp.MustCompile(`^/[0-9]{1,32}$`)
var bilibiliDynamicOpusPath = regexp.MustCompile(`^/opus/[0-9]{1,32}$`)

type bilibiliDynamicJump struct {
	JumpURL string `json:"jump_url"`
}

type bilibiliDynamicModule struct {
	Major struct {
		Archive *bilibiliDynamicJump `json:"archive"`
		PGC     *bilibiliDynamicJump `json:"pgc"`
	} `json:"major"`
	Additional struct {
		Reserve *bilibiliDynamicJump `json:"reserve"`
		Common  *bilibiliDynamicJump `json:"common"`
	} `json:"additional"`
}

type bilibiliDynamicItem struct {
	Modules bilibiliDynamicModules `json:"modules"`
	Orig    *bilibiliDynamicItem   `json:"orig"`
}

type bilibiliDynamicModules struct {
	ModuleDynamic bilibiliDynamicModule `json:"module_dynamic"`
}

type bilibiliDynamicResponse struct {
	Code int `json:"code"`
	Data struct {
		Item bilibiliDynamicItem `json:"item"`
	} `json:"data"`
}

func bilibiliDynamicJumpURLs(item bilibiliDynamicItem) []string {
	items := []bilibiliDynamicItem{item}
	if item.Orig != nil {
		items = append(items, *item.Orig)
	}
	urls := make([]string, 0, len(items)*4)
	for _, candidate := range items {
		module := candidate.Modules.ModuleDynamic
		for _, jump := range []*bilibiliDynamicJump{
			module.Major.Archive,
			module.Major.PGC,
			module.Additional.Reserve,
			module.Additional.Common,
		} {
			if jump != nil && jump.JumpURL != "" {
				urls = append(urls, jump.JumpURL)
			}
		}
	}
	return urls
}

func bilibiliDynamicVideoChild(rawURL string) (*url.URL, bool) {
	if strings.HasPrefix(rawURL, "//") {
		rawURL = "https:" + rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || !NewBilibili().Suitable(parsed) {
		return nil, false
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	if !strings.HasPrefix(path, "/video/") || !bilibiliID.MatchString(strings.TrimPrefix(path, "/video/")) {
		return nil, false
	}
	return parsed, true
}

func (BilibiliDynamic) Suitable(parsed *url.URL) bool {
	if parsed == nil || !bilibiliStrictURL(parsed, "t.bilibili.com", "bilibili.com", "www.bilibili.com") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return (host == "t.bilibili.com" && bilibiliDynamicTPath.MatchString(parsed.Path)) || (host != "t.bilibili.com" && bilibiliDynamicOpusPath.MatchString(parsed.Path))
}

func (BilibiliDynamic) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewBilibiliDynamic().Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	postID := strings.TrimPrefix(parsed.Path, "/")
	postID = strings.TrimPrefix(postID, "opus/")
	endpoint := "https://api.bilibili.com/x/polymer/web-dynamic/v1/detail?id=" + postID
	var response bilibiliDynamicResponse
	if err := bilibiliIsolatedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, nil, &response); err != nil {
		return Extraction{}, err
	}
	if err := bilibiliAPIError(response.Code); err != nil {
		return Extraction{}, err
	}
	for _, rawChild := range bilibiliDynamicJumpURLs(response.Data.Item) {
		childURL, ok := bilibiliDynamicVideoChild(rawChild)
		if !ok {
			continue
		}
		return URLResult(Entry{URL: childURL.String(), ExtractorKey: "bilibili", ID: bilibiliURLID(childURL), Transparent: true})
	}
	return Extraction{}, fmt.Errorf("%w: dynamic has no supported video", ErrUnavailable)
}

type BiliIntl struct{}

func NewBiliIntl() BiliIntl   { return BiliIntl{} }
func (BiliIntl) Name() string { return "biliintl" }

var biliIntlPlayPath = regexp.MustCompile(`^(?:/[A-Za-z]{2})?/play/([0-9]{1,32})/([0-9]{1,32})$`)
var biliIntlVideoPath = regexp.MustCompile(`^(?:/[A-Za-z]{2})?/video/([0-9]{1,32})$`)
var biliIntlEpisodeTitle = regexp.MustCompile(`(?i)^E([0-9]+)(?:$| - )`)
var biliIntlHTMLMetaTag = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
var biliIntlHTMLAttribute = regexp.MustCompile(`(?i)([a-z0-9:_-]{1,64})\s*=\s*["']([^"']{0,1000})["']`)
var biliIntlHTMLTitle = regexp.MustCompile(`(?is)<title\b[^>]*>([^<]{1,1000})</title>`)

type biliIntlPageVideoData struct {
	TitleDisplay     string  `json:"title_display"`
	Title            string  `json:"title"`
	Desc             string  `json:"desc"`
	Cover            string  `json:"cover"`
	Pic              string  `json:"pic"`
	FormattedPubDate string  `json:"formatted_pub_date"`
	PubDate          int64   `json:"pub_date"`
	Duration         float64 `json:"duration"`
}

type biliIntlPageState struct {
	OgvVideo struct {
		EpDetail biliIntlPageVideoData `json:"epDetail"`
	} `json:"OgvVideo"`
	UgcVideo struct {
		VideoData biliIntlPageVideoData `json:"videoData"`
	} `json:"UgcVideo"`
	Ugc struct {
		Archive biliIntlPageVideoData `json:"archive"`
	} `json:"ugc"`
}

type biliIntlVideoResource struct {
	URL       string `json:"url"`
	Width     int64  `json:"width"`
	Height    int64  `json:"height"`
	Bandwidth int64  `json:"bandwidth"`
	Codecs    string `json:"codecs"`
	Size      int64  `json:"size"`
}

type biliIntlPlayResponse struct {
	Code int `json:"code"`
	Data struct {
		PlayURL struct {
			Video []struct {
				VideoResource biliIntlVideoResource `json:"video_resource"`
				StreamInfo    struct {
					DescWords string `json:"desc_words"`
				} `json:"stream_info"`
			} `json:"video"`
			AudioResource []biliIntlVideoResource `json:"audio_resource"`
		} `json:"playurl"`
	} `json:"data"`
}

func bilibiliIntlPageMetadata(page []byte) biliIntlPageVideoData {
	var metadata biliIntlPageVideoData
	for _, marker := range []string{"window.__INITIAL_STATE__", "window.__INITIAL_DATA__"} {
		raw, err := extractJSONObject(page, marker)
		if err != nil {
			continue
		}
		var state biliIntlPageState
		if json.Unmarshal(raw, &state) != nil {
			continue
		}
		for _, candidate := range []biliIntlPageVideoData{state.OgvVideo.EpDetail, state.UgcVideo.VideoData, state.Ugc.Archive} {
			if candidate.TitleDisplay != "" || candidate.Title != "" || candidate.Desc != "" || candidate.Cover != "" || candidate.Pic != "" {
				metadata = candidate
				break
			}
		}
		if metadata.TitleDisplay != "" || metadata.Title != "" || metadata.Desc != "" || metadata.Cover != "" || metadata.Pic != "" {
			break
		}
	}
	if metadata.TitleDisplay == "" {
		metadata.TitleDisplay = bilibiliIntlHTMLMeta(page, "og:title")
	}
	if metadata.Title == "" {
		metadata.Title = bilibiliIntlHTMLMeta(page, "og:title")
	}
	if metadata.Desc == "" {
		metadata.Desc = bilibiliIntlHTMLMeta(page, "og:description")
	}
	if metadata.Cover == "" {
		metadata.Cover = bilibiliIntlHTMLMeta(page, "og:image")
	}
	if metadata.TitleDisplay == "" {
		if match := biliIntlHTMLTitle.FindSubmatch(page); len(match) == 2 {
			metadata.TitleDisplay = html.UnescapeString(string(match[1]))
		}
	}
	return metadata
}

func bilibiliIntlHTMLMeta(page []byte, wanted string) string {
	for _, tag := range biliIntlHTMLMetaTag.FindAll(page, 512) {
		attributes := make(map[string]string)
		for _, match := range biliIntlHTMLAttribute.FindAllSubmatch(tag, -1) {
			attributes[strings.ToLower(string(match[1]))] = html.UnescapeString(string(match[2]))
		}
		property := strings.ToLower(firstBilibiliString(attributes["property"], attributes["name"]))
		if property == wanted {
			return attributes["content"]
		}
	}
	return ""
}

func bilibiliIntlTimestamp(metadata biliIntlPageVideoData) int64 {
	if metadata.PubDate > 0 {
		return metadata.PubDate
	}
	if raw := strings.TrimSpace(metadata.FormattedPubDate); raw != "" {
		if timestamp, err := strconv.ParseInt(raw, 10, 64); err == nil && timestamp > 0 {
			return timestamp
		}
		for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00", time.RFC3339} {
			if parsed, err := time.Parse(layout, raw); err == nil {
				return parsed.Unix()
			}
		}
	}
	return 0
}

func biliIntlHost(parsed *url.URL) bool {
	return parsed != nil && (strings.ToLower(parsed.Hostname()) == "bilibili.tv" || strings.ToLower(parsed.Hostname()) == "www.bilibili.tv" || strings.ToLower(parsed.Hostname()) == "biliintl.com" || strings.ToLower(parsed.Hostname()) == "www.biliintl.com")
}

func (BiliIntl) Suitable(parsed *url.URL) bool {
	if !bilibiliStrictURL(parsed) || !biliIntlHost(parsed) {
		return false
	}
	return biliIntlPlayPath.MatchString(parsed.Path) || biliIntlVideoPath.MatchString(parsed.Path)
}

func (BiliIntl) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewBiliIntl().Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	videoID := ""
	queryKey := ""
	if match := biliIntlPlayPath.FindStringSubmatch(parsed.Path); len(match) == 3 {
		videoID, queryKey = match[2], "ep_id="+match[2]
	} else if match := biliIntlVideoPath.FindStringSubmatch(parsed.Path); len(match) == 2 {
		videoID, queryKey = match[1], "aid="+match[1]
	}
	if queryKey == "" {
		return Extraction{}, ErrUnsupported
	}
	page, err := bilibiliIsolatedReadPage(ctx, request.Transport, request.URL)
	if err != nil {
		return Extraction{}, err
	}
	metadata := bilibiliIntlPageMetadata(page)
	title := firstBilibiliString(metadata.TitleDisplay, metadata.Title)
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: international page has no bounded metadata", ErrInvalidMetadata)
	}
	endpoint := "https://api.bilibili.tv/intl/gateway/web/playurl?platform=web&" + queryKey
	var response biliIntlPlayResponse
	headers := make(http.Header)
	headers.Set("Referer", bilibiliIntlReferer)
	if err := bilibiliIsolatedRequestJSONWithReferer(ctx, request.Transport, http.MethodGet, endpoint, nil, headers, &response); err != nil {
		return Extraction{}, err
	}
	if err := bilibiliAPIError(response.Code); err != nil {
		return Extraction{}, err
	}
	formats := make([]value.Value, 0, len(response.Data.PlayURL.Video)+len(response.Data.PlayURL.AudioResource))
	for index, video := range response.Data.PlayURL.Video {
		resource := video.VideoResource
		if !validBilibiliRoleURL(resource.URL, bilibiliInternationalMedia) {
			continue
		}
		format := value.NewObject(value.Field{Key: "format_id", Value: value.String(fmt.Sprintf("video-%d", index))}, value.Field{Key: "url", Value: value.String(resource.URL)}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "protocol", Value: value.String("https")}, value.Field{Key: "vcodec", Value: value.String(resource.Codecs)}, value.Field{Key: "acodec", Value: value.String("none")}, value.Field{Key: "width", Value: value.Int(resource.Width)}, value.Field{Key: "height", Value: value.Int(resource.Height)}, value.Field{Key: "tbr", Value: value.Int(resource.Bandwidth)}, value.Field{Key: "_credential_isolated", Value: value.Bool(true)})
		setPositiveInt(format, "filesize", resource.Size)
		if video.StreamInfo.DescWords != "" {
			format.Set("format_note", value.String(video.StreamInfo.DescWords))
		}
		formats = append(formats, value.ObjectValue(format))
	}
	for index, audio := range response.Data.PlayURL.AudioResource {
		if !validBilibiliRoleURL(audio.URL, bilibiliInternationalMedia) {
			continue
		}
		format := value.NewObject(value.Field{Key: "format_id", Value: value.String(fmt.Sprintf("audio-%d", index))}, value.Field{Key: "url", Value: value.String(audio.URL)}, value.Field{Key: "ext", Value: value.String("m4a")}, value.Field{Key: "protocol", Value: value.String("https")}, value.Field{Key: "vcodec", Value: value.String("none")}, value.Field{Key: "acodec", Value: value.String(audio.Codecs)}, value.Field{Key: "abr", Value: value.Int(audio.Bandwidth)}, value.Field{Key: "_credential_isolated", Value: value.Bool(true)})
		setPositiveInt(format, "filesize", audio.Size)
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: international video has no public formats", ErrUnavailable)
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(videoID)}, value.Field{Key: "title", Value: value.String(title)}, value.Field{Key: "formats", Value: value.List(formats...)}, value.Field{Key: "webpage_url", Value: value.String(request.URL)}, value.Field{Key: "extractor", Value: value.String("biliintl")}, value.Field{Key: "extractor_key", Value: value.String("BiliIntl")}))
	if metadata.Desc != "" {
		info.Set("description", value.String(metadata.Desc))
	}
	if timestamp := bilibiliIntlTimestamp(metadata); timestamp > 0 {
		info.Set("timestamp", value.Int(timestamp))
	}
	if match := biliIntlEpisodeTitle.FindStringSubmatch(metadata.TitleDisplay); len(match) == 2 {
		if episodeNumber, err := strconv.ParseInt(match[1], 10, 64); err == nil {
			info.Set("episode_number", value.Int(episodeNumber))
		}
	}
	setBilibiliThumbnail(info.Fields(), firstBilibiliString(metadata.Cover, metadata.Pic), bilibiliInternationalThumbnail)
	return Media(info), nil
}

type BiliIntlSeries struct{}

func NewBiliIntlSeries() BiliIntlSeries { return BiliIntlSeries{} }
func (BiliIntlSeries) Name() string     { return "biliintl_series" }

var biliIntlSeriesPath = regexp.MustCompile(`^(?:/[A-Za-z]{2})?/(?:play|media)/([0-9]{1,32})/?$`)

func (BiliIntlSeries) Suitable(parsed *url.URL) bool {
	return bilibiliStrictURL(parsed) && biliIntlHost(parsed) && biliIntlSeriesPath.MatchString(parsed.Path)
}

func (BiliIntlSeries) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewBiliIntlSeries().Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	seasonID := biliIntlSeriesPath.FindStringSubmatch(parsed.Path)[1]
	headers := make(http.Header)
	headers.Set("Referer", bilibiliIntlReferer)
	var season struct {
		Code int `json:"code"`
		Data struct {
			Season struct {
				Title       string `json:"title"`
				Description string `json:"description"`
				Cover       string `json:"horizontal_cover"`
			} `json:"season"`
		} `json:"data"`
	}
	seasonEndpoint := "https://api.bilibili.tv/intl/gateway/web/v2/ogv/play/season_info?season_id=" + seasonID + "&platform=web"
	if err := bilibiliIsolatedRequestJSONWithReferer(ctx, request.Transport, http.MethodGet, seasonEndpoint, nil, headers, &season); err != nil {
		return Extraction{}, err
	}
	if err := bilibiliAPIError(season.Code); err != nil {
		return Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(seasonID)}, value.Field{Key: "title", Value: value.String(season.Data.Season.Title)}, value.Field{Key: "webpage_url", Value: value.String(request.URL)}, value.Field{Key: "extractor", Value: value.String("biliintl_series")}, value.Field{Key: "extractor_key", Value: value.String("BiliIntlSeries")}))
	if season.Data.Season.Description != "" {
		info.Set("description", value.String(season.Data.Season.Description))
	}
	setBilibiliThumbnail(info.Fields(), season.Data.Season.Cover, bilibiliInternationalThumbnail)
	sequence, err := LazyFirstPageEntries(1000, func(ctx context.Context) ([]Entry, error) {
		var episodes struct {
			Code int `json:"code"`
			Data struct {
				Sections []struct {
					Episodes []struct {
						ID    int64  `json:"episode_id"`
						Title string `json:"title"`
						Cover string `json:"cover"`
					} `json:"episodes"`
				} `json:"sections"`
			} `json:"data"`
		}
		endpoint := "https://api.bilibili.tv/intl/gateway/web/v2/ogv/play/episodes?season_id=" + seasonID + "&platform=web"
		if err := bilibiliIsolatedRequestJSONWithReferer(ctx, request.Transport, http.MethodGet, endpoint, nil, headers, &episodes); err != nil {
			return nil, err
		}
		if err := bilibiliAPIError(episodes.Code); err != nil {
			return nil, err
		}
		entries := make([]Entry, 0)
		for _, section := range episodes.Data.Sections {
			for _, episode := range section.Episodes {
				if episode.ID > 0 {
					entry := Entry{URL: "https://www.bilibili.tv/en/play/" + seasonID + "/" + strconv.FormatInt(episode.ID, 10), ExtractorKey: "biliintl", ID: strconv.FormatInt(episode.ID, 10), Title: episode.Title}
					if validBilibiliRoleURL(episode.Cover, bilibiliInternationalThumbnail) {
						entry.Thumbnail = episode.Cover
					}
					entries = append(entries, entry)
				}
			}
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}
