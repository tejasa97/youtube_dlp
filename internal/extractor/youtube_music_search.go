package extractor

// Bounded WEB_REMIX Music search. Songs/videos remain playable watch URLs;
// albums, artists, playlists, and podcasts are emitted as typed URL results
// without hydrating nested children. WEB and WEB_REMIX credentials never mix.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	youtubeMusicSearchMaxCount    = 50
	youtubeMusicSearchMaxURLBytes = 4096
	youtubeMusicSearchAPIURL      = "https://music.youtube.com/youtubei/v1/search"
)

// Pinned upstream YoutubeMusicSearchURLIE section params.
var youtubeMusicSearchSections = map[string]string{
	"songs":               "EgWKAQIIAWoKEAoQAxAEEAkQBQ==",
	"videos":              "EgWKAQIQAWoKEAoQAxAEEAkQBQ==",
	"albums":              "EgWKAQIYAWoKEAoQAxAEEAkQBQ==",
	"artists":             "EgWKAQIgAWoKEAoQAxAEEAkQBQ==",
	"community playlists": "EgeKAQQoAEABagoQChADEAQQCRAF",
	"featured playlists":  "EgeKAQQoADgBagwQAxAJEAQQDhAKEAU==",
}

var (
	ErrYouTubeMusicSearchRateLimited = errors.New("YouTube Music search rate limited")
	ErrYouTubeMusicSearchNetwork     = errors.New("YouTube Music search network failure")
)

// YouTubeMusicSearch accepts only public music.youtube.com/search URLs with the
// supported filter sections above.
type YouTubeMusicSearch struct{}

func NewYouTubeMusicSearch() YouTubeMusicSearch { return YouTubeMusicSearch{} }
func (YouTubeMusicSearch) Name() string         { return "youtube_music_search" }
func (YouTubeMusicSearch) Suitable(u *url.URL) bool {
	_, _, _, _, ok := youtubeMusicSearchTarget(u)
	return ok
}

func (YouTubeMusicSearch) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	u, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid YouTube Music search URL", ErrUnsupported)
	}
	query, count, canonical, section, ok := youtubeMusicSearchTarget(u)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: unsupported YouTube Music search", ErrUnsupported)
	}
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, categorizeYouTubeMusicSearchError(err)
	}
	raw, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: YouTube Music search initial data", ErrInvalidMetadata)
	}
	first, err := parseYouTubeMusicSearchData(raw, section)
	if err != nil {
		return Extraction{}, err
	}
	if first.alert != "" && len(first.entries) == 0 {
		return Extraction{}, youtubeMusicSearchAlertError(first.alert)
	}
	config := extractYouTubePlaylistConfig(page)
	// Music continuations stay on WEB_REMIX and never inherit WEB SID state.
	entries, err := youtubeMusicSearchEntries(first.entries, first.continuation, count, func(ctx context.Context, token string) ([]Entry, string, error) {
		return fetchYouTubeMusicSearchContinuation(ctx, request.Transport, token, config, section)
	})
	if err != nil {
		return Extraction{}, err
	}
	title := query
	if section != "" {
		title += " - " + section
	}
	return Playlist(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(title)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	)), entries)
}

func youtubeMusicSearchTarget(u *url.URL) (query string, count int, canonical, section string, ok bool) {
	if u == nil || len(u.String()) > youtubeMusicSearchMaxURLBytes {
		return
	}
	fragment := strings.ToLower(strings.ReplaceAll(u.Fragment, "+", " "))
	if fragment != "" {
		if _, known := youtubeMusicSearchSections[fragment]; !known {
			return
		}
	}
	if _, _, err := validateYouTubeURLPolicy(u); err != nil {
		return
	}
	if strings.ToLower(strings.TrimSuffix(u.Hostname(), ".")) != "music.youtube.com" || u.Path != "/search" {
		return
	}
	low := strings.ToLower(u.RawQuery)
	if strings.Contains(low, "%2f") || strings.Contains(low, "%5c") || strings.Contains(low, "%00") {
		return
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return
	}
	query = values.Get("search_query")
	if query == "" {
		query = values.Get("q")
	}
	if !validYouTubeSearchQuery(query) {
		return "", 0, "", "", false
	}
	section = fragment
	params := values.Get("sp")
	if section != "" {
		params = youtubeMusicSearchSections[section]
	} else if params != "" {
		for name, value := range youtubeMusicSearchSections {
			if params == value {
				section = name
				break
			}
		}
		if section == "" {
			return "", 0, "", "", false
		}
	}
	canonicalValues := url.Values{"search_query": {query}}
	if params != "" {
		canonicalValues.Set("sp", params)
	}
	return query, youtubeMusicSearchMaxCount, "https://music.youtube.com/search?" + canonicalValues.Encode(), section, true
}

