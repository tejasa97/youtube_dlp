package extractor

// Anonymous public VK and VK Play extraction. The route and asset policies in
// this file are intentionally narrower than the upstream regular expressions:
// a URL is accepted only when its identity, query cardinality, and attributable
// origin are known before any request is made.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const (
	vkMaxURLBytes        = 8 << 10
	vkMaxPageBytes       = 4 << 20
	vkMaxJSONBytes       = 4 << 20
	vkMaxFormats         = 256
	vkMaxSubtitles       = 128
	vkMaxThumbnails      = 64
	vkMaxPlaylistPages   = 10_000
	vkMaxPlaylistEntries = 100_000
	vkMaxWallEntries     = 256
	vkMaxOpaqueBytes     = 16 << 10
)

var (
	ErrVKNetwork          = errors.New("VK public request failed")
	ErrVKRateLimited      = errors.New("VK public request rate limited")
	ErrVKAuthentication   = errors.New("VK public media requires authentication")
	ErrVKRegionRestricted = errors.New("VK public media is region restricted")
	ErrVKUnavailable      = errors.New("VK public media is unavailable")
	ErrVKNotLive          = errors.New("VK public stream is not live")
	ErrVKRepeatedPage     = errors.New("VK public playlist repeated a page")
	ErrVKInvalidStatus    = errors.New("VK public request returned an unsupported status")
	ErrVKUnsafeAsset      = errors.New("VK public media hop is not attributable")

	vkNumericIDPattern = regexp.MustCompile(`^-?[0-9]{1,20}$`)
	vkVideoIDPattern   = regexp.MustCompile(`^(-?[0-9]{1,20})_([0-9]{1,20})$`)
	vkSlugPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	vkUsernamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	vkUUIDPattern      = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	vkVKAudioPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

const (
	VKPublicAssetPolicy     = "vk_public"
	VKPlayPublicAssetPolicy = "vkplay_public"
)

// AssetURLValidator returns the extractor-owned policy used by the native
// HLS/DASH downloaders. The policy is carried in prepared format selections,
// so every manifest, variant, initialization, key, and segment hop is checked
// before it reaches the transport.
func AssetURLValidator(policy string) (func(string) error, error) {
	switch policy {
	case "":
		return nil, nil
	case VKPublicAssetPolicy:
		return func(rawURL string) error {
			if _, ok := vkAssetURL(rawURL, vkRoleMedia); ok {
				return nil
			}
			return ErrVKUnsafeAsset
		}, nil
	case VKPlayPublicAssetPolicy:
		return func(rawURL string) error {
			if _, ok := vkPlayAssetURL(rawURL, vkRoleMedia); ok {
				return nil
			}
			return ErrVKUnsafeAsset
		}, nil
	default:
		return nil, fmt.Errorf("%w: unknown VK asset policy", ErrInvalidMetadata)
	}
}

var vkPageHosts = map[string]bool{
	"vk.com": true, "m.vk.com": true, "new.vk.com": true, "vkvideo.ru": true,
}

var vkVideoHosts = map[string]bool{
	"vk.com": true, "m.vk.com": true, "new.vk.com": true, "vkvideo.ru": true,
	"vksport.vkvideo.ru": true,
}

var vkCoreHosts = map[string]bool{
	"vk.com": true, "m.vk.com": true, "new.vk.com": true, "vkvideo.ru": true,
}

var vkPlayPageHosts = map[string]bool{
	"vkplay.live": true, "live.vkplay.ru": true, "live.vkvideo.ru": true,
}

// vkURLCommon rejects URL forms that could change origin or path identity.
// HTTP input is retained for parity with the pinned extractor, but every
// generated request and every emitted asset is HTTPS.
func vkURLCommon(parsed *url.URL, hosts map[string]bool) bool {
	if parsed == nil || len(parsed.String()) == 0 || len(parsed.String()) > vkMaxURLBytes ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil ||
		parsed.Port() != "" || strings.Contains(parsed.Host, ":") || parsed.Fragment != "" ||
		parsed.RawFragment != "" || parsed.RawPath != "" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if !hosts[host] {
		return false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	return strings.HasPrefix(escaped, "/") && !strings.Contains(escaped, "%2f") &&
		!strings.Contains(escaped, "%5c") && !strings.Contains(escaped, "%00") &&
		!strings.Contains(escaped, "%2e")
}

func vkQueryValues(parsed *url.URL, allowed map[string]bool) (url.Values, bool) {
	if parsed == nil {
		return nil, false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return nil, false
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 || values[0] == "" {
			return nil, false
		}
	}
	return query, true
}

type vkVideoTarget struct {
	ownerID string
	videoID string
	listID  string
	rawURL  string
	embed   bool
}

func parseVKVideoID(raw string) (string, string, bool) {
	match := vkVideoIDPattern.FindStringSubmatch(raw)
	if match == nil || match[2] == "0" {
		return "", "", false
	}
	return match[1], match[2], true
}

func parseVKVideoURL(parsed *url.URL) (vkVideoTarget, bool) {
	if !vkURLCommon(parsed, vkVideoHosts) {
		return vkVideoTarget{}, false
	}
	pathName := parsed.Path
	if pathName == "/video_ext.php" {
		query, ok := vkQueryValues(parsed, map[string]bool{
			"oid": true, "id": true, "hash": true, "list": true,
			"hd": true, "autoplay": true, "js_api": true, "t": true,
		})
		if !ok || !vkNumericIDPattern.MatchString(query.Get("oid")) ||
			!regexp.MustCompile(`^[0-9]{1,20}$`).MatchString(query.Get("id")) {
			return vkVideoTarget{}, false
		}
		return vkVideoTarget{ownerID: query.Get("oid"), videoID: query.Get("id"),
			listID: query.Get("list"), rawURL: parsed.String(), embed: true}, true
	}

	parts := strings.Split(strings.TrimPrefix(pathName, "/"), "/")
	if len(parts) != 1 || parts[0] == "" {
		// Opaque page forms carry the selected video in z= and are limited to
		// the two public video feeds and the public feed page.
		if len(parts) != 1 || (parts[0] != "feed" && parts[0] != "videos" && parts[0] != "clips") {
			return vkVideoTarget{}, false
		}
	}
	if (strings.HasPrefix(parts[0], "video") && !strings.HasPrefix(parts[0], "videos")) ||
		(strings.HasPrefix(parts[0], "clip") && !strings.HasPrefix(parts[0], "clips")) {
		prefix := "video"
		if strings.HasPrefix(parts[0], "clip") {
			prefix = "clip"
		}
		ownerID, videoID, ok := parseVKVideoID(strings.TrimPrefix(parts[0], prefix))
		if !ok {
			return vkVideoTarget{}, false
		}
		query, ok := vkQueryValues(parsed, map[string]bool{"list": true})
		if !ok {
			return vkVideoTarget{}, false
		}
		return vkVideoTarget{ownerID: ownerID, videoID: videoID, listID: query.Get("list"), rawURL: parsed.String()}, true
	}

	query, ok := vkQueryValues(parsed, map[string]bool{"z": true})
	if !ok {
		return vkVideoTarget{}, false
	}
	selected := query.Get("z")
	selected = strings.SplitN(selected, "/", 2)[0]
	selected = strings.TrimPrefix(selected, "video")
	if selected == query.Get("z") {
		selected = strings.TrimPrefix(query.Get("z"), "clip")
	}
	ownerID, videoID, valid := parseVKVideoID(selected)
	if !valid {
		return vkVideoTarget{}, false
	}
	return vkVideoTarget{ownerID: ownerID, videoID: videoID, rawURL: parsed.String()}, true
}

func (target vkVideoTarget) id() string { return target.ownerID + "_" + target.videoID }

func canonicalVKVideoURL(target vkVideoTarget) string {
	return "https://vk.com/video" + target.id()
}

type VKIE struct{}

func NewVK() VKIE         { return VKIE{} }
func (VKIE) Name() string { return "vk" }
func (VKIE) Suitable(parsed *url.URL) bool {
	_, ok := parseVKVideoURL(parsed)
	return ok
}

func (VKIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseVKVideoURL(parsed)
	if !ok || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	var infoPage []byte
	var options map[string]json.RawMessage
	var mvData map[string]json.RawMessage
	if target.embed {
		// Preserve the validated signed query byte-for-byte. The host is
		// normalized to the public canonical origin without forwarding cookies.
		embedURL := "https://vk.com/video_ext.php?" + parsed.RawQuery
		infoPage, err = vkRead(ctx, request.Transport, embedURL, vkMaxPageBytes)
		if err != nil {
			return Extraction{}, vkCategorize(err)
		}
		if vkPageHasFailure(infoPage) {
			return Extraction{}, vkPageFailure(infoPage)
		}
		options, err = vkPlayerOptionsFromPage(infoPage)
		if err != nil {
			return Extraction{}, err
		}
	} else {
		body := url.Values{"act": {"show"}, "video": {target.id()}}
		if target.listID != "" {
			body.Set("list", target.listID)
		}
		payload, payloadPage, payloadOptions, payloadErr := vkVideoPayload(ctx, request.Transport, body, request.Referer)
		if payloadErr != nil {
			return Extraction{}, vkCategorize(payloadErr)
		}
		infoPage, options, mvData = payloadPage, payloadOptions, payload
		_ = infoPage
	}
	if mvData == nil {
		mvData = vkRawObject(options, "mvData")
	}
	player := vkRawObject(options, "player")
	if player == nil {
		// video_ext.php exposes playerParams directly, while al_video.php
		// nests the same object under opts.player.
		player = options
	}
	var params []map[string]json.RawMessage
	if json.Unmarshal(player["params"], &params) != nil || len(params) == 0 {
		return Extraction{}, fmt.Errorf("%w: VK player parameters missing", ErrInvalidMetadata)
	}
	return normalizeVKVideo(ctx, request.URL, target.id(), infoPage, mvData, params[0])
}

// vkVideoPayload returns mvData, the HTML fragment, and player options. The
// separate HTML value keeps page-derived view counts and error markers intact.
func vkVideoPayload(ctx context.Context, transport Transport, form url.Values, referer string) (map[string]json.RawMessage, []byte, map[string]json.RawMessage, error) {
	payload, err := vkPostPayload(ctx, transport, "https://vk.com/al_video.php", form, referer)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(payload) < 2 {
		return nil, nil, nil, fmt.Errorf("%w: VK video payload shape", ErrInvalidMetadata)
	}
	page, ok := payload[0].(string)
	if !ok {
		return nil, nil, nil, fmt.Errorf("%w: VK video page payload", ErrInvalidMetadata)
	}
	last, ok := payload[len(payload)-1].(map[string]json.RawMessage)
	if !ok {
		return nil, nil, nil, fmt.Errorf("%w: VK video options payload", ErrInvalidMetadata)
	}
	return vkRawObject(last, "mvData"), []byte(page), last, nil
}

func vkPlayerOptionsFromPage(page []byte) (map[string]json.RawMessage, error) {
	marker := []byte("playerParams")
	index := bytes.Index(page, marker)
	if index < 0 {
		return nil, fmt.Errorf("%w: VK player parameters missing", ErrInvalidMetadata)
	}
	raw, _, err := extractJSONObjectFrom(page, index+len(marker), 256)
	if err != nil {
		return nil, fmt.Errorf("%w: VK player parameters malformed", ErrInvalidMetadata)
	}
	var options map[string]json.RawMessage
	if json.Unmarshal(raw, &options) != nil {
		return nil, fmt.Errorf("%w: VK player parameters malformed", ErrInvalidMetadata)
	}
	return options, nil
}

func normalizeVKVideo(ctx context.Context, webpageURL, videoID string, page []byte, mvData, data map[string]json.RawMessage) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	formats, subtitles, thumbnails, err := vkVideoFormats(ctx, videoID, data)
	if err != nil {
		return Extraction{}, err
	}
	if len(formats) == 0 {
		return Extraction{}, ErrVKUnavailable
	}
	title := vkString(mvData, "title")
	if title == "" {
		title = vkString(data, "md_title")
	}
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: VK video title missing", ErrInvalidMetadata)
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "title", Value: value.String(html.UnescapeString(title))},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	if description := vkFirstNonEmpty(vkString(mvData, "desc"), vkString(data, "description")); description != "" {
		info.Set("description", value.String(html.UnescapeString(description)))
	}
	if uploader := vkString(data, "md_author"); uploader != "" {
		info.Set("uploader", value.String(html.UnescapeString(uploader)))
	}
	if uploaderID := vkFirstNonEmpty(vkString(data, "author_id"), vkString(data, "authorId")); uploaderID != "" {
		info.Set("uploader_id", value.String(uploaderID))
	}
	for field, key := range map[string]string{"duration": "duration", "like_count": "likes", "comment_count": "commcount"} {
		if number, ok := vkInt(mvData, key); ok && number >= 0 {
			info.Set(field, value.Int(number))
		}
	}
	if number, ok := vkInt(data, "duration"); ok && info.Lookup("duration").IsMissing() {
		info.Set("duration", value.Int(number))
	}
	if timestamp, ok := vkInt(data, "date"); ok && timestamp > 0 {
		info.Set("timestamp", value.Int(timestamp))
	}
	if views := vkPageViewCount(page); views >= 0 {
		info.Set("view_count", value.Int(views))
	}
	if live, ok := vkInt(data, "live"); ok {
		info.Set("is_live", value.Bool(live == 2))
	}
	if thumbnail := vkString(data, "jpg"); thumbnail != "" {
		if validated, ok := vkAssetURL(thumbnail, vkRoleThumbnail); ok {
			info.Set("thumbnail", value.String(validated))
		}
	}
	if len(thumbnails) > 0 {
		info.Set("thumbnails", value.List(thumbnails...))
	}
	if len(subtitles) > 0 {
		langs := make([]string, 0, len(subtitles))
		for lang := range subtitles {
			langs = append(langs, lang)
		}
		sort.Strings(langs)
		subsObject := value.NewObject()
		for _, lang := range langs {
			subsObject.Set(lang, value.List(subtitles[lang]...))
		}
		info.Set("subtitles", value.ObjectValue(subsObject))
	}
	if chapters := vkChapters(data); len(chapters) > 0 {
		info.Set("chapters", value.List(chapters...))
	}
	return Media(value.NewInfo(info)), nil
}

