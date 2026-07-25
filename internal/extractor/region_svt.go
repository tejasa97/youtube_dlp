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

const (
	svtVideoAPIBase          = "https://api.svt.se/videoplayer-api/video/"
	svtSeriesGraphQLEndpoint = "https://api.svt.se/contento/graphql"
	svtSeriesWebpageBase     = "https://www.svtplay.se/"

	svtMaxSeriesSlugLength         = 128
	svtMaxSeasonTabLength          = 256
	svtMaxSeriesSeasons            = 64
	svtMaxSeriesItemsPerSeason     = 500
	svtMaxSeriesPlaylistEntries    = 5_000
	svtMaxSeriesMetadataIDLength   = 256
	svtMaxSeriesMetadataNameLength = 256
	svtMaxSeriesDescriptionLength  = 4_096
)

var (
	svtPlayPath                = regexp.MustCompile(`^/(?:video|klipp|kanaler)/[^/?#]+(?:/[^?#]*)?/?$`)
	svtIDPattern               = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	svtPageVideoID             = regexp.MustCompile(`(?i)["']videoSvtId["']\s*:\s*["']([A-Za-z0-9_-]{1,128})["']`)
	svtSeriesSlugPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	svtSeasonTabPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$`)
	svtSeriesMetadataIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,255}$`)
	svtSeriesPath              = regexp.MustCompile(`^/([^/?#]+)/?$`)
)

// ErrSVTSeriesNetwork is returned for opaque SVT series GraphQL transport
// failures after stable categories such as cancellation, isolation, HTTP status,
// and JSON bounds have been ruled out.
var ErrSVTSeriesNetwork = errors.New("SVT series network failure")

// RegionSVT is the bounded SVT Play regional pilot. It implements the public
// single-video JSON flow, bounded series/season playlists, and Sweden-only
// availability signal.
type RegionSVT struct{}

func NewRegionSVT() RegionSVT { return RegionSVT{} }

func (RegionSVT) Name() string { return "region_svt" }

func (RegionSVT) Suitable(parsed *url.URL) bool {
	_, ok := classifySVTURL(parsed)
	return ok
}

func (RegionSVT) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifySVTURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	switch target.kind {
	case "series":
		return extractSVTSeriesPlaylist(ctx, request, target.seriesSlug, target.seasonTab)
	default:
		return extractSVTVideo(ctx, request, parsed, target.videoID)
	}
}

type svtTarget struct {
	kind       string
	videoID    string
	seriesSlug string
	seasonTab  string
}

func classifySVTURL(parsed *url.URL) (svtTarget, bool) {
	if parsed == nil {
		return svtTarget{}, false
	}
	if videoID, ok := classifySVTPseudoURL(parsed); ok {
		return svtTarget{kind: "pseudo", videoID: videoID}, true
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return svtTarget{}, false
	}
	if parsed.Port() != "" || parsed.User != nil {
		return svtTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "svtplay.se", "www.svtplay.se":
		if svtPlayPath.MatchString(parsed.Path) {
			return svtTarget{kind: "video"}, true
		}
		if parsed.Scheme == "https" {
			if slug, seasonTab, ok := classifySVTSeriesURL(parsed); ok {
				return svtTarget{kind: "series", seriesSlug: slug, seasonTab: seasonTab}, true
			}
		}
		return svtTarget{}, false
	case "oppetarkiv.se", "www.oppetarkiv.se":
		if svtPlayPath.MatchString(parsed.Path) {
			return svtTarget{kind: "video"}, true
		}
		return svtTarget{}, false
	default:
		return svtTarget{}, false
	}
}

func classifySVTPseudoURL(parsed *url.URL) (string, bool) {
	if parsed.Scheme != "svt" || parsed.Host != "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return "", false
	}
	videoID := parsed.Opaque
	if !svtIDPattern.MatchString(videoID) {
		return "", false
	}
	return videoID, true
}

func classifySVTSeriesURL(parsed *url.URL) (slug, seasonTab string, ok bool) {
	if parsed.Fragment != "" || parsed.ForceQuery {
		return "", "", false
	}
	if strings.Contains(parsed.EscapedPath(), "%") {
		return "", "", false
	}
	slug, ok = svtSeriesSlugFromPath(parsed.Path)
	if !ok {
		return "", "", false
	}
	seasonTab, ok = svtSeriesTabQuery(parsed)
	if !ok {
		return "", "", false
	}
	return slug, seasonTab, true
}

func svtSeriesTabQuery(parsed *url.URL) (string, bool) {
	values := parsed.Query()
	if len(values) == 0 {
		return "", true
	}
	for key := range values {
		if key != "tab" {
			return "", false
		}
	}
	tabs := values["tab"]
	if len(tabs) == 0 {
		return "", true
	}
	if len(tabs) != 1 {
		return "", false
	}
	tab := tabs[0]
	if tab == "" || !svtSeasonTabPattern.MatchString(tab) || len(tab) > svtMaxSeasonTabLength {
		return "", false
	}
	return tab, true
}

