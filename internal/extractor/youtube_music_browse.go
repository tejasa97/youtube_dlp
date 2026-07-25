package extractor

// Bounded WEB_REMIX Music browse for registered album, artist, playlist, and
// podcast families at music.youtube.com/browse/{id}. Every Music browse network
// request requires cookie isolation and uses WEB_REMIX only; WEB SID state never
// crosses. Anonymous public pages succeed; logged-in or premium Music pages fail
// closed until a secure WEB_REMIX auth boundary exists. Albums require a
// canonical playlist identity or fail closed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	youtubeMusicBrowseMaxCount       = 100
	youtubeMusicBrowseMaxURLBytes    = 4096
	youtubeMusicBrowseMaxPageBytes   = 16 << 20
	youtubeMusicBrowseMaxParamsBytes = 4096
	youtubeMusicBrowseAPIURL         = "https://music.youtube.com/youtubei/v1/browse"
	youtubeMusicBrowseResolveURL     = "https://music.youtube.com/youtubei/v1/navigation/resolve_url"
	youtubeMusicDefaultVersion       = "1.20260707.12.00"
)

var (
	ErrYouTubeMusicBrowseRateLimited = errors.New("YouTube Music browse rate limited")
	ErrYouTubeMusicBrowseNetwork     = errors.New("YouTube Music browse network failure")
)

// YouTubeMusicBrowse accepts exact music.youtube.com/browse URLs for the
// registered Music browse ID families emitted by Music search.
type YouTubeMusicBrowse struct{}

func NewYouTubeMusicBrowse() YouTubeMusicBrowse { return YouTubeMusicBrowse{} }
func (YouTubeMusicBrowse) Name() string         { return "youtube_music_browse" }
func (YouTubeMusicBrowse) Suitable(u *url.URL) bool {
	_, _, _, ok := youtubeMusicBrowseTarget(u)
	return ok
}

func (YouTubeMusicBrowse) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	u, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid YouTube Music browse URL", ErrUnsupported)
	}
	browseID, family, canonical, ok := youtubeMusicBrowseTarget(u)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: unsupported YouTube Music browse", ErrUnsupported)
	}
	// Require cookie isolation before any Music browse network request.
	if _, ok := request.Transport.(CookieIsolatedTransport); !ok {
		return Extraction{}, ErrTransportIsolation
	}
	page, err := readYouTubeMusicBrowsePage(ctx, request.Transport, canonical)
	if err != nil {
		return Extraction{}, categorizeYouTubeMusicBrowseError(err)
	}
	if err := youtubeMusicBrowseRejectAuthenticatedPage(page); err != nil {
		return Extraction{}, err
	}
	config := extractYouTubePlaylistConfig(page)
	if config.ClientVersion == youtubeDefaultClientVersion {
		if version := discoverYouTubePageConfig(page).ClientVersion; version != "" {
			config.ClientVersion = version
		} else {
			config.ClientVersion = youtubeMusicDefaultVersion
		}
	}
	raw, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: YouTube Music browse initial data", ErrInvalidMetadata)
	}
	parsed, err := parseYouTubeMusicBrowseData(raw)
	if err != nil {
		return Extraction{}, err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return Extraction{}, youtubeMusicBrowseAlertError(parsed.alert)
	}
	meta := youtubeMusicBrowseInfo(raw, parsed)
	if family == "album" {
		if meta.playlistID == "" {
			resolved, resolveErr := resolveYouTubeMusicAlbum(ctx, request.Transport, canonical, browseID, config)
			if resolveErr != nil {
				return Extraction{}, resolveErr
			}
			if len(parsed.entries) == 0 {
				parsed = resolved.page
			}
			meta.playlistID = resolved.playlistID
			if meta.title == "" {
				meta.title = resolved.title
			}
			if meta.title == "" {
				meta.title = resolved.page.title
			}
			if parsed.visitorData == "" {
				parsed.visitorData = resolved.page.visitorData
			}
			if parsed.continuation == "" {
				parsed.continuation = resolved.page.continuation
			}
		}
		if meta.playlistID == "" {
			return Extraction{}, fmt.Errorf("%w: album missing canonical playlist identity", ErrUnavailable)
		}
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return Extraction{}, youtubeMusicBrowseAlertError(parsed.alert)
	}
	if meta.title == "" {
		meta.title = browseID
	}
	visitorData := parsed.visitorData
	if visitorData == "" {
		visitorData = config.VisitorData
	}
	entries, err := youtubeMusicBrowseEntries(parsed.entries, parsed.continuation, visitorData, youtubeMusicBrowseMaxCount, func(ctx context.Context, token, visitor string) ([]Entry, string, string, error) {
		return fetchYouTubeMusicBrowseContinuation(ctx, request.Transport, token, visitor, config)
	})
	if err != nil {
		return Extraction{}, err
	}
	id := browseID
	if family == "album" {
		id = meta.playlistID
	} else if family == "playlist" && strings.HasPrefix(browseID, "VL") {
		id = browseID[2:]
	} else if family == "podcast" && strings.HasPrefix(browseID, "MPSP") {
		id = browseID[4:]
	} else if family == "artist" && strings.HasPrefix(browseID, "MPLA") {
		id = browseID[4:]
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(meta.title)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
		value.Field{Key: "extractor", Value: value.String("youtube_music_browse")},
		value.Field{Key: "music_browse_family", Value: value.String(family)},
	))
	return Playlist(info, entries)
}

