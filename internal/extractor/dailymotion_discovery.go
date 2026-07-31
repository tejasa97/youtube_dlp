package extractor

import (
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
	"sync"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	dailymotionGraphQLEndpoint     = "https://graphql.api.dailymotion.com/"
	dailymotionTokenEndpoint       = "https://graphql.api.dailymotion.com/oauth/token"
	dailymotionGraphQLOrigin       = "https://www.dailymotion.com"
	dailymotionUserPageSize        = 100
	dailymotionPlaylistPageSize    = 100
	dailymotionSearchPageSize      = 20
	dailymotionMaxIdentifierBytes  = 256
	dailymotionMaxSearchTermBytes  = 512
	dailymotionMaxAccessTokenBytes = 2048
	dailymotionSearchMaxPages      = 50
	dailymotionSearchMaxEntries    = 1000
	dailymotionUserMaxPages        = 100
	dailymotionUserMaxEntries      = 10_000
	dailymotionPlaylistMaxPages    = 100
	dailymotionPlaylistMaxEntries  = 10_000
	// OAuth client credentials embedded by the pinned yt-dlp
	// DailymotionBaseInfoExtractor for anonymous client_credentials access.
	// They are request material, not user secrets, and must never appear in
	// diagnostics, metadata, events, cookies, or media requests.
	dailymotionAnonymousClientID     = "f1a362d288c1b98099c7"
	dailymotionAnonymousClientSecret = "eea605b96e01c796ff369935357eca920c5da4c5"
)

var (
	ErrDailymotionDiscoveryToken = errors.New("Dailymotion discovery token acquisition failed")

	dailymotionTLD                  = regexp.MustCompile(`^[a-z]{2,3}$`)
	dailymotionReservedUserSegments = map[string]struct{}{
		"embed": {}, "swf": {}, "video": {}, "playlist": {}, "search": {}, "crawler": {}, "player": {},
	}
	dailymotionSearchQuery = "query SEARCH_QUERY( $query: String! $page: Int $limit: Int ) { search { videos( query: $query first: $limit page: $page ) { edges { node { xid } } } } } "
)

type dailymotionDiscoveryClient struct {
	transport Transport
	mu        sync.Mutex
	token     string
}

func newDailymotionDiscoveryClient(transport Transport) *dailymotionDiscoveryClient {
	return &dailymotionDiscoveryClient{transport: transport}
}

func dailymotionDiscoveryHostOK(host string) bool {
	if host == "" || strings.HasSuffix(host, ".") {
		return false
	}
	host = strings.ToLower(host)
	switch host {
	case "dailymotion.com", "www.dailymotion.com":
		return true
	}
	if strings.HasPrefix(host, "www.") {
		host = host[4:]
	}
	const prefix = "dailymotion."
	if !strings.HasPrefix(host, prefix) || strings.Contains(host[len(prefix):], ".") {
		return false
	}
	return dailymotionTLD.MatchString(host[len(prefix):])
}

