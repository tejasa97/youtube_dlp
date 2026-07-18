package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const tiktokImpersonationProfile = "chrome-133"

var (
	tiktokVideoPathPattern = regexp.MustCompile(`^/@([A-Za-z0-9_.-]+)/video/([0-9]+)/*$`)
	tiktokEmbedPathPattern = regexp.MustCompile(`^/embed/([0-9]+)/*$`)
	tiktokUniversalScript  = regexp.MustCompile(`(?is)<script\b[^>]*\bid=["']__UNIVERSAL_DATA_FOR_REHYDRATION__["'][^>]*>(.*?)</script\s*>`)
	tiktokURLKeyPattern    = regexp.MustCompile(`v[^_]+_([^_]+)_([0-9]+p)_([0-9]+)`)
)

type TikTok struct{}

func NewTikTok() TikTok { return TikTok{} }

func (TikTok) Name() string { return "tiktok" }

func (TikTok) Suitable(parsed *url.URL) bool {
	host := strings.ToLower(parsed.Hostname())
	if host != "www.tiktok.com" && host != "tiktok.com" {
		return false
	}
	return tiktokVideoPathPattern.MatchString(parsed.Path) || tiktokEmbedPathPattern.MatchString(parsed.Path)
}

func (TikTok) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	videoID, userID := tiktokVideoIdentity(parsed.Path)
	if videoID == "" {
		return Extraction{}, ErrUnsupported
	}
	webpageURL := "https://www.tiktok.com/@_/video/" + videoID
	if userID != "" {
		webpageURL = "https://www.tiktok.com/@" + userID + "/video/" + videoID
	}
	page, _, err := ReadPageWithProfile(ctx, request.Transport, webpageURL, tiktokImpersonationProfile)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	return parseTikTokPage(page, videoID, webpageURL)
}

func tiktokVideoIdentity(path string) (videoID, userID string) {
	if match := tiktokVideoPathPattern.FindStringSubmatch(path); len(match) == 3 {
		return match[2], match[1]
	}
	if match := tiktokEmbedPathPattern.FindStringSubmatch(path); len(match) == 2 {
		return match[1], ""
	}
	return "", ""
}

func parseTikTokPage(page []byte, videoID, webpageURL string) (Extraction, error) {
	match := tiktokUniversalScript.FindSubmatch(page)
	if len(match) != 2 {
		lower := bytes.ToLower(page)
		switch {
		case bytes.Contains(lower, []byte("log in")), bytes.Contains(lower, []byte("login")), bytes.Contains(lower, []byte("session expired")):
			return Extraction{}, ErrAuthentication
		case bytes.Contains(lower, []byte("please wait")), bytes.Contains(lower, []byte("wafchallenge")), bytes.Contains(lower, []byte("id=\"cs\"")):
			return Extraction{}, ErrChallengeSolver
		default:
			return Extraction{}, fmt.Errorf("%w: missing TikTok hydration data", ErrInvalidMetadata)
		}
	}
	if int64(len(match[1])) > maxExtractorJSONBytes {
		return Extraction{}, ErrJSONResponseTooLarge
	}
	var hydration tiktokHydration
	decoder := json.NewDecoder(bytes.NewReader(match[1]))
	decoder.UseNumber()
	if err := decoder.Decode(&hydration); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid TikTok hydration JSON", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Extraction{}, fmt.Errorf("%w: trailing TikTok hydration JSON", ErrInvalidMetadata)
	}
	detail := hydration.DefaultScope.VideoDetail
	switch detail.StatusCode {
	case 0:
	case 10216, 10222:
		return Extraction{}, ErrAuthentication
	case 10204:
		return Extraction{}, ErrUnavailable
	default:
		return Extraction{}, fmt.Errorf("%w: TikTok status %d", ErrUnavailable, detail.StatusCode)
	}
	item := detail.ItemInfo.Item
	if item.ID != "" && item.ID != videoID {
		return Extraction{}, fmt.Errorf("%w: TikTok video id mismatch", ErrInvalidMetadata)
	}
	if item.Video.Width == 0 && item.Video.Height == 0 && item.Classified {
		return Extraction{}, ErrAuthentication
	}
	return parseTikTokItem(item, videoID, webpageURL)
}

type tiktokHydration struct {
	DefaultScope struct {
		VideoDetail struct {
			StatusCode int `json:"statusCode"`
			ItemInfo   struct {
				Item tiktokItem `json:"itemStruct"`
			} `json:"itemInfo"`
		} `json:"webapp.video-detail"`
	} `json:"__DEFAULT_SCOPE__"`
}

