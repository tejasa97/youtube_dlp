package extractor

// This file deliberately implements the small, public-video subset of
// yt-dlp's YouTube search extractors.  Registration is owned by the client
// package so this extractor can remain a narrowly auditable route.

import (
	"context"
	"encoding/json"
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
	youtubeSearchDefaultCount  = 1
	youtubeSearchMaxCount      = 50 // bounded to avoid unbounded remote paging
	youtubeSearchMaxQueryBytes = 500
	youtubeSearchMaxURLBytes   = 4096
	youtubeSearchParams        = "EgIQAfABAQ==" // upstream YoutubeSearchIE: videos only
	youtubeSearchAPIURL        = "https://www.youtube.com/youtubei/v1/search"
)

var youtubeSearchScheme = regexp.MustCompile(`^ytsearch([0-9]*|all)$`)

var (
	ErrYouTubeSearchRateLimited = errors.New("YouTube search rate limited")
	ErrYouTubeSearchNetwork     = errors.New("YouTube search network failure")
)

// YouTubeSearch accepts ytsearch[N]:query, ytsearchall:query (locally capped
// at 50), and exact public /results or /search URLs with search_query or q.
// It intentionally does not model Music, channel, playlist, hashtag, sorting,
// or authenticated search.
type YouTubeSearch struct{}

func NewYouTubeSearch() YouTubeSearch { return YouTubeSearch{} }
func (YouTubeSearch) Name() string    { return "youtube_search" }
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
	entries, err := youtubeSearchEntries(first.entries, first.continuation, count, func(ctx context.Context, token string) ([]Entry, string, error) {
		return fetchYouTubeSearchContinuation(ctx, request.Transport, token, config)
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
		return query, count, "https://www.youtube.com/results?" + url.Values{"search_query": {query}, "sp": {youtubeSearchParams}}.Encode(), true
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
	return query, youtubeSearchDefaultCount, (&url.URL{Scheme: "https", Host: "www.youtube.com", Path: parsed.Path, RawQuery: parsed.RawQuery}).String(), true
}

func validYouTubeSearchQuery(query string) bool {
	return query != "" && len(query) <= youtubeSearchMaxQueryBytes && !strings.ContainsAny(query, "\x00\r\n")
}

type youtubeSearchPage struct {
	entries             []Entry
	continuation, alert string
}

func parseYouTubeSearchData(data []byte) (youtubeSearchPage, error) {
	var root value.Value
	if err := json.Unmarshal(data, &root); err != nil {
		return youtubeSearchPage{}, fmt.Errorf("%w: decode YouTube search data", ErrInvalidMetadata)
	}
	if _, ok := root.Object(); !ok {
		return youtubeSearchPage{}, fmt.Errorf("%w: YouTube search root", ErrInvalidMetadata)
	}
	var page youtubeSearchPage
	nodes := 0
	err := walkOrderedJSON(root, 0, &nodes, func(key string, object *value.Object) {
		switch key {
		case "videoRenderer", "reelItemRenderer":
			if entry, ok := youtubeSearchVideoEntry(object); ok {
				page.entries = append(page.entries, entry)
			}
		case "lockupViewModel":
			if entry, ok := youtubePlaylistLockupEntry(object); ok {
				page.entries = append(page.entries, entry)
			}
		case "continuationItemRenderer":
			if token := validYouTubeContinuationToken(objectString(object, "continuationEndpoint", "continuationCommand", "token")); token != "" {
				page.continuation = token
			}
		case "continuationItemViewModel":
			if token := youtubeContinuationViewModelToken(object); token != "" {
				page.continuation = token
			}
		case "nextContinuationData":
			if token := validYouTubeContinuationToken(objectString(object, "continuation")); token != "" {
				page.continuation = token
			}
		case "alertRenderer":
			if page.alert == "" {
				page.alert = rendererText(object.Lookup("text"))
			}
		}
	})
	if err != nil {
		return youtubeSearchPage{}, err
	}
	return page, nil
}

func youtubeSearchVideoEntry(renderer *value.Object) (Entry, bool) {
	videoID := objectString(renderer, "videoId")
	if !youtubeIDPattern.MatchString(videoID) {
		return Entry{}, false
	}
	path := "/watch?v="
	if objectString(renderer, "navigationEndpoint", "reelWatchEndpoint", "videoId") == videoID || objectString(renderer, "videoType") == "SHORT" {
		path = "/shorts/"
	}
	return Entry{URL: "https://www.youtube.com" + path + videoID, ExtractorKey: "youtube", ID: videoID, Title: rendererText(renderer.Lookup("title"))}, true
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

func fetchYouTubeSearchContinuation(ctx context.Context, transport Transport, token string, config youtubePlaylistConfig) ([]Entry, string, error) {
	if token = validYouTubeContinuationToken(token); token == "" {
		return nil, "", fmt.Errorf("%w: invalid YouTube search continuation", ErrInvalidPlaylist)
	}
	version := config.ClientVersion
	if version == "" {
		version = youtubeDefaultClientVersion
	}
	payload := map[string]any{"context": map[string]any{"client": map[string]any{"clientName": "WEB", "clientVersion": version, "hl": "en", "timeZone": "UTC", "utcOffsetMinutes": 0, "visitorData": config.VisitorData}}, "continuation": token}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("%w: encode YouTube search continuation", ErrInvalidMetadata)
	}
	endpoint, _ := url.Parse(youtubeSearchAPIURL)
	values := endpoint.Query()
	values.Set("prettyPrint", "false")
	if config.APIKey != "" {
		values.Set("key", config.APIKey)
	}
	endpoint.RawQuery = values.Encode()
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Origin", "https://www.youtube.com")
	headers.Set("X-Youtube-Client-Name", "1")
	headers.Set("X-Youtube-Client-Version", version)
	var response json.RawMessage
	if err := RequestJSON(ctx, transport, http.MethodPost, endpoint.String(), body, headers, &response); err != nil {
		return nil, "", categorizeYouTubeSearchError(err)
	}
	page, err := parseYouTubeSearchData(response)
	if err != nil {
		return nil, "", err
	}
	if page.alert != "" && len(page.entries) == 0 {
		return nil, "", youtubeSearchAlertError(page.alert)
	}
	return page.entries, page.continuation, nil
}

func categorizeYouTubeSearchError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
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
