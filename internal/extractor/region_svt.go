package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const svtVideoAPIBase = "https://api.svt.se/videoplayer-api/video/"

var (
	ErrRegionRestricted = errors.New("media region restricted")
	svtPlayPath         = regexp.MustCompile(`^/(?:video|klipp|kanaler)/[^/?#]+(?:/[^?#]*)?/?$`)
	svtIDPattern        = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	svtPageVideoID      = regexp.MustCompile(`(?i)["']videoSvtId["']\s*:\s*["']([A-Za-z0-9_-]{1,128})["']`)
)

// RegionSVT is the bounded SVT Play regional pilot. It implements the public
// single-video JSON flow and Sweden-only availability signal; series and page
// playlists remain outside this pilot.
type RegionSVT struct{}

func NewRegionSVT() RegionSVT { return RegionSVT{} }

func (RegionSVT) Name() string { return "region_svt" }

func (RegionSVT) Suitable(parsed *url.URL) bool {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "svtplay.se" && host != "www.svtplay.se" && host != "oppetarkiv.se" && host != "www.oppetarkiv.se" {
		return false
	}
	return svtPlayPath.MatchString(parsed.Path)
}

func (RegionSVT) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewRegionSVT().Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	videoID := parsed.Query().Get("modalId")
	if videoID == "" {
		videoID = parsed.Query().Get("id")
	}
	if videoID != "" && !svtIDPattern.MatchString(videoID) {
		return Extraction{}, fmt.Errorf("%w: invalid SVT video ID", ErrInvalidMetadata)
	}
	if videoID == "" {
		page, _, err := request.Transport.ReadPage(ctx, request.URL)
		if err != nil {
			return Extraction{}, err
		}
		match := svtPageVideoID.FindSubmatch(page)
		if len(match) != 2 {
			return Extraction{}, fmt.Errorf("%w: missing SVT video ID", ErrInvalidMetadata)
		}
		videoID = string(match[1])
	}

	var response svtVideoResponse
	if err := RequestJSON(ctx, request.Transport, http.MethodGet, svtVideoAPIBase+url.PathEscape(videoID), nil, make(http.Header), &response); err != nil {
		var status *HTTPStatusError
		if errors.As(err, &status) {
			switch status.Code {
			case http.StatusForbidden, http.StatusUnavailableForLegalReasons:
				return Extraction{}, ErrRegionRestricted
			case http.StatusNotFound, http.StatusGone:
				return Extraction{}, ErrUnavailable
			}
		}
		return Extraction{}, err
	}
	return normalizeSVTVideo(response, videoID, request.URL)
}

type svtVideoResponse struct {
	Title                    string              `json:"title"`
	ProgramTitle             string              `json:"programTitle"`
	Season                   json.RawMessage     `json:"season"`
	EpisodeTitle             string              `json:"episodeTitle"`
	EpisodeNumber            json.RawMessage     `json:"episodeNumber"`
	MaterialLength           json.RawMessage     `json:"materialLength"`
	ContentDuration          json.RawMessage     `json:"contentDuration"`
	Live                     bool                `json:"live"`
	Simulcast                bool                `json:"simulcast"`
	InappropriateForChildren *bool               `json:"inappropriateForChildren"`
	BlockedForChildren       *bool               `json:"blockedForChildren"`
	VideoReferences          []svtVideoReference `json:"videoReferences"`
	Rights                   struct {
		GeoBlockedSweden bool   `json:"geoBlockedSweden"`
		ValidFrom        string `json:"validFrom"`
	} `json:"rights"`
	Subtitles struct {
		References []svtSubtitleReference `json:"subtitleReferences"`
	} `json:"subtitles"`
	SubtitleReferences []svtSubtitleReference `json:"subtitleReferences"`
}

type svtVideoReference struct {
	PlayerType string `json:"playerType"`
	Format     string `json:"format"`
	URL        string `json:"url"`
}

type svtSubtitleReference struct {
	Language string `json:"language"`
	URL      string `json:"url"`
}

