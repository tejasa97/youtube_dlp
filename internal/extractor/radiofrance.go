package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const (
	radioFranceMaxURLBytes         = 8 << 10
	radioFranceMaxHTMLBytes        = 4 << 20
	radioFranceMaxJSONBytes        = 16 << 20
	radioFranceMaxPlaylistEntries  = defaultMaxPlaylistEntries
	radioFranceMaxTitleBytes       = 2048
	radioFranceMaxDescriptionBytes = 8192
	radioFranceMaxPathBytes        = 512
	radioFranceMaxSlugBytes        = 256
	radioFranceMaxEpisodeIDBytes   = 32
	radioFranceMaxCursorBytes      = 1024
	radioFranceMaxFormats          = 32
	radioFranceAPIBase             = "https://www.radiofrance.fr"
	radioFrancePathAPI             = radioFranceAPIBase + "/api/v2.1/path"
	radioFranceLegacyHost          = "maison.radiofrance.fr"
)

var (
	radioFranceStations = []string{
		"franceculture",
		"franceinfo",
		"franceinter",
		"francemusique",
		"fip",
		"mouv",
	}
	radioFranceStationPattern = strings.Join([]string{
		"franceculture",
		"franceinfo",
		"franceinter",
		"francemusique",
		"fip",
		"mouv",
	}, "|")
	radioFranceEpisodeIDPattern     = regexp.MustCompile(`^\d{6,}$`)
	radioFrancePodcastSlugPattern   = regexp.MustCompile(`^[\w-]{1,256}$`)
	radioFranceProfileSlugPattern   = regexp.MustCompile(`^[\w-]+$`)
	radioFranceSubstationPattern    = regexp.MustCompile(`^radio-[\w-]+$`)
	radioFranceLegacyIDPattern      = regexp.MustCompile(`^[^?#]+$`)
	radioFranceDataMarkerPattern    = regexp.MustCompile(`(?is)\bconst\s+data\s*=`)
	radioFranceAudioObjectPattern   = regexp.MustCompile(`(?is)\{\s*"@type"\s*:\s*"AudioObject"[^}]*\}`)
	radioFranceTitleH1Pattern       = regexp.MustCompile(`(?is)<h1[^>]*itemprop="[^"]*name[^"]*"[^>]*>(.+?)</h1>`)
	radioFranceMetaDescription      = regexp.MustCompile(`(?is)<meta\s+name="description"\s*content="([^"]+)`)
	radioFranceDatePublishedPattern = regexp.MustCompile(`"datePublished"\s*:\s*"([^"]+)"`)
	radioFranceLegacyTitlePattern   = regexp.MustCompile(`(?is)<h1>(.*?)</h1>`)
	radioFranceLegacyDescription    = regexp.MustCompile(`(?is)<div class="bloc_page_wrapper"><div class="text">(.*?)</div>`)
	radioFranceLegacyUploader       = regexp.MustCompile(`(?is)<div class="credit">&nbsp;&nbsp;&copy;&nbsp;(.*?)</div>`)
	radioFranceLegacyAudioSource    = regexp.MustCompile(`(?is)class="jp-jplayer[^"]*"\s+data-source="([^"]+)"`)
	radioFranceLegacyFormatPattern  = regexp.MustCompile(`([a-z0-9]+)\s*:\s*'([^']+)'`)
	radioFranceScheduleDatePattern  = regexp.MustCompile(`^\d{2}-\d{2}-\d{4}$`)
	radioFranceThumbnailExtPattern  = regexp.MustCompile(`(?i)\.(?:jpg|png)(?:\?|$)`)
)

type radioFranceStationTarget struct {
	station    string
	substation string
	webpageURL string
}

type radioFranceEpisodeTarget struct {
	station    string
	displayID  string
	id         string
	webpageURL string
}

type radioFrancePodcastTarget struct {
	station    string
	slug       string
	webpageURL string
}

type radioFranceProfileTarget struct {
	slug       string
	webpageURL string
}

type radioFranceScheduleTarget struct {
	station    string
	date       string
	webpageURL string
}

// RadioFrance extracts legacy Maison Radio France radiovisions pages.
type RadioFrance struct{}

func NewRadioFrance() RadioFrance { return RadioFrance{} }
func (RadioFrance) Name() string  { return "radiofrance" }

func (RadioFrance) Suitable(parsed *url.URL) bool {
	_, ok := classifyRadioFranceLegacyURL(parsed)
	return ok
}

