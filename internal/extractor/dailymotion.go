package extractor

// Anonymous Dailymotion public-family extraction. Routes and API fields are
// pinned to yt_dlp/extractor/dailymotion.py at
// aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8. Cookie/login/password, explicit
// age-gated, private, geo-gated, and DRM playback deliberately fail closed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	dailymotionMaxURLBytes         = 8 << 10
	dailymotionMaxRouteQueryBytes  = 4 << 10
	dailymotionMaxRouteQueryFields = 32
	dailymotionMaxFormats          = 256
	dailymotionMaxSubtitles        = 128
	dailymotionMaxThumbnails       = 128
	dailymotionMaxTags             = 256
	dailymotionHostPolicy          = "dailymotion"
)

var (
	ErrDailymotionAgeRestricted = errors.New("Dailymotion age-restricted media is deferred")
	ErrDailymotionRateLimited   = errors.New("Dailymotion rate limited")
	ErrDailymotionNetwork       = errors.New("Dailymotion service unavailable")
	ErrDailymotionRedirect      = errors.New("Dailymotion redirect refused")

	dailymotionID             = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)
	dailymotionPlaylistID     = regexp.MustCompile(`^x[0-9a-z]{1,128}$`)
	dailymotionVideoSlug      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,512}$`)
	dailymotionPlayerID       = regexp.MustCompile(`^[0-9A-Za-z]{1,128}$`)
	dailymotionHTMLTag        = regexp.MustCompile(`(?s)<[^>]*>`)
	dailymotionDimensionsPath = regexp.MustCompile(`/H264-(\d+)x(\d+)(?:-(60)/)?`)
)

type dailymotionTarget struct {
	videoID  string
	playlist string
	webpage  string
}

// Dailymotion implements public video, short-link, embed, crawler, SWF,
// player, and L'Equipe aliases from the pinned DailymotionIE route.
type Dailymotion struct{}

func NewDailymotion() Dailymotion { return Dailymotion{} }
func (Dailymotion) Name() string  { return "dailymotion" }

func (Dailymotion) Suitable(parsed *url.URL) bool {
	_, ok := dailymotionVideoTarget(parsed)
	return ok
}

func (Dailymotion) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := dailymotionVideoTarget(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if target.playlist != "" {
		return URLResult(Entry{
			URL:          "https://www.dailymotion.com/playlist/" + target.playlist,
			ExtractorKey: "dailymotion_playlist",
			ID:           target.playlist,
			Transparent:  true,
		})
	}

	client := newDailymotionDiscoveryClient(request.Transport)
	media, err := dailymotionFetchMedia(ctx, client, target.videoID)
	if err != nil {
		return Extraction{}, categorizeDailymotionError(err)
	}
	if media.XID != target.videoID {
		return Extraction{}, fmt.Errorf("%w: Dailymotion media identity mismatch", ErrInvalidMetadata)
	}
	var metadata dailymotionMetadata
	endpoint := "https://www.dailymotion.com/player/metadata/video/" + url.PathEscape(media.XID) + "?app=com.dailymotion.neon"
	if err := RequestJSONWithoutCredentialsNoRedirect(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &metadata); err != nil {
		return Extraction{}, categorizeDailymotionError(err)
	}
	return normalizeDailymotion(metadata, media, target.videoID, target.webpage)
}

func dailymotionVideoTarget(parsed *url.URL) (dailymotionTarget, bool) {
	if !dailymotionBaseURLSafe(parsed) || parsed.Fragment != "" || parsed.RawFragment != "" {
		return dailymotionTarget{}, false
	}
	query, ok := dailymotionRouteQuery(parsed)
	if !ok {
		return dailymotionTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	parts, ok := dailymotionLiteralPathParts(parsed.EscapedPath())
	if !ok {
		return dailymotionTarget{}, false
	}
	target := dailymotionTarget{webpage: parsed.String()}
	if host == "dai.ly" {
		if len(parts) != 1 {
			return dailymotionTarget{}, false
		}
		target.videoID, ok = dailymotionVideoSegmentID(parts[0])
		if !ok || query.Has("video") {
			return dailymotionTarget{}, false
		}
		if values := query["playlist"]; len(values) == 1 {
			if !dailymotionPlaylistID.MatchString(values[0]) {
				return dailymotionTarget{}, false
			}
			target.playlist = values[0]
		}
		return target, true
	}

	dailymotionHost := dailymotionVideoHostOK(host)
	lequipeHost := host == "lequipe.fr" || host == "www.lequipe.fr"
	if !dailymotionHost && !lequipeHost {
		return dailymotionTarget{}, false
	}
	if videoID, routeOK := dailymotionVideoPathID(parts); routeOK {
		if query.Has("video") {
			return dailymotionTarget{}, false
		}
		target.videoID = videoID
		if values := query["playlist"]; len(values) == 1 {
			if !dailymotionPlaylistID.MatchString(values[0]) {
				return dailymotionTarget{}, false
			}
			target.playlist = values[0]
		}
		return target, true
	}
	if !dailymotionPlayerPath(parts) || !dailymotionPlayerQuery(parsed.RawQuery) {
		return dailymotionTarget{}, false
	}
	videoValues, playlistValues := query["video"], query["playlist"]
	if (len(videoValues) == 1) == (len(playlistValues) == 1) {
		return dailymotionTarget{}, false
	}
	if len(videoValues) == 1 {
		if !dailymotionID.MatchString(videoValues[0]) {
			return dailymotionTarget{}, false
		}
		target.videoID = videoValues[0]
		return target, true
	}
	if !dailymotionPlaylistID.MatchString(playlistValues[0]) {
		return dailymotionTarget{}, false
	}
	target.playlist = playlistValues[0]
	return target, true
}

func dailymotionBaseURLSafe(parsed *url.URL) bool {
	return parsed != nil && len(parsed.String()) <= dailymotionMaxURLBytes &&
		(parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Opaque == "" &&
		parsed.User == nil && parsed.Port() == "" && parsed.Hostname() != "" &&
		!strings.HasSuffix(parsed.Hostname(), ".") && !parsed.ForceQuery
}

func dailymotionRouteQuery(parsed *url.URL) (url.Values, bool) {
	if parsed == nil || len(parsed.RawQuery) > dailymotionMaxRouteQueryBytes {
		return nil, false
	}
	if parsed.RawQuery == "" {
		return make(url.Values), true
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) > dailymotionMaxRouteQueryFields {
		return nil, false
	}
	count := 0
	for key, values := range query {
		count += len(values)
		if key == "" || len(key) > 256 || len(values) != 1 || len(values[0]) > 2048 ||
			strings.ContainsAny(key+values[0], "\x00\r\n") {
			return nil, false
		}
	}
	return query, count <= dailymotionMaxRouteQueryFields
}

func dailymotionLiteralPathParts(escapedPath string) ([]string, bool) {
	lower := strings.ToLower(escapedPath)
	if escapedPath == "" || escapedPath[0] != '/' || strings.HasSuffix(escapedPath, "/") ||
		strings.HasPrefix(escapedPath, "//") || strings.Contains(escapedPath, "//") || strings.Contains(escapedPath, "%") ||
		strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(lower, "%00") {
		return nil, false
	}
	parts := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
	for _, part := range parts {
		if part == "" || strings.ContainsAny(part, "\\\x00\r\n") {
			return nil, false
		}
	}
	return parts, true
}

func dailymotionVideoHostOK(host string) bool {
	return dailymotionLocalizedHostOK(host, "", "www", "touch", "geo")
}

func dailymotionLocalizedHostOK(host string, prefixes ...string) bool {
	if host == "" || strings.HasSuffix(host, ".") {
		return false
	}
	labels := strings.Split(strings.ToLower(host), ".")
	if len(labels) == 2 {
		return labels[0] == "dailymotion" && dailymotionTLD.MatchString(labels[1])
	}
	if len(labels) != 3 || labels[1] != "dailymotion" || !dailymotionTLD.MatchString(labels[2]) {
		return false
	}
	for _, prefix := range prefixes {
		if prefix != "" && labels[0] == prefix {
			return true
		}
	}
	return false
}

func dailymotionVideoSegmentID(segment string) (string, bool) {
	parts := strings.SplitN(segment, "_", 2)
	if !dailymotionID.MatchString(parts[0]) {
		return "", false
	}
	if len(parts) == 2 && !dailymotionVideoSlug.MatchString(parts[1]) {
		return "", false
	}
	return parts[0], true
}

func dailymotionVideoPathID(parts []string) (string, bool) {
	var segment string
	lower := make([]string, len(parts))
	for index := range parts {
		lower[index] = strings.ToLower(parts[index])
	}
	switch {
	case len(parts) == 2 && lower[0] == "video":
		segment = parts[1]
	case len(parts) == 2 && lower[0] == "swf" && lower[1] != "video":
		segment = parts[1]
	case len(parts) == 3 && (lower[0] == "crawler" || lower[0] == "embed" || lower[0] == "swf") && lower[1] == "video":
		segment = parts[2]
	default:
		return "", false
	}
	return dailymotionVideoSegmentID(segment)
}

func dailymotionPlayerPath(parts []string) bool {
	if len(parts) == 1 {
		return strings.EqualFold(parts[0], "player.html")
	}
	if len(parts) != 2 || !strings.EqualFold(parts[0], "player") || !strings.HasSuffix(strings.ToLower(parts[1]), ".html") {
		return false
	}
	return dailymotionPlayerID.MatchString(parts[1][:len(parts[1])-len(".html")])
}

func dailymotionPlayerQuery(rawQuery string) bool {
	if rawQuery == "" {
		return false
	}
	first := rawQuery
	if index := strings.IndexByte(first, '&'); index >= 0 {
		first = first[:index]
	}
	if index := strings.IndexByte(first, '='); index >= 0 {
		first = first[:index]
	}
	key, err := url.QueryUnescape(first)
	return err == nil && (strings.EqualFold(key, "video") || strings.EqualFold(key, "playlist"))
}

func dailymotionVideoID(parsed *url.URL) string {
	target, ok := dailymotionVideoTarget(parsed)
	if !ok {
		return ""
	}
	return target.videoID
}

type dailymotionMedia struct {
	Description string `json:"description"`
	XID         string `json:"xid"`
	Geo         struct {
		Allowed []string `json:"allowed"`
	} `json:"geoblockedCountries"`
	Stats struct {
		Likes struct {
			Total int64 `json:"total"`
		} `json:"likes"`
		Views struct {
			Total int64 `json:"total"`
		} `json:"views"`
	} `json:"stats"`
	AudienceCount int64 `json:"audienceCount"`
	IsOnAir       *bool `json:"isOnAir"`
}

func dailymotionFetchMedia(ctx context.Context, client *dailymotionDiscoveryClient, videoID string) (dailymotionMedia, error) {
	query := fmt.Sprintf(`{
  media(xid: %s) {
    ... on Video {
      description
      geoblockedCountries {
        allowed
      }
      xid
      stats {
        likes {
          total
        }
        views {
          total
        }
      }
    }
    ... on Live {
      description
      geoblockedCountries {
        allowed
      }
      xid
      audienceCount
      isOnAir
    }
  }
}`, dailymotionGraphQLString(videoID))
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return dailymotionMedia{}, fmt.Errorf("%w: encode Dailymotion media request", ErrInvalidMetadata)
	}
	raw, err := client.graphQL(ctx, body)
	if err != nil {
		return dailymotionMedia{}, err
	}
	if dailymotionGraphQLErrorsPresent(raw) {
		return dailymotionMedia{}, ErrUnavailable
	}
	var envelope struct {
		Data *struct {
			Media *dailymotionMedia `json:"media"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return dailymotionMedia{}, fmt.Errorf("%w: malformed Dailymotion media response", ErrInvalidMetadata)
	}
	if envelope.Data == nil || envelope.Data.Media == nil {
		return dailymotionMedia{}, ErrUnavailable
	}
	media := *envelope.Data.Media
	if !dailymotionID.MatchString(media.XID) {
		return dailymotionMedia{}, fmt.Errorf("%w: invalid Dailymotion media identity", ErrInvalidMetadata)
	}
	return media, nil
}

