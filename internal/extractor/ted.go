package extractor

// TED public family. The route and metadata shapes are based on the pinned
// yt-dlp implementation at aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8
// (yt_dlp/extractor/ted.py). This port deliberately stops at anonymous public
// pages and attributable TED playback assets.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/protocol/hls"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	tedMaxURLBytes      = 8 << 10
	tedMaxPageBytes     = 8 << 20
	tedMaxJSONBytes     = 8 << 20
	tedMaxIDBytes       = 256
	tedMaxLanguageBytes = 64
	tedMaxEntries       = 10000
	tedMaxFormats       = 256
	tedMaxSubtitles     = 128
	tedMaxChapters      = 256
	tedMaxQueryParams   = 32
)

var (
	tedIDPattern           = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,255}$`)
	tedLanguagePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	tedSeasonPattern       = regexp.MustCompile(`^season_(\d+)$`)
	tedDatePattern         = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})`)
	tedExternalCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{6,32}$`)
	tedNextDataMarker      = regexp.MustCompile(`(?is)<script\b[^>]*\bid\s*=\s*["']__NEXT_DATA__["'][^>]*>`)
	tedMetaPattern         = regexp.MustCompile(`(?is)<meta\b[^>]*(?:property|name)\s*=\s*["']([^"']+)["'][^>]*content\s*=\s*["']([^"']*)["'][^>]*>`)
	tedMetaAltPattern      = regexp.MustCompile(`(?is)<meta\b[^>]*content\s*=\s*["']([^"']*)["'][^>]*(?:property|name)\s*=\s*["']([^"']+)["'][^>]*>`)
	ErrTedRateLimited      = errors.New("TED rate limited")
	ErrTedNetwork          = errors.New("TED service unavailable")
	ErrTedRedirect         = errors.New("TED redirect refused")
)

type tedKind uint8

const (
	tedTalkKind tedKind = iota + 1
	tedSeriesKind
	tedPlaylistKind
)

type tedTarget struct {
	kind      tedKind
	slug      string
	numeric   string
	language  string
	season    string
	canonical string
	pageURL   string
}

// TED's public assets are served by these role-specific, attributable hosts.
// HLS manifests may come from the stream or download host; direct media and
// HLS segments are download-host resources; thumbnails use the image CDN.
// Signed query strings are intentionally never decoded and re-encoded.
var tedRoleHosts = map[string][]string{
	"manifest":  {"hls.ted.com", "download.ted.com"},
	"media":     {"download.ted.com"},
	"segment":   {"download.ted.com"},
	"subtitle":  {"download.ted.com", "hls.ted.com"},
	"thumbnail": {"pi.tedcdn.com"},
}

func NewTedTalk() TedTalkIE           { return TedTalkIE{} }
func NewTedSeries() TedSeriesIE       { return TedSeriesIE{} }
func NewTedPlaylist() TedPlaylistIE   { return TedPlaylistIE{} }
func NewTedEmbed() TedEmbedIE         { return TedEmbedIE{} }
func NewTedTalkIE() TedTalkIE         { return NewTedTalk() }
func NewTedSeriesIE() TedSeriesIE     { return NewTedSeries() }
func NewTedPlaylistIE() TedPlaylistIE { return NewTedPlaylist() }
func NewTedEmbedIE() TedEmbedIE       { return NewTedEmbed() }

type TedTalkIE struct{}

func (TedTalkIE) Name() string { return "ted_talk" }
func (TedTalkIE) Suitable(parsed *url.URL) bool {
	target, ok := parseTedTarget(parsed, tedTalkKind, false)
	return ok && target.kind == tedTalkKind
}

func (TedTalkIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseTedTarget(parsed, tedTalkKind, false)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	page, err := tedRead(ctx, request.Transport, http.MethodGet, target.pageURL, http.Header{
		"Accept": {"text/html,application/xhtml+xml"},
	})
	if err != nil {
		return Extraction{}, tedPageError(err)
	}
	data, err := tedNextData(page)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: TED Next data", ErrInvalidMetadata)
	}
	video, ok := tedObject(data, "props", "pageProps", "videoData")
	if !ok {
		return Extraction{}, fmt.Errorf("%w: missing TED videoData", ErrInvalidMetadata)
	}
	return tedTalkExtraction(ctx, request.Transport, target, page, video)
}

