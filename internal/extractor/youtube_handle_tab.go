package extractor

// This file intentionally implements only the small public subset of
// YoutubeTabIE needed for explicit handle browse tabs.  It does not attempt to
// resolve a handle, infer a tab, or emulate all renderer variants.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// validYouTubeHandle mirrors the pinned reference's Unicode-aware
// @[\w.-]{3,30} grammar. Python's Unicode \w accepts letters, numbers, and
// underscore; dots and hyphens are the two additional handle characters.
// Length is measured in Unicode code points, matching the reference regex.
func validYouTubeHandle(handle string) bool {
	if !utf8.ValidString(handle) || !strings.HasPrefix(handle, "@") {
		return false
	}
	value := strings.TrimPrefix(handle, "@")
	count := utf8.RuneCountInString(value)
	if count < 3 || count > 30 {
		return false
	}
	for _, character := range value {
		if character == '_' || character == '.' || character == '-' ||
			unicode.IsLetter(character) || unicode.IsNumber(character) {
			continue
		}
		return false
	}
	return true
}

var (
	ErrYouTubeHandleTabRateLimited = errors.New("YouTube handle tab rate limited")
	ErrYouTubeHandleTabNetwork     = errors.New("YouTube handle tab network failure")
)

// YouTubeHandleTab handles explicit public /@handle tab URLs. Registration is
// intentionally owned by the client package.
type YouTubeHandleTab struct{}

func NewYouTubeHandleTab() YouTubeHandleTab { return YouTubeHandleTab{} }
func (YouTubeHandleTab) Name() string       { return "youtube_handle_tab" }

func (YouTubeHandleTab) Suitable(parsed *url.URL) bool {
	_, _, ok := youtubeHandleTabTarget(parsed)
	return ok
}

func (YouTubeHandleTab) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid YouTube handle tab URL", ErrUnsupported)
	}
	handle, tab, ok := youtubeHandleTabTarget(parsed)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: unsupported YouTube handle tab", ErrUnsupported)
	}
	return extractYouTubeHandleTab(ctx, request.Transport, handle, tab)
}

// youtubeHandleTabTarget is shared by Suitable and Extract.  It admits only
// canonicalizable, exact web routes; query strings are allowed but are never
// preserved in the canonical fetch URL.
func youtubeHandleTabTarget(parsed *url.URL) (handle, tab string, ok bool) {
	if parsed == nil || parsed.Fragment != "" || len(parsed.String()) > 4096 {
		return "", "", false
	}
	if _, _, err := validateYouTubeURLPolicy(parsed); err != nil {
		return "", "", false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host != "youtube.com" && host != "www.youtube.com" {
		return "", "", false
	}
	if raw := strings.ToLower(parsed.RawQuery); strings.Contains(raw, "%2f") || strings.Contains(raw, "%5c") || strings.Contains(raw, "%00") {
		return "", "", false
	}
	parts := strings.Split(parsed.Path, "/")
	if len(parts) != 3 || parts[0] != "" || !validYouTubeHandle(parts[1]) {
		return "", "", false
	}
	if youtubePublicTabType(parts[2]) != youtubeTabUnsupported {
		return parts[1], parts[2], true
	}
	return "", "", false
}

func extractYouTubeHandleTab(ctx context.Context, transport Transport, handle, tab string) (Extraction, error) {
	canonical := "https://www.youtube.com/" + handle + "/" + tab
	page, _, err := transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, categorizeYouTubeHandleTabError(err)
	}
	raw, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: YouTube handle tab initial data", ErrInvalidMetadata)
	}
	if err := validateYouTubeSelectedTab(raw, tab); err != nil {
		return Extraction{}, err
	}
	parsed, err := parseYouTubeHandleTabData(raw, tab)
	if err != nil {
		return Extraction{}, err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return Extraction{}, youtubeHandleTabAlertError(parsed.alert)
	}
	if parsed.title == "" {
		return Extraction{}, fmt.Errorf("%w: missing YouTube handle tab metadata", ErrInvalidMetadata)
	}
	id := "handle:" + handle
	if youtubeChannelIDPattern.MatchString(parsed.channelID) {
		id = parsed.channelID
	}
	config := extractYouTubePlaylistConfig(page)
	visitorData := parsed.visitorData
	if visitorData == "" {
		visitorData = config.VisitorData
	}
	entries, err := StatefulContinuationEntries(parsed.entries, parsed.continuation, visitorData, func(ctx context.Context, token, visitorData string) ([]Entry, string, string, error) {
		return fetchYouTubeHandleTabContinuation(ctx, transport, token, visitorData, config, tab)
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(parsed.title)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	)), entries)
}

