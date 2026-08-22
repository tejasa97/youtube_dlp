package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const (
	nrkMaxWebpageBytes   = 4 << 20
	nrkMaxRedirectHops   = 8
	nrkSkoleAPIBase      = "https://nrkno-skole-prod.kube.nrk.no/skole/api/media/"
	nrkMaxSkoleMediaID   = 16
	nrkMaxPlaylistItems  = 200
	nrkMaxRichVideoItems = 64
)

var (
	nrkPodcastUUIDPattern = regexp.MustCompile(`(?i)^l_[\da-f]{8}-[\da-f]{4}-[\da-f]{4}-[\da-f]{4}-[\da-f]{12}$`)
	nrkRichVideoIDPattern = regexp.MustCompile(`(?i)class="[^"]*\brich\b[^"]*"[^>]+data-video-id="([^"]+)"`)
	nrkEpisodeDataPattern = regexp.MustCompile(`(?i)data-episode=["']([a-zA-Z]{4}\d{8})`)
	nrkPageDataPattern    = regexp.MustCompile(`(?is)<script\b[^>]+\bid="pageData"[^>]*>([^<]+)</script>`)
	nrkOGTitlePattern     = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:title["'][^>]+content=["']([^"']+)["']`)
	nrkOGDescPattern      = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:description["'][^>]+content=["']([^"']+)["']`)
	nrkTitleH1Pattern     = regexp.MustCompile(`(?is)<h1[^>]*>([^<]+)</h1>`)
)

type nrkDomain string

const (
	nrkDomainTV    nrkDomain = "tv"
	nrkDomainRadio nrkDomain = "radio"
)

func nrkRejectUnsafeURL(parsed *url.URL) bool {
	if parsed == nil || len(parsed.String()) > sharedHostingMaxURLBytes {
		return true
	}
	return strictURLPolicyRejects(parsed)
}

func nrkTVIEHost(host string) bool {
	switch strings.ToLower(host) {
	case "tv.nrk.no", "tv.nrksuper.no", "radio.nrk.no", "radio.nrksuper.no":
		return true
	default:
		return false
	}
}

func nrkSeriesHost(host string) bool {
	switch strings.ToLower(host) {
	case "tv.nrk.no", "tv.nrksuper.no", "nrksuper.no", "radio.nrk.no":
		return true
	default:
		return false
	}
}

func nrkArticleHost(host string) bool {
	switch strings.ToLower(host) {
	case "nrk.no", "www.nrk.no":
		return true
	default:
		return false
	}
}

func nrkHostDomain(host string) (nrkDomain, bool) {
	switch strings.ToLower(host) {
	case "tv.nrk.no", "tv.nrksuper.no", "nrksuper.no", "www.nrksuper.no":
		return nrkDomainTV, true
	case "radio.nrk.no":
		return nrkDomainRadio, true
	default:
		return "", false
	}
}

func nrkSkoleMediaID(parsed *url.URL) (string, bool) {
	if parsed == nil {
		return "", false
	}
	if strings.Trim(parsed.EscapedPath(), "/") != "skole" {
		return "", false
	}
	if parsed.RawQuery == "" {
		return "", false
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", false
	}
	mediaIDs, ok := values["mediaId"]
	if !ok || len(mediaIDs) != 1 {
		return "", false
	}
	mediaID := mediaIDs[0]
	if mediaID == "" || len(mediaID) > nrkMaxSkoleMediaID || !nrkDigitsOnly.MatchString(mediaID) {
		return "", false
	}
	return mediaID, true
}

func nrkPathParts(parsed *url.URL) []string {
	return strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
}

func nrkMatchProgramID(segment string) (string, bool) {
	if match := nrkPathProgramID.FindStringSubmatch(segment); len(match) == 2 {
		return strings.ToUpper(match[1]), true
	}
	return "", false
}

func nrkFindProgramID(parts []string) (string, bool) {
	for _, part := range parts {
		if id, ok := nrkMatchProgramID(part); ok {
			return id, true
		}
	}
	return "", false
}