func dailymotionDiscoveryURLSafe(parsed *url.URL) bool {
	if parsed == nil || len(parsed.String()) > sharedHostingMaxURLBytes {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawQuery != "" {
		return false
	}
	if !dailymotionDiscoveryHostOK(parsed.Hostname()) {
		return false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if !strings.HasPrefix(escaped, "/") || strings.HasSuffix(escaped, "/") || strings.HasPrefix(escaped, "//") || strings.Contains(escaped, "//") || strings.Contains(escaped, "%2f") ||
		strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") || strings.Contains(parsed.String(), "\x00") {
		return false
	}
	for _, part := range strings.Split(strings.TrimPrefix(escaped, "/"), "/") {
		if part == "" {
			return false
		}
		decoded, err := url.PathUnescape(part)
		if err != nil || strings.Contains(decoded, "/") || strings.Contains(decoded, "\\") {
			return false
		}
	}
	return true
}

func dailymotionValidIdentifier(value string) bool {
	if value == "" || len(value) > dailymotionMaxIdentifierBytes || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsAny(value, "\x00\r\n/")
}

func dailymotionValidSearchTerm(value string) bool {
	if value == "" || len(value) > dailymotionMaxSearchTermBytes || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsAny(value, "\x00\r\n")
}

func dailymotionValidAccessToken(token string) bool {
	if token == "" || len(token) > dailymotionMaxAccessTokenBytes {
		return false
	}
	for i := 0; i < len(token); i++ {
		if token[i] < 0x21 || token[i] > 0x7e {
			return false
		}
	}
	return true
}

func dailymotionGraphQLString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func dailymotionGraphQLURLSafe(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && parsed.Scheme == "https" && parsed.Host == "graphql.api.dailymotion.com" &&
		parsed.Path == "/" && parsed.RawQuery == "" && parsed.Fragment == "" &&
		parsed.Opaque == "" && parsed.User == nil && parsed.Port() == ""
}

func requestDailymotionGraphQL(ctx context.Context, transport Transport, body []byte, headers http.Header, target any) error {
	if !dailymotionGraphQLURLSafe(dailymotionGraphQLEndpoint) {
		return ErrTransportIsolation
	}
	return RequestJSONWithScopedAuthorizationNoRedirect(ctx, transport, http.MethodPost, dailymotionGraphQLEndpoint, body, headers, target)
}

func (client *dailymotionDiscoveryClient) tokenValue(ctx context.Context, refresh bool) (string, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if !refresh && client.token != "" {
		return client.token, nil
	}
	token, err := dailymotionAcquireToken(ctx, client.transport)
	if err != nil {
		return "", err
	}
	client.token = token
	return token, nil
}

func dailymotionAcquireToken(ctx context.Context, transport Transport) (string, error) {
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return "", ErrTransportIsolation
	}
	form := url.Values{
		"client_id":     {dailymotionAnonymousClientID},
		"client_secret": {dailymotionAnonymousClientSecret},
		"grant_type":    {"client_credentials"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, dailymotionTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w: construct token request", ErrDailymotionDiscoveryToken)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", dailymotionGraphQLOrigin)
	// An explicit empty value prevents the shared default-header layer from
	// adding an ambient Referer. Dailymotion's anonymous token policy is Origin
	// only; the generic scoped-Authorization boundary does not carry Referer.
	request.Header.Set("Referer", "")
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return "", categorizeDailymotionError(err)
	}
	if response == nil || response.Body == nil {
		return "", fmt.Errorf("%w: nil token response", ErrDailymotionDiscoveryToken)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxExtractorJSONBytes+1))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		return "", fmt.Errorf("%w: read token response", ErrDailymotionDiscoveryToken)
	}
	if int64(len(data)) > maxExtractorJSONBytes {
		return "", ErrJSONResponseTooLarge
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", categorizeDailymotionError(&HTTPStatusError{Code: response.StatusCode})
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || !dailymotionValidAccessToken(payload.AccessToken) {
		return "", fmt.Errorf("%w: invalid token response", ErrInvalidMetadata)
	}
	return payload.AccessToken, nil
}

func (client *dailymotionDiscoveryClient) graphQL(ctx context.Context, body []byte) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		token, err := client.tokenValue(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}
		headers := make(http.Header)
		headers.Set("Content-Type", "application/json")
		headers.Set("Origin", dailymotionGraphQLOrigin)
		// See the token request above: keep this policy explicit at the
		// Dailymotion call site while preserving main's generic auth seam.
		headers.Set("Referer", "")
		headers.Set("Authorization", "Bearer "+token)
		var payload json.RawMessage
		err = requestDailymotionGraphQL(ctx, client.transport, body, headers, &payload)
		if err == nil {
			return payload, nil
		}
		var status *HTTPStatusError
		if errors.As(err, &status) && status.Code == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		return nil, categorizeDailymotionError(err)
	}
	return nil, ErrAuthentication
}

type dailymotionGraphNode struct {
	XID string `json:"xid"`
	URL string `json:"url"`
}

func dailymotionSearchTarget(parsed *url.URL) (term, canonical string, ok bool) {
	if !dailymotionDiscoveryURLSafe(parsed) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 3 || parts[0] != "search" || parts[2] != "videos" {
		return "", "", false
	}
	term, err := url.QueryUnescape(parts[1])
	if err != nil || !dailymotionValidSearchTerm(term) {
		return "", "", false
	}
	return term, parsed.String(), true
}

func dailymotionUserTarget(parsed *url.URL) (string, string, bool) {
	if !dailymotionDiscoveryURLSafe(parsed) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(parsed.EscapedPath(), "/"), "/")
	var id string
	var err error
	switch len(parts) {
	case 1:
		id, err = url.PathUnescape(parts[0])
	case 2:
		if parts[0] == "user" {
			id, err = url.PathUnescape(parts[1])
		} else {
			return "", "", false
		}
	case 3:
		if parts[0] == "old" && parts[1] == "user" {
			id, err = url.PathUnescape(parts[2])
		} else {
			return "", "", false
		}
	default:
		return "", "", false
	}
	if err != nil || strings.Contains(parsed.EscapedPath(), "%") || !dailymotionValidIdentifier(id) {
		return "", "", false
	}
	if _, reserved := dailymotionReservedUserSegments[strings.ToLower(id)]; reserved {
		return "", "", false
	}
	return id, parsed.String(), true
}