func vkVideoFormats(ctx context.Context, videoID string, data map[string]json.RawMessage) ([]value.Value, map[string][]value.Value, []value.Value, error) {
	keys := make([]string, 0, len(data))
	for key := range data {
		if strings.HasPrefix(key, "url") || strings.HasPrefix(key, "cache") ||
			strings.HasPrefix(key, "hls") || strings.HasPrefix(key, "dash") ||
			key == "extra_data" || key == "live_mp4" || key == "postlive_mp4" {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		ri, rj := vkFormatRank(keys[i]), vkFormatRank(keys[j])
		if ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
	formats := make([]value.Value, 0, minInt(len(keys), vkMaxFormats))
	seen := make(map[string]bool)
	for _, key := range keys {
		if err := contextError(ctx); err != nil {
			return nil, nil, nil, err
		}
		rawURL := vkString(data, key)
		if rawURL == "" || seen[rawURL] {
			continue
		}
		kind := "direct"
		protocol := "https"
		role := vkRoleMedia
		lower := strings.ToLower(rawURL)
		switch {
		case strings.HasPrefix(key, "hls") && key != "hls_live_playback", strings.HasSuffix(lower, ".m3u8"):
			kind, protocol, role = "hls", "m3u8_native", vkRoleManifest
		case strings.HasPrefix(key, "dash") && key != "dash_live_playback" && key != "dash_uni", strings.HasSuffix(lower, ".mpd"):
			kind, protocol, role = "dash", "http_dash_segments", vkRoleManifest
		case key == "live_mp4" || key == "postlive_mp4" || strings.HasPrefix(key, "url") || strings.HasPrefix(key, "cache") || key == "extra_data":
		default:
			continue
		}
		validated, ok := vkAssetURL(rawURL, role)
		if !ok {
			continue
		}
		format := value.NewObject(
			value.Field{Key: "format_id", Value: value.String(key)},
			value.Field{Key: "url", Value: value.String(validated)},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "protocol", Value: value.String(protocol)},
			value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
			value.Field{Key: "_asset_policy", Value: value.String(VKPublicAssetPolicy)},
		)
		if kind == "direct" {
			if height := vkFormatHeight(key); height > 0 {
				format.Set("height", value.Int(int64(height)))
			}
		}
		formats = append(formats, value.ObjectValue(format))
		seen[rawURL] = true
		if len(formats) == vkMaxFormats {
			break
		}
	}
	subtitles := vkSubtitles(data)
	thumbnails := vkThumbnails(data)
	return formats, subtitles, thumbnails, nil
}

type vkAssetRole uint8

const (
	vkRoleMedia vkAssetRole = iota + 1
	vkRoleManifest
	vkRoleSubtitle
	vkRoleThumbnail
)

func vkAssetURL(rawURL string, role vkAssetRole) (string, bool) {
	if len(rawURL) == 0 || len(rawURL) > vkMaxURLBytes {
		return "", false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Fragment != "" || parsed.RawPath != "" || parsed.Host == "" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	allowed := false
	switch role {
	case vkRoleMedia, vkRoleManifest, vkRoleSubtitle:
		allowed = vkCoreHosts[host] || strings.HasSuffix(host, ".vkuservideo.net") ||
			strings.HasSuffix(host, ".vkuseraudio.net") || strings.HasSuffix(host, ".userapi.com")
	case vkRoleThumbnail:
		allowed = vkCoreHosts[host] || strings.HasSuffix(host, ".userapi.com") ||
			strings.HasSuffix(host, ".vkuservideo.net") || strings.HasSuffix(host, ".vkuseraudio.net")
	}
	if !allowed || !strings.HasPrefix(parsed.EscapedPath(), "/") {
		return "", false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2e") || strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
		return "", false
	}
	return parsed.String(), true
}

func vkSubtitles(data map[string]json.RawMessage) map[string][]value.Value {
	result := make(map[string][]value.Value)
	raw := data["subs"]
	if len(raw) == 0 {
		return result
	}
	var list []map[string]json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		for _, sub := range list {
			vkAddSubtitle(result, sub)
		}
		return result
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			var sub map[string]json.RawMessage
			if json.Unmarshal(object[key], &sub) == nil {
				if vkString(sub, "lang") == "" {
					sub["lang"] = json.RawMessage(strconv.Quote(key))
				}
				vkAddSubtitle(result, sub)
			}
		}
	}
	return result
}