type tiktokItem struct {
	ID          string      `json:"id"`
	Description string      `json:"desc"`
	CreateTime  json.Number `json:"createTime"`
	Classified  bool        `json:"isContentClassified"`
	Author      struct {
		UniqueID string      `json:"uniqueId"`
		Nickname string      `json:"nickname"`
		AuthorID json.Number `json:"authorId"`
		SecUID   string      `json:"secUid"`
	} `json:"author"`
	AuthorInfo struct {
		UniqueID string      `json:"uniqueId"`
		Nickname string      `json:"nickname"`
		AuthorID json.Number `json:"authorId"`
		SecUID   string      `json:"authorSecId"`
	} `json:"authorInfo"`
	Video tiktokVideo `json:"video"`
	Music struct {
		Title      string `json:"title"`
		Album      string `json:"album"`
		AuthorName string `json:"authorName"`
		Duration   int64  `json:"duration"`
		PlayURL    string `json:"playUrl"`
	} `json:"music"`
	Stats struct {
		PlayCount    int64 `json:"playCount"`
		DiggCount    int64 `json:"diggCount"`
		ShareCount   int64 `json:"shareCount"`
		CommentCount int64 `json:"commentCount"`
		CollectCount int64 `json:"collectCount"`
	} `json:"stats"`
}

type tiktokVideo struct {
	Duration     int64         `json:"duration"`
	Width        int64         `json:"width"`
	Height       int64         `json:"height"`
	PlayAddr     tiktokAddress `json:"-"`
	DownloadAddr tiktokAddress `json:"-"`
	BitrateInfo  []struct {
		PlayAddr struct {
			URLList  []string `json:"UrlList"`
			URLKey   string   `json:"UrlKey"`
			DataSize int64    `json:"DataSize"`
		} `json:"PlayAddr"`
	} `json:"bitrateInfo"`
	Thumbnail    string `json:"thumbnail"`
	Cover        string `json:"cover"`
	DynamicCover string `json:"dynamicCover"`
	OriginCover  string `json:"originCover"`
}

type tiktokAddress struct{ URLs []string }

func (video *tiktokVideo) UnmarshalJSON(data []byte) error {
	type plain tiktokVideo
	var raw struct {
		*plain
		PlayAddr     json.RawMessage `json:"playAddr"`
		DownloadAddr json.RawMessage `json:"downloadAddr"`
	}
	raw.plain = (*plain)(video)
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	video.PlayAddr = parseTikTokAddress(raw.PlayAddr)
	video.DownloadAddr = parseTikTokAddress(raw.DownloadAddr)
	return nil
}

func parseTikTokAddress(raw json.RawMessage) tiktokAddress {
	if len(raw) == 0 {
		return tiktokAddress{}
	}
	var direct string
	if json.Unmarshal(raw, &direct) == nil {
		return tiktokAddress{URLs: []string{direct}}
	}
	var object struct {
		Src     string   `json:"src"`
		URL     string   `json:"url"`
		URLList []string `json:"urlList"`
	}
	if json.Unmarshal(raw, &object) != nil {
		return tiktokAddress{}
	}
	urls := append([]string(nil), object.URLList...)
	urls = append(urls, object.Src, object.URL)
	return tiktokAddress{URLs: urls}
}

