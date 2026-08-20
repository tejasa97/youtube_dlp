package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const (
	nrkAPIBase          = "https://psapi.nrk.no/"
	nrkPlaylistPageSize = 50
	nrkMaxJSONBytes     = 16 << 20
)

var (
	nrkProgramIDPattern = regexp.MustCompile(`(?i)^[a-z]{4}[0-9]{8}$`)
	nrkPathProgramID    = regexp.MustCompile(`(?i)([a-z]{4}[0-9]{8})`)
	nrkGeneralIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	nrkDigitsOnly       = regexp.MustCompile(`^\d+$`)
)

var ErrNRKHTMLNetwork = errors.New("NRK HTML page network failure")

type NRK struct{}

func NewNRK() NRK { return NRK{} }

func (NRK) Name() string { return "nrk" }

func (NRK) Suitable(parsed *url.URL) bool {
	_, ok := classifyNRKOpaque(parsed)
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
	target, ok := classifyNRKOpaque(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return extractNRKMedia(ctx, request.Transport, target, request.URL)
}

type nrkTarget struct {
	id        string
	kind      string
	domain    string
	series    string
	season    string
	serieKind string
	playlist  bool
}

func classifyNRKOpaque(parsed *url.URL) (nrkTarget, bool) {
	if parsed == nil || parsed.Scheme != "nrk" || parsed.Opaque == "" {
		return nrkTarget{}, false
	}
	id := strings.TrimPrefix(strings.Trim(parsed.Opaque, "/"), "program/")
	kind := "program"
	if strings.HasPrefix(id, "channel/") {
		id, kind = strings.TrimPrefix(id, "channel/"), "channel"
	}
	if strings.HasPrefix(id, "podcast/") {
		id = strings.TrimPrefix(id, "podcast/")
	}
	if nrkProgramIDPattern.MatchString(id) || nrkPodcastUUIDPattern.MatchString(id) {
		return nrkTarget{id: id, kind: kind, domain: "tv"}, true
	}
	if kind == "channel" && nrkGeneralIDPattern.MatchString(id) {
		return nrkTarget{id: id, kind: kind, domain: "tv"}, true
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
		format.Set("_credential_isolated", value.Bool(true))
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
		info.Set("subtitles", value.ObjectValue(nrkCredentialIsolateSubtitles(subtitles)))
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
	err := requestNRKJSON(ctx, transport, endpoint, headers, target)
	if riskHTTPStatus(err) == http.StatusBadRequest && kind == "program" {
		endpoint = nrkAPIBase + "playback/" + item + "/" + url.PathEscape(id)
		if item == "manifest" {
			endpoint += "?preferredCdn=akamai"
		}
		err = requestNRKJSON(ctx, transport, endpoint, headers, target)
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

func categorizeNRKHTTPStatus(status int) error {
	switch status {
	case http.StatusUnauthorized:
		return ErrAuthentication
	case http.StatusForbidden, http.StatusUnavailableForLegalReasons:
		return ErrRegionRestricted
	case http.StatusNotFound, http.StatusGone:
		return ErrUnavailable
	default:
		return fmt.Errorf("%w: NRK request status %d", ErrNRKHTMLNetwork, status)
	}
}

func categorizeNRKIsolatedPageError(err error) error {
	if err == nil {
		return nil
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return categorizeNRKHTTPStatus(status.Code)
	}
	if strings.Contains(err.Error(), "read NRK response failed") {
		return fmt.Errorf("%w: read NRK HTML page failed", ErrNRKHTMLNetwork)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrTransportIsolation) || errors.Is(err, ErrInvalidMetadata) ||
		errors.Is(err, ErrJSONResponseTooLarge) {
		return err
	}
	return fmt.Errorf("%w: request failed", ErrNRKHTMLNetwork)
}

func requestNRKJSON(ctx context.Context, transport Transport, rawURL string, headers http.Header, target any) error {
	if target == nil {
		return fmt.Errorf("%w: invalid NRK JSON target", ErrInvalidMetadata)
	}
	body, err := requestNRKIsolated(ctx, transport, http.MethodGet, rawURL, nrkMaxJSONBytes, headers)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid JSON response", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: trailing JSON response", ErrInvalidMetadata)
	}
	return nil
}

func requestNRKIsolated(ctx context.Context, transport Transport, method, rawURL string, maxBytes int64, headers http.Header) ([]byte, error) {
	if transport == nil {
		return nil, fmt.Errorf("%w: missing transport", ErrInvalidMetadata)
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid NRK request", ErrInvalidMetadata)
	}
	if headers != nil {
		request.Header = headers.Clone()
	}
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("%w: empty NRK response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &HTTPStatusError{Code: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read NRK response failed", ErrInvalidMetadata)
	}
	if int64(len(body)) > maxBytes {
		return nil, ErrJSONResponseTooLarge
	}
	return body, nil
}

func nrkCredentialIsolateSubtitles(object *value.Object) *value.Object {
	if object == nil {
		return object
	}
	for _, field := range object.Fields() {
		entries, ok := field.Value.ListValue()
		if !ok {
			continue
		}
		for i, entry := range entries {
			entryObject, ok := entry.Object()
			if !ok || entryObject == nil {
				continue
			}
			entryObject.Set("_credential_isolated", value.Bool(true))
			entries[i] = value.ObjectValue(entryObject)
		}
		object.Set(field.Key, value.List(entries...))
	}
	return object
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

func extractNRKSeriesPlaylist(ctx context.Context, transport Transport, target nrkTarget, webpageURL string) (Extraction, error) {
	endpoint := nrkCatalogURL(target)
	first, err := requestNRKCatalog(ctx, transport, endpoint)
	if err != nil {
		return Extraction{}, err
	}
	entries, next, title, description, err := parseNRKSeriesCatalog(first, target)
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
	catalogKind := nrkCatalogKind(target.serieKind)
	endpoint := nrkAPIBase + target.domain + "/catalog/" + catalogKind + "/" + url.PathEscape(target.series)
	query := make(url.Values)
	if target.season != "" {
		endpoint += "/seasons/" + url.PathEscape(target.season)
		query.Set("pageSize", strconv.Itoa(nrkPlaylistPageSize))
	} else if target.domain == "radio" || catalogKind == "podcast" {
		query.Set("pageSize", strconv.Itoa(nrkPlaylistPageSize))
	} else {
		query.Set("embeddedInstalmentsPageSize", strconv.Itoa(nrkPlaylistPageSize))
	}
	return endpoint + "?" + query.Encode()
}

func requestNRKCatalog(ctx context.Context, transport Transport, rawURL string) (any, error) {
	canonical, err := validateNRKCatalogCursor(rawURL)
	if err != nil {
		return nil, err
	}
	var response any
	if err := requestNRKJSON(ctx, transport, canonical, nrkHeaders(), &response); err != nil {
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

func validateNRKCatalogCursor(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "psapi.nrk.no") ||
		parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", fmt.Errorf("%w: invalid NRK playlist cursor", ErrInvalidPlaylist)
	}
	return parsed.String(), nil
}

func parseNRKSeriesCatalog(root any, target nrkTarget) ([]Entry, string, string, string, error) {
	seen := make(map[string]bool)
	entries, next, title, description, err := parseNRKCatalogSeen(root, seen)
	if err != nil {
		return nil, "", "", "", err
	}
	embedded, _ := root.(map[string]any)
	if embedded == nil {
		return entries, next, title, description, nil
	}
	linkedSeasons := findNRKValueSlice(embedded, []string{"_links", "seasons"}, 0)
	embeddedSeasons := findNRKValue(embedded, []string{"_embedded", "seasons"}, 0)
	embeddedList, _ := embeddedSeasons.([]any)
	if len(linkedSeasons) > len(embeddedList) {
		seasonEntries := make([]Entry, 0, len(linkedSeasons))
		for _, season := range linkedSeasons {
			seasonMap, _ := season.(map[string]any)
			if seasonMap == nil {
				continue
			}
			if entry, ok := nrkSeasonEntryFromLink(target, seasonMap); ok {
				seasonEntries = append(seasonEntries, entry)
			}
		}
		if len(seasonEntries) > 0 {
			entries = append(seasonEntries, entries...)
		}
	} else if len(embeddedList) > 0 {
		for _, season := range embeddedList {
			collectNRKCatalogEntries(season, &entries, seen, 0)
		}
	}
	if extraMaterial := findNRKValue(embedded, []string{"_embedded", "extraMaterial"}, 0); extraMaterial != nil {
		collectNRKCatalogEntries(extraMaterial, &entries, seen, 0)
	}
	if len(entries) > nrkMaxPlaylistItems {
		return nil, "", "", "", fmt.Errorf("%w: NRK page too large", ErrInvalidPlaylist)
	}
	return entries, next, title, description, nil
}

func nrkSeasonEntryFromLink(target nrkTarget, season map[string]any) (Entry, bool) {
	name, _ := season["name"].(string)
	title, _ := season["title"].(string)
	if name != "" && (nrkDigitsOnly.MatchString(name) || nrkGeneralIDPattern.MatchString(name)) {
		seasonURL := "https://" + target.domain + ".nrk.no/serie/" + target.series + "/sesong/" + name
		parsed, err := url.Parse(seasonURL)
		if err != nil || nrkRejectUnsafeURL(parsed) {
			return Entry{}, false
		}
		return Entry{URL: seasonURL, ExtractorKey: "nrktv_season", Title: title}, true
	}
	href, _ := season["href"].(string)
	if href == "" {
		return Entry{}, false
	}
	base, err := url.Parse("https://" + target.domain + ".nrk.no/")
	if err != nil {
		return Entry{}, false
	}
	resolved, err := base.Parse(href)
	if err != nil || nrkRejectUnsafeURL(resolved) || resolved.Scheme != "https" ||
		resolved.User != nil || resolved.Port() != "" || resolved.Fragment != "" || resolved.RawFragment != "" {
		return Entry{}, false
	}
	if strings.ToLower(resolved.Hostname()) != target.domain+".nrk.no" {
		return Entry{}, false
	}
	if seasonTarget, ok := nrkSeasonTarget(resolved); !ok || seasonTarget.series != target.series {
		return Entry{}, false
	}
	return Entry{URL: resolved.String(), ExtractorKey: "nrktv_season", Title: title}, true
}

func findNRKValueSlice(node any, path []string, depth int) []any {
	value := findNRKValue(node, path, depth)
	if slice, ok := value.([]any); ok {
		return slice
	}
	return nil
}

func findNRKValue(node any, path []string, depth int) any {
	if depth > 64 || len(path) == 0 {
		return nil
	}
	switch node := node.(type) {
	case map[string]any:
		if child, ok := node[path[0]]; ok {
			if len(path) == 1 {
				return child
			}
			if result := findNRKValue(child, path[1:], depth+1); result != nil {
				return result
			}
		}
		for _, child := range node {
			if result := findNRKValue(child, path, depth+1); result != nil {
				return result
			}
		}
	case []any:
		for _, child := range node {
			if result := findNRKValue(child, path, depth+1); result != nil {
				return result
			}
		}
	}
	return nil
}

func parseNRKCatalog(root any) ([]Entry, string, string, string, error) {
	seen := make(map[string]bool)
	return parseNRKCatalogSeen(root, seen)
}

func parseNRKCatalogSeen(root any, seen map[string]bool) ([]Entry, string, string, string, error) {
	entries := make([]Entry, 0)
	collectNRKCatalogEntries(root, &entries, seen, 0)
	if len(entries) > nrkMaxPlaylistItems {
		return nil, "", "", "", fmt.Errorf("%w: NRK page too large", ErrInvalidPlaylist)
	}
	next := findNRKString(root, []string{"_links", "next", "href"}, 0)
	if next != "" {
		canonical, err := validateNRKCatalogCursor(riskAbsoluteURL(nrkAPIBase, next))
		if err != nil {
			return nil, "", "", "", err
		}
		next = canonical
	}
	title := findNRKString(root, []string{"titles", "title"}, 0)
	description := findNRKString(root, []string{"titles", "subtitle"}, 0)
	return entries, next, title, description, nil
}

func collectNRKCatalogEntries(node any, entries *[]Entry, seen map[string]bool, depth int) {
	if depth > 64 || len(*entries) > nrkMaxPlaylistItems {
		return
	}
	switch node := node.(type) {
	case map[string]any:
		id, _ := node["prfId"].(string)
		if id == "" {
			id, _ = node["episodeId"].(string)
		}
		if id != "" && (nrkProgramIDPattern.MatchString(id) || nrkPodcastUUIDPattern.MatchString(id)) && !seen[id] {
			seen[id] = true
			title, _ := node["title"].(string)
			*entries = append(*entries, Entry{URL: "nrk:" + id, ExtractorKey: "nrk", ID: id, Title: title, Transparent: true})
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