func svtSeriesSlugFromPath(rawPath string) (string, bool) {
	if svtPlayPath.MatchString(rawPath) {
		return "", false
	}
	match := svtSeriesPath.FindStringSubmatch(rawPath)
	if len(match) != 2 {
		return "", false
	}
	slug := match[1]
	if !svtSeriesSlugPattern.MatchString(slug) || len(slug) > svtMaxSeriesSlugLength {
		return "", false
	}
	return slug, true
}

func svtCanonicalSeriesURL(slug, seasonTab string) string {
	raw := svtSeriesWebpageBase + slug
	if seasonTab == "" {
		return raw
	}
	return raw + "?tab=" + url.QueryEscape(seasonTab)
}

func extractSVTVideo(ctx context.Context, request Request, parsed *url.URL, explicitID string) (Extraction, error) {
	videoID := explicitID
	if videoID == "" {
		videoID = parsed.Query().Get("modalId")
	}
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

func buildSVTSeriesGraphQLQuery(slug string) (string, error) {
	if !svtSeriesSlugPattern.MatchString(slug) || len(slug) > svtMaxSeriesSlugLength {
		return "", fmt.Errorf("%w: invalid SVT series slug", ErrInvalidMetadata)
	}
	slugJSON, err := json.Marshal(slug)
	if err != nil {
		return "", fmt.Errorf("%w: invalid SVT series slug", ErrInvalidMetadata)
	}
	return fmt.Sprintf(`{
  listablesBySlug(slugs: [%s]) {
    associatedContent(include: [productionPeriod, season]) {
      items {
        item {
          ... on Episode {
            videoSvtId
          }
        }
      }
      id
      name
    }
    id
    longDescription
    name
    shortDescription
  }
}`, string(slugJSON)), nil
}

type svtSeriesGraphQLResponse struct {
	Data struct {
		ListablesBySlug []svtSeriesListable `json:"listablesBySlug"`
	} `json:"data"`
	Errors []json.RawMessage `json:"errors"`
}

type svtSeriesListable struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	LongDescription   string            `json:"longDescription"`
	ShortDescription  string            `json:"shortDescription"`
	AssociatedContent []svtSeriesSeason `json:"associatedContent"`
}

type svtSeriesSeason struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Items []svtSeriesItem `json:"items"`
}

type svtSeriesItem struct {
	Item struct {
		VideoSvtID string `json:"videoSvtId"`
	} `json:"item"`
}

type svtSeriesPlaylist struct {
	playlistID  string
	title       string
	description string
	entries     []Entry
}

func extractSVTSeriesPlaylist(ctx context.Context, request Request, slug, seasonTab string) (Extraction, error) {
	query, err := buildSVTSeriesGraphQLQuery(slug)
	if err != nil {
		return Extraction{}, err
	}
	apiURL := svtSeriesGraphQLEndpoint + "?query=" + url.QueryEscape(query)
	var envelope svtSeriesGraphQLResponse
	if err := requestSVTSeriesJSON(ctx, request.Transport, apiURL, &envelope); err != nil {
		return Extraction{}, categorizeSVTSeriesTransportError(err)
	}
	playlist, err := parseSVTSeriesResponse(ctx, envelope, seasonTab)
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(playlist.playlistID)},
		value.Field{Key: "title", Value: value.String(playlist.title)},
		value.Field{Key: "webpage_url", Value: value.String(svtCanonicalSeriesURL(slug, seasonTab))},
	)
	if playlist.description != "" {
		info.Set("description", value.String(playlist.description))
	}
	return Playlist(value.NewInfo(info), StaticEntries(playlist.entries...))
}

func requestSVTSeriesJSON(ctx context.Context, transport Transport, apiURL string, target any) error {
	if transport == nil {
		return errors.New("invalid JSON request")
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	return requestJSON(ctx, isolated.DoWithoutCredentialsNoRedirect, http.MethodGet, apiURL, nil, make(http.Header), target)
}

func categorizeSVTSeriesTransportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrTransportIsolation) || errors.Is(err, ErrInvalidMetadata) ||
		errors.Is(err, ErrJSONResponseTooLarge) || errors.Is(err, ErrUnavailable) ||
		errors.Is(err, ErrRegionRestricted) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusForbidden, http.StatusUnavailableForLegalReasons:
			return ErrRegionRestricted
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		}
	}
	return ErrSVTSeriesNetwork
}