func vkAddSubtitle(result map[string][]value.Value, sub map[string]json.RawMessage) {
	if len(result) >= vkMaxSubtitles {
		return
	}
	lang := vkString(sub, "lang")
	rawURL := vkString(sub, "url")
	if lang == "" || rawURL == "" {
		return
	}
	validated, ok := vkAssetURL(rawURL, vkRoleSubtitle)
	if !ok {
		return
	}
	ext := strings.TrimPrefix(vkString(sub, "title"), ".")
	if ext == "" {
		ext = "srt"
	}
	result[lang] = append(result[lang], value.ObjectValue(value.NewObject(
		value.Field{Key: "url", Value: value.String(validated)},
		value.Field{Key: "ext", Value: value.String(ext)},
		value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
	)))
}

func vkThumbnails(data map[string]json.RawMessage) []value.Value {
	var result []value.Value
	if rawURL := vkString(data, "jpg"); rawURL != "" {
		if validated, ok := vkAssetURL(rawURL, vkRoleThumbnail); ok {
			result = append(result, value.ObjectValue(value.NewObject(
				value.Field{Key: "id", Value: value.String("0")},
				value.Field{Key: "url", Value: value.String(validated)},
				value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
			)))
		}
	}
	return result
}

func vkChapters(data map[string]json.RawMessage) []value.Value {
	var raw []map[string]json.RawMessage
	if json.Unmarshal(data["time_codes"], &raw) != nil {
		return nil
	}
	chapters := make([]value.Value, 0, len(raw))
	for _, chapter := range raw {
		at, ok := vkInt(chapter, "time")
		if !ok || at < 0 {
			continue
		}
		object := value.NewObject(value.Field{Key: "start_time", Value: value.Int(at)})
		if title := vkString(chapter, "text"); title != "" {
			object.Set("title", value.String(html.UnescapeString(title)))
		}
		chapters = append(chapters, value.ObjectValue(object))
	}
	return chapters
}

func vkFormatRank(key string) int {
	switch {
	case strings.HasPrefix(key, "url"), strings.HasPrefix(key, "cache"):
		return 0
	case key == "extra_data":
		return 1
	case strings.HasPrefix(key, "hls"):
		return 2
	case strings.HasPrefix(key, "dash"):
		return 3
	default:
		return 4
	}
}

func vkFormatHeight(key string) int {
	key = strings.TrimPrefix(strings.TrimPrefix(key, "cache"), "url")
	value, _ := strconv.Atoi(key)
	return value
}

func vkPageViewCount(page []byte) int64 {
	match := regexp.MustCompile(`(?i)mv_views_count[^>]*>\s*([0-9][0-9 .,]*)`).FindSubmatch(page)
	if match == nil {
		return -1
	}
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, string(match[1]))
	value, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return -1
	}
	return value
}