func parseTikTokItem(item tiktokItem, videoID, webpageURL string) (Extraction, error) {
	formats := make([]value.Value, 0)
	seen := map[string]struct{}{}
	appendFormat := func(rawURL, id, note, codec string, width, height, size, bitrate int64) {
		rawURL = normalizeTikTokURL(rawURL)
		if !validHTTPURL(rawURL) {
			return
		}
		parsed, _ := url.Parse(rawURL)
		if strings.EqualFold(parsed.Hostname(), "www.tiktok.com") {
			return
		}
		if _, exists := seen[rawURL]; exists {
			return
		}
		seen[rawURL] = struct{}{}
		format := value.NewObject(
			value.Field{Key: "format_id", Value: value.String(id)},
			value.Field{Key: "url", Value: value.String(rawURL)},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String(codec)},
			value.Field{Key: "acodec", Value: value.String("aac")},
		)
		if note != "" {
			format.Set("format_note", value.String(note))
		}
		setPositiveInt(format, "width", width)
		setPositiveInt(format, "height", height)
		setPositiveInt(format, "filesize", size)
		setPositiveInt(format, "tbr", bitrate)
		formats = append(formats, value.ObjectValue(format))
	}
	for _, bitrate := range item.Video.BitrateInfo {
		formatID, codec, dimension, rate := parseTikTokURLKey(bitrate.PlayAddr.URLKey)
		width, height := tiktokDimensions(item.Video.Width, item.Video.Height, dimension)
		for _, rawURL := range bitrate.PlayAddr.URLList {
			appendFormat(rawURL, formatID, "", codec, width, height, bitrate.PlayAddr.DataSize, rate)
		}
	}
	for _, rawURL := range item.Video.PlayAddr.URLs {
		appendFormat(rawURL, "play", "Direct video", "h264", item.Video.Width, item.Video.Height, 0, 0)
	}
	for _, rawURL := range item.Video.DownloadAddr.URLs {
		appendFormat(rawURL, "download", "watermarked", "h264", item.Video.Width, item.Video.Height, 0, 0)
	}
	if len(formats) == 0 && validHTTPURL(normalizeTikTokURL(item.Music.PlayURL)) {
		audio := value.NewObject(
			value.Field{Key: "format_id", Value: value.String("audio")},
			value.Field{Key: "url", Value: value.String(normalizeTikTokURL(item.Music.PlayURL))},
			value.Field{Key: "ext", Value: value.String("m4a")},
			value.Field{Key: "vcodec", Value: value.String("none")},
			value.Field{Key: "acodec", Value: value.String("aac")},
		)
		formats = append(formats, value.ObjectValue(audio))
	}
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: no TikTok formats", ErrInvalidMetadata)
	}
	authorID := item.Author.AuthorID.String()
	uploader, channel, secUID := item.Author.UniqueID, item.Author.Nickname, item.Author.SecUID
	if uploader == "" {
		uploader = item.AuthorInfo.UniqueID
	}
	if channel == "" {
		channel = item.AuthorInfo.Nickname
	}
	if secUID == "" {
		secUID = item.AuthorInfo.SecUID
	}
	if authorID == "" {
		authorID = item.AuthorInfo.AuthorID.String()
	}
	title := item.Description
	if title == "" {
		title = "TikTok video #" + videoID
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "title", Value: value.String(truncateTikTokTitle(title))},
		value.Field{Key: "description", Value: value.String(item.Description)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
		value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(value.Field{Key: "Referer", Value: value.String(webpageURL)}))},
	)
	setString := func(key, text string) {
		if text != "" {
			info.Set(key, value.String(text))
		}
	}
	setString("uploader", uploader)
	setString("uploader_id", authorID)
	setString("channel", channel)
	setString("channel_id", secUID)
	if secUID != "" {
		info.Set("channel_url", value.String("https://www.tiktok.com/@"+secUID))
	}
	if uploader != "" {
		info.Set("uploader_url", value.String("https://www.tiktok.com/@"+uploader))
	}
	setString("track", item.Music.Title)
	setString("album", item.Music.Album)
	if item.Music.AuthorName != "" {
		artists := make([]value.Value, 0)
		for _, artist := range splitTikTokArtists(item.Music.AuthorName) {
			artists = append(artists, value.String(artist))
		}
		info.Set("artists", value.List(artists...))
	}
	setPositiveInt(info, "duration", item.Video.Duration)
	if item.Video.Duration == 0 {
		setPositiveInt(info, "duration", item.Music.Duration)
	}
	if timestamp, err := strconv.ParseInt(item.CreateTime.String(), 10, 64); err == nil {
		setPositiveInt(info, "timestamp", timestamp)
	}
	setPositiveInt(info, "view_count", item.Stats.PlayCount)
	setPositiveInt(info, "like_count", item.Stats.DiggCount)
	setPositiveInt(info, "repost_count", item.Stats.ShareCount)
	setPositiveInt(info, "comment_count", item.Stats.CommentCount)
	setPositiveInt(info, "save_count", item.Stats.CollectCount)
	for _, thumbnail := range []string{item.Video.Thumbnail, item.Video.Cover, item.Video.DynamicCover, item.Video.OriginCover} {
		if thumbnail = normalizeTikTokURL(thumbnail); validHTTPURL(thumbnail) {
			info.Set("thumbnail", value.String(thumbnail))
			break
		}
	}
	return Media(value.NewInfo(info)), nil
}

func parseTikTokURLKey(key string) (id, codec string, dimension, bitrate int64) {
	match := tiktokURLKeyPattern.FindStringSubmatch(key)
	if len(match) != 4 {
		return "bitrate", "h264", 0, 0
	}
	codec = match[1]
	if codec == "bytevc1" {
		codec = "h265"
	}
	dimension, _ = strconv.ParseInt(strings.TrimSuffix(match[2], "p"), 10, 64)
	if dimension == 540 {
		dimension = 576
	}
	bitrate, _ = strconv.ParseInt(match[3], 10, 64)
	bitrate /= 1000
	return match[1] + "_" + match[2] + "_" + match[3], codec, dimension, bitrate
}

func tiktokDimensions(sourceWidth, sourceHeight, dimension int64) (width, height int64) {
	if sourceWidth <= 0 || sourceHeight <= 0 || dimension <= 0 {
		return 0, 0
	}
	if sourceWidth < sourceHeight {
		width = dimension
		height = dimension * sourceHeight / sourceWidth
		height -= height % 2
		return width, height
	}
	height = dimension
	width = dimension * sourceWidth / sourceHeight
	width += width % 2
	return width, height
}

func splitTikTokArtists(text string) []string {
	artists := strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == '&' })
	for index := range artists {
		artists[index] = strings.TrimSpace(artists[index])
	}
	return slices.DeleteFunc(artists, func(artist string) bool { return artist == "" })
}

func normalizeTikTokURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "//") {
		return "https:" + rawURL
	}
	return rawURL
}

func truncateTikTokTitle(text string) string {
	runes := []rune(text)
	if len(runes) <= 72 {
		return text
	}
	return string(runes[:69]) + "..."
}