func dailymotionPlaylistTarget(parsed *url.URL) (string, string, bool) {
	if !dailymotionBaseURLSafe(parsed) || parsed.RawQuery != "" || !dailymotionLocalizedHostOK(strings.ToLower(parsed.Hostname()), "", "www") {
		return "", "", false
	}
	parts, ok := dailymotionLiteralPathParts(parsed.EscapedPath())
	if !ok || (len(parts) != 2 && len(parts) != 3) || parts[0] != "playlist" {
		return "", "", false
	}
	identity := strings.SplitN(parts[1], "_", 2)
	if !dailymotionPlaylistID.MatchString(identity[0]) || (len(identity) == 2 && !dailymotionVideoSlug.MatchString(identity[1])) {
		return "", "", false
	}
	if len(parts) == 3 {
		if len(parts[2]) > 9 {
			return "", "", false
		}
		if _, err := strconv.ParseUint(parts[2], 10, 31); err != nil {
			return "", "", false
		}
	}
	if parsed.RawFragment != "" {
		return "", "", false
	}
	if parsed.Fragment != "" {
		fragment, err := url.ParseQuery(parsed.Fragment)
		if err != nil || len(fragment) != 1 || len(fragment["video"]) != 1 || !dailymotionID.MatchString(fragment["video"][0]) {
			return "", "", false
		}
	}
	return identity[0], parsed.String(), true
}