func normalizeSVTVideo(response svtVideoResponse, videoID, webpageURL string) (Extraction, error) {
	isLive := response.Live || response.Simulcast
	formats := make([]value.Value, 0, len(response.VideoReferences))
	for _, reference := range response.VideoReferences {
		if !validHTTPURL(reference.URL) {
			continue
		}
		formatID := reference.PlayerType
		if formatID == "" {
			formatID = reference.Format
		}
		if formatID == "" {
			formatID = "http"
		}
		extension := strings.ToLower(strings.TrimPrefix(path.Ext(mustURLPath(reference.URL)), "."))
		var format *value.Object
		switch extension {
		case "m3u8":
			protocol := "m3u8_native"
			if isLive {
				protocol = "m3u8"
			}
			format = manifestFormat(formatID, reference.URL, protocol)
		case "mpd":
			format = manifestFormat(formatID, reference.URL, "http_dash_segments")
		default:
			if extension == "" {
				extension = "mp4"
			}
			format = value.NewObject(
				value.Field{Key: "format_id", Value: value.String(formatID)},
				value.Field{Key: "url", Value: value.String(reference.URL)},
				value.Field{Key: "ext", Value: value.String(extension)},
				value.Field{Key: "protocol", Value: value.String(strings.ToLower(mustURLScheme(reference.URL)))},
			)
		}
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		if response.Rights.GeoBlockedSweden {
			return Extraction{}, ErrRegionRestricted
		}
		return Extraction{}, ErrUnavailable
	}
	title := response.Title
	if title == "" {
		title = response.EpisodeTitle
	}
	if title == "" {
		title = response.ProgramTitle
	}
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing SVT title", ErrInvalidMetadata)
	}

	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
		value.Field{Key: "is_live", Value: value.Bool(isLive)},
	)
	setSVTString(info, "series", response.ProgramTitle)
	setSVTString(info, "episode", response.EpisodeTitle)
	setSVTInt(info, "season_number", flexibleSVTInt(response.Season))
	setSVTInt(info, "episode_number", flexibleSVTInt(response.EpisodeNumber))
	duration := flexibleSVTInt(response.MaterialLength)
	if duration == 0 {
		duration = flexibleSVTInt(response.ContentDuration)
	}
	setSVTInt(info, "duration", duration)
	if timestamp, err := time.Parse(time.RFC3339, response.Rights.ValidFrom); err == nil {
		info.Set("timestamp", value.Int(timestamp.Unix()))
	}
	adult := response.InappropriateForChildren
	if adult == nil {
		adult = response.BlockedForChildren
	}
	if adult != nil {
		age := int64(0)
		if *adult {
			age = 18
		}
		info.Set("age_limit", value.Int(age))
	}
	if subtitles := normalizeSVTSubtitles(response); subtitles.Len() != 0 {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	return Media(value.NewInfo(info)), nil
}

func normalizeSVTSubtitles(response svtVideoResponse) *value.Object {
	references := response.Subtitles.References
	if len(references) == 0 {
		references = response.SubtitleReferences
	}
	grouped := make(map[string][]value.Value)
	order := make([]string, 0)
	for _, subtitle := range references {
		if !validHTTPURL(subtitle.URL) {
			continue
		}
		language := subtitle.Language
		if language == "" {
			language = "sv"
		}
		if strings.Contains(subtitle.URL, "text-open") {
			language += "-forced"
		}
		if _, exists := grouped[language]; !exists {
			order = append(order, language)
		}
		entry := value.NewObject(value.Field{Key: "url", Value: value.String(subtitle.URL)})
		if strings.EqualFold(path.Ext(mustURLPath(subtitle.URL)), ".m3u8") {
			entry.Set("ext", value.String("vtt"))
		}
		grouped[language] = append(grouped[language], value.ObjectValue(entry))
	}
	result := value.NewObject()
	for _, language := range order {
		result.Set(language, value.List(grouped[language]...))
	}
	return result
}

func flexibleSVTInt(raw json.RawMessage) int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var number int64
	if json.Unmarshal(raw, &number) == nil {
		return number
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		number, _ = strconv.ParseInt(text, 10, 64)
	}
	return number
}

func setSVTString(object *value.Object, key, text string) {
	if text != "" {
		object.Set(key, value.String(text))
	}
}

func setSVTInt(object *value.Object, key string, number int64) {
	if number > 0 {
		object.Set(key, value.Int(number))
	}
}

func mustURLScheme(rawURL string) string {
	parsed, _ := url.Parse(rawURL)
	return parsed.Scheme
}
