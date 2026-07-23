package extractor

// This file is intentionally a small compatibility slice for yt-dlp's
// SoundcloudSearchIE. Registration is product-owned, so this extractor is
// independently auditable until the primary integrates it.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	soundCloudSearchDefaultCount  = 1
	soundCloudSearchMaxCount      = 200 // SoundCloud's documented per-page maximum; also caps scsearchall.
	soundCloudSearchMaxQueryBytes = 500
	soundCloudSearchMaxURLBytes   = 4096
	// The requested API page size is the result count (at most 200), so one
	// populated page is sufficient. Three additional pages tolerate empty or
	// unsupported service results without permitting unbounded cursor churn.
	soundCloudSearchMaxPages = 4
	soundCloudSearchEndpoint = "https://api-v2.soundcloud.com/search/tracks"
)

var (
	soundCloudSearchScheme     = regexp.MustCompile(`^scsearch([0-9]*|all)$`)
	ErrSoundCloudSearchNetwork = errors.New("SoundCloud search network failure")
)

// SoundCloudSearch implements scsearch:query, scsearchN:query and
// scsearchall:query. scsearchall remains deliberately bounded to 200 tracks.
// It only emits canonical public track URLs, never profiles, sets, or API URLs.
type SoundCloudSearch struct{ client *SoundCloud }

func NewSoundCloudSearch() SoundCloudSearch { return SoundCloudSearch{client: NewSoundCloud()} }
func (SoundCloudSearch) Name() string       { return "soundcloud_search" }
func (extractor SoundCloudSearch) Suitable(parsed *url.URL) bool {
	_, _, _, ok := soundCloudSearchTarget(parsed)
	return ok
}

func (extractor SoundCloudSearch) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil || extractor.client == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid SoundCloud search URL", ErrUnsupported)
	}
	query, count, canonical, ok := soundCloudSearchTarget(parsed)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: unsupported SoundCloud search", ErrUnsupported)
	}
	firstURL := soundCloudSearchRequestURL(query, count)
	policy := soundCloudContinuationPolicy{allowedPath: "/search/tracks"}
	sequence, err := soundCloudSearchEntries(firstURL, func(ctx context.Context, cursor string) ([]Entry, string, error) {
		return extractor.fetchPage(ctx, request.Transport, cursor, policy, query, count)
	})
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(query)},
		value.Field{Key: "title", Value: value.String(query)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	return Playlist(info, limitedEntries{source: sequence, limit: count})
}

func soundCloudSearchEntries(firstURL string, fetch ContinuationFetcher) (EntrySequence, error) {
	if firstURL == "" || fetch == nil {
		return nil, fmt.Errorf("%w: invalid SoundCloud search source", ErrInvalidPlaylist)
	}
	return continuationEntries{nextToken: firstURL, fetch: fetch, maxPages: soundCloudSearchMaxPages}, nil
}