func (RadioFrance) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyRadioFranceLegacyURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	page, err := requestRadioFranceIsolatedPage(ctx, request.Transport, target.webpageURL)
	if err != nil {
		return Extraction{}, categorizeRadioFrancePageError(err)
	}
	title := radioFranceBoundedText(radioFranceHTMLField(page, radioFranceLegacyTitlePattern), radioFranceMaxTitleBytes)
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Radio France title", ErrInvalidMetadata)
	}
	source := radioFranceHTMLField(page, radioFranceLegacyAudioSource)
	if source == "" {
		return Extraction{}, ErrUnavailable
	}
	formats, err := radioFranceLegacyFormats(source)
	if err != nil || len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	ext, _ := formats[0].Object()
	extension, _ := ext.Lookup("ext").StringValue()
	if extension == "" {
		extension = "ogg"
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.webpageURL)},
		value.Field{Key: "ext", Value: value.String(extension)},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	riskString(info, "description", radioFranceBoundedText(radioFranceHTMLField(page, radioFranceLegacyDescription), radioFranceMaxDescriptionBytes))
	riskString(info, "uploader", radioFranceBoundedText(radioFranceHTMLField(page, radioFranceLegacyUploader), radioFranceMaxTitleBytes))
	return Media(value.NewInfo(info)), nil
}

// FranceCulture extracts public Radio France podcast episode pages.
type FranceCulture struct{}

func NewFranceCulture() FranceCulture { return FranceCulture{} }
func (FranceCulture) Name() string    { return "franceculture" }

func (FranceCulture) Suitable(parsed *url.URL) bool {
	_, ok := classifyFranceCultureURL(parsed)
	return ok
}

func (FranceCulture) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyFranceCultureURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	page, err := requestRadioFranceIsolatedPage(ctx, request.Transport, target.webpageURL)
	if err != nil {
		return Extraction{}, categorizeRadioFrancePageError(err)
	}
	audioObject, err := radioFranceExtractAudioObject(page)
	if err != nil {
		return Extraction{}, err
	}
	contentURL, _ := audioObject["contentUrl"].(string)
	contentURL = strings.TrimSpace(contentURL)
	if contentURL == "" || !radioFranceValidMediaURL(contentURL) {
		return Extraction{}, ErrUnavailable
	}
	format, ok := radioFranceDirectAudioFormat(contentURL, "direct")
	if !ok {
		return Extraction{}, ErrUnavailable
	}
	if encoding, _ := audioObject["encodingFormat"].(string); strings.EqualFold(encoding, "mp3") {
		format.Set("vcodec", value.String("none"))
	}
	if durationRaw, _ := audioObject["duration"].(string); durationRaw != "" {
		if duration := radioFranceParseDuration(durationRaw); duration > 0 {
			format.Set("duration", value.Float(duration))
		}
	}
	radioFranceMarkCredentialIsolated(format)
	title := radioFranceBoundedText(radioFranceHTMLField(page, radioFranceTitleH1Pattern), radioFranceMaxTitleBytes)
	if title == "" {
		title = target.displayID
	}
	ext, _ := format.Lookup("ext").StringValue()
	if ext == "" {
		ext = "mp3"
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.id)},
		value.Field{Key: "display_id", Value: value.String(target.displayID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.webpageURL)},
		value.Field{Key: "ext", Value: value.String(ext)},
		value.Field{Key: "url", Value: value.String(contentURL)},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))},
	)
	riskString(info, "description", radioFranceBoundedText(radioFranceHTMLField(page, radioFranceMetaDescription), radioFranceMaxDescriptionBytes))
	if thumb := radioFranceValidThumbnail(radioFranceOGThumbnail(page)); thumb != "" {
		info.Set("thumbnail", value.String(thumb))
	}
	if published := radioFranceHTMLField(page, radioFranceDatePublishedPattern); published != "" {
		if timestamp := radioFranceUnifiedStrDate(published); timestamp > 0 {
			info.Set("upload_date", value.String(strconv.FormatInt(timestamp, 10)))
		}
	}
	if duration, ok := format.Lookup("duration").Float(); ok && duration > 0 {
		info.Set("duration", value.Float(duration))
	}
	return Media(value.NewInfo(info)), nil
}

// RadioFranceLive extracts station and substation live streams.
type RadioFranceLive struct{}

func NewRadioFranceLive() RadioFranceLive { return RadioFranceLive{} }
func (RadioFranceLive) Name() string      { return "radiofrance_live" }

func (RadioFranceLive) Suitable(parsed *url.URL) bool {
	_, ok := classifyRadioFranceLiveURL(parsed)
	return ok
}

func (RadioFranceLive) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyRadioFranceLiveURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	var apiResponse map[string]any
	if target.substation != "" {
		page, err := requestRadioFranceIsolatedPage(ctx, request.Transport, target.webpageURL)
		if err != nil {
			return Extraction{}, categorizeRadioFrancePageError(err)
		}
		apiResponse, err = radioFranceExtractEmbeddedData(page, "webRadioData")
		if err != nil {
			return Extraction{}, err
		}
	} else {
		endpoint := radioFranceAPIBase + "/" + url.PathEscape(target.station) + "/api/live"
		if err := requestRadioFranceIsolatedJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, nil, &apiResponse); err != nil {
			return Extraction{}, categorizeRadioFranceAPIError(err)
		}
	}
	formats := radioFranceLiveFormats(apiResponse)
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	title := radioFranceLiveTitle(apiResponse)
	if title == "" {
		title = target.station
	}
	id := target.station
	if target.substation != "" {
		id = target.station + "-" + target.substation
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.webpageURL)},
		value.Field{Key: "ext", Value: value.String("aac")},
		value.Field{Key: "is_live", Value: value.Bool(true)},
		value.Field{Key: "live_status", Value: value.String("is_live")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	return Media(value.NewInfo(info)), nil
}