func dailymotionValidateNodeURL(rawURL, xid string) error {
	if rawURL == "" {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: invalid Dailymotion node URL", ErrInvalidMetadata)
	}
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawQuery != "" {
		return fmt.Errorf("%w: invalid Dailymotion node URL", ErrInvalidMetadata)
	}
	if !strings.EqualFold(parsed.Hostname(), "www.dailymotion.com") {
		return fmt.Errorf("%w: invalid Dailymotion node URL", ErrInvalidMetadata)
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 2 || parts[0] != "video" {
		return fmt.Errorf("%w: invalid Dailymotion node URL", ErrInvalidMetadata)
	}
	id := strings.Split(parts[1], "_")[0]
	if id != xid || !dailymotionID.MatchString(id) {
		return fmt.Errorf("%w: Dailymotion node identity mismatch", ErrInvalidMetadata)
	}
	return nil
}

func dailymotionEntryFromNode(xid, rawURL string) (Entry, error) {
	if !dailymotionID.MatchString(xid) {
		return Entry{}, fmt.Errorf("%w: invalid Dailymotion video id", ErrInvalidPlaylist)
	}
	if err := dailymotionValidateNodeURL(rawURL, xid); err != nil {
		return Entry{}, err
	}
	return Entry{
		URL:          "https://www.dailymotion.com/video/" + xid,
		ExtractorKey: "dailymotion",
		ID:           xid,
		Transparent:  true,
	}, nil
}

func dailymotionPageFingerprint(entries []Entry) string {
	ids := make([]string, len(entries))
	for i, entry := range entries {
		ids[i] = entry.ID
	}
	return strings.Join(ids, ",")
}

type dailymotionDiscoveryPageFetcher func(context.Context, int) ([]Entry, bool, error)

type dailymotionDiscoverySequence struct {
	pageSize   int
	maxPages   int
	maxEntries int
	fetch      dailymotionDiscoveryPageFetcher
}

func newDailymotionDiscoverySequence(pageSize, maxPages, maxEntries int, fetch dailymotionDiscoveryPageFetcher) (EntrySequence, error) {
	if pageSize <= 0 || maxPages <= 0 || maxEntries <= 0 || fetch == nil {
		return nil, fmt.Errorf("%w: invalid Dailymotion discovery source", ErrInvalidPlaylist)
	}
	return dailymotionDiscoverySequence{pageSize: pageSize, maxPages: maxPages, maxEntries: maxEntries, fetch: fetch}, nil
}

func (sequence dailymotionDiscoverySequence) Iterator() EntryIterator {
	return &dailymotionDiscoveryIterator{source: sequence, seenFullFP: make(map[string]struct{})}
}

type dailymotionDiscoveryIterator struct {
	source        dailymotionDiscoverySequence
	page          []Entry
	pageIndex     int
	pageNum       int
	emitted       int
	seenFullFP    map[string]struct{}
	noMoreFetches bool
	finished      bool
}

func (iterator *dailymotionDiscoveryIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := contextError(ctx); err != nil {
		iterator.finished = true
		return Entry{}, false, err
	}
	if iterator.finished {
		return Entry{}, false, nil
	}
	if iterator.noMoreFetches && iterator.pageIndex >= len(iterator.page) {
		return Entry{}, false, nil
	}
	for iterator.pageIndex >= len(iterator.page) {
		if iterator.noMoreFetches {
			return Entry{}, false, nil
		}
		if iterator.pageNum >= iterator.source.maxPages {
			iterator.finished = true
			return Entry{}, false, ErrPlaylistLimit
		}
		if iterator.emitted >= iterator.source.maxEntries {
			iterator.finished = true
			return Entry{}, false, ErrPlaylistLimit
		}
		page, lastPage, err := iterator.source.fetch(ctx, iterator.pageNum+1)
		if err != nil {
			iterator.finished = true
			return Entry{}, false, err
		}
		iterator.pageNum++
		if len(page) > iterator.source.pageSize {
			iterator.finished = true
			return Entry{}, false, fmt.Errorf("%w: Dailymotion discovery page overflow", ErrInvalidMetadata)
		}
		if len(page) == iterator.source.pageSize {
			fingerprint := dailymotionPageFingerprint(page)
			if _, seen := iterator.seenFullFP[fingerprint]; seen {
				iterator.finished = true
				return Entry{}, false, fmt.Errorf("%w: repeated Dailymotion discovery page", ErrInvalidPlaylist)
			}
			iterator.seenFullFP[fingerprint] = struct{}{}
		}
		if len(page) == 0 {
			iterator.noMoreFetches = true
			return Entry{}, false, nil
		}
		iterator.page = append(iterator.page[:0], page...)
		iterator.pageIndex = 0
		if lastPage {
			iterator.noMoreFetches = true
		}
	}
	if iterator.emitted >= iterator.source.maxEntries {
		iterator.finished = true
		return Entry{}, false, ErrPlaylistLimit
	}
	entry := iterator.page[iterator.pageIndex]
	iterator.pageIndex++
	iterator.emitted++
	return entry, true, nil
}

func dailymotionGraphQLErrorsPresent(raw []byte) bool {
	var envelope struct {
		Errors []json.RawMessage `json:"errors"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return false
	}
	return len(envelope.Errors) > 0
}

func dailymotionParseSearchNodes(raw []byte) ([]dailymotionGraphNode, error) {
	if dailymotionGraphQLErrorsPresent(raw) {
		return nil, ErrUnavailable
	}
	var envelope struct {
		Data *struct {
			Search *struct {
				Videos *struct {
					Edges json.RawMessage `json:"edges"`
				} `json:"videos"`
			} `json:"search"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("%w: malformed Dailymotion search response", ErrInvalidMetadata)
	}
	if envelope.Data == nil || envelope.Data.Search == nil || envelope.Data.Search.Videos == nil {
		return nil, fmt.Errorf("%w: missing Dailymotion search envelope", ErrInvalidMetadata)
	}
	if envelope.Data.Search.Videos.Edges == nil {
		return nil, fmt.Errorf("%w: null Dailymotion search edges", ErrInvalidMetadata)
	}
	if string(envelope.Data.Search.Videos.Edges) == "null" {
		return nil, fmt.Errorf("%w: null Dailymotion search edges", ErrInvalidMetadata)
	}
	var edges []struct {
		Node dailymotionGraphNode `json:"node"`
	}
	if err := json.Unmarshal(envelope.Data.Search.Videos.Edges, &edges); err != nil {
		return nil, fmt.Errorf("%w: malformed Dailymotion search edges", ErrInvalidMetadata)
	}
	nodes := make([]dailymotionGraphNode, len(edges))
	for i, edge := range edges {
		nodes[i] = edge.Node
	}
	return nodes, nil
}

func dailymotionParseUserNodes(raw []byte) ([]dailymotionGraphNode, error) {
	if dailymotionGraphQLErrorsPresent(raw) {
		return nil, ErrUnavailable
	}
	var envelope struct {
		Data *struct {
			Channel *struct {
				Videos *struct {
					Edges json.RawMessage `json:"edges"`
				} `json:"videos"`
			} `json:"channel"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("%w: malformed Dailymotion user response", ErrInvalidMetadata)
	}
	if envelope.Data == nil || envelope.Data.Channel == nil || envelope.Data.Channel.Videos == nil {
		return nil, fmt.Errorf("%w: missing Dailymotion channel envelope", ErrInvalidMetadata)
	}
	if envelope.Data.Channel.Videos.Edges == nil {
		return nil, fmt.Errorf("%w: null Dailymotion channel edges", ErrInvalidMetadata)
	}
	if string(envelope.Data.Channel.Videos.Edges) == "null" {
		return nil, fmt.Errorf("%w: null Dailymotion channel edges", ErrInvalidMetadata)
	}
	var edges []struct {
		Node dailymotionGraphNode `json:"node"`
	}
	if err := json.Unmarshal(envelope.Data.Channel.Videos.Edges, &edges); err != nil {
		return nil, fmt.Errorf("%w: malformed Dailymotion channel edges", ErrInvalidMetadata)
	}
	nodes := make([]dailymotionGraphNode, len(edges))
	for i, edge := range edges {
		nodes[i] = edge.Node
	}
	return nodes, nil
}

func dailymotionParseCollectionNodes(raw []byte) ([]dailymotionGraphNode, error) {
	if dailymotionGraphQLErrorsPresent(raw) {
		return nil, ErrUnavailable
	}
	var envelope struct {
		Data *struct {
			Collection *struct {
				Videos *struct {
					Edges json.RawMessage `json:"edges"`
				} `json:"videos"`
			} `json:"collection"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("%w: malformed Dailymotion collection response", ErrInvalidMetadata)
	}
	if envelope.Data == nil || envelope.Data.Collection == nil || envelope.Data.Collection.Videos == nil {
		return nil, fmt.Errorf("%w: missing Dailymotion collection envelope", ErrInvalidMetadata)
	}
	if envelope.Data.Collection.Videos.Edges == nil || string(envelope.Data.Collection.Videos.Edges) == "null" {
		return nil, fmt.Errorf("%w: null Dailymotion collection edges", ErrInvalidMetadata)
	}
	var edges []struct {
		Node dailymotionGraphNode `json:"node"`
	}
	if err := json.Unmarshal(envelope.Data.Collection.Videos.Edges, &edges); err != nil {
		return nil, fmt.Errorf("%w: malformed Dailymotion collection edges", ErrInvalidMetadata)
	}
	nodes := make([]dailymotionGraphNode, len(edges))
	for index, edge := range edges {
		nodes[index] = edge.Node
	}
	return nodes, nil
}

func dailymotionNodesToEntries(nodes []dailymotionGraphNode, requireURL bool) ([]Entry, error) {
	entries := make([]Entry, 0, len(nodes))
	for _, node := range nodes {
		if requireURL && node.URL == "" {
			return nil, fmt.Errorf("%w: missing Dailymotion node URL", ErrInvalidPlaylist)
		}
		entry, err := dailymotionEntryFromNode(node.XID, node.URL)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func categorizeDailymotionDiscoveryError(err error) error { return categorizeDailymotionError(err) }

// DailymotionPlaylist implements pinned public collection pagination.
type DailymotionPlaylist struct{}

func NewDailymotionPlaylist() DailymotionPlaylist { return DailymotionPlaylist{} }
func (DailymotionPlaylist) Name() string          { return "dailymotion_playlist" }

func (DailymotionPlaylist) Suitable(parsed *url.URL) bool {
	_, _, ok := dailymotionPlaylistTarget(parsed)
	return ok
}

func (extractor DailymotionPlaylist) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	playlistID, webpage, ok := dailymotionPlaylistTarget(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	client := newDailymotionDiscoveryClient(request.Transport)
	sequence, err := newDailymotionDiscoverySequence(dailymotionPlaylistPageSize, dailymotionPlaylistMaxPages, dailymotionPlaylistMaxEntries,
		func(ctx context.Context, page int) ([]Entry, bool, error) {
			return extractor.fetchPlaylistPage(ctx, client, playlistID, page)
		})
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(playlistID)},
		value.Field{Key: "webpage_url", Value: value.String(webpage)},
	))
	return Playlist(info, sequence)
}

func (DailymotionPlaylist) fetchPlaylistPage(ctx context.Context, client *dailymotionDiscoveryClient, playlistID string, page int) ([]Entry, bool, error) {
	query := fmt.Sprintf(`{
  collection(xid: %s) {
    videos(allowExplicit: true, first: %d, page: %d) {
      edges {
        node {
          xid
          url
        }
      }
    }
  }
}`, dailymotionGraphQLString(playlistID), dailymotionPlaylistPageSize, page)
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, false, fmt.Errorf("%w: encode Dailymotion playlist request", ErrInvalidMetadata)
	}
	raw, err := client.graphQL(ctx, body)
	if err != nil {
		return nil, false, err
	}
	nodes, err := dailymotionParseCollectionNodes(raw)
	if err != nil {
		return nil, false, err
	}
	entries, err := dailymotionNodesToEntries(nodes, true)
	if err != nil {
		return nil, false, err
	}
	return entries, len(entries) < dailymotionPlaylistPageSize, nil
}