func youtubeMusicRendererPolicy(section string) youtubeRendererPolicy {
	switch section {
	case "songs", "videos":
		return youtubeRendererPolicy{kinds: youtubeRendererVideo}
	case "albums", "community playlists", "featured playlists":
		return youtubeRendererPolicy{kinds: youtubeRendererPlaylist | youtubeRendererMusicBrowse}
	case "artists":
		return youtubeRendererPolicy{kinds: youtubeRendererChannel | youtubeRendererMusicBrowse}
	default:
		return youtubeRendererPolicy{kinds: youtubeRendererMusicAll}
	}
}

func parseYouTubeMusicSearchData(data []byte, section string) (youtubeRendererPage, error) {
	return parseYouTubeRendererData(data, youtubeMusicRendererPolicy(section))
}

func youtubeMusicSearchEntries(first []Entry, token string, count int, fetch ContinuationFetcher) (EntrySequence, error) {
	if count < 1 || count > youtubeMusicSearchMaxCount {
		return nil, fmt.Errorf("%w: invalid YouTube Music search count", ErrInvalidPlaylist)
	}
	base, err := ContinuationEntries(first, token, fetch)
	if err != nil {
		return nil, err
	}
	return limitedEntries{source: base, limit: count}, nil
}

func fetchYouTubeMusicSearchContinuation(ctx context.Context, transport Transport, token string, config youtubePlaylistConfig, section string) ([]Entry, string, error) {
	if token = validYouTubeContinuationToken(token); token == "" {
		return nil, "", fmt.Errorf("%w: invalid YouTube Music search continuation", ErrInvalidPlaylist)
	}
	version := config.ClientVersion
	if version == "" {
		version = youtubeDefaultClientVersion
	}
	body, err := json.Marshal(map[string]any{"context": map[string]any{"client": map[string]any{
		"clientName": "WEB_REMIX", "clientVersion": version, "hl": "en",
		"timeZone": "UTC", "utcOffsetMinutes": 0, "visitorData": config.VisitorData,
	}}, "continuation": token})
	if err != nil {
		return nil, "", fmt.Errorf("%w: encode YouTube Music search continuation", ErrInvalidMetadata)
	}
	endpoint, _ := url.Parse(youtubeMusicSearchAPIURL)
	values := endpoint.Query()
	values.Set("prettyPrint", "false")
	if config.APIKey != "" {
		values.Set("key", config.APIKey)
	}
	endpoint.RawQuery = values.Encode()
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Origin", "https://music.youtube.com")
	headers.Set("X-Youtube-Client-Name", "67")
	headers.Set("X-Youtube-Client-Version", version)
	var response json.RawMessage
	if err := RequestJSON(ctx, transport, http.MethodPost, endpoint.String(), body, headers, &response); err != nil {
		return nil, "", categorizeYouTubeMusicSearchError(err)
	}
	page, err := parseYouTubeMusicSearchData(response, section)
	if err != nil {
		return nil, "", err
	}
	if page.alert != "" && len(page.entries) == 0 {
		return nil, "", youtubeMusicSearchAlertError(page.alert)
	}
	return page.entries, page.continuation, nil
}

func categorizeYouTubeMusicSearchError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
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
			return ErrYouTubeMusicSearchRateLimited
		}
	}
	return fmt.Errorf("%w: request failed", ErrYouTubeMusicSearchNetwork)
}

func youtubeMusicSearchAlertError(alert string) error {
	lower := strings.ToLower(alert)
	if strings.Contains(lower, "sign in") || strings.Contains(lower, "login") {
		return fmt.Errorf("%w: Music search access denied", ErrAuthentication)
	}
	return fmt.Errorf("%w: Music search unavailable", ErrUnavailable)
}