type dailymotionMetadata struct {
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Duration    float64           `json:"duration"`
	CreatedTime int64             `json:"created_time"`
	PosterURL   string            `json:"poster_url"`
	Posters     map[string]string `json:"posters"`
	Thumbnails  map[string]string `json:"thumbnails"`
	Screenname  string            `json:"screenname"`
	Explicit    bool              `json:"explicit"`
	Tags        []string          `json:"tags"`
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

func normalizeDailymotion(metadata dailymotionMetadata, media dailymotionMedia, id, webpage string) (Extraction, error) {
	if metadata.Error.Code != "" || metadata.Error.Title != "" || metadata.Error.Raw != "" {
		if strings.EqualFold(metadata.Error.Code, "DM007") {
			return Extraction{}, ErrRegionRestricted
		}
		if strings.Contains(strings.ToLower(metadata.Error.Title+" "+metadata.Error.Raw), "private") {
			return Extraction{}, ErrAuthentication
		}
		return Extraction{}, ErrUnavailable
	}
	if metadata.Explicit {
		return Extraction{}, fmt.Errorf("%w: %w", ErrDailymotionAgeRestricted, ErrAuthentication)
	}
	if strings.TrimSpace(metadata.Title) == "" {
		return Extraction{}, fmt.Errorf("%w: missing Dailymotion title", ErrInvalidMetadata)
	}
	if len(metadata.Tags) > dailymotionMaxTags || len(metadata.Qualities) > dailymotionMaxFormats || len(metadata.Subtitles.Data) > dailymotionMaxSubtitles {
		return Extraction{}, fmt.Errorf("%w: Dailymotion metadata limit", ErrInvalidMetadata)
	}

	qualities := make([]string, 0, len(metadata.Qualities))
	for quality := range metadata.Qualities {
		qualities = append(qualities, quality)
	}
	sort.Strings(qualities)
	formats := make([]value.Value, 0)
	for _, quality := range qualities {
		for _, item := range metadata.Qualities[quality] {
			if len(formats) >= dailymotionMaxFormats {
				return Extraction{}, fmt.Errorf("%w: Dailymotion format limit", ErrInvalidMetadata)
			}
			kind := strings.ToLower(strings.TrimSpace(item.Type))
			if item.URL == "" || strings.Contains(kind, "lumberjack") || strings.Contains(kind, "dash") {
				continue
			}
			rawURL := dailymotionWithoutFragment(item.URL)
			if strings.EqualFold(path.Ext(mustURLPath(rawURL)), ".mpd") {
				continue
			}
			var format *value.Object
			if kind == "application/x-mpegurl" || strings.EqualFold(path.Ext(mustURLPath(rawURL)), ".m3u8") {
				if !DailymotionAttributableURL(rawURL, "manifest") {
					return Extraction{}, fmt.Errorf("%w: unattributable Dailymotion manifest", ErrInvalidMetadata)
				}
				format = manifestFormat("hls-"+quality, rawURL, "m3u8_native")
			} else {
				if !DailymotionAttributableURL(rawURL, "media") {
					return Extraction{}, fmt.Errorf("%w: unattributable Dailymotion media", ErrInvalidMetadata)
				}
				ext := strings.ToLower(strings.TrimPrefix(path.Ext(mustURLPath(rawURL)), "."))
				if ext == "" {
					ext = "mp4"
				}
				format = value.NewObject(
					value.Field{Key: "format_id", Value: value.String("http-" + quality)},
					value.Field{Key: "url", Value: value.String(rawURL)},
					value.Field{Key: "ext", Value: value.String(ext)},
					value.Field{Key: "protocol", Value: value.String("https")},
				)
			}
			format.Set("_credential_isolated", value.Bool(true))
			format.Set("_host_policy", value.String(dailymotionHostPolicy))
			setPositiveInt(format, "width", item.Width)
			setPositiveInt(format, "height", item.Height)
			if item.Bitrate > 0 {
				format.Set("tbr", value.Float(float64(item.Bitrate)/1000))
			}
			if match := dailymotionDimensionsPath.FindStringSubmatch(mustURLPath(rawURL)); len(match) == 4 {
				if width, parseErr := strconv.ParseInt(match[1], 10, 64); parseErr == nil {
					setPositiveInt(format, "width", width)
				}
				if height, parseErr := strconv.ParseInt(match[2], 10, 64); parseErr == nil {
					setPositiveInt(format, "height", height)
				}
				if match[3] != "" {
					format.Set("fps", value.Int(60))
				}
			}
			if _, ok := format.Lookup("fps").Int(); !ok && strings.HasSuffix(quality, "@60") {
				format.Set("fps", value.Int(60))
			}
			formats = append(formats, value.ObjectValue(format))
		}
	}
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: Dailymotion DRM, gated, or unsupported formats", ErrUnavailable)
	}

	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(metadata.Title)},
		value.Field{Key: "webpage_url", Value: value.String(webpage)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
		value.Field{Key: "age_limit", Value: value.Int(0)},
	)
	description := cleanDailymotionHTML(media.Description)
	if description == "" {
		description = cleanDailymotionHTML(metadata.Description)
	}
	if description != "" {
		info.Set("description", value.String(description))
	}
	if media.IsOnAir != nil {
		info.Set("is_live", value.Bool(*media.IsOnAir))
	}
	setPositiveInt(info, "timestamp", metadata.CreatedTime)
	if metadata.Duration > 0 {
		info.Set("duration", value.Float(metadata.Duration))
	}
	if metadata.Owner.Screenname != "" {
		info.Set("uploader", value.String(metadata.Owner.Screenname))
	}
	uploaderID := metadata.Owner.ID
	if uploaderID == "" {
		uploaderID = metadata.Screenname
	}
	if uploaderID != "" {
		info.Set("uploader_id", value.String(uploaderID))
	}
	if media.Stats.Views.Total > 0 {
		info.Set("view_count", value.Int(media.Stats.Views.Total))
	} else if media.AudienceCount > 0 {
		info.Set("view_count", value.Int(media.AudienceCount))
	}
	if media.Stats.Likes.Total > 0 {
		info.Set("like_count", value.Int(media.Stats.Likes.Total))
	}
	if len(metadata.Tags) > 0 {
		tags := make([]value.Value, 0, len(metadata.Tags))
		for _, tag := range metadata.Tags {
			tags = append(tags, value.String(tag))
		}
		info.Set("tags", value.List(tags...))
	}
	if err := dailymotionSetThumbnails(info, metadata); err != nil {
		return Extraction{}, err
	}
	if subtitles, err := dailymotionSubtitleValues(metadata); err != nil {
		return Extraction{}, err
	} else if subtitles.Len() > 0 {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	return Media(value.NewInfo(info)), nil
}