// DailymotionSearch implements public Dailymotion search result playlists.
type DailymotionSearch struct{}

func NewDailymotionSearch() DailymotionSearch { return DailymotionSearch{} }
func (DailymotionSearch) Name() string        { return "dailymotion_search" }

func (DailymotionSearch) Suitable(parsed *url.URL) bool {
	_, _, ok := dailymotionSearchTarget(parsed)
	return ok
}

func (extractor DailymotionSearch) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	term, canonical, ok := dailymotionSearchTarget(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	client := newDailymotionDiscoveryClient(request.Transport)
	sequence, err := newDailymotionDiscoverySequence(dailymotionSearchPageSize, dailymotionSearchMaxPages, dailymotionSearchMaxEntries,
		func(ctx context.Context, page int) ([]Entry, bool, error) {
			return extractor.fetchSearchPage(ctx, client, term, page)
		})
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(term)},
		value.Field{Key: "title", Value: value.String(term)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	return Playlist(info, sequence)
}

func (DailymotionSearch) fetchSearchPage(ctx context.Context, client *dailymotionDiscoveryClient, term string, page int) ([]Entry, bool, error) {
	body, err := json.Marshal(map[string]any{
		"operationName": "SEARCH_QUERY",
		"query":         dailymotionSearchQuery,
		"variables": map[string]any{
			"limit": dailymotionSearchPageSize,
			"page":  page,
			"query": term,
		},
	})
	if err != nil {
		return nil, false, fmt.Errorf("%w: encode Dailymotion search request", ErrInvalidMetadata)
	}
	raw, err := client.graphQL(ctx, body)
	if err != nil {
		return nil, false, err
	}
	nodes, err := dailymotionParseSearchNodes(raw)
	if err != nil {
		return nil, false, err
	}
	entries, err := dailymotionNodesToEntries(nodes, false)
	if err != nil {
		return nil, false, err
	}
	return entries, len(entries) < dailymotionSearchPageSize, nil
}

