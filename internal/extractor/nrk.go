package extractor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	nrkAPIBase          = "https://psapi.nrk.no/"
	nrkPlaylistPageSize = 50
)

var (
	nrkProgramIDPattern = regexp.MustCompile(`(?i)^[a-z]{4}[0-9]{8}$`)
	nrkPathProgramID    = regexp.MustCompile(`(?i)([a-z]{4}[0-9]{8})`)
	nrkGeneralIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
)

type NRK struct{}

func NewNRK() NRK { return NRK{} }

func (NRK) Name() string { return "nrk" }

func (NRK) Suitable(parsed *url.URL) bool {
	_, ok := classifyNRKURL(parsed)
	return ok
}

func (NRK) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyNRKURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if target.playlist {
		return extractNRKPlaylist(ctx, request.Transport, target, request.URL)
	}
	return extractNRKMedia(ctx, request.Transport, target, request.URL)
}

type nrkTarget struct {
	id       string
	kind     string
	domain   string
	series   string
	season   string
	playlist bool
}

func classifyNRKURL(parsed *url.URL) (nrkTarget, bool) {
	if parsed == nil {
		return nrkTarget{}, false
	}
	if parsed.Scheme == "nrk" && parsed.Opaque != "" {
		id := strings.TrimPrefix(strings.Trim(parsed.Opaque, "/"), "program/")
		kind := "program"
		if strings.HasPrefix(id, "channel/") {
			id, kind = strings.TrimPrefix(id, "channel/"), "channel"
		}
		if nrkGeneralIDPattern.MatchString(id) {
			return nrkTarget{id: id, kind: kind, domain: "tv"}, true
		}
		return nrkTarget{}, false
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Port() != "" || parsed.User != nil {
		return nrkTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	domain := "tv"
	switch host {
	case "tv.nrk.no", "tv.nrksuper.no", "nrksuper.no", "www.nrksuper.no":
	case "radio.nrk.no":
		domain = "radio"
	case "nrk.no", "www.nrk.no", "v8.psapi.nrk.no", "v8-psapi.nrk.no":
	default:
		return nrkTarget{}, false
	}
	if match := nrkPathProgramID.FindStringSubmatch(parsed.Path); len(match) == 2 {
		return nrkTarget{id: strings.ToUpper(match[1]), kind: "program", domain: domain}, true
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 2 && parts[0] == "direkte" && nrkGeneralIDPattern.MatchString(parts[1]) {
		return nrkTarget{id: parts[1], kind: "channel", domain: domain}, true
	}
	if len(parts) >= 2 && (parts[0] == "serie" || parts[0] == "podcast" || parts[0] == "podkast") && nrkGeneralIDPattern.MatchString(parts[1]) {
		target := nrkTarget{id: parts[1], series: parts[1], domain: domain, kind: "series", playlist: true}
		if len(parts) >= 4 && parts[2] == "sesong" && nrkGeneralIDPattern.MatchString(parts[3]) {
			target.id, target.season, target.kind = parts[1]+"/"+parts[3], parts[3], "season"
		}
		return target, true
	}
	if len(parts) >= 2 && parts[0] == "video" {
		last := parts[len(parts)-1]
		if index := strings.LastIndex(last, "_"); index >= 0 {
			last = last[index+1:]
		}
		last = strings.TrimPrefix(last, "PS*")
		if nrkGeneralIDPattern.MatchString(last) {
			return nrkTarget{id: last, kind: "program", domain: domain}, true
		}
	}
	return nrkTarget{}, false
}

type nrkManifest struct {
	ID           string         `json:"id"`
	Playability  string         `json:"playability"`
	NonPlayable  nrkNonPlayable `json:"nonPlayable"`
	Availability struct {
		OnDemand struct {
			From string `json:"from"`
		} `json:"onDemand"`
	} `json:"availability"`
	Playable struct {
		Duration string `json:"duration"`
		IsLive   bool   `json:"isLive"`
		Assets   []struct {
			URL       string `json:"url"`
			Format    string `json:"format"`
			Encrypted bool   `json:"encrypted"`
		} `json:"assets"`
		Subtitles []struct {
			WebVTT   string `json:"webVtt"`
			Language string `json:"language"`
			Type     string `json:"type"`
		} `json:"subtitles"`
	} `json:"playable"`
}

type nrkNonPlayable struct {
	MessageType    string `json:"messageType"`
	EndUserMessage string `json:"endUserMessage"`
	UsageRights    struct {
		IsGeoBlocked bool `json:"isGeoBlocked"`
	} `json:"usageRights"`
}

type nrkMetadata struct {
	Duration string `json:"duration"`
	Preplay  struct {
		Titles struct {
			Title    string `json:"title"`
			Subtitle string `json:"subtitle"`
		} `json:"titles"`
		Description string `json:"description"`
		Poster      struct {
			Images []struct {
				URL         string `json:"url"`
				PixelWidth  int64  `json:"pixelWidth"`
				PixelHeight int64  `json:"pixelHeight"`
			} `json:"images"`
		} `json:"poster"`
	} `json:"preplay"`
	LegalAge struct {
		Body struct {
			Rating struct {
				Code string `json:"code"`
			} `json:"rating"`
		} `json:"body"`
	} `json:"legalAge"`
}

func extractNRKMedia(ctx context.Context, transport Transport, target nrkTarget, webpageURL string) (Extraction, error) {
	var manifest nrkManifest
	if err := requestNRKPlayback(ctx, transport, "manifest", target.kind, target.id, &manifest); err != nil {
		return Extraction{}, err
	}
	if strings.EqualFold(manifest.Playability, "nonPlayable") {
		return Extraction{}, categorizeNRKNonPlayable(manifest.NonPlayable)
	}
	videoID := manifest.ID
	if videoID == "" {
		videoID = target.id
	}
	formats := make([]value.Value, 0, len(manifest.Playable.Assets))
	seen := make(map[string]bool)
	for _, asset := range manifest.Playable.Assets {
		if asset.Encrypted || seen[asset.URL] {
			continue
		}
		formatID := strings.ToLower(asset.Format)
		if formatID == "" {
			formatID = "http"
		}
		format, ok := riskFormat(asset.URL, formatID)
		if !ok {
			continue
		}
		seen[asset.URL] = true
		if formatID == "mp3" {
			format.Set("vcodec", value.String("none"))
		}
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	var metadata nrkMetadata
	if err := requestNRKPlayback(ctx, transport, "metadata", target.kind, target.id, &metadata); err != nil {
		return Extraction{}, err
	}
	if metadata.Preplay.Titles.Title == "" {
		return Extraction{}, fmt.Errorf("%w: missing NRK title", ErrInvalidMetadata)
	}
	isLive := target.kind == "channel" || manifest.Playable.IsLive
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "title", Value: value.String(metadata.Preplay.Titles.Title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
		value.Field{Key: "is_live", Value: value.Bool(isLive)},
	)
	riskString(info, "alt_title", metadata.Preplay.Titles.Subtitle)
	riskString(info, "description", strings.ReplaceAll(metadata.Preplay.Description, "\r", "\n"))
	duration := parseNRKDuration(manifest.Playable.Duration)
	if duration == 0 {
		duration = parseNRKDuration(metadata.Duration)
	}
	riskFloat(info, "duration", duration)
	riskPositiveInt(info, "timestamp", riskTimestamp(manifest.Availability.OnDemand.From))
	if isLive {
		info.Set("live_status", value.String("is_live"))
	}
	if thumbnails := normalizeNRKThumbnails(metadata); len(thumbnails) != 0 {
		info.Set("thumbnails", value.List(thumbnails...))
	}
	if subtitles := normalizeNRKSubtitles(manifest); subtitles.Len() != 0 {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	ageCode := metadata.LegalAge.Body.Rating.Code
	if ageCode == "A" {
		info.Set("age_limit", value.Int(0))
	} else if age, err := strconv.ParseInt(ageCode, 10, 64); err == nil {
		info.Set("age_limit", value.Int(age))
	}
	return Media(value.NewInfo(info)), nil
}

func requestNRKPlayback(ctx context.Context, transport Transport, item, kind, id string, target any) error {
	headers := nrkHeaders()
	endpoint := nrkAPIBase + "playback/" + item + "/" + kind + "/" + url.PathEscape(id)
	if item == "manifest" {
		endpoint += "?preferredCdn=akamai"
	}
	err := requestRiskJSON(ctx, transport, http.MethodGet, endpoint, nil, headers, "", target)
	if riskHTTPStatus(err) == http.StatusBadRequest && kind == "program" {
		endpoint = nrkAPIBase + "playback/" + item + "/" + url.PathEscape(id)
		if item == "manifest" {
			endpoint += "?preferredCdn=akamai"
		}
		err = requestRiskJSON(ctx, transport, http.MethodGet, endpoint, nil, headers, "", target)
	}
	if err == nil {
		return nil
	}
	switch riskHTTPStatus(err) {
	case http.StatusUnauthorized:
		return ErrAuthentication
	case http.StatusForbidden, http.StatusUnavailableForLegalReasons:
		return ErrRegionRestricted
	case http.StatusNotFound, http.StatusGone:
		return ErrUnavailable
	default:
		return err
	}
}

func categorizeNRKNonPlayable(reason nrkNonPlayable) error {
	messageType := strings.ToLower(reason.MessageType)
	message := strings.ToLower(reason.EndUserMessage)
	switch {
	case reason.UsageRights.IsGeoBlocked, strings.Contains(messageType, "isgeoblocked"), strings.Contains(message, "utenfor norge"):
		return ErrRegionRestricted
	case strings.Contains(messageType, "login"), strings.Contains(messageType, "authentication"), strings.Contains(message, "logg inn"):
		return ErrAuthentication
	default:
		return ErrUnavailable
	}
}

func normalizeNRKThumbnails(metadata nrkMetadata) []value.Value {
	result := make([]value.Value, 0, len(metadata.Preplay.Poster.Images))
	for _, image := range metadata.Preplay.Poster.Images {
		if !validHTTPURL(image.URL) {
			continue
		}
		entry := value.NewObject(value.Field{Key: "url", Value: value.String(image.URL)})
		riskPositiveInt(entry, "width", image.PixelWidth)
		riskPositiveInt(entry, "height", image.PixelHeight)
		result = append(result, value.ObjectValue(entry))
	}
	return result
}

func normalizeNRKSubtitles(manifest nrkManifest) *value.Object {
	grouped := make(map[string][]value.Value)
	order := make([]string, 0)
	for _, subtitle := range manifest.Playable.Subtitles {
		if !validHTTPURL(subtitle.WebVTT) {
			continue
		}
		language := subtitle.Language
		if language == "" {
			language = "nb"
		}
		if subtitle.Type != "" {
			language += "-" + subtitle.Type
		}
		if _, ok := grouped[language]; !ok {
			order = append(order, language)
		}
		grouped[language] = append(grouped[language], value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(subtitle.WebVTT)},
			value.Field{Key: "ext", Value: value.String("vtt")},
		)))
	}
	result := value.NewObject()
	for _, language := range order {
		result.Set(language, value.List(grouped[language]...))
	}
	return result
}

func parseNRKDuration(input string) float64 {
	if input == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(input, 64); err == nil {
		return seconds
	}
	if !strings.HasPrefix(input, "PT") {
		return 0
	}
	input = strings.TrimPrefix(input, "PT")
	total := 0.0
	for _, unit := range []struct {
		marker string
		scale  float64
	}{{"H", 3600}, {"M", 60}, {"S", 1}} {
		if index := strings.Index(input, unit.marker); index >= 0 {
			number, err := strconv.ParseFloat(input[:index], 64)
			if err != nil {
				return 0
			}
			total += number * unit.scale
			input = input[index+1:]
		}
	}
	if input != "" {
		return 0
	}
	return total
}

func extractNRKPlaylist(ctx context.Context, transport Transport, target nrkTarget, webpageURL string) (Extraction, error) {
	endpoint := nrkCatalogURL(target)
	first, err := requestNRKCatalog(ctx, transport, endpoint)
	if err != nil {
		return Extraction{}, err
	}
	entries, next, title, description, err := parseNRKCatalog(first)
	if err != nil {
		return Extraction{}, err
	}
	sequence, err := ContinuationEntries(entries, next, func(ctx context.Context, cursor string) ([]Entry, string, error) {
		data, err := requestNRKCatalog(ctx, transport, cursor)
		if err != nil {
			return nil, "", err
		}
		entries, next, _, _, err := parseNRKCatalog(data)
		return entries, next, err
	})
	if err != nil {
		return Extraction{}, err
	}
	if title == "" {
		title = target.id
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
	)
	riskString(info, "description", description)
	return Playlist(value.NewInfo(info), sequence)
}

func nrkCatalogURL(target nrkTarget) string {
	catalogKind := "series"
	if target.domain == "radio" {
		catalogKind = "series"
	}
	endpoint := nrkAPIBase + target.domain + "/catalog/" + catalogKind + "/" + url.PathEscape(target.series)
	query := make(url.Values)
	if target.season != "" {
		endpoint += "/seasons/" + url.PathEscape(target.season)
		query.Set("pageSize", strconv.Itoa(nrkPlaylistPageSize))
	} else if target.domain == "radio" {
		query.Set("pageSize", strconv.Itoa(nrkPlaylistPageSize))
	} else {
		query.Set("embeddedInstalmentsPageSize", strconv.Itoa(nrkPlaylistPageSize))
	}
	return endpoint + "?" + query.Encode()
}

func requestNRKCatalog(ctx context.Context, transport Transport, rawURL string) (any, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "psapi.nrk.no") || parsed.User != nil || parsed.Port() != "" {
		return nil, fmt.Errorf("%w: invalid NRK playlist cursor", ErrInvalidPlaylist)
	}
	var response any
	if err := requestRiskJSON(ctx, transport, http.MethodGet, parsed.String(), nil, nrkHeaders(), "", &response); err != nil {
		switch riskHTTPStatus(err) {
		case http.StatusUnauthorized:
			return nil, ErrAuthentication
		case http.StatusForbidden, http.StatusUnavailableForLegalReasons:
			return nil, ErrRegionRestricted
		case http.StatusNotFound, http.StatusGone:
			return nil, ErrUnavailable
		default:
			return nil, err
		}
	}
	return response, nil
}

func parseNRKCatalog(root any) ([]Entry, string, string, string, error) {
	entries := make([]Entry, 0)
	seen := make(map[string]bool)
	collectNRKCatalogEntries(root, &entries, seen, 0)
	if len(entries) > 200 {
		return nil, "", "", "", fmt.Errorf("%w: NRK page too large", ErrInvalidPlaylist)
	}
	next := findNRKString(root, []string{"_links", "next", "href"}, 0)
	if next != "" {
		next = riskAbsoluteURL(nrkAPIBase, next)
		parsed, err := url.Parse(next)
		if err != nil || !strings.EqualFold(parsed.Hostname(), "psapi.nrk.no") {
			return nil, "", "", "", fmt.Errorf("%w: invalid NRK playlist cursor", ErrInvalidPlaylist)
		}
	}
	title := findNRKString(root, []string{"titles", "title"}, 0)
	description := findNRKString(root, []string{"titles", "subtitle"}, 0)
	return entries, next, title, description, nil
}

func collectNRKCatalogEntries(node any, entries *[]Entry, seen map[string]bool, depth int) {
	if depth > 64 || len(*entries) > 200 {
		return
	}
	switch node := node.(type) {
	case map[string]any:
		id, _ := node["prfId"].(string)
		if id == "" {
			id, _ = node["episodeId"].(string)
		}
		if id != "" && nrkGeneralIDPattern.MatchString(id) && !seen[id] {
			seen[id] = true
			title, _ := node["title"].(string)
			*entries = append(*entries, Entry{URL: "https://tv.nrk.no/program/" + id, ExtractorKey: "nrk", ID: id, Title: title})
		}
		for key, child := range node {
			if key == "_links" {
				continue
			}
			collectNRKCatalogEntries(child, entries, seen, depth+1)
		}
	case []any:
		for _, child := range node {
			collectNRKCatalogEntries(child, entries, seen, depth+1)
		}
	}
}

func findNRKString(node any, path []string, depth int) string {
	if depth > 64 || len(path) == 0 {
		return ""
	}
	switch node := node.(type) {
	case map[string]any:
		if child, ok := node[path[0]]; ok {
			if len(path) == 1 {
				text, _ := child.(string)
				return text
			}
			if result := findNRKString(child, path[1:], depth+1); result != "" {
				return result
			}
		}
		for _, child := range node {
			if result := findNRKString(child, path, depth+1); result != "" {
				return result
			}
		}
	case []any:
		for _, child := range node {
			if result := findNRKString(child, path, depth+1); result != "" {
				return result
			}
		}
	}
	return ""
}

func nrkHeaders() http.Header {
	headers := make(http.Header)
	headers.Set("Accept", "application/vnd.nrk.psapi+json; version=9; player=tv-player; device=player-core")
	return headers
}