func nrkTransparentMedia(id string) (Extraction, error) {
	if id == "" || (!nrkProgramIDPattern.MatchString(id) && !nrkPodcastUUIDPattern.MatchString(id) && !nrkGeneralIDPattern.MatchString(id)) {
		return Extraction{}, fmt.Errorf("%w: invalid NRK media id", ErrInvalidMetadata)
	}
	return URLResult(Entry{URL: "nrk:" + id, ExtractorKey: "nrk", ID: id, Transparent: true})
}

func nrkCatalogKind(serieKind string) string {
	if serieKind == "podcast" || serieKind == "podkast" {
		return "podcast"
	}
	return "series"
}

// NRKSkole resolves educational media IDs via the Skole API and reenters NRK playback.
type NRKSkole struct{}

func NewNRKSkole() NRKSkole                    { return NRKSkole{} }
func (NRKSkole) Name() string                  { return "nrk_skole" }
func (NRKSkole) Suitable(parsed *url.URL) bool { return nrkSkoleSuitable(parsed) }
func (NRKSkole) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !nrkSkoleSuitable(parsed) || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	mediaID, ok := nrkSkoleMediaID(parsed)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: invalid NRK Skole media id", ErrInvalidMetadata)
	}
	var payload struct {
		PSID string `json:"psId"`
	}
	if err := requestNRKSkoleMedia(ctx, request.Transport, mediaID, &payload); err != nil {
		return Extraction{}, err
	}
	if payload.PSID == "" {
		return Extraction{}, fmt.Errorf("%w: missing NRK Skole psId", ErrInvalidMetadata)
	}
	return nrkTransparentMedia(payload.PSID)
}

func nrkSkoleSuitable(parsed *url.URL) bool {
	if nrkRejectUnsafeURL(parsed) || !nrkArticleHost(parsed.Hostname()) {
		return false
	}
	_, ok := nrkSkoleMediaID(parsed)
	return ok
}

func requestNRKSkoleMedia(ctx context.Context, transport Transport, mediaID string, target any) error {
	endpoint := nrkSkoleAPIBase + url.PathEscape(mediaID)
	body, err := requestNRKIsolated(ctx, transport, http.MethodGet, endpoint, nrkMaxJSONBytes, nil)
	if err != nil {
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
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid NRK Skole response", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: trailing NRK Skole response", ErrInvalidMetadata)
	}
	return nil
}

// NRKRadioPodkast resolves podcast episode UUIDs and reenters NRK playback.
type NRKRadioPodkast struct{}

func NewNRKRadioPodkast() NRKRadioPodkast             { return NRKRadioPodkast{} }
func (NRKRadioPodkast) Name() string                  { return "nrk_radio_podkast" }
func (NRKRadioPodkast) Suitable(parsed *url.URL) bool { return nrkRadioPodkastSuitable(parsed) }
func (NRKRadioPodkast) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !nrkRadioPodkastSuitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	parts := nrkPathParts(parsed)
	if len(parts) == 0 {
		return Extraction{}, ErrUnsupported
	}
	id := parts[len(parts)-1]
	return nrkTransparentMedia(id)
}

func nrkRadioPodkastSuitable(parsed *url.URL) bool {
	if nrkRejectUnsafeURL(parsed) || strings.ToLower(parsed.Hostname()) != "radio.nrk.no" {
		return false
	}
	parts := nrkPathParts(parsed)
	if len(parts) < 3 || (parts[0] != "podcast" && parts[0] != "podkast") {
		return false
	}
	return nrkPodcastUUIDPattern.MatchString(parts[len(parts)-1])
}

// NRKTVEpisode resolves canonical episode URLs via redirect or pageData JSON.
type NRKTVEpisode struct{}