// RadioFrancePodcast extracts bounded public podcast playlists.
type RadioFrancePodcast struct{}

func NewRadioFrancePodcast() RadioFrancePodcast { return RadioFrancePodcast{} }
func (RadioFrancePodcast) Name() string         { return "radiofrance_podcast" }

func (RadioFrancePodcast) Suitable(parsed *url.URL) bool {
	_, ok := classifyRadioFrancePodcastURL(parsed)
	return ok
}

func (RadioFrancePodcast) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyRadioFrancePodcastURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return extractRadioFrancePlaylist(ctx, request, "expressions", target.slug, target.webpageURL, parsed.Path)
}

// RadioFranceProfile extracts bounded public personality playlists.
type RadioFranceProfile struct{}

func NewRadioFranceProfile() RadioFranceProfile { return RadioFranceProfile{} }
func (RadioFranceProfile) Name() string         { return "radiofrance_profile" }

func (RadioFranceProfile) Suitable(parsed *url.URL) bool {
	_, ok := classifyRadioFranceProfileURL(parsed)
	return ok
}

func (RadioFranceProfile) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyRadioFranceProfileURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return extractRadioFrancePlaylist(ctx, request, "documents", target.slug, target.webpageURL, parsed.Path)
}

// RadioFranceProgramSchedule extracts daily program grids.
type RadioFranceProgramSchedule struct{}

func NewRadioFranceProgramSchedule() RadioFranceProgramSchedule { return RadioFranceProgramSchedule{} }
func (RadioFranceProgramSchedule) Name() string                 { return "radiofrance_program_schedule" }

func (RadioFranceProgramSchedule) Suitable(parsed *url.URL) bool {
	_, ok := classifyRadioFranceScheduleURL(parsed)
	return ok
}

func (RadioFranceProgramSchedule) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyRadioFranceScheduleURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	page, err := requestRadioFranceIsolatedPage(ctx, request.Transport, target.webpageURL)
	if err != nil {
		return Extraction{}, categorizeRadioFrancePageError(err)
	}
	gridData, err := radioFranceExtractEmbeddedData(page, "grid")
	if err != nil {
		return Extraction{}, err
	}
	entries, err := radioFranceScheduleEntries(ctx, target.webpageURL, gridData)
	if err != nil {
		return Extraction{}, err
	}
	uploadDate := radioFranceGridUploadDate(gridData)
	playlistID := target.station + "-program-" + uploadDate
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(playlistID)},
		value.Field{Key: "title", Value: value.String(target.station + " program " + uploadDate)},
		value.Field{Key: "webpage_url", Value: value.String(target.webpageURL)},
	)
	if uploadDate != "" {
		info.Set("upload_date", value.String(uploadDate))
	}
	return Playlist(value.NewInfo(info), StaticEntries(entries...))
}