type youtubeHandleTabPage struct {
	entries                               []Entry
	continuation, title, channelID, alert string
	visitorData                           string
}

func parseYouTubeHandleTabData(data []byte, tab string) (youtubeHandleTabPage, error) {
	var root value.Value
	if err := json.Unmarshal(data, &root); err != nil {
		return youtubeHandleTabPage{}, fmt.Errorf("%w: decode YouTube handle tab data", ErrInvalidMetadata)
	}
	rootObject, ok := root.Object()
	if !ok {
		return youtubeHandleTabPage{}, fmt.Errorf("%w: YouTube handle tab root", ErrInvalidMetadata)
	}
	var page youtubeHandleTabPage
	var suppressed map[string]int
	if tab == "community" {
		suppressed = make(map[string]int)
	}
	appendEntry := func(entry Entry, ok bool) {
		if !ok {
			return
		}
		key := youtubeTabEntryKey(entry)
		if suppressed[key] > 0 {
			suppressed[key]--
			return
		}
		appendYouTubeTabEntry(&page.entries, entry, true)
	}
	nodes := 0
	err := walkOrderedJSON(youtubePlaylistContentScope(rootObject), 0, &nodes, func(key string, object *value.Object) {
		switch key {
		case "videoRenderer", "gridVideoRenderer", "reelItemRenderer":
			if youtubeTabAllowsVideos(tab) {
				entry, ok := youtubeHandleTabVideoEntry(object)
				appendEntry(entry, ok)
			}
		case "playlistRenderer", "gridPlaylistRenderer":
			if youtubeTabAllowsPlaylists(tab) {
				entry, ok := youtubeTabPlaylistEntry(object)
				appendEntry(entry, ok)
			}
		case "lockupViewModel":
			if youtubeTabAllowsPlaylists(tab) {
				entry, ok := youtubeTabPlaylistLockupEntry(object)
				appendEntry(entry, ok)
			}
			if youtubeTabAllowsVideos(tab) {
				entry, ok := youtubePlaylistLockupEntry(object)
				appendEntry(entry, ok)
			}
		case "backstagePostRenderer":
			if tab == "community" {
				for _, entry := range youtubeCommunityPostEntries(object) {
					appendYouTubeTabEntry(&page.entries, entry, true)
				}
				for _, entry := range youtubeCommunityAttachmentEntries(object) {
					suppressed[youtubeTabEntryKey(entry)]++
				}
			}
		}
		if token := youtubeContinuationToken(key, object); token != "" {
			page.continuation = token
		}
	})
	if err != nil {
		return youtubeHandleTabPage{}, err
	}
	continuationNodes := 0
	if err := walkOrderedJSON(rootObject.Lookup("continuationContents"), 0, &continuationNodes, func(key string, object *value.Object) {
		if token := youtubeContinuationToken(key, object); token != "" {
			page.continuation = token
		}
	}); err != nil {
		return youtubeHandleTabPage{}, err
	}
	metadataNodes := 0
	if err := walkOrderedJSON(rootObject.Lookup("metadata"), 0, &metadataNodes, func(key string, object *value.Object) {
		if key != "channelMetadataRenderer" {
			return
		}
		if page.title == "" {
			page.title = objectString(object, "title")
		}
		if page.channelID == "" {
			page.channelID = objectString(object, "externalId")
		}
	}); err != nil {
		return youtubeHandleTabPage{}, err
	}
	headerNodes := 0
	if err := walkOrderedJSON(rootObject.Lookup("header"), 0, &headerNodes, func(key string, object *value.Object) {
		if key != "c4TabbedHeaderRenderer" && key != "pageHeaderRenderer" {
			return
		}
		if page.title == "" {
			page.title = rendererText(object.Lookup("title"))
		}
		if page.channelID == "" {
			page.channelID = objectString(object, "channelId")
		}
	}); err != nil {
		return youtubeHandleTabPage{}, err
	}
	alertNodes := 0
	if err := walkOrderedJSON(rootObject.Lookup("alerts"), 0, &alertNodes, func(key string, object *value.Object) {
		if key == "alertRenderer" && page.alert == "" {
			page.alert = rendererText(object.Lookup("text"))
		}
	}); err != nil {
		return youtubeHandleTabPage{}, err
	}
	page.visitorData = objectString(rootObject, "responseContext", "visitorData")
	return page, nil
}