func parseSVTSeriesResponse(ctx context.Context, envelope svtSeriesGraphQLResponse, seasonTab string) (svtSeriesPlaylist, error) {
	if len(envelope.Errors) != 0 {
		return svtSeriesPlaylist{}, fmt.Errorf("%w: SVT series GraphQL error", ErrInvalidMetadata)
	}
	if len(envelope.Data.ListablesBySlug) == 0 {
		return svtSeriesPlaylist{}, ErrUnavailable
	}
	series := envelope.Data.ListablesBySlug[0]
	if err := validateSVTSeriesMetadataID(series.ID, "series id"); err != nil {
		return svtSeriesPlaylist{}, err
	}
	if err := validateSVTSeriesMetadataName(series.Name, "series name"); err != nil {
		return svtSeriesPlaylist{}, err
	}
	if len(series.AssociatedContent) > svtMaxSeriesSeasons {
		return svtSeriesPlaylist{}, fmt.Errorf("%w: SVT series season bound exceeded", ErrPlaylistLimit)
	}

	description, err := svtSeriesDescription(series.LongDescription, series.ShortDescription)
	if err != nil {
		return svtSeriesPlaylist{}, err
	}

	seen := make(map[string]struct{})
	entries := make([]Entry, 0)
	var matchedSeasonName string
	var matchedSeasonID string
	seasonRequested := seasonTab != ""

	for _, season := range series.AssociatedContent {
		if err := ctx.Err(); err != nil {
			return svtSeriesPlaylist{}, err
		}
		if seasonRequested && season.ID != seasonTab {
			continue
		}
		if season.Items == nil {
			continue
		}
		if len(season.Items) > svtMaxSeriesItemsPerSeason {
			return svtSeriesPlaylist{}, fmt.Errorf("%w: SVT series item bound exceeded", ErrPlaylistLimit)
		}
		if seasonRequested {
			if err := validateSVTSeriesMetadataID(season.ID, "season id"); err != nil {
				return svtSeriesPlaylist{}, err
			}
			if err := validateSVTSeriesMetadataName(season.Name, "season name"); err != nil {
				return svtSeriesPlaylist{}, err
			}
			matchedSeasonID = season.ID
			matchedSeasonName = season.Name
		}
		for _, item := range season.Items {
			if err := ctx.Err(); err != nil {
				return svtSeriesPlaylist{}, err
			}
			videoID := strings.TrimSpace(item.Item.VideoSvtID)
			if !svtIDPattern.MatchString(videoID) {
				continue
			}
			if _, exists := seen[videoID]; exists {
				continue
			}
			seen[videoID] = struct{}{}
			entries = append(entries, Entry{
				URL:          "svt:" + videoID,
				ExtractorKey: "region_svt",
				ID:           videoID,
			})
			if len(entries) > svtMaxSeriesPlaylistEntries {
				return svtSeriesPlaylist{}, fmt.Errorf("%w: SVT series playlist bound exceeded", ErrPlaylistLimit)
			}
		}
	}

	if seasonRequested && matchedSeasonID == "" {
		return svtSeriesPlaylist{}, ErrUnavailable
	}

	playlistID := series.ID
	seasonLabel := matchedSeasonName
	if seasonLabel == "" {
		seasonLabel = seasonTab
	}
	if seasonRequested {
		playlistID = matchedSeasonID
		if playlistID == "" {
			playlistID = seasonTab
		}
	}

	title := svtSeriesPlaylistTitle(series.Name, seasonLabel, seasonTab)
	if title == "" || len(title) > svtMaxSeriesMetadataNameLength+svtMaxSeriesMetadataNameLength+3 {
		return svtSeriesPlaylist{}, fmt.Errorf("%w: missing SVT series title", ErrInvalidMetadata)
	}

	return svtSeriesPlaylist{
		playlistID:  playlistID,
		title:       title,
		description: description,
		entries:     entries,
	}, nil
}

func validateSVTSeriesMetadataID(id, label string) error {
	if id == "" {
		return fmt.Errorf("%w: missing SVT %s", ErrInvalidMetadata, label)
	}
	if !svtSeriesMetadataIDPattern.MatchString(id) || len(id) > svtMaxSeriesMetadataIDLength {
		return fmt.Errorf("%w: invalid SVT %s", ErrInvalidMetadata, label)
	}
	return nil
}

func validateSVTSeriesMetadataName(name, label string) error {
	if name == "" {
		return nil
	}
	if len(name) > svtMaxSeriesMetadataNameLength {
		return fmt.Errorf("%w: SVT %s bound exceeded", ErrInvalidMetadata, label)
	}
	return nil
}

func svtSeriesDescription(longDescription, shortDescription string) (string, error) {
	description := longDescription
	if description == "" {
		description = shortDescription
	}
	if len(description) > svtMaxSeriesDescriptionLength {
		return "", fmt.Errorf("%w: SVT series description bound exceeded", ErrInvalidMetadata)
	}
	return description, nil
}

func svtSeriesPlaylistTitle(seriesName, seasonName, seasonTab string) string {
	label := seasonName
	if label == "" {
		label = seasonTab
	}
	switch {
	case seriesName != "" && label != "":
		return seriesName + " - " + label
	case seriesName != "":
		return seriesName
	case label != "":
		return label
	default:
		return ""
	}
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
			format = manifestFormat(formatID, reference.URL, "m3u8_native")
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