func NewNRKTVEpisode() NRKTVEpisode                { return NRKTVEpisode{} }
func (NRKTVEpisode) Name() string                  { return "nrktv_episode" }
func (NRKTVEpisode) Suitable(parsed *url.URL) bool { return nrkTVEpisodeSuitable(parsed) }
func (NRKTVEpisode) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !nrkTVEpisodeSuitable(parsed) || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	page, finalURL, err := nrkFetchEpisodePage(ctx, request.Transport, parsed.String())
	if err != nil {
		return Extraction{}, err
	}
	if finalParsed, err := url.Parse(finalURL); err == nil {
		if id, ok := nrkFindProgramID(nrkPathParts(finalParsed)); ok {
			return nrkTransparentMedia(id)
		}
	}
	match := nrkPageDataPattern.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, fmt.Errorf("%w: missing NRK episode page data", ErrInvalidMetadata)
	}
	var pageData struct {
		InitialState struct {
			SelectedEpisodePrfID string `json:"selectedEpisodePrfId"`
		} `json:"initialState"`
	}
	if err := json.Unmarshal(match[1], &pageData); err != nil || pageData.InitialState.SelectedEpisodePrfID == "" {
		return Extraction{}, fmt.Errorf("%w: invalid NRK episode page data", ErrInvalidMetadata)
	}
	id := pageData.InitialState.SelectedEpisodePrfID
	if !nrkProgramIDPattern.MatchString(id) {
		return Extraction{}, fmt.Errorf("%w: invalid NRK episode id", ErrInvalidMetadata)
	}
	return nrkTransparentMedia(id)
}

func nrkTVEpisodeSuitable(parsed *url.URL) bool {
	if nrkRejectUnsafeURL(parsed) || strings.ToLower(parsed.Hostname()) != "tv.nrk.no" {
		return false
	}
	parts := nrkPathParts(parsed)
	return len(parts) == 6 && parts[0] == "serie" && parts[2] == "sesong" && parts[4] == "episode" &&
		nrkGeneralIDPattern.MatchString(parts[1]) && nrkDigitsOnly.MatchString(parts[3]) &&
		nrkDigitsOnly.MatchString(parts[5])
}

// NRKTVEpisodes resolves legacy episode listing pages.
type NRKTVEpisodes struct{}

func NewNRKTVEpisodes() NRKTVEpisodes               { return NRKTVEpisodes{} }
func (NRKTVEpisodes) Name() string                  { return "nrktv_episodes" }
func (NRKTVEpisodes) Suitable(parsed *url.URL) bool { return nrkTVEpisodesSuitable(parsed) }
func (NRKTVEpisodes) Extract(ctx context.Context, request Request) (Extraction, error) {
	return nrkExtractHTMLPlaylist(ctx, request, nrkTVEpisodesSuitable, nrkEpisodeDataPattern)
}

func nrkTVEpisodesSuitable(parsed *url.URL) bool {
	if nrkRejectUnsafeURL(parsed) || strings.ToLower(parsed.Hostname()) != "tv.nrk.no" {
		return false
	}
	parts := nrkPathParts(parsed)
	return len(parts) == 4 && strings.EqualFold(parts[0], "program") && strings.EqualFold(parts[1], "episodes") &&
		nrkGeneralIDPattern.MatchString(parts[2]) && nrkDigitsOnly.MatchString(parts[3])
}

// NRKTVDirekte resolves live channel URLs and reenters NRK playback.
type NRKTVDirekte struct{}

func NewNRKTVDirekte() NRKTVDirekte                { return NRKTVDirekte{} }
func (NRKTVDirekte) Name() string                  { return "nrktv_direkte" }
func (NRKTVDirekte) Suitable(parsed *url.URL) bool { return nrkTVDirekteSuitable(parsed) }
func (NRKTVDirekte) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !nrkTVDirekteSuitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	parts := nrkPathParts(parsed)
	return nrkTransparentMedia(parts[1])
}