func vkPageHasFailure(page []byte) bool {
	text := strings.ToLower(string(page))
	message := regexp.MustCompile(`(?s)(?:video_layer_message|video_ext_msg)[^>]*>\s*[^<\s]`).MatchString(text)
	return message ||
		strings.Contains(text, "please log in") || strings.Contains(text, "access denied") ||
		strings.Contains(text, "temporarily unavailable")
}

func vkPageFailure(page []byte) error {
	text := strings.ToLower(string(page))
	switch {
	case strings.Contains(text, "not available in your region"):
		return ErrVKRegionRestricted
	case strings.Contains(text, "please log in"), strings.Contains(text, "security_check"), strings.Contains(text, "access denied"):
		return ErrVKAuthentication
	case strings.Contains(text, "unknown error"), strings.Contains(text, "deleted"), strings.Contains(text, "blocked"), strings.Contains(text, "copyright"), strings.Contains(text, "temporarily unavailable"):
		return ErrVKUnavailable
	default:
		return ErrVKUnavailable
	}
}

// --- VK public user/group video playlists -------------------------------

type vkUserVideoTarget struct {
	pageID  string
	section string
	pageURL string
}

func parseVKUserVideoURL(parsed *url.URL) (vkUserVideoTarget, bool) {
	if !vkURLCommon(parsed, vkPageHosts) {
		return vkUserVideoTarget{}, false
	}
	query, ok := vkQueryValues(parsed, map[string]bool{"section": true})
	if !ok {
		return vkUserVideoTarget{}, false
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if strings.ToLower(parsed.Hostname()) == "vkvideo.ru" {
		if len(parts) == 2 && parts[0] == "playlist" {
			owner, list, valid := parseVKVideoID(parts[1])
			if valid {
				return vkUserVideoTarget{pageID: owner, section: "playlist_" + list, pageURL: parsed.String()}, true
			}
		}
		if len(parts) == 1 || (len(parts) == 2 && parts[1] == "all") {
			slug := parts[0]
			if strings.HasPrefix(slug, "@") && vkSlugPattern.MatchString(strings.TrimPrefix(slug, "@")) &&
				(len(parts) == 1 || parsed.Path == "/"+slug+"/all") {
				section := query.Get("section")
				if section == "" {
					section = "all"
				}
				if !vkUserSection(section) {
					return vkUserVideoTarget{}, false
				}
				return vkUserVideoTarget{pageID: "@" + strings.TrimPrefix(slug, "@"), section: section, pageURL: parsed.String()}, true
			}
		}
		return vkUserVideoTarget{}, false
	}
	if len(parts) == 3 && parts[0] == "video" && parts[1] == "playlist" {
		owner, list, valid := parseVKVideoID(parts[2])
		if valid {
			return vkUserVideoTarget{pageID: owner, section: "playlist_" + list, pageURL: parsed.String()}, true
		}
	}
	if len(parts) >= 2 && len(parts) <= 3 && parts[0] == "video" && strings.HasPrefix(parts[1], "@") {
		slug := strings.TrimPrefix(parts[1], "@")
		if !vkSlugPattern.MatchString(slug) || (len(parts) == 3 && parts[2] != "all") {
			return vkUserVideoTarget{}, false
		}
		section := query.Get("section")
		if section == "" {
			section = "all"
		}
		if !vkUserSection(section) || parsed.Query().Get("z") != "" {
			return vkUserVideoTarget{}, false
		}
		return vkUserVideoTarget{pageID: "@" + slug, section: section, pageURL: parsed.String()}, true
	}
	return vkUserVideoTarget{}, false
}

func vkUserSection(section string) bool {
	return regexp.MustCompile(`^(?:all|uploaded|live|clips)$`).MatchString(section)
}

type VKUserVideosIE struct{}

func NewVKUserVideos() VKUserVideosIE { return VKUserVideosIE{} }
func (VKUserVideosIE) Name() string   { return "vk_uservideos" }
func (VKUserVideosIE) Suitable(parsed *url.URL) bool {
	_, ok := parseVKUserVideoURL(parsed)
	return ok
}

func (VKUserVideosIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseVKUserVideoURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	page, err := vkRead(ctx, request.Transport, request.URL, vkMaxPageBytes)
	if err != nil {
		return Extraction{}, vkCategorize(err)
	}
	pageID := target.pageID
	if strings.HasPrefix(pageID, "@") {
		marker := bytes.Index(page, []byte("newCur"))
		if marker < 0 {
			return Extraction{}, fmt.Errorf("%w: VK public page identity missing", ErrInvalidMetadata)
		}
		raw, _, parseErr := extractJSONObjectFrom(page, marker+len("newCur"), 256)
		if parseErr != nil {
			return Extraction{}, fmt.Errorf("%w: VK public page identity malformed", ErrInvalidMetadata)
		}
		var cursor map[string]json.RawMessage
		if json.Unmarshal(raw, &cursor) != nil || !vkNumericIDPattern.MatchString(vkString(cursor, "oid")) {
			return Extraction{}, fmt.Errorf("%w: VK public page identity malformed", ErrInvalidMetadata)
		}
		pageID = vkString(cursor, "oid")
	}
	title := vkHTMLClassText(page, "VideoInfoPanel__title")
	if title == "" {
		title = vkHTMLTitle(page)
	}
	entries, err := newVKUserVideoEntries(request.Transport, pageID, target.section, request.Referer)
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(pageID + "_" + target.section)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(request.URL)},
	))
	return Playlist(info, entries)
}

type vkVideoPageFetcher func(context.Context, int) (rows []vkVideoRow, total int, count int, err error)

type vkVideoRow struct{ ownerID, videoID string }

type vkUserVideoEntries struct {
	transport                Transport
	pageID, section, referer string
}

func newVKUserVideoEntries(transport Transport, pageID, section, referer string) (EntrySequence, error) {
	if transport == nil || pageID == "" || section == "" {
		return nil, fmt.Errorf("%w: VK playlist source missing", ErrInvalidPlaylist)
	}
	return vkUserVideoEntries{transport: transport, pageID: pageID, section: section, referer: referer}, nil
}

func (entries vkUserVideoEntries) Iterator() EntryIterator {
	return &vkUserVideoIterator{source: entries, seen: make(map[string]bool)}
}

type vkUserVideoIterator struct {
	source                        vkUserVideoEntries
	page                          []Entry
	index                         int
	offset, total, pages, emitted int
	initialized, done             bool
	seen                          map[string]bool
}