type youtubeMusicBrowseMeta struct {
	title      string
	playlistID string
}

func youtubeMusicBrowseInfo(data []byte, page youtubeRendererPage) youtubeMusicBrowseMeta {
	meta := youtubeMusicBrowseMeta{title: page.title}
	var root value.Value
	if json.Unmarshal(data, &root) != nil {
		return meta
	}
	rootObject, ok := root.Object()
	if !ok {
		return meta
	}
	nodes := 0
	_ = walkOrderedJSON(rootObject.Lookup("microformat"), 0, &nodes, func(key string, object *value.Object) {
		if key != "microformatDataRenderer" {
			return
		}
		if meta.title == "" {
			meta.title = objectString(object, "title")
		}
		if canonicalURL := objectString(object, "urlCanonical"); canonicalURL != "" {
			if id, ok := youtubeMusicAlbumPlaylistIDFromCanonical(canonicalURL); ok {
				meta.playlistID = id
			}
		}
	})
	headerNodes := 0
	_ = walkOrderedJSON(rootObject.Lookup("header"), 0, &headerNodes, func(key string, object *value.Object) {
		switch key {
		case "musicDetailHeaderRenderer", "musicEditablePlaylistDetailHeaderRenderer", "musicImmersiveHeaderRenderer":
			if meta.title == "" {
				meta.title = rendererText(object.Lookup("title"))
			}
		}
	})
	return meta
}