type TedSeriesIE struct{}

func (TedSeriesIE) Name() string { return "ted_series" }
func (TedSeriesIE) Suitable(parsed *url.URL) bool {
	_, ok := parseTedTarget(parsed, tedSeriesKind, true)
	return ok
}

func (TedSeriesIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseTedTarget(parsed, tedSeriesKind, true)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	page, err := tedRead(ctx, request.Transport, http.MethodGet, target.pageURL, http.Header{
		"Accept": {"text/html,application/xhtml+xml"},
	})
	if err != nil {
		return Extraction{}, tedPageError(err)
	}
	root, err := tedNextData(page)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: TED Next data", ErrInvalidMetadata)
	}
	pageProps, ok := tedObject(root, "props", "pageProps")
	if !ok {
		return Extraction{}, fmt.Errorf("%w: missing TED series pageProps", ErrInvalidMetadata)
	}
	series, _ := tedObject(pageProps, "series")
	seriesID := tedString(series, "id")
	seriesName := tedString(series, "name")
	if seriesName == "" {
		seriesName = tedMeta(page, "og:title")
	}
	if seriesName == "" {
		seriesName = target.slug
	}
	entries := make([]Entry, 0)
	seasons, ok := tedArray(pageProps, "seasons")
	if !ok {
		return Extraction{}, fmt.Errorf("%w: missing TED seasons", ErrInvalidMetadata)
	}
	for _, rawSeason := range seasons {
		season, ok := rawSeason.(map[string]any)
		if !ok {
			continue
		}
		seasonNumber := tedInt(season, "seasonNumber")
		if target.season != "" && strconv.Itoa(int(seasonNumber)) != target.season {
			continue
		}
		playlist, _ := season["videos"].(map[string]any)
		rows, _ := playlist["nodes"].([]any)
		part, err := tedPlaylistEntries(rows)
		if err != nil {
			return Extraction{}, err
		}
		entries = append(entries, part...)
	}
	if len(entries) > tedMaxEntries {
		return Extraction{}, ErrPlaylistLimit
	}
	playlistID := seriesID
	playlistTitle := seriesName
	seasonNumber := int64(0)
	if target.season != "" {
		playlistID += "_" + target.season
		playlistTitle += " Season " + target.season
		seasonNumber, _ = strconv.ParseInt(target.season, 10, 64)
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(playlistID)},
		value.Field{Key: "title", Value: value.String(playlistTitle)},
		value.Field{Key: "series", Value: value.String(seriesName)},
		value.Field{Key: "webpage_url", Value: value.String(parsed.String())},
	)
	if description := tedMeta(page, "og:description"); description != "" {
		info.Set("description", value.String(description))
	}
	if seasonNumber > 0 {
		info.Set("season_number", value.Int(seasonNumber))
	}
	return Playlist(value.NewInfo(info), StaticEntries(entries...))
}

type TedPlaylistIE struct{}

func (TedPlaylistIE) Name() string { return "ted_playlist" }
func (TedPlaylistIE) Suitable(parsed *url.URL) bool {
	_, ok := parseTedTarget(parsed, tedPlaylistKind, false)
	return ok
}

func (TedPlaylistIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseTedTarget(parsed, tedPlaylistKind, false)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	page, err := tedRead(ctx, request.Transport, http.MethodGet, target.pageURL, http.Header{
		"Accept": {"text/html,application/xhtml+xml"},
	})
	if err != nil {
		return Extraction{}, tedPageError(err)
	}
	root, err := tedNextData(page)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: TED Next data", ErrInvalidMetadata)
	}
	playlist, ok := tedObject(root, "props", "pageProps", "playlist")
	if !ok {
		return Extraction{}, fmt.Errorf("%w: missing TED playlist", ErrInvalidMetadata)
	}
	rows, _ := playlist["videos"].(map[string]any)
	nodes, _ := rows["nodes"].([]any)
	entries, err := tedPlaylistEntries(nodes)
	if err != nil {
		return Extraction{}, err
	}
	playlistID := tedString(playlist, "id")
	if playlistID == "" {
		playlistID = target.numeric
	}
	title := tedString(playlist, "title")
	if title == "" {
		title = strings.TrimSuffix(tedMeta(page, "og:title"), " | TED Talks")
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(playlistID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(parsed.String())},
	)
	if description := tedMeta(page, "og:description"); description != "" {
		info.Set("description", value.String(description))
	}
	return Playlist(value.NewInfo(info), StaticEntries(entries...))
}