// DailymotionUser implements public Dailymotion channel upload playlists.
type DailymotionUser struct{}

func NewDailymotionUser() DailymotionUser { return DailymotionUser{} }
func (DailymotionUser) Name() string      { return "dailymotion_user" }

func (DailymotionUser) Suitable(parsed *url.URL) bool {
	_, _, ok := dailymotionUserTarget(parsed)
	return ok
}

func (extractor DailymotionUser) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	userID, canonical, ok := dailymotionUserTarget(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	client := newDailymotionDiscoveryClient(request.Transport)
	sequence, err := newDailymotionDiscoverySequence(dailymotionUserPageSize, dailymotionUserMaxPages, dailymotionUserMaxEntries,
		func(ctx context.Context, page int) ([]Entry, bool, error) {
			return extractor.fetchUserPage(ctx, client, userID, page)
		})
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(userID)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	return Playlist(info, sequence)
}

func (DailymotionUser) fetchUserPage(ctx context.Context, client *dailymotionDiscoveryClient, userID string, page int) ([]Entry, bool, error) {
	query := fmt.Sprintf(`{
  channel(xid: %s) {
    videos(allowExplicit: true, first: %d, page: %d) {
      edges {
        node {
          xid
          url
        }
      }
    }
  }
}`, dailymotionGraphQLString(userID), dailymotionUserPageSize, page)
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, false, fmt.Errorf("%w: encode Dailymotion user request", ErrInvalidMetadata)
	}
	raw, err := client.graphQL(ctx, body)
	if err != nil {
		return nil, false, err
	}
	nodes, err := dailymotionParseUserNodes(raw)
	if err != nil {
		return nil, false, err
	}
	entries, err := dailymotionNodesToEntries(nodes, true)
	if err != nil {
		return nil, false, err
	}
	return entries, len(entries) < dailymotionUserPageSize, nil
}