// youtubeMusicAlbumPlaylistIDFromCanonical accepts only exact Music playlist
// canonical URLs. Hostile hosts, ports, userinfo, and non-playlist paths fail.
func youtubeMusicAlbumPlaylistIDFromCanonical(raw string) (string, bool) {
	if raw == "" || len(raw) > youtubeMusicBrowseMaxURLBytes || strings.ContainsAny(raw, "\x00\r\n") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Fragment != "" || parsed.RawPath != "" {
		return "", false
	}
	if strings.ToLower(strings.TrimSuffix(parsed.Hostname(), ".")) != "music.youtube.com" || parsed.Port() != "" {
		return "", false
	}
	if parsed.Path != "/playlist" {
		return "", false
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(values) == 0 {
		return "", false
	}
	id := values.Get("list")
	if !youtubePlaylistIDPattern.MatchString(id) || strings.ContainsAny(id, "\x00\r\n/\\?#") {
		return "", false
	}
	for key := range values {
		if key != "list" {
			return "", false
		}
	}
	return id, true
}

func youtubeMusicBrowseTarget(u *url.URL) (browseID, family, canonical string, ok bool) {
	if u == nil || len(u.String()) > youtubeMusicBrowseMaxURLBytes {
		return
	}
	if _, _, err := validateYouTubeURLPolicy(u); err != nil {
		return
	}
	if strings.ToLower(strings.TrimSuffix(u.Hostname(), ".")) != "music.youtube.com" {
		return
	}
	if u.Port() != "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return
	}
	if u.RawPath != "" {
		return
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 3 || parts[1] != "browse" || parts[2] == "" {
		return
	}
	browseID = parts[2]
	family, ok = youtubeMusicBrowseFamily(browseID)
	if !ok {
		return "", "", "", false
	}
	return browseID, family, "https://music.youtube.com/browse/" + browseID, true
}

// youtubeMusicBrowseFamily reports whether id belongs to a registered Music
// browse consumer family. Unregistered prefixes stay omitted from search.
func youtubeMusicBrowseFamily(id string) (family string, ok bool) {
	if !validYouTubeMusicBrowseID(id) {
		return "", false
	}
	switch {
	case strings.HasPrefix(id, "MPRE"):
		return "album", true
	case strings.HasPrefix(id, "MPSP"):
		return "podcast", true
	case strings.HasPrefix(id, "VL") && youtubePlaylistIDPattern.MatchString(id[2:]):
		return "playlist", true
	case youtubeChannelIDPattern.MatchString(id):
		return "artist", true
	case strings.HasPrefix(id, "MPLA") && youtubeChannelIDPattern.MatchString(id[4:]):
		return "artist", true
	default:
		return "", false
	}
}

func youtubeMusicBrowseResult(browseID, title string) Entry {
	return Entry{
		URL: "https://music.youtube.com/browse/" + browseID, ExtractorKey: "youtube_music_browse",
		ID: browseID, Title: title,
	}
}

func parseYouTubeMusicBrowseData(data []byte) (youtubeRendererPage, error) {
	// Browse pages yield playable tracks/episodes. Nested Music browse tiles are
	// not hydrated here; search remains responsible for emitting browse URLs.
	return parseYouTubeRendererData(data, youtubeRendererPolicy{kinds: youtubeRendererVideo})
}

func youtubeMusicBrowseEntries(first []Entry, token, visitor string, count int, fetch StatefulContinuationFetcher) (EntrySequence, error) {
	if count < 1 || count > youtubeMusicBrowseMaxCount {
		return nil, fmt.Errorf("%w: invalid YouTube Music browse count", ErrInvalidPlaylist)
	}
	base, err := StatefulContinuationEntries(first, token, visitor, fetch)
	if err != nil {
		return nil, err
	}
	return limitedEntries{source: base, limit: count}, nil
}

func fetchYouTubeMusicBrowseContinuation(ctx context.Context, transport Transport, token, visitorData string, config youtubePlaylistConfig) ([]Entry, string, string, error) {
	if token = validYouTubeContinuationToken(token); token == "" {
		return nil, "", visitorData, fmt.Errorf("%w: invalid YouTube Music browse continuation", ErrInvalidPlaylist)
	}
	version := config.ClientVersion
	if version == "" {
		version = youtubeMusicDefaultVersion
	}
	body, err := json.Marshal(map[string]any{"context": map[string]any{"client": map[string]any{
		"clientName": "WEB_REMIX", "clientVersion": version, "hl": "en",
		"timeZone": "UTC", "utcOffsetMinutes": 0, "visitorData": visitorData,
	}}, "continuation": token})
	if err != nil {
		return nil, "", visitorData, fmt.Errorf("%w: encode YouTube Music browse continuation", ErrInvalidMetadata)
	}
	response, err := postYouTubeMusicRemixJSON(ctx, transport, youtubeMusicBrowseAPIURL, body, version, config.APIKey)
	if err != nil {
		return nil, "", visitorData, categorizeYouTubeMusicBrowseError(err)
	}
	page, err := parseYouTubeMusicBrowseData(response)
	if err != nil {
		return nil, "", visitorData, err
	}
	if page.alert != "" && len(page.entries) == 0 {
		return nil, "", visitorData, youtubeMusicBrowseAlertError(page.alert)
	}
	if page.visitorData == "" {
		page.visitorData = visitorData
	}
	return page.entries, page.continuation, page.visitorData, nil
}

type youtubeMusicAlbumResolve struct {
	page       youtubeRendererPage
	playlistID string
	title      string
}

func resolveYouTubeMusicAlbum(ctx context.Context, transport Transport, pageURL, browseID string, config youtubePlaylistConfig) (youtubeMusicAlbumResolve, error) {
	version := config.ClientVersion
	if version == "" {
		version = youtubeMusicDefaultVersion
	}
	resolveBody, err := json.Marshal(map[string]any{
		"context": map[string]any{"client": map[string]any{
			"clientName": "WEB_REMIX", "clientVersion": version, "hl": "en",
			"timeZone": "UTC", "utcOffsetMinutes": 0, "visitorData": config.VisitorData,
		}},
		"url": pageURL,
	})
	if err != nil {
		return youtubeMusicAlbumResolve{}, fmt.Errorf("%w: encode Music album resolve", ErrInvalidMetadata)
	}
	resolveRaw, err := postYouTubeMusicRemixJSON(ctx, transport, youtubeMusicBrowseResolveURL, resolveBody, version, config.APIKey)
	if err != nil {
		return youtubeMusicAlbumResolve{}, categorizeYouTubeMusicBrowseError(err)
	}
	browseQueryID, params, err := youtubeMusicAlbumResolveEndpoint(resolveRaw, browseID)
	if err != nil {
		return youtubeMusicAlbumResolve{}, err
	}
	browsePayload := map[string]any{
		"context": map[string]any{"client": map[string]any{
			"clientName": "WEB_REMIX", "clientVersion": version, "hl": "en",
			"timeZone": "UTC", "utcOffsetMinutes": 0, "visitorData": config.VisitorData,
		}},
		"browseId": browseQueryID,
	}
	if params != "" {
		browsePayload["params"] = params
	}
	browseBody, err := json.Marshal(browsePayload)
	if err != nil {
		return youtubeMusicAlbumResolve{}, fmt.Errorf("%w: encode Music album browse", ErrInvalidMetadata)
	}
	browseRaw, err := postYouTubeMusicRemixJSON(ctx, transport, youtubeMusicBrowseAPIURL, browseBody, version, config.APIKey)
	if err != nil {
		return youtubeMusicAlbumResolve{}, categorizeYouTubeMusicBrowseError(err)
	}
	page, err := parseYouTubeMusicBrowseData(browseRaw)
	if err != nil {
		return youtubeMusicAlbumResolve{}, err
	}
	if page.alert != "" && len(page.entries) == 0 {
		return youtubeMusicAlbumResolve{}, youtubeMusicBrowseAlertError(page.alert)
	}
	meta := youtubeMusicBrowseInfo(browseRaw, page)
	if meta.playlistID == "" {
		return youtubeMusicAlbumResolve{}, fmt.Errorf("%w: failed to resolve album to playlist", ErrUnavailable)
	}
	title := meta.title
	if title == "" {
		title = page.title
	}
	return youtubeMusicAlbumResolve{page: page, playlistID: meta.playlistID, title: title}, nil
}

func youtubeMusicAlbumResolveEndpoint(resolveRaw []byte, expectedBrowseID string) (browseID, params string, err error) {
	var resolveRoot value.Value
	if err := json.Unmarshal(resolveRaw, &resolveRoot); err != nil {
		return "", "", fmt.Errorf("%w: decode Music album resolve", ErrInvalidMetadata)
	}
	resolveObject, ok := resolveRoot.Object()
	if !ok {
		return "", "", fmt.Errorf("%w: Music album resolve root", ErrInvalidMetadata)
	}
	endpoint, ok := resolveObject.Lookup("endpoint").Object()
	if !ok {
		return "", "", fmt.Errorf("%w: failed to resolve album to playlist", ErrUnavailable)
	}
	browseEndpoint, ok := endpoint.Lookup("browseEndpoint").Object()
	if !ok {
		return "", "", fmt.Errorf("%w: failed to resolve album to playlist", ErrUnavailable)
	}
	browseID = objectString(browseEndpoint, "browseId")
	if browseID == "" || !validYouTubeMusicBrowseID(browseID) {
		return "", "", fmt.Errorf("%w: invalid Music album resolve browseId", ErrInvalidMetadata)
	}
	family, ok := youtubeMusicBrowseFamily(browseID)
	if !ok || family != "album" || browseID != expectedBrowseID {
		return "", "", fmt.Errorf("%w: Music album resolve identity pivot", ErrInvalidMetadata)
	}
	params = objectString(browseEndpoint, "params")
	if !validYouTubeMusicBrowseParams(params) {
		return "", "", fmt.Errorf("%w: invalid Music album resolve params", ErrInvalidMetadata)
	}
	return browseID, params, nil
}

func validYouTubeMusicBrowseParams(params string) bool {
	if params == "" {
		return true
	}
	if len(params) > youtubeMusicBrowseMaxParamsBytes || strings.ContainsAny(params, "\x00\r\n") {
		return false
	}
	return true
}

func readYouTubeMusicBrowsePage(ctx context.Context, transport Transport, rawURL string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	isolated, ok := transport.(CookieIsolatedTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Music browse page request", ErrInvalidMetadata)
	}
	request.Header = make(http.Header)
	request.Header.Set("Origin", "https://music.youtube.com")
	response, err := isolated.DoWithoutCookies(ctx, request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &HTTPStatusError{Code: response.StatusCode}
	}
	reader := &io.LimitedReader{R: response.Body, N: youtubeMusicBrowseMaxPageBytes + 1}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("%w: read Music browse page", ErrYouTubeMusicBrowseNetwork)
	}
	if int64(len(body)) > youtubeMusicBrowseMaxPageBytes {
		return nil, ErrJSONResponseTooLarge
	}
	return body, nil
}