func (iterator *vkUserVideoIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := contextError(ctx); err != nil {
		return Entry{}, false, err
	}
	for {
		if iterator.index < len(iterator.page) {
			entry := iterator.page[iterator.index]
			iterator.index++
			iterator.emitted++
			return entry, true, nil
		}
		if iterator.done {
			return Entry{}, false, nil
		}
		if iterator.pages >= vkMaxPlaylistPages || iterator.emitted >= vkMaxPlaylistEntries {
			return Entry{}, false, ErrPlaylistLimit
		}
		rows, total, count, err := vkFetchUserVideoPage(ctx, iterator.source.transport, iterator.source.pageID, iterator.source.section, iterator.offset, iterator.source.referer)
		if err != nil {
			return Entry{}, false, vkCategorize(err)
		}
		iterator.pages++
		if !iterator.initialized {
			iterator.initialized = true
			iterator.total = total
		}
		if len(rows) == 0 || count <= 0 {
			iterator.done = true
			return Entry{}, false, nil
		}
		fingerprint := vkRowsFingerprint(rows, count, total)
		if iterator.seen[fingerprint] {
			return Entry{}, false, ErrVKRepeatedPage
		}
		iterator.seen[fingerprint] = true
		iterator.page = iterator.page[:0]
		iterator.index = 0
		for _, row := range rows {
			iterator.page = append(iterator.page, Entry{
				URL:          canonicalVKVideoURL(vkVideoTarget{ownerID: row.ownerID, videoID: row.videoID}),
				ExtractorKey: "vk", ID: row.ownerID + "_" + row.videoID,
			})
		}
		if iterator.total > 0 && iterator.offset+count >= iterator.total {
			iterator.done = true
		}
		iterator.offset += count
		if iterator.offset <= 0 || iterator.offset > vkMaxPlaylistEntries+iterator.total {
			return Entry{}, false, fmt.Errorf("%w: VK playlist offset", ErrInvalidPlaylist)
		}
	}
}

func vkFetchUserVideoPage(ctx context.Context, transport Transport, pageID, section string, offset int, referer string) ([]vkVideoRow, int, int, error) {
	payload, err := vkPostPayload(ctx, transport, "https://vk.com/al_video.php", url.Values{
		"act": {"load_videos_silent"}, "offset": {strconv.Itoa(offset)}, "oid": {pageID}, "section": {section},
	}, referer)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(payload) == 0 {
		return nil, 0, 0, fmt.Errorf("%w: VK playlist payload shape", ErrInvalidMetadata)
	}
	root, ok := payload[0].(map[string]json.RawMessage)
	if !ok {
		return nil, 0, 0, fmt.Errorf("%w: VK playlist page shape", ErrInvalidMetadata)
	}
	sectionRaw := root[section]
	var page struct {
		Count int               `json:"count"`
		Total int               `json:"total"`
		List  []json.RawMessage `json:"list"`
	}
	if json.Unmarshal(sectionRaw, &page) != nil || page.Count < 0 || page.Total < 0 {
		return nil, 0, 0, fmt.Errorf("%w: VK playlist rows malformed", ErrInvalidMetadata)
	}
	rows := make([]vkVideoRow, 0, len(page.List))
	for _, raw := range page.List {
		var pair []json.RawMessage
		if json.Unmarshal(raw, &pair) != nil || len(pair) < 2 {
			continue
		}
		owner := vkRawNumber(pair[0])
		video := vkRawNumber(pair[1])
		if !vkNumericIDPattern.MatchString(owner) || !regexp.MustCompile(`^[0-9]{1,20}$`).MatchString(video) || video == "0" {
			continue
		}
		rows = append(rows, vkVideoRow{ownerID: owner, videoID: video})
	}
	return rows, page.Total, page.Count, nil
}

func vkRowsFingerprint(rows []vkVideoRow, count, total int) string {
	var builder strings.Builder
	builder.WriteString(strconv.Itoa(count))
	builder.WriteByte('/')
	builder.WriteString(strconv.Itoa(total))
	builder.WriteByte(':')
	for _, row := range rows {
		builder.WriteString(row.ownerID)
		builder.WriteByte('_')
		builder.WriteString(row.videoID)
		builder.WriteByte(',')
	}
	return builder.String()
}

// --- VK public wall posts -----------------------------------------------

type vkWallTarget struct{ postID, pageURL string }

func parseVKWallURL(parsed *url.URL) (vkWallTarget, bool) {
	if !vkURLCommon(parsed, map[string]bool{"vk.com": true, "m.vk.com": true, "new.vk.com": true}) {
		return vkWallTarget{}, false
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if len(parts) == 1 && strings.HasPrefix(parts[0], "wall") {
		_, ok := vkQueryValues(parsed, map[string]bool{})
		if !ok {
			return vkWallTarget{}, false
		}
		postID := strings.TrimPrefix(parts[0], "wall")
		if _, _, ok := parseVKVideoID(postID); !ok {
			return vkWallTarget{}, false
		}
		return vkWallTarget{postID: postID, pageURL: parsed.String()}, true
	}
	if len(parts) != 1 || !vkSlugPattern.MatchString(parts[0]) {
		return vkWallTarget{}, false
	}
	query, ok := vkQueryValues(parsed, map[string]bool{"w": true})
	if !ok || !strings.HasPrefix(query.Get("w"), "wall") {
		return vkWallTarget{}, false
	}
	postID := strings.TrimPrefix(query.Get("w"), "wall")
	if _, _, valid := parseVKVideoID(postID); !valid {
		return vkWallTarget{}, false
	}
	return vkWallTarget{postID: postID, pageURL: parsed.String()}, true
}

type VKWallPostIE struct{}

func NewVKWallPost() VKWallPostIE                  { return VKWallPostIE{} }
func (VKWallPostIE) Name() string                  { return "vk_wallpost" }
func (VKWallPostIE) Suitable(parsed *url.URL) bool { _, ok := parseVKWallURL(parsed); return ok }

func (VKWallPostIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseVKWallURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	payload, err := vkPostPayload(ctx, request.Transport, "https://vk.com/wkview.php", url.Values{"act": {"show"}, "w": {"wall" + target.postID}}, request.Referer)
	if err != nil {
		return Extraction{}, vkCategorize(err)
	}
	if len(payload) < 1 {
		return Extraction{}, fmt.Errorf("%w: VK wall payload shape", ErrInvalidMetadata)
	}
	page, ok := payload[0].(string)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: VK wall page shape", ErrInvalidMetadata)
	}
	uploader := vkHTMLClassText([]byte(page), "PostHeaderTitle__authorName")
	title := uploader
	if title != "" {
		title += " - "
	}
	title += "Wall post " + target.postID
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(target.postID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(request.URL)},
	))
	if description := vkHTMLClassText([]byte(page), "wall_post_text"); description != "" {
		info.Set("description", value.String(description))
	}
	entries := make([]Entry, 0, vkMaxWallEntries)
	for _, raw := range regexp.MustCompile(`data-audio="([^"]+)"`).FindAllStringSubmatch(page, vkMaxWallEntries) {
		if len(raw) != 2 {
			continue
		}
		var audio map[string]json.RawMessage
		if json.Unmarshal([]byte(html.UnescapeString(raw[1])), &audio) != nil {
			continue
		}
		asset, ok := vkAssetURL(vkString(audio, "url"), vkRoleMedia)
		if !ok {
			continue
		}
		owner := vkFirstNonEmpty(vkString(audio, "owner_id"), vkString(audio, "ownerId"))
		id := vkFirstNonEmpty(vkString(audio, "id"), "0")
		encoded, encodeErr := vkEncodeAudio(vkAudioPayload{
			ID: id, OwnerID: owner, URL: asset, Title: vkString(audio, "title"), Artist: vkFirstNonEmpty(vkString(audio, "artist"), vkString(audio, "performer")),
			Duration: vkIntDefault(audio, "duration"), CoverURL: vkString(audio, "coverUrl"), Uploader: uploader, WebpageURL: request.URL,
		})
		if encodeErr != nil {
			continue
		}
		entries = append(entries, Entry{URL: "vkaudio:" + encoded, ExtractorKey: "vk_audio", ID: owner + "_" + id,
			Title: vkAudioTitle(vkString(audio, "artist"), vkString(audio, "title"))})
	}
	seen := make(map[string]bool)
	body := vkHTMLElementByID([]byte(page), "wl_post_body")
	for _, raw := range regexp.MustCompile(`(?i)<a[^>]+href=["']([^"']+)["']`).FindAllStringSubmatch(string(body), vkMaxWallEntries) {
		if len(raw) != 2 {
			continue
		}
		candidate, err := url.Parse(raw[1])
		if err != nil || !strings.HasPrefix(candidate.Path, "/video") && !strings.HasPrefix(candidate.Path, "/clip") {
			continue
		}
		resolved := parsed.ResolveReference(candidate)
		videoTarget, valid := parseVKVideoURL(resolved)
		if !valid {
			continue
		}
		canonical := videoTarget.rawURL
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		entries = append(entries, Entry{URL: canonical, ExtractorKey: "vk", ID: videoTarget.id(), Transparent: true, Referer: target.pageURL})
		if len(entries) == vkMaxWallEntries {
			break
		}
	}
	return Playlist(info, StaticEntries(entries...))
}