type TedEmbedIE struct{}

func (TedEmbedIE) Name() string { return "ted_embed" }
func (TedEmbedIE) Suitable(parsed *url.URL) bool {
	return tedEmbedCanonical(parsed) != ""
}

func (TedEmbedIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	canonical := tedEmbedCanonical(parsed)
	if canonical == "" {
		return Extraction{}, ErrUnsupported
	}
	canonicalParsed, err := url.Parse(canonical)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseTedTarget(canonicalParsed, tedTalkKind, false)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return URLResult(Entry{URL: canonical, ExtractorKey: "ted_talk", ID: target.slug, Transparent: true})
}

func parseTedTarget(parsed *url.URL, requested tedKind, allowSeason bool) (tedTarget, bool) {
	if parsed == nil || len(parsed.String()) == 0 || len(parsed.String()) > tedMaxURLBytes ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Port() != "" ||
		parsed.Fragment != "" && !allowSeason || parsed.RawFragment != "" && !allowSeason || parsed.ForceQuery {
		return tedTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "ted.com" && host != "www.ted.com" {
		return tedTarget{}, false
	}
	if !tedQueryOK(parsed.RawQuery) || !tedPathSafe(parsed.EscapedPath()) {
		return tedTarget{}, false
	}
	parts, ok := tedPathParts(parsed.EscapedPath())
	if !ok || len(parts) == 0 {
		return tedTarget{}, false
	}
	kind := tedKind(0)
	switch parts[0] {
	case "talks":
		kind = tedTalkKind
	case "series":
		kind = tedSeriesKind
	case "playlists":
		kind = tedPlaylistKind
	default:
		return tedTarget{}, false
	}
	if requested != 0 && kind != requested {
		return tedTarget{}, false
	}
	if parsed.RawFragment != "" {
		return tedTarget{}, false
	}
	if parsed.Fragment != "" && !(allowSeason && kind == tedSeriesKind) {
		return tedTarget{}, false
	}
	slug, language, numeric, ok := tedRouteIdentity(kind, parts)
	if !ok {
		return tedTarget{}, false
	}
	season := ""
	if allowSeason && kind == tedSeriesKind && parsed.Fragment != "" {
		match := tedSeasonPattern.FindStringSubmatch(parsed.Fragment)
		if len(match) != 2 || match[1] == "" {
			return tedTarget{}, false
		}
		season = match[1]
	}
	page := *parsed
	page.Fragment, page.RawFragment = "", ""
	return tedTarget{
		kind: kind, slug: slug, numeric: numeric, language: language, season: season,
		canonical: parsed.String(), pageURL: page.String(),
	}, true
}

func tedRouteIdentity(kind tedKind, parts []string) (slug, language, numeric string, ok bool) {
	switch kind {
	case tedTalkKind, tedSeriesKind:
		switch len(parts) {
		case 2:
			slug = parts[1]
		case 4:
			if parts[1] != "lang" {
				return "", "", "", false
			}
			language, slug = parts[2], parts[3]
		default:
			return "", "", "", false
		}
	case tedPlaylistKind:
		switch len(parts) {
		case 2:
			slug = parts[1]
		case 3:
			if !tedDigits(parts[1]) {
				return "", "", "", false
			}
			numeric, slug = parts[1], parts[2]
		case 4:
			if parts[1] != "lang" {
				return "", "", "", false
			}
			language, slug = parts[2], parts[3]
		case 5:
			if !tedDigits(parts[1]) || parts[2] != "lang" {
				return "", "", "", false
			}
			numeric, language, slug = parts[1], parts[3], parts[4]
		default:
			return "", "", "", false
		}
	default:
		return "", "", "", false
	}
	if !tedIDPattern.MatchString(slug) || len(slug) > tedMaxIDBytes {
		return "", "", "", false
	}
	if language != "" && (!tedLanguagePattern.MatchString(language) || len(language) > tedMaxLanguageBytes) {
		return "", "", "", false
	}
	if numeric != "" && len(numeric) > tedMaxIDBytes {
		return "", "", "", false
	}
	return slug, language, numeric, true
}

func tedEmbedCanonical(parsed *url.URL) string {
	if parsed == nil || len(parsed.String()) == 0 || len(parsed.String()) > tedMaxURLBytes ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Port() != "" ||
		parsed.RawFragment != "" || parsed.Fragment != "" || parsed.ForceQuery || !tedQueryOK(parsed.RawQuery) ||
		!tedPathSafe(parsed.EscapedPath()) {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "embed.ted.com" && host != "embed-ssl.ted.com" {
		return ""
	}
	// The embed and public route matrices intentionally overlap exactly. The
	// canonical target is checked before returning so an embed never becomes an
	// arbitrary public TED URL handoff.
	copy := *parsed
	copy.Host = "www.ted.com"
	if _, ok := parseTedTarget(&copy, tedTalkKind, false); !ok {
		return ""
	}
	return copy.String()
}

func tedPathParts(escaped string) ([]string, bool) {
	if escaped == "" || !strings.HasPrefix(escaped, "/") || strings.HasPrefix(escaped, "//") {
		return nil, false
	}
	if strings.HasSuffix(escaped, "/") {
		return nil, false
	}
	raw := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
	for _, part := range raw {
		if part == "" || strings.Contains(part, "%") {
			return nil, false
		}
	}
	return raw, true
}

func tedPathSafe(escaped string) bool {
	lower := strings.ToLower(escaped)
	return escaped != "" && !strings.ContainsAny(escaped, "\\\x00\r\n") &&
		!strings.Contains(lower, "%2f") && !strings.Contains(lower, "%5c") &&
		!strings.Contains(lower, "%00") && !strings.Contains(lower, "%25")
}

func tedQueryOK(raw string) bool {
	if raw == "" {
		return true
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) > tedMaxQueryParams {
		return false
	}
	for key, entries := range values {
		if key == "" || len(entries) != 1 || len(key) > 256 || len(entries[0]) > tedMaxURLBytes {
			return false
		}
	}
	return true
}

func tedDigits(input string) bool {
	if input == "" {
		return false
	}
	for _, r := range input {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func tedPlaylistEntries(nodes []any) ([]Entry, error) {
	entries := make([]Entry, 0, len(nodes))
	for _, raw := range nodes {
		if len(entries) >= tedMaxEntries {
			return nil, ErrPlaylistLimit
		}
		node, ok := raw.(map[string]any)
		if !ok || tedString(node, "__typename") != "Video" {
			continue
		}
		canonical := tedString(node, "canonicalUrl")
		parsed, err := url.Parse(canonical)
		if err != nil {
			continue
		}
		target, ok := parseTedTarget(parsed, tedTalkKind, false)
		if !ok {
			continue
		}
		entry := Entry{URL: target.canonical, ExtractorKey: "ted_talk", ID: tedString(node, "id"), Title: tedString(node, "title")}
		if entry.ID == "" {
			entry.ID = tedString(node, "videoId")
		}
		if thumbnail := tedString(node, "thumbnail"); tedURLRole(thumbnail, "thumbnail") {
			entry.Thumbnail = thumbnail
		}
		if duration := tedNumber(node, "duration"); duration > 0 {
			entry.Duration, entry.HasDuration = duration, true
		}
		if views := tedInt(node, "viewedCount"); views >= 0 && tedPresent(node, "viewedCount") {
			entry.ViewCount, entry.HasViewCount = views, true
		}
		if timestamp := tedTimestamp(tedString(node, "publishedAt")); timestamp > 0 {
			entry.Timestamp, entry.HasTimestamp = timestamp, true
		}
		if language := tedString(node, "language"); tedLanguagePattern.MatchString(language) {
			entry.Language = language
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func tedTalkExtraction(ctx context.Context, transport Transport, target tedTarget, page []byte, video map[string]any) (Extraction, error) {
	videoID := tedString(video, "id")
	if videoID == "" || len(videoID) > tedMaxIDBytes {
		return Extraction{}, fmt.Errorf("%w: missing TED video id", ErrInvalidMetadata)
	}
	player := map[string]any{}
	if raw := tedString(video, "playerData"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &player); err != nil {
			return Extraction{}, fmt.Errorf("%w: invalid TED playerData", ErrInvalidMetadata)
		}
	} else if rawPlayer, ok := video["playerData"].(map[string]any); ok {
		player = rawPlayer
	}
	formats := make([]value.Value, 0)
	seenFormats := make(map[string]bool)
	subtitles := make(map[string][]value.Value)
	addFormat := func(format *value.Object) {
		if format == nil || len(formats) >= tedMaxFormats {
			return
		}
		rawURL, _ := format.Lookup("url").StringValue()
		if rawURL == "" || seenFormats[rawURL] {
			return
		}
		seenFormats[rawURL] = true
		formats = append(formats, value.ObjectValue(format))
	}
	resources, _ := player["resources"].(map[string]any)
	for formatID, rawResources := range resources {
		if strings.EqualFold(formatID, "hls") {
			resource, _ := rawResources.(map[string]any)
			stream := tedString(resource, "stream")
			if tedURLRole(stream, "manifest") {
				addFormat(tedManifestFormat("hls", stream))
				if err := tedParseHLS(ctx, transport, stream, &formats, subtitles, seenFormats); err != nil {
					return Extraction{}, err
				}
			}
			continue
		}
		rows, _ := rawResources.([]any)
		if !strings.EqualFold(formatID, "h264") {
			continue
		}
		for _, raw := range rows {
			resource, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			mediaURL := tedString(resource, "file")
			if !tedURLRole(mediaURL, "media") {
				continue
			}
			bitrate := tedInt(resource, "bitrate")
			id := formatID
			if bitrate > 0 {
				id += "-" + strconv.FormatInt(bitrate, 10) + "k"
			}
			format := tedHTTPFormat(id, mediaURL)
			if bitrate > 0 {
				format.Set("tbr", value.Float(float64(bitrate)))
			}
			if width := tedInt(resource, "width"); width > 0 {
				format.Set("width", value.Int(width))
			}
			if height := tedInt(resource, "height"); height > 0 {
				format.Set("height", value.Int(height))
			}
			addFormat(format)
		}
	}
	if audio := tedString(video, "audioDownload"); tedURLRole(audio, "media") {
		format := tedHTTPFormat("audio", audio)
		format.Set("vcodec", value.String("none"))
		addFormat(format)
	}
	tedInlineSubtitles(player, subtitles)
	thumbnail := tedString(player, "thumb")
	if !tedURLRole(thumbnail, "thumbnail") {
		thumbnail = tedMeta(page, "og:image")
	}
	if !tedURLRole(thumbnail, "thumbnail") {
		thumbnail = ""
	}
	if len(formats) == 0 {
		if external := tedSafeExternal(player); external != "" {
			return URLResult(Entry{URL: external, ExtractorKey: "youtube", ID: videoID, Transparent: true})
		}
		return Extraction{}, ErrUnavailable
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "title", Value: value.String(tedFirstNonEmpty(tedString(video, "title"), tedMeta(page, "og:title")))},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	if description := tedFirstNonEmpty(tedString(video, "description"), tedMeta(page, "og:description")); description != "" {
		info.Set("description", value.String(description))
	}
	if uploader := tedFirstNonEmpty(tedString(video, "presenterDisplayName"), tedString(video, "presenterName")); uploader != "" {
		info.Set("uploader", value.String(uploader))
	}
	if duration := tedNumber(video, "duration"); duration > 0 {
		info.Set("duration", value.Float(duration))
	}
	if views := tedInt(video, "viewedCount"); tedPresent(video, "viewedCount") && views >= 0 {
		info.Set("view_count", value.Int(views))
	}
	if thumbnail != "" {
		info.Set("thumbnail", value.String(thumbnail))
		info.Set("thumbnails", value.List(value.ObjectValue(tedThumbnail(thumbnail, "0"))))
	}
	if language := tedFirstNonEmpty(tedString(video, "language"), tedString(player, "language")); tedLanguagePattern.MatchString(language) {
		info.Set("language", value.String(language))
	}
	if tags := tedTags(player); len(tags) > 0 {
		values := make([]value.Value, 0, len(tags))
		for _, tag := range tags {
			values = append(values, value.String(tag))
		}
		info.Set("tags", value.List(values...))
	}
	if timestamp := tedTimestamp(tedString(video, "publishedAt")); timestamp > 0 {
		info.Set("timestamp", value.Int(timestamp))
	}
	if date := tedDate(tedString(video, "publishedAt")); date != "" {
		info.Set("upload_date", value.String(date))
	}
	if date := tedDate(tedString(video, "recordedOn")); date != "" {
		info.Set("release_date", value.String(date))
	}
	if series := tedFirstNonEmpty(tedString(video, "series"), tedString(video, "seriesName")); series != "" {
		info.Set("series", value.String(series))
	}
	if chapters := tedChapters(video); len(chapters) > 0 {
		info.Set("chapters", value.List(chapters...))
	}
	if len(subtitles) > 0 {
		subtitleObject := value.NewObject()
		langs := make([]string, 0, len(subtitles))
		for language := range subtitles {
			langs = append(langs, language)
		}
		sort.Strings(langs)
		for _, language := range langs {
			subtitleObject.Set(language, value.List(subtitles[language]...))
		}
		info.Set("subtitles", value.ObjectValue(subtitleObject))
	}
	return Media(value.NewInfo(info)), nil
}

func tedParseHLS(ctx context.Context, transport Transport, rawURL string, formats *[]value.Value, subtitles map[string][]value.Value, seen map[string]bool) error {
	body, err := tedRead(ctx, transport, http.MethodGet, rawURL, http.Header{"Accept": {"application/vnd.apple.mpegurl,application/x-mpegURL"}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		// The pinned Python extractor uses fatal=False for HLS discovery. Keep
		// the master format usable when a public manifest is transiently absent.
		return nil
	}
	playlist, err := hls.Parse(rawURL, body)
	if err != nil {
		return nil
	}
	for index, variant := range playlist.Variants {
		if len(*formats) >= tedMaxFormats || !tedURLRole(variant.URL, "manifest") || seen[variant.URL] {
			continue
		}
		id := "hls"
		if variant.Bandwidth > 0 {
			id += "-" + strconv.FormatInt(variant.Bandwidth/1000, 10) + "k"
		} else {
			id += "-" + strconv.Itoa(index+1)
		}
		format := tedManifestFormat(id, variant.URL)
		if variant.Bandwidth > 0 {
			format.Set("tbr", value.Float(float64(variant.Bandwidth)/1000))
		}
		if variant.Resolution != "" {
			if width, height := tedResolution(variant.Resolution); width > 0 && height > 0 {
				format.Set("width", value.Int(width))
				format.Set("height", value.Int(height))
			}
		}
		seen[variant.URL] = true
		*formats = append(*formats, value.ObjectValue(format))
	}
	renditions, err := hls.ParseMasterSubtitles(rawURL, body)
	if err != nil {
		return nil
	}
	for _, rendition := range renditions {
		if len(subtitles) >= tedMaxSubtitles || !tedURLRole(rendition.URL, "subtitle") {
			continue
		}
		language := strings.ToLower(strings.TrimSpace(rendition.Language))
		if !tedLanguagePattern.MatchString(language) {
			language = "und"
		}
		if len(subtitles[language]) >= 32 {
			continue
		}
		subtitles[language] = append(subtitles[language], value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(rendition.URL)},
			value.Field{Key: "ext", Value: value.String("vtt")},
			value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
			value.Field{Key: "_ted_host_policy", Value: value.String("ted")},
		)))
	}
	return nil
}

func tedInlineSubtitles(player map[string]any, subtitles map[string][]value.Value) {
	items, _ := player["subtitles"].([]any)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rawURL := tedString(item, "url")
		if !tedURLRole(rawURL, "subtitle") {
			continue
		}
		language := strings.ToLower(tedFirstNonEmpty(tedString(item, "language"), tedString(item, "lang")))
		if !tedLanguagePattern.MatchString(language) {
			language = "und"
		}
		if len(subtitles[language]) >= 32 {
			continue
		}
		object := value.NewObject(
			value.Field{Key: "url", Value: value.String(rawURL)},
			value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
			value.Field{Key: "_ted_host_policy", Value: value.String("ted")},
		)
		if ext := tedExtension(rawURL); ext != "" {
			object.Set("ext", value.String(ext))
		}
		subtitles[language] = append(subtitles[language], value.ObjectValue(object))
	}
}

func tedManifestFormat(id, rawURL string) *value.Object {
	object := manifestFormat(id, rawURL, "m3u8_native")
	object.Set("_credential_isolated", value.Bool(true))
	object.Set("_ted_host_policy", value.String("ted"))
	return object
}

func tedHTTPFormat(id, rawURL string) *value.Object {
	object := value.NewObject(
		value.Field{Key: "format_id", Value: value.String(id)},
		value.Field{Key: "url", Value: value.String(rawURL)},
		value.Field{Key: "ext", Value: value.String(tedExtension(rawURL))},
		value.Field{Key: "protocol", Value: value.String("https")},
		value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
		value.Field{Key: "_ted_host_policy", Value: value.String("ted")},
	)
	return object
}

func tedThumbnail(rawURL, id string) *value.Object {
	return value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "url", Value: value.String(rawURL)},
		value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
		value.Field{Key: "_ted_host_policy", Value: value.String("ted")},
	)
}

func tedURLRole(rawURL, role string) bool {
	if rawURL == "" || len(rawURL) > tedMaxURLBytes {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawFragment != "" || parsed.Hostname() == "" {
		return false
	}
	if strictPathUnsafe(parsed.EscapedPath()) {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, allowed := range tedRoleHosts[role] {
		if host == allowed {
			return true
		}
	}
	return false
}

// TedAttributableURL is the product-side host-policy seam for credential
// isolated TED playback and sidecar dispatch. It intentionally accepts only
// the role-specific HTTPS host families used by the public extractor.
func TedAttributableURL(rawURL, role string) bool {
	if role == "playback" {
		return tedURLRole(rawURL, "manifest") || tedURLRole(rawURL, "segment")
	}
	return tedURLRole(rawURL, role)
}

func tedRead(ctx context.Context, transport Transport, method, rawURL string, headers http.Header) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid TED request", ErrInvalidMetadata)
	}
	request.Header = headers.Clone()
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("%w: empty TED response", ErrTedNetwork)
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, tedMaxPageBytes+1))
	if readErr != nil {
		return nil, fmt.Errorf("%w: TED response read failed", ErrTedNetwork)
	}
	if int64(len(data)) > tedMaxPageBytes {
		return nil, ErrJSONResponseTooLarge
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, fmt.Errorf("%w: %w", ErrTedRedirect, &HTTPStatusError{Code: response.StatusCode})
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, tedStatusError(response.StatusCode)
	}
	return data, nil
}