// soundCloudSearchTarget is deliberately shared by route selection and
// extraction. Exact public /search is accepted only with one q parameter.
func soundCloudSearchTarget(parsed *url.URL) (query string, count int, canonical string, ok bool) {
	if parsed == nil {
		return "", 0, "", false
	}
	if match := soundCloudSearchScheme.FindStringSubmatch(strings.ToLower(parsed.Scheme)); match != nil {
		if parsed.User != nil || parsed.Host != "" || parsed.Opaque == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", 0, "", false
		}
		count = soundCloudSearchDefaultCount
		if match[1] == "all" {
			count = soundCloudSearchMaxCount
		} else if match[1] != "" {
			n, err := strconv.Atoi(match[1])
			if err != nil || n < 1 || n > soundCloudSearchMaxCount {
				return "", 0, "", false
			}
			count = n
		}
		query = parsed.Opaque
		if !validSoundCloudSearchQuery(query) {
			return "", 0, "", false
		}
		return query, count, "https://soundcloud.com/search?q=" + url.QueryEscape(query), true
	}
	if len(parsed.String()) > soundCloudSearchMaxURLBytes || parsed.Scheme != "https" || parsed.Port() != "" || parsed.User != nil || parsed.Fragment != "" || soundCloudEncodedSeparators(parsed) {
		return "", 0, "", false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host != "soundcloud.com" && host != "www.soundcloud.com" {
		return "", 0, "", false
	}
	if parsed.Path != "/search" || strings.Contains(strings.ToLower(parsed.RawQuery), "%2f") || strings.Contains(strings.ToLower(parsed.RawQuery), "%5c") || strings.Contains(strings.ToLower(parsed.RawQuery), "%00") {
		return "", 0, "", false
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(values) != 1 || len(values["q"]) != 1 {
		return "", 0, "", false
	}
	query = values.Get("q")
	if !validSoundCloudSearchQuery(query) {
		return "", 0, "", false
	}
	return query, soundCloudSearchDefaultCount, "https://soundcloud.com/search?q=" + url.QueryEscape(query), true
}

func validSoundCloudSearchQuery(query string) bool {
	return query != "" && len(query) <= soundCloudSearchMaxQueryBytes && !strings.ContainsAny(query, "\x00\r\n")
}

func soundCloudSearchRequestURL(query string, count int) string {
	return soundCloudSearchEndpoint + "?" + url.Values{"q": {query}, "limit": {strconv.Itoa(count)}, "linked_partitioning": {"1"}, "offset": {"0"}}.Encode()
}

func (extractor SoundCloudSearch) fetchPage(ctx context.Context, transport Transport, cursor string, policy soundCloudContinuationPolicy, query string, count int) ([]Entry, string, error) {
	validated, err := soundCloudSearchCursor(cursor, policy, query, count)
	if err != nil {
		return nil, "", err
	}
	var page soundCloudSearchPage
	if err := extractor.requestJSON(ctx, transport, validated, &page); err != nil {
		return nil, "", categorizeSoundCloudSearchError(err)
	}
	if page.Collection == nil {
		return nil, "", fmt.Errorf("%w: malformed SoundCloud search page", ErrInvalidMetadata)
	}
	if len(page.Collection) > soundCloudSearchMaxCount {
		return nil, "", fmt.Errorf("%w: SoundCloud search page too large", ErrPlaylistLimit)
	}
	entries := make([]Entry, 0, len(page.Collection))
	for _, track := range page.Collection {
		if entry, ok := soundCloudSearchTrackEntry(track); ok {
			entries = append(entries, entry)
		}
	}
	next := ""
	if page.NextHref != "" {
		next, err = soundCloudSearchCursor(page.NextHref, policy, query, count)
		if err != nil {
			return nil, "", err
		}
	}
	return entries, next, nil
}

// requestJSON keeps first-party client-ID bootstrap behavior but intentionally
// isolates public search API requests from operation cookies. SoundCloud's
// search endpoint accepts its public client ID; it does not require browser
// cookies, and permitting them would couple a public request to user state.
func (extractor SoundCloudSearch) requestJSON(ctx context.Context, transport Transport, endpoint string, target any) error {
	for attempt := 0; attempt < 2; attempt++ {
		clientID, err := extractor.client.discoverClientID(ctx, transport, attempt > 0)
		if err != nil {
			return categorizeSoundCloudSearchError(err)
		}
		requestURL := addSoundCloudQuery(endpoint, "client_id", clientID)
		err = RequestJSONWithoutCookies(ctx, transport, http.MethodGet, requestURL, nil, make(http.Header), target)
		var status *HTTPStatusError
		if errors.As(err, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden) && attempt == 0 {
			continue
		}
		return categorizeSoundCloudSearchError(categorizeSoundCloudError(err))
	}
	return ErrAuthentication
}

// soundCloudSearchCursor validates both the shared HTTPS authority/path policy
// and the search-specific pagination contract. It makes continuation URLs
// incapable of changing a query or increasing the requested bound.
func soundCloudSearchCursor(raw string, policy soundCloudContinuationPolicy, query string, count int) (string, error) {
	validated, err := policy.validate(raw)
	if err != nil {
		return "", err
	}
	parsed, _ := url.Parse(validated)
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || !validSoundCloudSearchCursorQuery(values, query) {
		return "", fmt.Errorf("%w: invalid SoundCloud search continuation", ErrInvalidPlaylist)
	}
	limit, err := strconv.Atoi(values.Get("limit"))
	if err != nil || limit < 1 || limit > count {
		return "", fmt.Errorf("%w: invalid SoundCloud search continuation", ErrInvalidPlaylist)
	}
	offset, err := strconv.ParseUint(values.Get("offset"), 10, 64)
	if err != nil || offset > 1_000_000_000 {
		return "", fmt.Errorf("%w: invalid SoundCloud search continuation", ErrInvalidPlaylist)
	}
	return validated, nil
}

func validSoundCloudSearchCursorQuery(values url.Values, query string) bool {
	for key, items := range values {
		if key != "q" && key != "limit" && key != "linked_partitioning" && key != "offset" && key != "cursor" {
			return false
		}
		if len(items) != 1 {
			return false
		}
	}
	if len(values["q"]) != 1 || values.Get("q") != query || len(values["limit"]) != 1 || len(values["linked_partitioning"]) != 1 || values.Get("linked_partitioning") != "1" || len(values["offset"]) != 1 {
		return false
	}
	if cursor, present := values["cursor"]; present && (len(cursor) != 1 || cursor[0] == "") {
		return false
	}
	return true
}

type soundCloudSearchPage struct {
	Collection []soundCloudTrack `json:"collection"`
	NextHref   string            `json:"next_href"`
}

func soundCloudSearchTrackEntry(track soundCloudTrack) (Entry, bool) {
	if track.Kind != "track" || !validSoundCloudJSONID(track.ID) || strings.TrimSpace(track.Title) == "" {
		return Entry{}, false
	}
	parsed, err := url.Parse(track.PermalinkURL)
	if err != nil {
		return Entry{}, false
	}
	target, ok := classifySoundCloudURL(parsed)
	if !ok || target.kind != soundCloudTrackTarget || target.id != "" || target.secretToken != "" || parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "soundcloud.com" {
		return Entry{}, false
	}
	return Entry{URL: target.canonical, ExtractorKey: "soundcloud", ID: track.ID.String(), Title: track.Title, Transparent: true}, true
}

func categorizeSoundCloudSearchError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrAuthentication) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrInvalidPlaylist) || errors.Is(err, ErrPlaylistLimit) || errors.Is(err, ErrJSONResponseTooLarge) || errors.Is(err, ErrTransportIsolation) {
		return err
	}
	return fmt.Errorf("%w: request failed", ErrSoundCloudSearchNetwork)
}