func classifyRadioFranceLegacyURL(parsed *url.URL) (struct {
	id, webpageURL string
}, bool) {
	var target struct {
		id, webpageURL string
	}
	if parsed == nil || hostedRejectUnsafeURL(parsed) {
		return target, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != radioFranceLegacyHost {
		return target, false
	}
	match := regexp.MustCompile(`^/radiovisions/([^?#]+)$`).FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || !radioFranceLegacyIDPattern.MatchString(match[1]) {
		return target, false
	}
	target.id = match[1]
	target.webpageURL = parsed.Scheme + "://" + host + "/radiovisions/" + url.PathEscape(match[1])
	return target, true
}

func classifyFranceCultureURL(parsed *url.URL) (radioFranceEpisodeTarget, bool) {
	if !radioFranceValidWebURL(parsed) {
		return radioFranceEpisodeTarget{}, false
	}
	pattern := regexp.MustCompile(`^/(?:` + radioFranceStationPattern + `)/podcasts/(?:[^?#]+/)?([^?#]+)-(\d{6,})$`)
	match := pattern.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 3 {
		return radioFranceEpisodeTarget{}, false
	}
	displayID, id := match[1], match[2]
	if len(displayID) > radioFranceMaxSlugBytes || !radioFranceEpisodeIDPattern.MatchString(id) {
		return radioFranceEpisodeTarget{}, false
	}
	return radioFranceEpisodeTarget{
		station:    strings.Split(strings.Trim(parsed.Path, "/"), "/")[0],
		displayID:  displayID,
		id:         id,
		webpageURL: radioFranceCanonicalURL(parsed),
	}, true
}

func classifyRadioFrancePodcastURL(parsed *url.URL) (radioFrancePodcastTarget, bool) {
	if !radioFranceValidWebURL(parsed) {
		return radioFrancePodcastTarget{}, false
	}
	if _, ok := classifyFranceCultureURL(parsed); ok {
		return radioFrancePodcastTarget{}, false
	}
	pattern := regexp.MustCompile(`^/(?:` + radioFranceStationPattern + `)/podcasts/([\w-]+)/?$`)
	match := pattern.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || !radioFrancePodcastSlugPattern.MatchString(match[1]) {
		return radioFrancePodcastTarget{}, false
	}
	station := strings.Split(strings.Trim(parsed.Path, "/"), "/")[0]
	return radioFrancePodcastTarget{
		station:    station,
		slug:       match[1],
		webpageURL: radioFranceCanonicalURL(parsed),
	}, true
}

func classifyRadioFranceProfileURL(parsed *url.URL) (radioFranceProfileTarget, bool) {
	if !radioFranceValidWebURL(parsed) {
		return radioFranceProfileTarget{}, false
	}
	match := regexp.MustCompile(`^/personnes/([\w-]+)/?$`).FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || !radioFranceProfileSlugPattern.MatchString(match[1]) {
		return radioFranceProfileTarget{}, false
	}
	if !radioFranceAllowedProfileQuery(parsed) {
		return radioFranceProfileTarget{}, false
	}
	return radioFranceProfileTarget{
		slug:       match[1],
		webpageURL: radioFranceCanonicalURL(parsed),
	}, true
}

func classifyRadioFranceScheduleURL(parsed *url.URL) (radioFranceScheduleTarget, bool) {
	if !radioFranceValidWebURL(parsed) {
		return radioFranceScheduleTarget{}, false
	}
	pattern := regexp.MustCompile(`^/(?:` + radioFranceStationPattern + `)/grille-programmes/?$`)
	if !pattern.MatchString(parsed.EscapedPath()) {
		return radioFranceScheduleTarget{}, false
	}
	if !radioFranceAllowedScheduleQuery(parsed) {
		return radioFranceScheduleTarget{}, false
	}
	station := strings.Split(strings.Trim(parsed.Path, "/"), "/")[0]
	date := strings.TrimSpace(parsed.Query().Get("date"))
	return radioFranceScheduleTarget{
		station:    station,
		date:       date,
		webpageURL: radioFranceCanonicalURL(parsed),
	}, true
}

func classifyRadioFranceLiveURL(parsed *url.URL) (radioFranceStationTarget, bool) {
	if !radioFranceValidWebURL(parsed) {
		return radioFranceStationTarget{}, false
	}
	pattern := regexp.MustCompile(`^/(` + radioFranceStationPattern + `)/?(radio-[\w-]+)?$`)
	match := pattern.FindStringSubmatch(parsed.EscapedPath())
	if len(match) < 2 {
		return radioFranceStationTarget{}, false
	}
	substation := ""
	if len(match) > 2 {
		substation = match[2]
	}
	if substation != "" && !radioFranceSubstationPattern.MatchString(substation) {
		return radioFranceStationTarget{}, false
	}
	return radioFranceStationTarget{
		station:    match[1],
		substation: substation,
		webpageURL: radioFranceCanonicalURL(parsed),
	}, true
}

func radioFranceValidWebURL(parsed *url.URL) bool {
	if parsed == nil || hostedRejectUnsafeURL(parsed) {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "radiofrance.fr" || host == "www.radiofrance.fr"
}

func radioFranceCanonicalURL(parsed *url.URL) string {
	canonical := *parsed
	canonical.Scheme = "https"
	canonical.Host = "www.radiofrance.fr"
	canonical.User = nil
	return canonical.String()
}

func radioFranceAllowedProfileQuery(parsed *url.URL) bool {
	values := parsed.Query()
	for key := range values {
		if key != "p" {
			return false
		}
	}
	if pValues, ok := values["p"]; ok {
		if len(pValues) != 1 {
			return false
		}
		page, err := strconv.Atoi(strings.TrimSpace(pValues[0]))
		if err != nil || page <= 0 || page > defaultMaxPlaylistPages {
			return false
		}
	}
	return true
}

func radioFranceAllowedScheduleQuery(parsed *url.URL) bool {
	values := parsed.Query()
	for key := range values {
		if key != "date" {
			return false
		}
	}
	dateValues, ok := values["date"]
	if !ok {
		return true
	}
	if len(dateValues) != 1 {
		return false
	}
	date := strings.TrimSpace(dateValues[0])
	if date == "" {
		return false
	}
	_, valid := radioFranceParseScheduleDate(date)
	return valid
}

func radioFranceParseScheduleDate(raw string) (time.Time, bool) {
	if !radioFranceScheduleDatePattern.MatchString(raw) {
		return time.Time{}, false
	}
	parsed, err := time.Parse("02-01-2006", raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func requestRadioFranceIsolatedPage(ctx context.Context, transport Transport, rawURL string) ([]byte, error) {
	if transport == nil {
		return nil, ErrUnsupported
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Radio France page request", ErrInvalidMetadata)
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, categorizeRadioFrancePageError(&HTTPStatusError{Code: response.StatusCode})
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, radioFranceMaxHTMLBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read Radio France page failed", ErrInvalidMetadata)
	}
	if len(body) > radioFranceMaxHTMLBytes {
		return nil, ErrJSONResponseTooLarge
	}
	return body, nil
}

func requestRadioFranceIsolatedJSON(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	if transport == nil || target == nil {
		return fmt.Errorf("%w: invalid Radio France JSON request", ErrInvalidMetadata)
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	if headers == nil {
		headers = make(http.Header)
	}
	headers = headers.Clone()
	if body != nil {
		headers.Set("Content-Type", "application/json")
	}
	return requestJSON(ctx, isolated.DoWithoutCredentialsNoRedirect, method, rawURL, body, headers, target)
}

func categorizeRadioFrancePageError(err error) error {
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

func categorizeRadioFranceAPIError(err error) error {
	return categorizeRadioFrancePageError(err)
}

func radioFranceHTMLField(page []byte, pattern *regexp.Regexp) string {
	match := pattern.FindSubmatch(page)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(string(match[1])))
}

func radioFranceBoundedText(text string, maxBytes int) string {
	if text == "" || !utf8.ValidString(text) {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	for maxBytes > 0 && !utf8.ValidString(text[:maxBytes]) {
		maxBytes--
	}
	return text[:maxBytes]
}

func radioFranceExtractAudioObject(page []byte) (map[string]any, error) {
	match := radioFranceAudioObjectPattern.Find(page)
	if match == nil {
		return nil, fmt.Errorf("%w: missing Radio France audio metadata", ErrInvalidMetadata)
	}
	var payload map[string]any
	if err := json.Unmarshal(match, &payload); err != nil {
		return nil, fmt.Errorf("%w: invalid Radio France audio metadata", ErrInvalidMetadata)
	}
	return payload, nil
}

func radioFranceExtractEmbeddedData(page []byte, key string) (map[string]any, error) {
	location := radioFranceDataMarkerPattern.FindIndex(page)
	if location == nil {
		return nil, fmt.Errorf("%w: missing Radio France embedded data", ErrInvalidMetadata)
	}
	start := bytes.IndexByte(page[location[1]:], '{')
	if start < 0 {
		return nil, fmt.Errorf("%w: missing Radio France embedded data object", ErrInvalidMetadata)
	}
	rawObject, err := radioFranceExtractJSONObject(page[location[1]+start:])
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Radio France embedded data", ErrInvalidMetadata)
	}
	var root map[string]any
	if err := json.Unmarshal(rawObject, &root); err != nil {
		return nil, fmt.Errorf("%w: invalid Radio France embedded data", ErrInvalidMetadata)
	}
	data, _ := root["data"].(map[string]any)
	if data == nil {
		return nil, fmt.Errorf("%w: missing Radio France embedded data section", ErrInvalidMetadata)
	}
	section, _ := data[key].(map[string]any)
	if section == nil {
		return nil, fmt.Errorf("%w: missing Radio France %s data", ErrInvalidMetadata, key)
	}
	return section, nil
}

func radioFranceExtractJSONObject(page []byte) ([]byte, error) {
	if len(page) == 0 || page[0] != '{' {
		return nil, fmt.Errorf("object absent")
	}
	depth, quoted, escaped := 0, false, false
	for index := 0; index < len(page) && int64(index) <= radioFranceMaxJSONBytes; index++ {
		character := page[index]
		if quoted {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				quoted = false
			}
			continue
		}
		if character == '"' {
			quoted = true
			continue
		}
		if character == '{' {
			depth++
		}
		if character == '}' {
			depth--
			if depth == 0 {
				return append([]byte(nil), page[:index+1]...), nil
			}
		}
	}
	return nil, fmt.Errorf("unterminated object")
}

func radioFranceLegacyFormats(source string) ([]value.Value, error) {
	matches := radioFranceLegacyFormatPattern.FindAllStringSubmatch(source, -1)
	if len(matches) == 0 {
		return nil, ErrUnavailable
	}
	formats := make([]value.Value, 0, len(matches))
	for index, match := range matches {
		if len(match) != 3 {
			continue
		}
		format, ok := radioFranceDirectAudioFormat(match[2], match[1])
		if !ok {
			return nil, fmt.Errorf("%w: unsafe Radio France legacy audio URL", ErrInvalidMetadata)
		}
		format.Set("vcodec", value.String("none"))
		format.Set("quality", value.Int(int64(index)))
		radioFranceMarkCredentialIsolated(format)
		formats = append(formats, value.ObjectValue(format))
		if len(formats) >= radioFranceMaxFormats {
			break
		}
	}
	return formats, nil
}

func radioFranceDirectAudioFormat(rawURL, formatID string) (*value.Object, bool) {
	if !radioFranceValidMediaURL(rawURL) {
		return nil, false
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(strings.Split(rawURL, "?")[0]), "."))
	if ext == "" {
		ext = "mp3"
	}
	return value.NewObject(
		value.Field{Key: "format_id", Value: value.String(formatID)},
		value.Field{Key: "url", Value: value.String(rawURL)},
		value.Field{Key: "ext", Value: value.String(ext)},
		value.Field{Key: "protocol", Value: value.String("https")},
	), true
}

func radioFranceLiveFormats(apiResponse map[string]any) []value.Value {
	sources := radioFranceLiveMediaSources(apiResponse)
	formats := make([]value.Value, 0, len(sources))
	for index, source := range sources {
		rawURL, _ := source["url"].(string)
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			continue
		}
		formatType, _ := source["format"].(string)
		var format *value.Object
		switch strings.ToLower(formatType) {
		case "hls":
			if !radioFranceValidMediaURL(rawURL) {
				continue
			}
			format = manifestFormat("hls-"+strconv.Itoa(index), rawURL, "m3u8_native")
			format.Set("ext", value.String("aac"))
			format.Set("vcodec", value.String("none"))
		default:
			var ok bool
			format, ok = radioFranceDirectAudioFormat(rawURL, "direct-"+strconv.Itoa(index))
			if !ok {
				continue
			}
			if bitrate, ok := radioFranceIntFromAny(source["bitrate"]); ok && bitrate > 0 {
				format.Set("abr", value.Int(bitrate))
			}
			format.Set("ext", value.String("aac"))
			format.Set("vcodec", value.String("none"))
		}
		radioFranceMarkCredentialIsolated(format)
		formats = append(formats, value.ObjectValue(format))
		if len(formats) >= radioFranceMaxFormats {
			break
		}
	}
	return formats
}

func radioFranceLiveMediaSources(apiResponse map[string]any) []map[string]any {
	now, _ := apiResponse["now"].(map[string]any)
	if now == nil {
		now = apiResponse
	}
	media, _ := now["media"].(map[string]any)
	if media == nil {
		return nil
	}
	rawSources, _ := media["sources"].([]any)
	out := make([]map[string]any, 0, len(rawSources))
	for _, item := range rawSources {
		source, _ := item.(map[string]any)
		if source == nil {
			continue
		}
		if urlValue, _ := source["url"].(string); strings.TrimSpace(urlValue) != "" {
			out = append(out, source)
		}
	}
	return out
}

func radioFranceLiveTitle(apiResponse map[string]any) string {
	if visual, _ := apiResponse["visual"].(map[string]any); visual != nil {
		if legend, _ := visual["legend"].(string); strings.TrimSpace(legend) != "" {
			return radioFranceBoundedText(legend, radioFranceMaxTitleBytes)
		}
	}
	now, _ := apiResponse["now"].(map[string]any)
	if now == nil {
		return ""
	}
	first := radioFranceNestedString(now, "firstLine", "title")
	second := radioFranceNestedString(now, "secondLine", "title")
	switch {
	case first != "" && second != "":
		return radioFranceBoundedText(first+" - "+second, radioFranceMaxTitleBytes)
	case first != "":
		return radioFranceBoundedText(first, radioFranceMaxTitleBytes)
	default:
		return radioFranceBoundedText(second, radioFranceMaxTitleBytes)
	}
}

func radioFranceNestedString(root map[string]any, keys ...string) string {
	current := any(root)
	for _, key := range keys {
		object, _ := current.(map[string]any)
		if object == nil {
			return ""
		}
		current = object[key]
	}
	text, _ := current.(string)
	return strings.TrimSpace(text)
}

func extractRadioFrancePlaylist(ctx context.Context, request Request, metadataKey, displayID, webpageURL, pathValue string) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if pathValue == "" {
		pathValue = "/"
	}
	var metadata map[string]any
	endpoint := radioFrancePathAPI + "?" + url.Values{"value": {pathValue}}.Encode()
	if err := requestRadioFranceIsolatedJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, nil, &metadata); err != nil {
		return Extraction{}, categorizeRadioFranceAPIError(err)
	}
	content, _ := metadata["content"].(map[string]any)
	if content == nil {
		return Extraction{}, ErrUnavailable
	}
	contentID, _ := content["id"].(string)
	if contentID == "" {
		return Extraction{}, fmt.Errorf("%w: missing Radio France playlist id", ErrInvalidMetadata)
	}
	section, _ := content[metadataKey].(map[string]any)
	if section == nil {
		return Extraction{}, fmt.Errorf("%w: missing Radio France playlist section", ErrInvalidMetadata)
	}
	firstEntries, nextCursor, err := radioFrancePlaylistPageEntries(section)
	if err != nil {
		return Extraction{}, err
	}
	sequence, err := ContinuationEntries(firstEntries, nextCursor, func(ctx context.Context, cursor string) ([]Entry, string, error) {
		if err := radioFranceValidateCursor(cursor); err != nil {
			return nil, "", err
		}
		var page map[string]any
		var pageErr error
		switch metadataKey {
		case "expressions":
			pageEndpoint := radioFranceAPIBase + "/api/v2.1/concepts/" + url.PathEscape(contentID) + "/expressions?" + url.Values{"pageCursor": {cursor}}.Encode()
			pageErr = requestRadioFranceIsolatedJSON(ctx, request.Transport, http.MethodGet, pageEndpoint, nil, nil, &page)
		case "documents":
			pageEndpoint := radioFranceAPIBase + "/api/v2.1/taxonomy/" + url.PathEscape(contentID) + "/documents?" + url.Values{
				"relation": {"personality"},
				"cursor":   {cursor},
			}.Encode()
			pageErr = requestRadioFranceIsolatedJSON(ctx, request.Transport, http.MethodGet, pageEndpoint, nil, nil, &page)
		default:
			return nil, "", fmt.Errorf("%w: unknown Radio France playlist kind", ErrInvalidPlaylist)
		}
		if pageErr != nil {
			return nil, "", categorizeRadioFranceAPIError(pageErr)
		}
		entries, next, err := radioFrancePlaylistPageEntries(page)
		return entries, next, err
	})
	if err != nil {
		return Extraction{}, err
	}
	title := radioFrancePlaylistTitle(content, displayID)
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(contentID)},
		value.Field{Key: "display_id", Value: value.String(displayID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
	)
	radioFranceApplyPlaylistMetadata(info, content)
	return Playlist(value.NewInfo(info), sequence)
}