func postYouTubeMusicRemixJSON(ctx context.Context, transport Transport, endpoint string, body []byte, version, apiKey string) (json.RawMessage, error) {
	if _, ok := transport.(CookieIsolatedTransport); !ok {
		return nil, ErrTransportIsolation
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Music API endpoint", ErrInvalidMetadata)
	}
	if strings.ToLower(strings.TrimSuffix(parsed.Hostname(), ".")) != "music.youtube.com" || parsed.Scheme != "https" {
		return nil, fmt.Errorf("%w: hostile Music API endpoint", ErrInvalidMetadata)
	}
	values := parsed.Query()
	values.Set("prettyPrint", "false")
	if apiKey != "" {
		values.Set("key", apiKey)
	}
	parsed.RawQuery = values.Encode()
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Origin", "https://music.youtube.com")
	headers.Set("X-Youtube-Client-Name", "67")
	headers.Set("X-Youtube-Client-Version", version)
	var response json.RawMessage
	if err := RequestJSONWithoutCookies(ctx, transport, http.MethodPost, parsed.String(), body, headers, &response); err != nil {
		return nil, err
	}
	return response, nil
}

func youtubeMusicBrowseRejectAuthenticatedPage(page []byte) error {
	config := discoverYouTubePageConfig(page)
	if config.LoggedIn != nil && *config.LoggedIn {
		return fmt.Errorf("%w: authenticated Music browse is not securely supported", ErrAuthentication)
	}
	if name := strings.ToUpper(config.InnertubeContext.Client.ClientName); name == "WEB" {
		return fmt.Errorf("%w: WEB identity on Music browse page", ErrAuthentication)
	}
	return nil
}

func categorizeYouTubeMusicBrowseError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrAuthentication) || errors.Is(err, ErrUnavailable) ||
		errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) ||
		errors.Is(err, ErrTransportIsolation) {
		return err
	}
	var s *HTTPStatusError
	if errors.As(err, &s) {
		switch s.Code {
		case 401, 403:
			return ErrAuthentication
		case 404, 410:
			return ErrUnavailable
		case 429:
			return ErrYouTubeMusicBrowseRateLimited
		}
	}
	return fmt.Errorf("%w: request failed", ErrYouTubeMusicBrowseNetwork)
}

func youtubeMusicBrowseAlertError(alert string) error {
	lower := strings.ToLower(alert)
	if strings.Contains(lower, "sign in") || strings.Contains(lower, "login") ||
		strings.Contains(lower, "premium") || strings.Contains(lower, "private") ||
		strings.Contains(lower, "members only") || strings.Contains(lower, "members-only") {
		return fmt.Errorf("%w: Music browse access denied", ErrAuthentication)
	}
	return fmt.Errorf("%w: Music browse unavailable", ErrUnavailable)
}