func nrkTVDirekteSuitable(parsed *url.URL) bool {
	if nrkRejectUnsafeURL(parsed) {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "tv.nrk.no" && host != "radio.nrk.no" {
		return false
	}
	parts := nrkPathParts(parsed)
	return len(parts) == 2 && parts[0] == "direkte" && nrkGeneralIDPattern.MatchString(parts[1])
}

// NRKTV resolves program IDs embedded in TV and radio paths.
type NRKTV struct{}

func NewNRKTV() NRKTV                       { return NRKTV{} }
func (NRKTV) Name() string                  { return "nrktv" }
func (NRKTV) Suitable(parsed *url.URL) bool { return nrkTVSuitable(parsed) }
func (NRKTV) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !nrkTVSuitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	id, ok := nrkFindProgramID(nrkPathParts(parsed))
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return nrkTransparentMedia(id)
}

func nrkTVSuitable(parsed *url.URL) bool {
	if nrkRejectUnsafeURL(parsed) || !nrkTVIEHost(parsed.Hostname()) {
		return false
	}
	if nrkRadioPodkastSuitable(parsed) || nrkTVEpisodeSuitable(parsed) ||
		nrkTVEpisodesSuitable(parsed) || nrkTVDirekteSuitable(parsed) {
		return false
	}
	if _, ok := nrkSeasonTarget(parsed); ok {
		return false
	}
	if _, ok := nrkSeriesTarget(parsed); ok {
		return false
	}
	_, ok := nrkFindProgramID(nrkPathParts(parsed))
	return ok
}

// NRKTVSeason resolves season catalog playlists.
type NRKTVSeason struct{}

func NewNRKTVSeason() NRKTVSeason                 { return NRKTVSeason{} }
func (NRKTVSeason) Name() string                  { return "nrktv_season" }
func (NRKTVSeason) Suitable(parsed *url.URL) bool { return nrkTVSeasonSuitable(parsed) }
func (NRKTVSeason) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !nrkTVSeasonSuitable(parsed) || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := nrkSeasonTarget(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return extractNRKPlaylist(ctx, request.Transport, target, request.URL)
}

func nrkTVSeasonSuitable(parsed *url.URL) bool {
	if nrkRejectUnsafeURL(parsed) {
		return false
	}
	domain, ok := nrkHostDomain(parsed.Hostname())
	if !ok || (domain != nrkDomainTV && domain != nrkDomainRadio) {
		return false
	}
	if nrkTVEpisodeSuitable(parsed) || nrkRadioPodkastSuitable(parsed) {
		return false
	}
	_, ok = nrkSeasonTarget(parsed)
	return ok
}

func nrkSeasonTarget(parsed *url.URL) (nrkTarget, bool) {
	parts := nrkPathParts(parsed)
	if len(parts) < 3 {
		return nrkTarget{}, false
	}
	if parts[0] != "serie" && parts[0] != "podcast" && parts[0] != "podkast" {
		return nrkTarget{}, false
	}
	if !nrkGeneralIDPattern.MatchString(parts[1]) {
		return nrkTarget{}, false
	}
	domain, _ := nrkHostDomain(parsed.Hostname())
	seasonID := ""
	switch {
	case len(parts) == 3 && nrkDigitsOnly.MatchString(parts[2]):
		seasonID = parts[2]
	case len(parts) == 4 && parts[2] == "sesong" && nrkGeneralIDPattern.MatchString(parts[3]):
		seasonID = parts[3]
	default:
		return nrkTarget{}, false
	}
	return nrkTarget{
		id: parts[1] + "/" + seasonID, series: parts[1], season: seasonID,
		domain: string(domain), kind: "season", playlist: true, serieKind: parts[0],
	}, true
}

// NRKTVSeries resolves series catalog playlists with optional season delegation.
type NRKTVSeries struct{}

func NewNRKTVSeries() NRKTVSeries                 { return NRKTVSeries{} }
func (NRKTVSeries) Name() string                  { return "nrktv_series" }
func (NRKTVSeries) Suitable(parsed *url.URL) bool { return nrkTVSeriesSuitable(parsed) }
func (NRKTVSeries) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !nrkTVSeriesSuitable(parsed) || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := nrkSeriesTarget(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return extractNRKSeriesPlaylist(ctx, request.Transport, target, request.URL)
}