func tedStatusError(code int) error {
	status := &HTTPStatusError{Code: code}
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %w", ErrAuthentication, status)
	case http.StatusNotFound, http.StatusGone:
		return fmt.Errorf("%w: %w", ErrUnavailable, status)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: %w", ErrTedRateLimited, status)
	case http.StatusUnavailableForLegalReasons:
		return fmt.Errorf("%w: %w", ErrRegionRestricted, status)
	default:
		if code >= 500 {
			return fmt.Errorf("%w: %w", ErrTedNetwork, status)
		}
		return status
	}
}

// TedStatusError exposes the pinned TED status categorization to the product
// transport boundary without exposing response bodies or URLs.
func TedStatusError(code int) error {
	if code >= 300 && code < 400 {
		return fmt.Errorf("%w: %w", ErrTedRedirect, &HTTPStatusError{Code: code})
	}
	return tedStatusError(code)
}

func tedPageError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrTransportIsolation) || errors.Is(err, ErrTedRateLimited) || errors.Is(err, ErrTedRedirect) || errors.Is(err, ErrAuthentication) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrRegionRestricted) || errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) {
		return err
	}
	return fmt.Errorf("%w: TED page request failed", ErrTedNetwork)
}

func tedNextData(page []byte) (map[string]any, error) {
	location := tedNextDataMarker.FindIndex(page)
	if location == nil {
		return nil, errors.New("missing __NEXT_DATA__")
	}
	raw, _, err := extractJSONObjectFrom(page, location[1], 256)
	if err != nil || len(raw) > tedMaxJSONBytes {
		return nil, errors.New("invalid __NEXT_DATA__")
	}
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil || ensureJSONEOF(decoder) != nil || root == nil {
		return nil, errors.New("invalid __NEXT_DATA__")
	}
	return root, nil
}