func radioFrancePlaylistTitle(content map[string]any, fallback string) string {
	if title, _ := content["title"].(string); strings.TrimSpace(title) != "" {
		return radioFranceBoundedText(title, radioFranceMaxTitleBytes)
	}
	if name, _ := content["name"].(string); strings.TrimSpace(name) != "" {
		return radioFranceBoundedText(name, radioFranceMaxTitleBytes)
	}
	return fallback
}

func radioFranceApplyPlaylistMetadata(info *value.Object, content map[string]any) {
	if description, _ := content["standFirst"].(string); strings.TrimSpace(description) != "" {
		riskString(info, "description", radioFranceBoundedText(description, radioFranceMaxDescriptionBytes))
	} else if role, _ := content["role"].(string); strings.TrimSpace(role) != "" {
		riskString(info, "description", radioFranceBoundedText(role, radioFranceMaxDescriptionBytes))
	}
	if visual, _ := content["visual"].(map[string]any); visual != nil {
		if thumb := radioFranceValidThumbnail(radioFranceStringFromAny(visual["src"])); thumb != "" {
			info.Set("thumbnail", value.String(thumb))
		}
	}
}

func radioFrancePlaylistPageEntries(section map[string]any) ([]Entry, string, error) {
	rawItems, _ := section["items"].([]any)
	if len(rawItems) > radioFranceMaxPlaylistEntries {
		return nil, "", fmt.Errorf("%w: Radio France playlist page too large", ErrInvalidPlaylist)
	}
	seen := make(map[string]struct{}, len(rawItems))
	entries := make([]Entry, 0, len(rawItems))
	for _, raw := range rawItems {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		entryPath, _ := item["path"].(string)
		entryPath = strings.TrimSpace(entryPath)
		if entryPath == "" || len(entryPath) > radioFranceMaxPathBytes {
			continue
		}
		if _, exists := seen[entryPath]; exists {
			continue
		}
		childURL := radioFranceChildURL(entryPath)
		if childURL == "" {
			continue
		}
		seen[entryPath] = struct{}{}
		entry := Entry{URL: childURL, Transparent: true}
		if title, _ := item["title"].(string); strings.TrimSpace(title) != "" {
			entry.Title = radioFranceBoundedText(title, radioFranceMaxTitleBytes)
		}
		if thumb := radioFrancePlaylistItemThumbnail(item); thumb != "" {
			entry.Thumbnail = thumb
		}
		if timestamp, ok := radioFranceIntFromAny(item["publishedDate"]); ok && timestamp > 0 {
			entry.Timestamp = timestamp
			entry.HasTimestamp = true
		}
		entries = append(entries, entry)
	}
	nextCursor := radioFrancePlaylistNextCursor(section)
	return entries, nextCursor, nil
}