func dailymotionSubtitleValues(metadata dailymotionMetadata) (*value.Object, error) {
	result := value.NewObject()
	languages := make([]string, 0, len(metadata.Subtitles.Data))
	for language := range metadata.Subtitles.Data {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	total := 0
	for _, language := range languages {
		items := make([]value.Value, 0, len(metadata.Subtitles.Data[language].URLs))
		for _, candidate := range metadata.Subtitles.Data[language].URLs {
			total++
			if total > dailymotionMaxSubtitles {
				return nil, fmt.Errorf("%w: Dailymotion subtitle limit", ErrInvalidMetadata)
			}
			rawURL := dailymotionWithoutFragment(candidate)
			if !DailymotionAttributableURL(rawURL, "subtitle") {
				return nil, fmt.Errorf("%w: unattributable Dailymotion subtitle", ErrInvalidMetadata)
			}
			object := value.NewObject(
				value.Field{Key: "url", Value: value.String(rawURL)},
				value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
				value.Field{Key: "_host_policy", Value: value.String(dailymotionHostPolicy)},
			)
			if ext := strings.ToLower(strings.TrimPrefix(path.Ext(mustURLPath(rawURL)), ".")); ext != "" {
				object.Set("ext", value.String(ext))
			}
			items = append(items, value.ObjectValue(object))
		}
		if len(items) > 0 {
			result.Set(language, value.List(items...))
		}
	}
	return result, nil
}

type dailymotionThumbnail struct {
	id     string
	url    string
	height int64
}

func dailymotionSetThumbnails(info *value.Object, metadata dailymotionMetadata) error {
	if info == nil {
		return nil
	}
	candidates := make([]dailymotionThumbnail, 0, len(metadata.Posters)+len(metadata.Thumbnails)+1)
	appendMap := func(prefix string, source map[string]string) {
		for id, rawURL := range source {
			rawURL = dailymotionWithoutFragment(rawURL)
			if !DailymotionAttributableURL(rawURL, "thumbnail") {
				continue
			}
			height, _ := strconv.ParseInt(id, 10, 64)
			candidates = append(candidates, dailymotionThumbnail{id: prefix + id, url: rawURL, height: height})
		}
	}
	appendMap("poster-", metadata.Posters)
	appendMap("thumbnail-", metadata.Thumbnails)
	if rawURL := dailymotionWithoutFragment(metadata.PosterURL); DailymotionAttributableURL(rawURL, "thumbnail") {
		candidates = append(candidates, dailymotionThumbnail{id: "poster", url: rawURL})
	}
	if len(candidates) > dailymotionMaxThumbnails {
		return fmt.Errorf("%w: Dailymotion thumbnail limit", ErrInvalidMetadata)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].height != candidates[j].height {
			return candidates[i].height < candidates[j].height
		}
		if candidates[i].id != candidates[j].id {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].url < candidates[j].url
	})
	seen := make(map[string]struct{}, len(candidates))
	values := make([]value.Value, 0, len(candidates))
	best := ""
	for _, candidate := range candidates {
		if len(values) >= dailymotionMaxThumbnails {
			break
		}
		if _, duplicate := seen[candidate.url]; duplicate {
			continue
		}
		seen[candidate.url] = struct{}{}
		object := value.NewObject(
			value.Field{Key: "id", Value: value.String(candidate.id)},
			value.Field{Key: "url", Value: value.String(candidate.url)},
			value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
			value.Field{Key: "_host_policy", Value: value.String(dailymotionHostPolicy)},
		)
		setPositiveInt(object, "height", candidate.height)
		values = append(values, value.ObjectValue(object))
		best = candidate.url
	}
	if len(values) > 0 {
		info.Set("thumbnails", value.List(values...))
		info.Set("thumbnail", value.String(best))
	}
	return nil
}

