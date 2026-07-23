package extractor

// A deliberately small, public WEB_REMIX search route.  Music result pages
// contain many non-playable renderers; only entries with an actual video id are
// returned so downstream extraction always targets the normal YouTube watch IE.
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
	youtubeMusicSongsParams       = "EgWKAQIIAWoKEAoQAxAEEAkQBQ=="
	youtubeMusicVideosParams      = "EgWKAQIQAWoKEAoQAxAEEAkQBQ=="
	youtubeMusicSearchAPIURL      = "https://music.youtube.com/youtubei/v1/search"
)

var (
	ErrYouTubeMusicSearchRateLimited = errors.New("YouTube Music search rate limited")
	ErrYouTubeMusicSearchNetwork     = errors.New("YouTube Music search network failure")
)

// YouTubeMusicSearch accepts only public music.youtube.com/search URLs.  The
// #songs and #videos sections are pinned to yt-dlp's corresponding upstream
// parameters.  Albums, artists, playlists, podcasts and authenticated Music
// flows are intentionally not represented.
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
	first, err := parseYouTubeMusicSearchData(raw)
	if err != nil {
		return Extraction{}, err
	}
	if first.alert != "" && len(first.entries) == 0 {
		return Extraction{}, youtubeMusicSearchAlertError(first.alert)
	}
	config := extractYouTubePlaylistConfig(page)
	entries, err := youtubeMusicSearchEntries(first.entries, first.continuation, count, func(ctx context.Context, token string) ([]Entry, string, error) {
		return fetchYouTubeMusicSearchContinuation(ctx, request.Transport, token, config)
	})
	if err != nil {
		return Extraction{}, err
	}
	title := query
	if section != "" {
		title += " - " + section
	}
	return Playlist(value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(title)}, value.Field{Key: "title", Value: value.String(title)}, value.Field{Key: "webpage_url", Value: value.String(canonical)})), entries)
}

func youtubeMusicSearchTarget(u *url.URL) (query string, count int, canonical, section string, ok bool) {
	if u == nil || len(u.String()) > youtubeMusicSearchMaxURLBytes || u.Fragment != "" && u.Fragment != "songs" && u.Fragment != "videos" {
		return
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
	section = u.Fragment
	params := values.Get("sp")
	// Do not silently claim compatibility with the rest of Music's filter matrix.
	// Only the two pinned upstream values are accepted when sp is supplied.
	if section == "songs" {
		params = youtubeMusicSongsParams
	} else if section == "videos" {
		params = youtubeMusicVideosParams
	} else if params == youtubeMusicSongsParams {
		section = "songs"
	} else if params == youtubeMusicVideosParams {
		section = "videos"
	} else if params != "" {
		return "", 0, "", "", false
	}
	canonicalValues := url.Values{"search_query": {query}}
	if params != "" {
		canonicalValues.Set("sp", params)
	}
	return query, youtubeMusicSearchMaxCount, "https://music.youtube.com/search?" + canonicalValues.Encode(), section, true
}

type youtubeMusicSearchPage struct {
	entries             []Entry
	continuation, alert string
}

func parseYouTubeMusicSearchData(data []byte) (youtubeMusicSearchPage, error) {
	var root value.Value
	if err := json.Unmarshal(data, &root); err != nil {
		return youtubeMusicSearchPage{}, fmt.Errorf("%w: decode YouTube Music search data", ErrInvalidMetadata)
	}
	if _, ok := root.Object(); !ok {
		return youtubeMusicSearchPage{}, fmt.Errorf("%w: YouTube Music search root", ErrInvalidMetadata)
	}
	var page youtubeMusicSearchPage
	nodes := 0
	err := walkOrderedJSON(root, 0, &nodes, func(key string, o *value.Object) {
		switch key {
		case "musicResponsiveListItemRenderer", "musicTwoRowItemRenderer", "videoRenderer", "reelItemRenderer":
			if e, ok := youtubeMusicSearchEntry(o); ok {
				page.entries = append(page.entries, e)
			}
		case "lockupViewModel":
			// Lockups can also represent albums, artists and playlists.  Reuse the
			// strict video-only policy used by the YouTube playlist parser.
			if e, ok := youtubePlaylistLockupEntry(o); ok {
				page.entries = append(page.entries, e)
			}
		case "continuationItemRenderer":
			page.continuation = firstMusicContinuation(page.continuation, objectString(o, "continuationEndpoint", "continuationCommand", "token"))
		case "continuationItemViewModel":
			page.continuation = firstMusicContinuation(page.continuation, youtubeContinuationViewModelToken(o))
		case "nextContinuationData":
			page.continuation = firstMusicContinuation(page.continuation, objectString(o, "continuation"))
		case "alertRenderer":
			if page.alert == "" {
				page.alert = rendererText(o.Lookup("text"))
			}
		}
	})
	if err != nil {
		return youtubeMusicSearchPage{}, err
	}
	return page, nil
}
func firstMusicContinuation(old, token string) string {
	if old != "" {
		return old
	}
	return validYouTubeContinuationToken(token)
}
func youtubeMusicSearchEntry(o *value.Object) (Entry, bool) {
	id := objectString(o, "videoId")
	if id == "" {
		id = objectString(o, "navigationEndpoint", "watchEndpoint", "videoId")
	}
	if id == "" {
		id = objectString(o, "playlistItemData", "videoId")
	}
	if !youtubeIDPattern.MatchString(id) {
		return Entry{}, false
	}
	title := rendererText(o.Lookup("title"))
	if title == "" {
		title = musicFlexTitle(o)
	}
	return Entry{URL: "https://www.youtube.com/watch?v=" + id, ExtractorKey: "youtube", ID: id, Title: title}, true
}
func musicFlexTitle(o *value.Object) string {
	columns, _ := o.Lookup("flexColumns").ListValue()
	if len(columns) == 0 {
		return ""
	}
	c, ok := columns[0].Object()
	if !ok {
		return ""
	}
	flex, ok := c.Lookup("musicResponsiveListItemFlexColumnRenderer").Object()
	if !ok {
		return ""
	}
	return rendererText(flex.Lookup("text"))
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
func fetchYouTubeMusicSearchContinuation(ctx context.Context, transport Transport, token string, config youtubePlaylistConfig) ([]Entry, string, error) {
	if token = validYouTubeContinuationToken(token); token == "" {
		return nil, "", fmt.Errorf("%w: invalid YouTube Music search continuation", ErrInvalidPlaylist)
	}
	version := config.ClientVersion
	if version == "" {
		version = youtubeDefaultClientVersion
	}
	body, err := json.Marshal(map[string]any{"context": map[string]any{"client": map[string]any{"clientName": "WEB_REMIX", "clientVersion": version, "hl": "en", "timeZone": "UTC", "utcOffsetMinutes": 0, "visitorData": config.VisitorData}}, "continuation": token})
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
	page, err := parseYouTubeMusicSearchData(response)
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