func tedObject(root map[string]any, keys ...string) (map[string]any, bool) {
	current := root
	for _, key := range keys {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func tedArray(root map[string]any, key string) ([]any, bool) {
	array, ok := root[key].([]any)
	return array, ok
}

func tedString(root map[string]any, key string) string {
	if root == nil {
		return ""
	}
	value, ok := root[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func tedNumber(root map[string]any, key string) float64 {
	input := tedString(root, key)
	if input == "" {
		return 0
	}
	parsed, _ := strconv.ParseFloat(strings.ReplaceAll(input, ",", ""), 64)
	return parsed
}

func tedInt(root map[string]any, key string) int64 {
	number := tedNumber(root, key)
	if number > float64(^uint64(0)>>1) {
		return 0
	}
	return int64(number)
}

func tedPresent(root map[string]any, key string) bool {
	_, ok := root[key]
	return ok
}

func tedTimestamp(input string) int64 {
	if input == "" {
		return 0
	}
	if number, err := strconv.ParseInt(input, 10, 64); err == nil {
		if number > 2_000_000_000_000 {
			return number / 1000
		}
		return number
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, input); err == nil {
			return parsed.Unix()
		}
	}
	return 0
}

func tedDate(input string) string {
	if match := tedDatePattern.FindStringSubmatch(input); len(match) == 4 {
		return match[1] + match[2] + match[3]
	}
	if timestamp := tedTimestamp(input); timestamp > 0 {
		return time.Unix(timestamp, 0).UTC().Format("20060102")
	}
	return ""
}

func tedMeta(page []byte, name string) string {
	for _, match := range tedMetaPattern.FindAllSubmatch(page, 64) {
		if len(match) == 3 && strings.EqualFold(string(match[1]), name) {
			return strings.TrimSpace(string(match[2]))
		}
	}
	for _, match := range tedMetaAltPattern.FindAllSubmatch(page, 64) {
		if len(match) == 3 && strings.EqualFold(string(match[2]), name) {
			return strings.TrimSpace(string(match[1]))
		}
	}
	return ""
}

func tedFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func tedTags(player map[string]any) []string {
	if targeting, ok := player["targeting"].(map[string]any); ok {
		if raw := tedString(targeting, "tag"); raw != "" {
			parts := strings.Split(raw, ",")
			result := make([]string, 0, len(parts))
			for _, part := range parts {
				if part = strings.TrimSpace(part); part != "" {
					result = append(result, part)
				}
			}
			return result
		}
	}
	return nil
}

func tedChapters(video map[string]any) []value.Value {
	items, _ := video["chapters"].([]any)
	if len(items) == 0 {
		return nil
	}
	chapters := make([]value.Value, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || len(chapters) >= tedMaxChapters {
			continue
		}
		start := tedNumber(item, "start_time")
		if start == 0 {
			start = tedNumber(item, "start")
		}
		end := tedNumber(item, "end_time")
		if end == 0 {
			end = tedNumber(item, "end")
		}
		title := tedString(item, "title")
		if start < 0 || end <= start || title == "" {
			continue
		}
		chapters = append(chapters, value.ObjectValue(value.NewObject(
			value.Field{Key: "start_time", Value: value.Float(start)},
			value.Field{Key: "end_time", Value: value.Float(end)},
			value.Field{Key: "title", Value: value.String(title)},
		)))
	}
	return chapters
}

func tedResolution(input string) (int64, int64) {
	parts := strings.Split(input, "x")
	if len(parts) != 2 {
		return 0, 0
	}
	width, _ := strconv.ParseInt(parts[0], 10, 64)
	height, _ := strconv.ParseInt(parts[1], 10, 64)
	return width, height
}

func tedExtension(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "mp4"
	}
	ext := strings.TrimPrefix(strings.ToLower(path.Ext(parsed.Path)), ".")
	if ext == "" {
		return "mp4"
	}
	if ext == "m3u8" {
		return "mp4"
	}
	return ext
}

func tedSafeExternal(player map[string]any) string {
	external, _ := player["external"].(map[string]any)
	if !strings.EqualFold(tedString(external, "service"), "youtube") {
		return ""
	}
	code := tedString(external, "code")
	if code == "" || !tedExternalCodePattern.MatchString(code) {
		return ""
	}
	return "https://www.youtube.com/watch?v=" + url.QueryEscape(code)
}