func youtubeHandleTabVideoEntry(renderer *value.Object) (Entry, bool) {
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

func fetchYouTubeHandleTabContinuation(ctx context.Context, transport Transport, token, visitorData string, config youtubePlaylistConfig, tab string) ([]Entry, string, string, error) {
	return fetchYouTubeTabContinuation(ctx, transport, token, visitorData, config, tab, "handle", categorizeYouTubeHandleTabError)
}

// fetchYouTubeTabContinuation is shared by the bounded channel-tab
// extractors. Renderer parsing and continuation state must stay identical
// across the different public channel URL forms.
func fetchYouTubeTabContinuation(
	ctx context.Context,
	transport Transport,
	token, visitorData string,
	config youtubePlaylistConfig,
	tab, subject string,
	categorize func(error) error,
) ([]Entry, string, string, error) {
	if token = validYouTubeContinuationToken(token); token == "" {
		return nil, "", visitorData, fmt.Errorf("%w: invalid YouTube %s tab continuation", ErrInvalidPlaylist, subject)
	}
	version := config.ClientVersion
	if version == "" {
		version = youtubeDefaultClientVersion
	}
	payload := map[string]any{"context": map[string]any{"client": map[string]any{"clientName": "WEB", "clientVersion": version, "hl": "en", "timeZone": "UTC", "utcOffsetMinutes": 0, "visitorData": visitorData}}, "continuation": token}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", visitorData, fmt.Errorf("%w: encode YouTube %s tab continuation", ErrInvalidMetadata, subject)
	}
	endpoint, _ := url.Parse(youtubePlaylistContinuationURL)
	query := endpoint.Query()
	query.Set("prettyPrint", "false")
	if config.APIKey != "" {
		query.Set("key", config.APIKey)
	}
	endpoint.RawQuery = query.Encode()
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Origin", "https://www.youtube.com")
	headers.Set("X-Youtube-Client-Name", "1")
	headers.Set("X-Youtube-Client-Version", version)
	var response json.RawMessage
	if err := RequestJSON(ctx, transport, http.MethodPost, endpoint.String(), body, headers, &response); err != nil {
		return nil, "", visitorData, categorize(err)
	}
	parsed, err := parseYouTubeHandleTabData(response, tab)
	if err != nil {
		return nil, "", visitorData, err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return nil, "", visitorData, youtubeHandleTabAlertError(parsed.alert)
	}
	if parsed.visitorData == "" {
		parsed.visitorData = visitorData
	}
	return parsed.entries, parsed.continuation, parsed.visitorData, nil
}

func categorizeYouTubeHandleTabError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) ||
		errors.Is(err, ErrInvalidPlaylist) || errors.Is(err, ErrAuthentication) ||
		errors.Is(err, ErrUnavailable) {
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
			return ErrYouTubeHandleTabRateLimited
		}
	}
	return fmt.Errorf("%w: request failed", ErrYouTubeHandleTabNetwork)
}

func youtubeHandleTabAlertError(alert string) error {
	lower := strings.ToLower(alert)
	if strings.Contains(lower, "private") || strings.Contains(lower, "sign in") || strings.Contains(lower, "login") {
		return fmt.Errorf("%w: handle tab access denied", ErrAuthentication)
	}
	return fmt.Errorf("%w: handle tab unavailable", ErrUnavailable)
}
