package extractor

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	anvatoAPIBase      = "https://tkx.mp.lura.live/rest/v2"
	anvatoMaxPublished = 64
	anvatoMaxCaptions  = 64
	anvatoMaxTags      = 64
)

var (
	anvatoAccessKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_]{8,128}$`)
	anvatoVideoIDPattern   = regexp.MustCompile(`^[0-9]{1,32}$`)
)

// Minimal access-key secrets required by counted Anvato adapters. The full
// reference table is intentionally not ported; secrets never appear in errors
// or returned metadata.
var anvatoAccessSecrets = map[string]string{
	"anvato_epfox_app_web_prod_b3373168e12f423f41504f207000188daf88251b": "GDKq1ixvX3MoBNdU5IOYmYa2DTUXYOozPjrCJnW7",
	"X8POa4zpGZMmeiq0wqiO8IP5rMqQM9VN":                                   "Dn5vOY9ooDw7VSl9qztjZI5o0g08mA0z",
}

var anvatoMCPAccessKeys = map[string]string{
	"lin": "anvato_mcp_lin_web_prod_4c36fbfd4d8d8ecae6488656e21ac6d1ac972749",
}

// anvatoAuthKey is the public 8-byte XOR key copied from anvplayer.min.js via
// the pinned reference. The reference calls aes_encrypt with this short key,
// which reduces to an 8-byte XOR of the auth input prefix.
var anvatoAuthKey = []byte{0x31, 0xc2, 0x42, 0x84, 0x9e, 0x73, 0xa0, 0xce}

// Anvato implements the documented Lura/Anvato MCP video JSON endpoint for
// opaque anvato:access_key:id URLs. DRM success is not claimed.
type Anvato struct{}

func NewAnvato() Anvato     { return Anvato{} }
func (Anvato) Name() string { return "anvato" }

func (Anvato) Suitable(parsed *url.URL) bool {
	_, ok := parseAnvatoURL(parsed)
	return ok
}

func (Anvato) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseAnvatoURL(parsed)
	if !ok || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	accessKey := resolveAnvatoAccessKey(target.accessKey)
	serverTime, err := anvatoServerTime(ctx, request.Transport, accessKey, target.videoID)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	payload, err := anvatoVideoJSON(ctx, request.Transport, accessKey, target.videoID, serverTime, target.token)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	return normalizeAnvato(payload, target)
}

type anvatoTarget struct {
	accessKey string
	videoID   string
	token     string
	canonical string
}

func parseAnvatoURL(parsed *url.URL) (anvatoTarget, bool) {
	if parsed == nil || len(parsed.String()) > sharedHostingMaxURLBytes {
		return anvatoTarget{}, false
	}
	if parsed.Scheme != "anvato" || parsed.Host != "" || parsed.User != nil || parsed.Opaque == "" {
		return anvatoTarget{}, false
	}
	parts := strings.Split(parsed.Opaque, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return anvatoTarget{}, false
	}
	accessKey, videoID := parts[0], parts[1]
	token := ""
	if len(parts) == 3 {
		token = parts[2]
		if token == "" || len(token) > 4096 || strings.ContainsAny(token, " \x00\r\n") {
			return anvatoTarget{}, false
		}
	}
	if !anvatoAccessKeyPattern.MatchString(accessKey) || !anvatoVideoIDPattern.MatchString(videoID) {
		return anvatoTarget{}, false
	}
	canonical := "anvato:" + accessKey + ":" + videoID
	return anvatoTarget{accessKey: accessKey, videoID: videoID, token: token, canonical: canonical}, true
}

func resolveAnvatoAccessKey(accessKey string) string {
	if mapped, ok := anvatoMCPAccessKeys[strings.ToLower(accessKey)]; ok {
		return mapped
	}
	return accessKey
}

func anvatoServerTime(ctx context.Context, transport Transport, accessKey, videoID string) (int64, error) {
	endpoint := anvatoAPIBase + "/server_time?anvack=" + url.QueryEscape(accessKey)
	var response struct {
		ServerTime hostingNumber `json:"server_time"`
	}
	if err := hostedRequestJSON(ctx, transport, http.MethodGet, endpoint, nil, make(http.Header), &response); err != nil {
		if errors.Is(err, ErrUnavailable) {
			return time.Now().Unix(), nil
		}
		return 0, err
	}
	if ts := response.ServerTime.int64(); ts > 0 {
		return ts, nil
	}
	return time.Now().Unix(), nil
}

func anvatoVideoJSON(ctx context.Context, transport Transport, accessKey, videoID string, serverTime int64, token string) (anvatoVideo, error) {
	videoURL := anvatoAPIBase + "/mcp/video/" + url.PathEscape(videoID) + "?anvack=" + url.QueryEscape(accessKey)
	auth := anvatoAdstAuth(videoURL, serverTime)
	anvrid := fmt.Sprintf("%x", md5.Sum([]byte(strconv.FormatInt(serverTime, 10))))[:30]
	api := map[string]any{
		"anvrid": anvrid,
		"anvts":  serverTime,
	}
	if token != "" {
		api["anvstk2"] = token
	} else if secret, ok := anvatoAccessSecrets[accessKey]; ok {
		api["anvstk"] = fmt.Sprintf("%x", md5.Sum([]byte(accessKey+"|"+anvrid+"|"+strconv.FormatInt(serverTime, 10)+"|"+secret)))
	} else {
		api["anvstk2"] = "default"
	}
	body, err := json.Marshal(map[string]any{"api": api})
	if err != nil {
		return anvatoVideo{}, fmt.Errorf("%w: encode Anvato request", ErrInvalidMetadata)
	}
	query := url.Values{}
	query.Set("anvack", accessKey)
	query.Set("X-Anvato-Adst-Auth", auth)
	query.Set("rtyp", "fp")
	endpoint := anvatoAPIBase + "/mcp/video/" + url.PathEscape(videoID) + "?" + query.Encode()
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	var payload anvatoVideo
	if err := hostedRequestJSON(ctx, transport, http.MethodPost, endpoint, body, headers, &payload); err != nil {
		return anvatoVideo{}, err
	}
	return payload, nil
}

// anvatoAdstAuth reproduces the pinned reference AnvatoIE._get_video_json
// X-Anvato-Adst-Auth value. The reference calls yt_dlp.aes.aes_encrypt with the
// 8-byte AUTH_KEY from anvplayer.min.js. Because that key is shorter than one
// AES block, aes_encrypt's round count becomes -1 and the operation reduces to
// XOR of the first 8 bytes of the truncated input. This short-key XOR behavior
// is intentional compatibility with the pinned extractor, not a full AES port.
func anvatoAdstAuth(videoDataURL string, serverTime int64) string {
	input := fmt.Sprintf("%d~%x~%x", serverTime, md5.Sum([]byte(videoDataURL)), md5.Sum([]byte(strconv.FormatInt(serverTime, 10))))
	if len(input) > 64 {
		input = input[:64]
	}
	auth := make([]byte, len(anvatoAuthKey))
	for i := range anvatoAuthKey {
		if i >= len(input) {
			break
		}
		auth[i] = input[i] ^ anvatoAuthKey[i]
	}
	return base64.StdEncoding.EncodeToString(auth)
}

type anvatoVideo struct {
	DefTitle       string            `json:"def_title"`
	DefDescription string            `json:"def_description"`
	DefTags        string            `json:"def_tags"`
	Categories     []string          `json:"categories"`
	SrcImageURL    string            `json:"src_image_url"`
	Thumbnail      string            `json:"thumbnail"`
	TSPublished    hostingNumber     `json:"ts_published"`
	TSAdded        hostingNumber     `json:"ts_added"`
	MCPID          string            `json:"mcp_id"`
	Duration       hostingNumber     `json:"duration"`
	PublishedURLs  []anvatoPublished `json:"published_urls"`
	Captions       []anvatoCaption   `json:"captions"`
}

type anvatoPublished struct {
	EmbedURL string        `json:"embed_url"`
	Format   string        `json:"format"`
	KBPS     hostingNumber `json:"kbps"`
	CDNName  string        `json:"cdn_name"`
	Width    hostingNumber `json:"width"`
	Height   hostingNumber `json:"height"`
}

type anvatoCaption struct {
	URL      string `json:"url"`
	Format   string `json:"format"`
	Language string `json:"language"`
}

func normalizeAnvato(video anvatoVideo, target anvatoTarget) (Extraction, error) {
	title := strings.TrimSpace(video.DefTitle)
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Anvato title", ErrInvalidMetadata)
	}
	if len(video.PublishedURLs) > anvatoMaxPublished {
		return Extraction{}, fmt.Errorf("%w: Anvato published URL limit", ErrInvalidMetadata)
	}
	formats := make([]value.Value, 0, len(video.PublishedURLs))
	for index, published := range video.PublishedURLs {
		rawURL := strings.TrimSpace(published.EmbedURL)
		if rawURL == "" || !strictValidHostedHTTPURL(rawURL) {
			continue
		}
		formatName := strings.ToLower(strings.TrimSpace(published.Format))
		switch {
		case formatName == "smil" || strings.HasSuffix(strings.ToLower(rawURL), ".smil"):
			continue
		case formatName == "m3u8" || formatName == "m3u8-variant" || strings.Contains(strings.ToLower(rawURL), ".m3u8"):
			formatID := "hls"
			if tbr := published.KBPS.int64(); tbr > 0 {
				formatID = fmt.Sprintf("hls-%d", tbr)
			}
			format, ok := strictHostedURLFormat(formatID, rawURL)
			if !ok {
				continue
			}
			formats = append(formats, value.ObjectValue(format))
		case formatName == "vtt":
			continue
		default:
			formatID := "http"
			if published.CDNName != "" {
				formatID = "http-" + strings.ToLower(published.CDNName)
			} else {
				formatID = fmt.Sprintf("http-%d", index)
			}
			format, ok := strictHostedURLFormat(formatID, rawURL)
			if !ok {
				continue
			}
			hostedSetInt(format, "width", published.Width.int64())
			hostedSetInt(format, "height", published.Height.int64())
			if tbr := published.KBPS.float64(); tbr > 0 {
				format.Set("tbr", value.Float(tbr))
			}
			if formatName == "mp3" || strings.HasSuffix(strings.ToLower(rawURL), ".mp3") {
				format.Set("vcodec", value.String("none"))
			}
			formats = append(formats, value.ObjectValue(format))
		}
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.videoID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	hostedSetString(info, "description", video.DefDescription)
	hostedSetString(info, "uploader", video.MCPID)
	hostedSetString(info, "thumbnail", firstValidHostedURL(video.SrcImageURL, video.Thumbnail))
	hostedSetInt(info, "duration", video.Duration.int64())
	if ts := video.TSPublished.int64(); ts > 0 {
		hostedSetInt(info, "timestamp", ts)
	} else if ts := video.TSAdded.int64(); ts > 0 {
		hostedSetInt(info, "timestamp", ts)
	}
	if tags := splitBounded(video.DefTags, ",", anvatoMaxTags); len(tags) > 0 {
		values := make([]value.Value, 0, len(tags))
		for _, tag := range tags {
			values = append(values, value.String(tag))
		}
		info.Set("tags", value.List(values...))
	}
	if len(video.Categories) > 0 && len(video.Categories) <= anvatoMaxTags {
		values := make([]value.Value, 0, len(video.Categories))
		for _, category := range video.Categories {
			category = strings.TrimSpace(category)
			if category != "" {
				values = append(values, value.String(category))
			}
		}
		if len(values) > 0 {
			info.Set("categories", value.List(values...))
		}
	}
	if len(video.Captions) > anvatoMaxCaptions {
		return Extraction{}, fmt.Errorf("%w: Anvato caption limit", ErrInvalidMetadata)
	}
	subtitles := value.NewObject()
	hasSubs := false
	for _, caption := range video.Captions {
		if !strictValidHostedHTTPURL(caption.URL) {
			continue
		}
		lang := strings.TrimSpace(caption.Language)
		if lang == "" {
			lang = "en"
		}
		entry := value.NewObject(value.Field{Key: "url", Value: value.String(caption.URL)})
		if strings.EqualFold(caption.Format, "SMPTE-TT") {
			entry.Set("ext", value.String("tt"))
		}
		existing, ok := subtitles.Lookup(lang).ListValue()
		if !ok {
			existing = nil
		}
		existing = append(existing, value.ObjectValue(entry))
		subtitles.Set(lang, value.List(existing...))
		hasSubs = true
	}
	if hasSubs {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	return Media(value.NewInfo(info)), nil
}

func firstValidHostedURL(inputs ...string) string {
	for _, input := range inputs {
		if strictValidHostedHTTPURL(input) {
			return input
		}
	}
	return ""
}

func splitBounded(input, sep string, limit int) []string {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	parts := strings.Split(input, sep)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
		if len(out) >= limit {
			break
		}
	}
	return out
}
