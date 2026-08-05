package extractor

// This file deliberately implements the bounded public subset of yt-dlp's
// YouTube search extractors. Registration is owned by the client package so
// this extractor can remain a narrowly auditable route.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	youtubeSearchDefaultCount  = 1
	youtubeSearchMaxCount      = 50 // bounded to avoid unbounded remote paging
	youtubeSearchMaxQueryBytes = 500
	youtubeSearchMaxURLBytes   = 4096
	youtubeSearchVideosParams  = "EgIQAfABAQ==" // upstream YoutubeSearchIE: videos only
	youtubeSearchAPIURL        = "https://www.youtube.com/youtubei/v1/search"
)

// Supported filter/sort params mirrored from documented upstream sp values.
// Keys are the decoded forms that url.ParseQuery returns (and that callers
// should store after normalizing percent-encoding).
var youtubeSearchSupportedParams = map[string]struct{}{
	"":                        {},
	youtubeSearchVideosParams: {}, // EgIQAfABAQ==
	"EgIQAg==":                {}, // channels
	"EgIQAw==":                {}, // playlists
	"EgIQAQ==":                {}, // videos (decoded from EgIQAQ%3D%3D)
	"EgQQARgB":                {}, // live
	"EgQQASAB":                {}, // short
	"CAI=":                    {}, // sort upload date
	"CAM=":                    {}, // sort view count
	"CAE=":                    {}, // sort rating
}

var youtubeSearchScheme = regexp.MustCompile(`^ytsearch([0-9]*|all)$`)

var (
	ErrYouTubeSearchRateLimited = errors.New("YouTube search rate limited")
	ErrYouTubeSearchNetwork     = errors.New("YouTube search network failure")
)

// YouTubeSearch accepts ytsearch[N]:query, ytsearchall:query (locally capped
// at 50), and exact public /results or /search URLs with search_query or q.
type YouTubeSearch struct{}

func NewYouTubeSearch() YouTubeSearch { return YouTubeSearch{} }
func (YouTubeSearch) Name() string    { return "youtube_search" }
func (YouTubeSearch) SupportsSearchPrefix(prefix string) bool {
	parsed, err := url.Parse(strings.ToLower(prefix) + ":routing")
	return err == nil && youtubeSearchScheme.MatchString(parsed.Scheme) && parsed.Opaque != ""
}
func (YouTubeSearch) SearchQueryAllowed(query string) bool { return validYouTubeSearchQuery(query) }
func (YouTubeSearch) Suitable(parsed *url.URL) bool {
	_, _, _, ok := youtubeSearchTarget(parsed)
	return ok
}

func (YouTubeSearch) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid YouTube search URL", ErrUnsupported)
	}
	query, count, canonical, ok := youtubeSearchTarget(parsed)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: unsupported YouTube search", ErrUnsupported)
	}
	if request.SearchQueryOverride != "" {
		if !validYouTubeSearchQuery(request.SearchQueryOverride) || !strings.HasPrefix(strings.ToLower(parsed.Scheme), "ytsearch") {
			return Extraction{}, fmt.Errorf("%w: invalid YouTube search query", ErrUnsupported)
		}
		query = request.SearchQueryOverride
		canonical = (&url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/results", RawQuery: url.Values{
			"search_query": {query}, "sp": {youtubeSearchVideosParams},
		}.Encode()}).String()
	}
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, categorizeYouTubeSearchError(err)
	}
	raw, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: YouTube search initial data", ErrInvalidMetadata)
	}
	first, err := parseYouTubeSearchData(raw)
	if err != nil {
		return Extraction{}, err
	}
	if first.alert != "" && len(first.entries) == 0 {
		return Extraction{}, youtubeSearchAlertError(first.alert)
	}
	config := extractYouTubePlaylistConfig(page)
	auth, err := youtubeBrowseAuthFromPage(page, request.Transport)
	if err != nil {
		return Extraction{}, categorizeYouTubeSearchError(err)
	}
	entries, err := youtubeSearchEntries(first.entries, first.continuation, count, func(ctx context.Context, token string) ([]Entry, string, error) {
		return fetchYouTubeSearchContinuationAuth(ctx, request.Transport, token, config, auth)
	})
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(query)},
		value.Field{Key: "title", Value: value.String(query)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	return Playlist(info, entries)
}