func radioFrancePlaylistNextCursor(section map[string]any) string {
	if next, _ := section["next"].(string); strings.TrimSpace(next) != "" {
		return strings.TrimSpace(next)
	}
	pagination, _ := section["pagination"].(map[string]any)
	if pagination == nil {
		return ""
	}
	next, _ := pagination["next"].(string)
	return strings.TrimSpace(next)
}

func radioFranceValidateCursor(cursor string) error {
	if cursor == "" || len(cursor) > radioFranceMaxCursorBytes || !utf8.ValidString(cursor) {
		return fmt.Errorf("%w: invalid Radio France playlist cursor", ErrInvalidPlaylist)
	}
	return nil
}

func radioFranceChildURL(entryPath string) string {
	entryPath = strings.Trim(entryPath, "/")
	if entryPath == "" || strings.Contains(entryPath, "..") {
		return ""
	}
	child := radioFranceAPIBase + "/" + entryPath
	parsed, err := url.Parse(child)
	if err != nil || !radioFranceValidWebURL(parsed) {
		return ""
	}
	return child
}

func radioFranceScheduleEntries(ctx context.Context, webpageURL string, gridData map[string]any) ([]Entry, error) {
	rawSteps, _ := gridData["steps"].([]any)
	if len(rawSteps) > radioFranceMaxPlaylistEntries {
		return nil, fmt.Errorf("%w: Radio France schedule too large", ErrInvalidPlaylist)
	}
	base, err := url.Parse(webpageURL)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Radio France schedule URL", ErrInvalidMetadata)
	}
	seen := make(map[string]struct{}, len(rawSteps))
	entries := make([]Entry, 0, len(rawSteps))
	for index, raw := range rawSteps {
		if index%32 == 0 {
			if err := contextError(ctx); err != nil {
				return nil, err
			}
		}
		step, _ := raw.(map[string]any)
		if step == nil {
			continue
		}
		expression, _ := step["expression"].(map[string]any)
		if expression == nil {
			continue
		}
		entryPath, _ := expression["path"].(string)
		entryPath = strings.TrimSpace(entryPath)
		if entryPath == "" {
			continue
		}
		if _, exists := seen[entryPath]; exists {
			continue
		}
		resolved := base.ResolveReference(&url.URL{Path: "/" + strings.TrimLeft(entryPath, "/")})
		if !radioFranceValidWebURL(resolved) {
			continue
		}
		seen[entryPath] = struct{}{}
		entry := Entry{
			URL:          resolved.String(),
			ExtractorKey: "franceculture",
			Transparent:  true,
		}
		if title, _ := expression["title"].(string); strings.TrimSpace(title) != "" {
			entry.Title = radioFranceBoundedText(title, radioFranceMaxTitleBytes)
		}
		if visual, _ := expression["visual"].(map[string]any); visual != nil {
			entry.Thumbnail = radioFranceValidThumbnail(radioFranceStringFromAny(visual["src"]))
		}
		if timestamp, ok := radioFranceIntFromAny(step["startTime"]); ok && timestamp > 0 {
			entry.Timestamp = timestamp
			entry.HasTimestamp = true
		}
		if concept, _ := step["concept"].(map[string]any); concept != nil {
			if seriesID := radioFranceBoundedText(radioFranceStringFromAny(concept["id"]), radioFranceMaxSlugBytes); seriesID != "" {
				entry.SeriesID = seriesID
			}
			if series := radioFranceBoundedText(radioFranceStringFromAny(concept["title"]), radioFranceMaxTitleBytes); series != "" {
				entry.Series = series
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func radioFranceGridUploadDate(gridData map[string]any) string {
	rawDate, _ := gridData["date"].(string)
	rawDate = strings.TrimSpace(rawDate)
	if rawDate == "" {
		return ""
	}
	parsed, err := time.Parse("2006-01-02", rawDate)
	if err != nil {
		parsed, err = time.Parse("02-01-2006", rawDate)
	}
	if err != nil {
		return ""
	}
	return parsed.Format("20060102")
}

func radioFranceOGThumbnail(page []byte) string {
	for _, pattern := range []*regexp.Regexp{
		regexp.MustCompile(`(?is)<meta\b[^>]*property=["']og:image["'][^>]*content=["']([^"']+)["']`),
		regexp.MustCompile(`(?is)<meta\b[^>]*content=["']([^"']+)["'][^>]*property=["']og:image["']`),
	} {
		if thumb := radioFranceHTMLField(page, pattern); thumb != "" {
			return thumb
		}
	}
	return ""
}

func radioFranceValidThumbnail(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || !validHTTPURL(rawURL) {
		return ""
	}
	if !radioFranceThumbnailExtPattern.MatchString(rawURL) {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.User != nil || parsed.Port() != "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if !radioFranceAttributableHost(host) {
		return ""
	}
	return rawURL
}

func radioFranceValidMediaURL(rawURL string) bool {
	if !strictValidHostedHTTPURL(rawURL) {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return radioFranceAttributableHost(strings.ToLower(parsed.Hostname()))
}

func radioFranceAttributableHost(host string) bool {
	return host == "radiofrance.fr" || strings.HasSuffix(host, ".radiofrance.fr") ||
		host == radioFranceLegacyHost || strings.HasSuffix(host, ".maison.radiofrance.fr")
}

func radioFranceMarkCredentialIsolated(format *value.Object) {
	format.Set("_credential_isolated", value.Bool(true))
}

func radioFrancePlaylistItemThumbnail(item map[string]any) string {
	if visual, _ := item["visual"].(map[string]any); visual != nil {
		return radioFranceValidThumbnail(radioFranceStringFromAny(visual["src"]))
	}
	return ""
}

func radioFranceStringFromAny(raw any) string {
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	default:
		return ""
	}
}

func radioFranceIntFromAny(raw any) (int64, bool) {
	switch typed := raw.(type) {
	case string:
		if typed == "" {
			return 0, false
		}
		value, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return value, err == nil
	case json.Number:
		value, err := typed.Int64()
		return value, err == nil
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}

func radioFranceParseDuration(input string) float64 {
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

func radioFranceUnifiedStrDate(input string) int64 {
	input = strings.TrimSpace(input)
	if input == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02", "2006-01-02T15:04:05"} {
		if parsed, err := time.Parse(layout, input); err == nil {
			year, month, day := parsed.Date()
			return int64(year*10000 + int(month)*100 + day)
		}
	}
	return 0
}