// --- Opaque public wall audio --------------------------------------------

type vkAudioPayload struct {
	ID, OwnerID, URL, Title, Artist, CoverURL, Uploader, WebpageURL string
	Duration                                                        int64
}

func vkEncodeAudio(payload vkAudioPayload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil || len(raw) > vkMaxOpaqueBytes {
		return "", fmt.Errorf("%w: VK audio result too large", ErrInvalidPlaylist)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

type VKAudioIE struct{}

func NewVKAudio() VKAudioIE    { return VKAudioIE{} }
func (VKAudioIE) Name() string { return "vk_audio" }
func (VKAudioIE) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Scheme == "vkaudio" && parsed.Host == "" && len(parsed.Opaque) <= vkMaxOpaqueBytes && vkVKAudioPattern.MatchString(parsed.Opaque)
}
func (VKAudioIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !(VKAudioIE{}).Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	raw, err := base64.RawURLEncoding.DecodeString(parsed.Opaque)
	if err != nil || len(raw) > vkMaxOpaqueBytes {
		return Extraction{}, fmt.Errorf("%w: malformed VK audio result", ErrInvalidMetadata)
	}
	var payload vkAudioPayload
	if json.Unmarshal(raw, &payload) != nil || payload.ID == "" || payload.OwnerID == "" {
		return Extraction{}, fmt.Errorf("%w: malformed VK audio result", ErrInvalidMetadata)
	}
	asset, ok := vkAssetURL(payload.URL, vkRoleMedia)
	if !ok {
		return Extraction{}, ErrVKUnavailable
	}
	format := value.NewObject(
		value.Field{Key: "format_id", Value: value.String("audio")}, value.Field{Key: "url", Value: value.String(asset)},
		value.Field{Key: "ext", Value: value.String("m4a")}, value.Field{Key: "protocol", Value: value.String("https")},
		value.Field{Key: "vcodec", Value: value.String("none")}, value.Field{Key: "acodec", Value: value.String("mp3")},
		value.Field{Key: "container", Value: value.String("m4a_dash")}, value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
		value.Field{Key: "_asset_policy", Value: value.String(VKPublicAssetPolicy)},
	)
	info := value.NewObject(value.Field{Key: "id", Value: value.String(payload.OwnerID + "_" + payload.ID)},
		value.Field{Key: "title", Value: value.String(vkAudioTitle(payload.Artist, payload.Title))},
		value.Field{Key: "webpage_url", Value: value.String(vkFirstNonEmpty(payload.WebpageURL, request.URL))},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))})
	if payload.Duration >= 0 {
		info.Set("duration", value.Int(payload.Duration))
	}
	if payload.Uploader != "" {
		info.Set("uploader", value.String(payload.Uploader))
	}
	if payload.Artist != "" {
		info.Set("artist", value.String(payload.Artist))
	}
	if payload.Title != "" {
		info.Set("track", value.String(payload.Title))
	}
	if cover, ok := vkAssetURL(strings.Split(payload.CoverURL, ",")[0], vkRoleThumbnail); ok {
		info.Set("thumbnail", value.String(cover))
		info.Set("thumbnails", value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(cover)}, value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
		))))
	}
	return Media(value.NewInfo(info)), nil
}

// --- VK Play public recordings and optional live HLS --------------------

type VKPlayIE struct{}
type VKPlayLiveIE struct{}

func NewVKPlay() VKPlayIE         { return VKPlayIE{} }
func NewVKPlayLive() VKPlayLiveIE { return VKPlayLiveIE{} }
func (VKPlayIE) Name() string     { return "vkplay" }
func (VKPlayLiveIE) Name() string { return "vkplay_live" }

func parseVKPlayURL(parsed *url.URL, recording bool) (string, string, bool) {
	if parsed == nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Fragment != "" || parsed.RawFragment != "" || parsed.RawPath != "" || parsed.RawQuery != "" ||
		!vkPlayPageHosts[strings.ToLower(parsed.Hostname())] {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
	if recording {
		if len(parts) != 3 && len(parts) != 4 {
			return "", "", false
		}
		if len(parts) == 4 && parts[3] != "records" {
			return "", "", false
		}
		if parts[1] != "record" || !vkUsernamePattern.MatchString(parts[0]) || !vkUUIDPattern.MatchString(parts[2]) {
			return "", "", false
		}
		return parts[0], parts[2], true
	}
	if len(parts) != 1 || !vkUsernamePattern.MatchString(parts[0]) {
		return "", "", false
	}
	return parts[0], "", true
}

func (VKPlayIE) Suitable(parsed *url.URL) bool { _, _, ok := parseVKPlayURL(parsed, true); return ok }
func (VKPlayLiveIE) Suitable(parsed *url.URL) bool {
	_, _, ok := parseVKPlayURL(parsed, false)
	return ok
}

func (VKPlayIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	return extractVKPlay(ctx, request, true)
}
func (VKPlayLiveIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	return extractVKPlay(ctx, request, false)
}

func extractVKPlay(ctx context.Context, request Request, recording bool) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	username, videoID, ok := parseVKPlayURL(parsed, recording)
	if !ok || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	endpoint := "https://api.vkplay.live/v1/blog/" + url.PathEscape(username) + "/public_video_stream"
	if recording {
		endpoint += "/record/" + url.PathEscape(videoID)
	}
	var root map[string]json.RawMessage
	apiErr := vkRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, nil, &root)
	if apiErr != nil {
		if !errors.Is(apiErr, ErrVKUnavailable) {
			return Extraction{}, vkCategorize(apiErr)
		}
		page, pageErr := vkRead(ctx, request.Transport, request.URL, vkMaxPageBytes)
		if pageErr != nil {
			return Extraction{}, vkCategorize(pageErr)
		}
		root, pageErr = vkPlayInitialState(page)
		if pageErr != nil {
			return Extraction{}, pageErr
		}
	}
	data := root
	if nested := vkRawObject(root, "data"); nested != nil {
		if recording {
			if record := vkRawObject(nested, "record"); record != nil {
				data = record
			}
		} else {
			if stream := vkRawObject(nested, "stream"); stream != nil {
				data = stream
			}
		}
	}
	if recording {
		return normalizeVKPlay(ctx, request.URL, videoID, data, false)
	}
	return normalizeVKPlay(ctx, request.URL, username, data, true)
}