func nrkTVSeriesSuitable(parsed *url.URL) bool {
	if nrkRejectUnsafeURL(parsed) || !nrkSeriesHost(parsed.Hostname()) {
		return false
	}
	domain, ok := nrkHostDomain(parsed.Hostname())
	if !ok || (domain != nrkDomainTV && domain != nrkDomainRadio) {
		return false
	}
	if nrkTVEpisodeSuitable(parsed) || nrkRadioPodkastSuitable(parsed) {
		return false
	}
	if _, ok := nrkSeasonTarget(parsed); ok {
		return false
	}
	if _, ok := nrkFindProgramID(nrkPathParts(parsed)); ok {
		return false
	}
	_, ok = nrkSeriesTarget(parsed)
	return ok
}

func nrkSeriesTarget(parsed *url.URL) (nrkTarget, bool) {
	parts := nrkPathParts(parsed)
	if len(parts) != 2 {
		return nrkTarget{}, false
	}
	if parts[0] != "serie" && parts[0] != "podcast" && parts[0] != "podkast" {
		return nrkTarget{}, false
	}
	if nrkPodcastUUIDPattern.MatchString(parts[1]) {
		return nrkTarget{}, false
	}
	if !nrkGeneralIDPattern.MatchString(parts[1]) {
		return nrkTarget{}, false
	}
	domain, _ := nrkHostDomain(parsed.Hostname())
	return nrkTarget{
		id: parts[1], series: parts[1], domain: string(domain),
		kind: "series", playlist: true, serieKind: parts[0],
	}, true
}

// NRKPlaylist resolves nrk.no article pages with embedded rich video widgets.
type NRKPlaylist struct{}

func NewNRKPlaylist() NRKPlaylist                 { return NRKPlaylist{} }
func (NRKPlaylist) Name() string                  { return "nrk_playlist" }
func (NRKPlaylist) Suitable(parsed *url.URL) bool { return nrkPlaylistSuitable(parsed) }
func (NRKPlaylist) Extract(ctx context.Context, request Request) (Extraction, error) {
	return nrkExtractHTMLPlaylist(ctx, request, nrkPlaylistSuitable, nrkRichVideoIDPattern)
}

func nrkPlaylistSuitable(parsed *url.URL) bool {
	if nrkRejectUnsafeURL(parsed) || !nrkArticleHost(parsed.Hostname()) {
		return false
	}
	if nrkSkoleSuitable(parsed) {
		return false
	}
	parts := nrkPathParts(parsed)
	if len(parts) < 2 {
		return false
	}
	if parts[0] == "video" || parts[0] == "skole" {
		return false
	}
	return true
}

func nrkExtractHTMLPlaylist(ctx context.Context, request Request, suitable func(*url.URL) bool, itemPattern *regexp.Regexp) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !suitable(parsed) || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parts := nrkPathParts(parsed)
	if len(parts) == 0 {
		return Extraction{}, fmt.Errorf("%w: missing NRK playlist id", ErrInvalidPlaylist)
	}
	playlistID := parts[len(parts)-1]
	page, err := requestNRKHTMLPage(ctx, request.Transport, parsed.String())
	if err != nil {
		return Extraction{}, err
	}
	matches := itemPattern.FindAllSubmatch(page, nrkMaxRichVideoItems+1)
	if len(matches) == 0 {
		return Extraction{}, fmt.Errorf("%w: missing NRK playlist entries", ErrInvalidPlaylist)
	}
	if len(matches) > nrkMaxRichVideoItems {
		return Extraction{}, fmt.Errorf("%w: NRK playlist too large", ErrInvalidPlaylist)
	}
	entries := make([]Entry, 0, len(matches))
	seen := make(map[string]bool)
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		id := string(match[1])
		if !nrkProgramIDPattern.MatchString(id) || seen[id] {
			continue
		}
		seen[id] = true
		entries = append(entries, Entry{URL: "nrk:" + id, ExtractorKey: "nrk", ID: id, Transparent: true})
	}
	if len(entries) == 0 {
		return Extraction{}, fmt.Errorf("%w: missing NRK playlist entries", ErrInvalidPlaylist)
	}
	title := nrkFirstHTMLMatch(page, nrkOGTitlePattern)
	if title == "" {
		title = nrkFirstHTMLMatch(page, nrkTitleH1Pattern)
	}
	if title == "" {
		title = playlistID
	}
	description := nrkFirstHTMLMatch(page, nrkOGDescPattern)
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(playlistID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(request.URL)},
	)
	riskString(info, "description", description)
	return Playlist(value.NewInfo(info), StaticEntries(entries...))
}