func cleanDailymotionHTML(input string) string {
	if input == "" {
		return ""
	}
	return strings.Join(strings.Fields(html.UnescapeString(dailymotionHTMLTag.ReplaceAllString(input, " "))), " ")
}

func dailymotionWithoutFragment(rawURL string) string {
	if index := strings.IndexByte(rawURL, '#'); index >= 0 {
		return rawURL[:index]
	}
	return rawURL
}

// DailymotionAttributableURL is the product host-policy seam for public
// playback and sidecars. It accepts only HTTPS Dailymotion-owned host families;
// signed RawQuery bytes are validated but never decoded or re-encoded.
func DailymotionAttributableURL(rawURL, role string) bool {
	if rawURL == "" || len(rawURL) > dailymotionMaxURLBytes || strings.ContainsAny(rawURL, "\x00\r\n") {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Fragment != "" || parsed.RawFragment != "" || parsed.Hostname() == "" || strings.HasSuffix(parsed.Hostname(), ".") ||
		parsed.EscapedPath() == "" || strictPathUnsafe(parsed.EscapedPath()) || len(parsed.RawQuery) > dailymotionMaxRouteQueryBytes ||
		strings.Count(parsed.RawQuery, "&") >= 64 {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	dailymotionHost := host == "dailymotion.com" || strings.HasSuffix(host, ".dailymotion.com")
	dmcdnHost := host == "dmcdn.net" || strings.HasSuffix(host, ".dmcdn.net")
	if role == "playback" {
		if strings.EqualFold(path.Ext(parsed.Path), ".m3u8") {
			role = "manifest"
		} else {
			role = "segment"
		}
	}
	switch role {
	case "page":
		return dailymotionHost
	case "manifest", "media", "segment", "subtitle":
		return dailymotionHost || dmcdnHost
	case "thumbnail":
		return dmcdnHost
	default:
		return false
	}
}

func dailymotionStatusError(code int) error {
	status := &HTTPStatusError{Code: code}
	switch {
	case code >= 300 && code < 400:
		return fmt.Errorf("%w: %w", ErrDailymotionRedirect, status)
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return fmt.Errorf("%w: %w", ErrAuthentication, status)
	case code == http.StatusNotFound || code == http.StatusGone:
		return fmt.Errorf("%w: %w", ErrUnavailable, status)
	case code == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %w", ErrDailymotionRateLimited, status)
	case code == http.StatusUnavailableForLegalReasons:
		return fmt.Errorf("%w: %w", ErrRegionRestricted, status)
	case code >= 500:
		return fmt.Errorf("%w: %w", ErrDailymotionNetwork, status)
	default:
		return status
	}
}

// DailymotionStatusError exposes status typing to the product host-policy
// transport without exposing response bodies or signed URLs.
func DailymotionStatusError(code int) error { return dailymotionStatusError(code) }

func categorizeDailymotionError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrTransportIsolation) || errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) ||
		errors.Is(err, ErrAuthentication) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrRegionRestricted) ||
		errors.Is(err, ErrDailymotionAgeRestricted) || errors.Is(err, ErrDailymotionRateLimited) ||
		errors.Is(err, ErrDailymotionNetwork) || errors.Is(err, ErrDailymotionRedirect) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return dailymotionStatusError(status.Code)
	}
	return fmt.Errorf("%w: Dailymotion request failed", ErrDailymotionNetwork)
}