func normalizeVKPlay(ctx context.Context, webpageURL, id string, data map[string]json.RawMessage, liveOnly bool) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	formats := vkPlayFormats(data, liveOnly)
	online, _ := vkBool(data, "isOnline")
	if liveOnly && len(formats) == 0 {
		if !online {
			return Extraction{}, ErrVKNotLive
		}
		return Extraction{}, ErrVKUnavailable
	}
	if !liveOnly && len(formats) == 0 {
		return Extraction{}, ErrVKUnavailable
	}
	title := vkString(data, "title")
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: VK Play title missing", ErrInvalidMetadata)
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)}, value.Field{Key: "formats", Value: value.List(formats...)})
	if ts, ok := vkInt(data, "startTime"); ok && ts > 0 {
		info.Set("release_timestamp", value.Int(ts))
	}
	if thumb, ok := vkAssetURL(vkString(data, "previewUrl"), vkRoleThumbnail); ok {
		info.Set("thumbnail", value.String(thumb))
		info.Set("thumbnails", value.List(value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(thumb)}, value.Field{Key: "_credential_isolated", Value: value.Bool(true)}))))
	}
	if duration, ok := vkInt(data, "duration"); ok && duration >= 0 {
		info.Set("duration", value.Int(duration))
	}
	if count := vkRawObject(data, "count"); count != nil {
		for field, key := range map[string]string{"view_count": "views", "like_count": "likes", "concurrent_view_count": "viewers"} {
			if number, ok := vkInt(count, key); ok && number >= 0 {
				info.Set(field, value.Int(number))
			}
		}
	}
	if category := vkRawObject(data, "category"); category != nil && vkString(category, "title") != "" {
		info.Set("categories", value.List(value.String(vkString(category, "title"))))
	}
	user := vkRawObject(data, "user")
	if user == nil {
		user = vkRawObject(data, "owner")
	}
	if user != nil {
		if nick := vkString(user, "nick"); nick != "" {
			info.Set("uploader", value.String(nick))
		}
		if userID := vkString(user, "id"); userID != "" {
			info.Set("uploader_id", value.String(userID))
		}
	}
	info.Set("is_live", value.Bool(online))
	return Media(value.NewInfo(info)), nil
}

func vkPlayFormats(data map[string]json.RawMessage, liveOnly bool) []value.Value {
	var rows []map[string]json.RawMessage
	var rawData []json.RawMessage
	if json.Unmarshal(data["data"], &rawData) == nil && len(rawData) > 0 {
		for _, raw := range rawData {
			var object map[string]json.RawMessage
			if json.Unmarshal(raw, &object) == nil {
				rows = append(rows, object)
			}
		}
	}
	if len(rows) == 0 {
		rows = []map[string]json.RawMessage{data}
	}
	var streams []map[string]json.RawMessage
	for _, row := range rows {
		var urls []map[string]json.RawMessage
		if json.Unmarshal(row["playerUrls"], &urls) == nil {
			streams = append(streams, urls...)
		}
	}
	sort.SliceStable(streams, func(i, j int) bool {
		a, b := vkString(streams[i], "type"), vkString(streams[j], "type")
		if vkPlayFormatRank(a) != vkPlayFormatRank(b) {
			return vkPlayFormatRank(a) < vkPlayFormatRank(b)
		}
		return vkString(streams[i], "url") < vkString(streams[j], "url")
	})
	result := make([]value.Value, 0, minInt(len(streams), vkMaxFormats))
	seen := map[string]bool{}
	for _, stream := range streams {
		rawURL := vkString(stream, "url")
		typ := strings.ToLower(vkString(stream, "type"))
		if liveOnly && typ != "live_hls" && typ != "hls" && !strings.Contains(strings.ToLower(rawURL), ".m3u8") {
			continue
		}
		role := vkRoleMedia
		protocol := "https"
		ext := "mp4"
		if typ == "hls" || typ == "live_hls" || strings.Contains(strings.ToLower(rawURL), ".m3u8") {
			role, protocol, ext = vkRoleManifest, "m3u8_native", "mp4"
		}
		if typ == "dash" || strings.Contains(strings.ToLower(rawURL), ".mpd") {
			role, protocol, ext = vkRoleManifest, "http_dash_segments", "mp4"
		}
		validated, ok := vkPlayAssetURL(rawURL, role)
		if !ok || seen[validated] {
			continue
		}
		format := value.NewObject(value.Field{Key: "format_id", Value: value.String(vkFirstNonEmpty(typ, "direct"))}, value.Field{Key: "url", Value: value.String(validated)}, value.Field{Key: "ext", Value: value.String(ext)}, value.Field{Key: "protocol", Value: value.String(protocol)}, value.Field{Key: "_credential_isolated", Value: value.Bool(true)}, value.Field{Key: "_asset_policy", Value: value.String(VKPlayPublicAssetPolicy)})
		if width, ok := vkInt(stream, "width"); ok && width > 0 {
			format.Set("width", value.Int(width))
		}
		if height, ok := vkInt(stream, "height"); ok && height > 0 {
			format.Set("height", value.Int(height))
		}
		result = append(result, value.ObjectValue(format))
		seen[validated] = true
	}
	return result
}

func vkPlayFormatRank(kind string) int {
	switch strings.ToLower(kind) {
	case "live_hls", "hls":
		return 0
	case "dash":
		return 1
	default:
		return 2
	}
}

func vkPlayAssetURL(rawURL string, role vkAssetRole) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Host == "" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "vkplay.live" && !strings.HasSuffix(host, ".vkplay.live") &&
		host != "live.vkplay.ru" && !strings.HasSuffix(host, ".vkplay.ru") &&
		host != "live.vkvideo.ru" && !strings.HasSuffix(host, ".vkvideo.ru") {
		return "", false
	}
	if !strings.HasPrefix(parsed.EscapedPath(), "/") {
		return "", false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2e") || strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
		return "", false
	}
	if role == vkRoleThumbnail && !strings.Contains(strings.ToLower(parsed.Path), "preview") {
		return "", false
	}
	return parsed.String(), true
}

func vkPlayInitialState(page []byte) (map[string]json.RawMessage, error) {
	marker := bytes.Index(page, []byte(`id="initial-state"`))
	if marker < 0 {
		marker = bytes.Index(page, []byte(`id='initial-state'`))
	}
	if marker < 0 {
		return nil, fmt.Errorf("%w: VK Play initial state missing", ErrInvalidMetadata)
	}
	raw, _, err := extractJSONObjectFrom(page, marker, 256)
	if err != nil {
		return nil, fmt.Errorf("%w: VK Play initial state malformed", ErrInvalidMetadata)
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return nil, fmt.Errorf("%w: VK Play initial state malformed", ErrInvalidMetadata)
	}
	return root, nil
}

// --- Shared isolated request and JSON helpers ----------------------------