// youtubeSearchTarget is the common policy for route selection and extraction.
func youtubeSearchTarget(parsed *url.URL) (query string, count int, canonical string, ok bool) {
	if parsed == nil {
		return "", 0, "", false
	}
	if match := youtubeSearchScheme.FindStringSubmatch(strings.ToLower(parsed.Scheme)); match != nil {
		if parsed.User != nil || parsed.Host != "" || parsed.Opaque == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return "", 0, "", false
		}
		count = youtubeSearchDefaultCount
		if match[1] == "all" {
			count = youtubeSearchMaxCount
		} else if match[1] != "" {
			n, err := strconv.Atoi(match[1])
			if err != nil || n < 1 || n > youtubeSearchMaxCount {
				return "", 0, "", false
			}
			count = n
		}
		query = parsed.Opaque
		if !validYouTubeSearchQuery(query) {
			return "", 0, "", false
		}
		// ytsearch: remains video-filtered for parity with YoutubeSearchIE.
		return query, count, "https://www.youtube.com/results?" + url.Values{"search_query": {query}, "sp": {youtubeSearchVideosParams}}.Encode(), true
	}
	if len(parsed.String()) > youtubeSearchMaxURLBytes || parsed.Fragment != "" {
		return "", 0, "", false
	}
	if _, _, err := validateYouTubeURLPolicy(parsed); err != nil {
		return "", 0, "", false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host != "youtube.com" && host != "www.youtube.com" {
		return "", 0, "", false
	}
	if parsed.Path != "/results" && parsed.Path != "/search" {
		return "", 0, "", false
	}
	rawQuery := strings.ToLower(parsed.RawQuery)
	if strings.Contains(rawQuery, "%2f") || strings.Contains(rawQuery, "%5c") || strings.Contains(rawQuery, "%00") {
		return "", 0, "", false
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", 0, "", false
	}
	query = values.Get("search_query")
	if query == "" {
		query = values.Get("q")
	}
	if !validYouTubeSearchQuery(query) {
		return "", 0, "", false
	}
	if sp := values.Get("sp"); sp != "" {
		if !youtubeSearchSPSupported(sp) {
			return "", 0, "", false
		}
	}
	return query, youtubeSearchMaxCount, (&url.URL{Scheme: "https", Host: "www.youtube.com", Path: parsed.Path, RawQuery: parsed.RawQuery}).String(), true
}

// youtubeSearchSPSupported accepts only decoded sp values that appear in the
// supported map. Callers must pass the value already returned by ParseQuery
// (or an equivalent decode); remaining percent-escapes are treated as unknown.
func youtubeSearchSPSupported(sp string) bool {
	if sp == "" {
		return true
	}
	if _, supported := youtubeSearchSupportedParams[sp]; supported {
		return true
	}
	return false
}

func validYouTubeSearchQuery(query string) bool {
	return query != "" && len(query) <= youtubeSearchMaxQueryBytes && utf8.ValidString(query) && !strings.ContainsAny(query, "\x00\r\n")
}

func parseYouTubeSearchData(data []byte) (youtubeRendererPage, error) {
	return parseYouTubeRendererData(data, youtubeRendererPolicy{kinds: youtubeRendererSearchAll})
}

func youtubeSearchEntries(first []Entry, token string, count int, fetch ContinuationFetcher) (EntrySequence, error) {
	if count < 1 || count > youtubeSearchMaxCount {
		return nil, fmt.Errorf("%w: invalid YouTube search count", ErrInvalidPlaylist)
	}
	base, err := ContinuationEntries(first, token, fetch)
	if err != nil {
		return nil, err
	}
	return limitedEntries{source: base, limit: count}, nil
}

type limitedEntries struct {
	source EntrySequence
	limit  int
}

func (entries limitedEntries) Iterator() EntryIterator {
	return &limitedEntryIterator{source: entries.source.Iterator(), left: entries.limit}
}

type limitedEntryIterator struct {
	source EntryIterator
	left   int
}

func (iterator *limitedEntryIterator) Next(ctx context.Context) (Entry, bool, error) {
	if iterator.left == 0 {
		return Entry{}, false, nil
	}
	entry, ok, err := iterator.source.Next(ctx)
	if ok {
		iterator.left--
	}
	return entry, ok, err
}

func categorizeYouTubeSearchError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) ||
		errors.Is(err, ErrInvalidPlaylist) || errors.Is(err, ErrAuthentication) ||
		errors.Is(err, ErrUnavailable) || errors.Is(err, ErrUnsupported) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusTooManyRequests:
			return ErrYouTubeSearchRateLimited
		}
	}
	return fmt.Errorf("%w: request failed", ErrYouTubeSearchNetwork)
}

func youtubeSearchAlertError(alert string) error {
	lower := strings.ToLower(alert)
	if strings.Contains(lower, "sign in") || strings.Contains(lower, "login") {
		return fmt.Errorf("%w: search access denied", ErrAuthentication)
	}
	return fmt.Errorf("%w: search unavailable", ErrUnavailable)
}