func requestNRKHTMLPage(ctx context.Context, transport Transport, rawURL string) ([]byte, error) {
	page, err := requestNRKIsolated(ctx, transport, http.MethodGet, rawURL, nrkMaxWebpageBytes, http.Header{
		"Accept": {"text/html,application/xhtml+xml"},
	})
	if err != nil {
		return nil, categorizeNRKIsolatedPageError(err)
	}
	return page, nil
}

func nrkFirstHTMLMatch(page []byte, pattern *regexp.Regexp) string {
	match := pattern.FindSubmatch(page)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(string(match[1]))
}

func nrkFetchEpisodePage(ctx context.Context, transport Transport, rawURL string) ([]byte, string, error) {
	current := rawURL
	base, err := url.Parse(current)
	if err != nil || nrkRejectUnsafeURL(base) || base.Scheme != "https" || strings.ToLower(base.Hostname()) != "tv.nrk.no" {
		return nil, "", fmt.Errorf("%w: invalid NRK episode URL", ErrInvalidMetadata)
	}
	seen := map[string]struct{}{current: {}}
	for hop := 0; hop < nrkMaxRedirectHops; hop++ {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		page, status, location, err := nrkEpisodePageHop(ctx, transport, current)
		if err != nil {
			return nil, "", err
		}
		if status >= 300 && status < 400 {
			if location == "" {
				return nil, "", fmt.Errorf("%w: invalid NRK episode redirect", ErrInvalidMetadata)
			}
			next, err := base.Parse(location)
			if err != nil || nrkRejectUnsafeURL(next) || next.Scheme != "https" ||
				strings.ToLower(next.Hostname()) != "tv.nrk.no" || next.User != nil || next.Port() != "" ||
				next.Fragment != "" || next.RawFragment != "" {
				return nil, "", fmt.Errorf("%w: invalid NRK episode redirect", ErrInvalidMetadata)
			}
			current = next.String()
			if _, ok := seen[current]; ok {
				return nil, "", fmt.Errorf("%w: NRK episode redirect loop", ErrInvalidPlaylist)
			}
			seen[current] = struct{}{}
			base = next
			continue
		}
		if status != http.StatusOK {
			return nil, "", categorizeNRKHTTPStatus(status)
		}
		return page, current, nil
	}
	return nil, "", fmt.Errorf("%w: NRK episode redirect limit", ErrInvalidPlaylist)
}

func nrkEpisodePageHop(ctx context.Context, transport Transport, rawURL string) ([]byte, int, string, error) {
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, 0, "", ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, "", fmt.Errorf("%w: invalid NRK episode request", ErrInvalidMetadata)
	}
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, 0, "", err
	}
	if response == nil || response.Body == nil {
		return nil, 0, "", fmt.Errorf("%w: empty NRK episode response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return nil, response.StatusCode, response.Header.Get("Location"), nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, response.StatusCode, "", nil
	}
	page, err := io.ReadAll(io.LimitReader(response.Body, nrkMaxWebpageBytes+1))
	if err != nil {
		return nil, 0, "", fmt.Errorf("%w: read NRK episode page failed", ErrNRKHTMLNetwork)
	}
	if int64(len(page)) > nrkMaxWebpageBytes {
		return nil, 0, "", ErrJSONResponseTooLarge
	}
	return page, response.StatusCode, "", nil
}