func vkRead(ctx context.Context, transport Transport, rawURL string, maxBytes int64) ([]byte, error) {
	return vkRequest(ctx, transport, http.MethodGet, rawURL, nil, nil, maxBytes)
}

func vkPostPayload(ctx context.Context, transport Transport, endpoint string, form url.Values, referer string) ([]any, error) {
	form.Set("al", "1")
	headers := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "X-Requested-With": {"XMLHttpRequest"}}
	// VK's public AJAX endpoints require their own exact endpoint as the
	// Referer. A playlist child may carry its wall-page Referer for transparent
	// re-entry, but it is never substituted into this API header.
	if referer != "" && !vkPublicReferer(referer) {
		return nil, fmt.Errorf("%w: invalid VK child referer", ErrInvalidMetadata)
	}
	headers.Set("Referer", endpoint)
	body, err := vkRequest(ctx, transport, http.MethodPost, endpoint, []byte(form.Encode()), headers, vkMaxJSONBytes)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Payload []json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Payload) == 0 {
		return nil, fmt.Errorf("%w: VK API payload malformed", ErrInvalidMetadata)
	}
	code := vkRawString(envelope.Payload[0])
	if code != "" && code != "0" {
		switch code {
		case "3", "4":
			return nil, ErrVKAuthentication
		case "6":
			return nil, ErrVKRateLimited
		default:
			return nil, ErrVKUnavailable
		}
	}
	values := make([]any, 0, len(envelope.Payload)-1)
	for _, raw := range envelope.Payload[1:] {
		var decoded any
		if json.Unmarshal(raw, &decoded) != nil {
			return nil, fmt.Errorf("%w: VK API payload malformed", ErrInvalidMetadata)
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) > 0 && trimmed[0] == '{' {
			var object map[string]json.RawMessage
			if json.Unmarshal(raw, &object) != nil {
				return nil, fmt.Errorf("%w: VK API payload malformed", ErrInvalidMetadata)
			}
			values = append(values, object)
			continue
		}
		values = append(values, decoded)
	}
	return values, nil
}

func vkRequestJSON(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	response, err := vkRequest(ctx, transport, method, rawURL, body, headers, vkMaxJSONBytes)
	if err != nil {
		return err
	}
	if json.Unmarshal(response, target) != nil {
		return fmt.Errorf("%w: VK JSON response malformed", ErrInvalidMetadata)
	}
	return nil
}

func vkRequest(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, maxBytes int64) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid VK request", ErrInvalidMetadata)
	}
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	var execute func(context.Context, *http.Request) (*http.Response, error) = isolated.DoWithoutCredentialsNoRedirect
	if request.Header.Get("Referer") != "" {
		refererTransport, ok := transport.(RefererCredentialIsolatedNoRedirectTransport)
		if !ok {
			return nil, ErrTransportIsolation
		}
		execute = refererTransport.DoWithoutCredentialsNoRedirectWithReferer
	}
	response, err := execute(ctx, request)
	if err != nil {
		return nil, vkCategorize(err)
	}
	if response == nil || response.Body == nil {
		return nil, ErrVKNetwork
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, vkStatusError(response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		if contextError(ctx) != nil {
			return nil, contextError(ctx)
		}
		return nil, ErrVKNetwork
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrJSONResponseTooLarge
	}
	return data, nil
}

func vkStatusError(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %w", ErrVKAuthentication, &HTTPStatusError{Code: status})
	case http.StatusNotFound, http.StatusGone:
		return fmt.Errorf("%w: %w", ErrVKUnavailable, &HTTPStatusError{Code: status})
	case http.StatusUnavailableForLegalReasons:
		return fmt.Errorf("%w: %w", ErrVKRegionRestricted, &HTTPStatusError{Code: status})
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: %w", ErrVKRateLimited, &HTTPStatusError{Code: status})
	case http.StatusRequestTimeout, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Errorf("%w: %w", ErrVKNetwork, &HTTPStatusError{Code: status})
	default:
		return fmt.Errorf("%w: %w", ErrVKInvalidStatus, &HTTPStatusError{Code: status})
	}
}

func vkCategorize(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrTransportIsolation) || errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) ||
		errors.Is(err, ErrPlaylistLimit) || errors.Is(err, ErrVKAuthentication) || errors.Is(err, ErrVKUnavailable) ||
		errors.Is(err, ErrVKRegionRestricted) || errors.Is(err, ErrVKRateLimited) || errors.Is(err, ErrVKNetwork) || errors.Is(err, ErrVKNotLive) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return vkStatusError(status.Code)
	}
	return ErrVKNetwork
}

func vkPublicReferer(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || !vkURLCommon(parsed, vkPageHosts) {
		return false
	}
	if strings.HasPrefix(parsed.Path, "/wall") {
		_, ok := parseVKWallURL(parsed)
		return ok
	}
	return false
}

func vkRawObject(object map[string]json.RawMessage, key string) map[string]json.RawMessage {
	var child map[string]json.RawMessage
	if json.Unmarshal(object[key], &child) != nil {
		return nil
	}
	return child
}

func vkString(object map[string]json.RawMessage, key string) string {
	if object == nil {
		return ""
	}
	var stringValue string
	if json.Unmarshal(object[key], &stringValue) == nil {
		return stringValue
	}
	return vkRawString(object[key])
}

func vkRawString(raw json.RawMessage) string {
	var stringValue string
	if json.Unmarshal(raw, &stringValue) == nil {
		return stringValue
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

func vkRawNumber(raw json.RawMessage) string { return vkRawString(raw) }

func vkInt(object map[string]json.RawMessage, key string) (int64, bool) {
	value := vkRawString(object[key])
	if value == "" {
		return 0, false
	}
	number, err := strconv.ParseInt(value, 10, 64)
	return number, err == nil
}

func vkIntDefault(object map[string]json.RawMessage, key string) int64 {
	number, _ := vkInt(object, key)
	return number
}

func vkBool(object map[string]json.RawMessage, key string) (bool, bool) {
	var boolean bool
	if json.Unmarshal(object[key], &boolean) == nil {
		return boolean, true
	}
	if number, ok := vkInt(object, key); ok {
		return number != 0, true
	}
	return false, false
}

func vkFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func vkAudioTitle(artist, title string) string {
	switch {
	case artist != "" && title != "":
		return artist + " - " + title
	case artist != "":
		return artist
	default:
		return title
	}
}
func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func vkHTMLClassText(page []byte, class string) string {
	pattern := regexp.MustCompile(`(?s)<[^>]+class=["'][^"']*` + regexp.QuoteMeta(class) + `[^"']*["'][^>]*>(.*?)</[^>]+>`)
	match := pattern.FindSubmatch(page)
	if match == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(string(match[1]), "")))
}

func vkHTMLTitle(page []byte) string {
	match := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindSubmatch(page)
	if match == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(string(match[1])))
}

func vkHTMLElementByID(page []byte, id string) []byte {
	pattern := regexp.MustCompile(`(?is)<[^>]+id=["']` + regexp.QuoteMeta(id) + `["'][^>]*>(.*?)</[^>]+>`)
	match := pattern.FindSubmatch(page)
	if match == nil {
		return page
	}
	return match[1]
}
