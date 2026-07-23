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
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// Handles accepted here are deliberately ASCII-only: an @ followed by 3–30
// letters, digits, dots, underscores, or hyphens, with at least one letter or
// digit.  This bounded subset avoids silently mis-canonicalizing Unicode
// handles while retaining ordinary public handles.
var youtubeHandlePattern = regexp.MustCompile(`^@[A-Za-z0-9._-]{3,30}$`)
var youtubeHandleHasAlnumPattern = regexp.MustCompile(`[A-Za-z0-9]`)

var (
	ErrYouTubeHandleTabRateLimited = errors.New("YouTube handle tab rate limited")
	ErrYouTubeHandleTabNetwork     = errors.New("YouTube handle tab network failure")
)

// YouTubeHandleTab handles exact public /@handle/{videos,shorts,streams}
// URLs. Registration is intentionally owned by the client package.
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
	// Refuse encoded paths even where net/url has decoded them: the route is
	// intentionally exact and this avoids alternate canonical forms.
	if parsed.RawPath != "" {
		return "", "", false
	}
	parts := strings.Split(parsed.Path, "/")
	if len(parts) != 3 || parts[0] != "" || !youtubeHandlePattern.MatchString(parts[1]) || !youtubeHandleHasAlnumPattern.MatchString(parts[1]) {
		return "", "", false
	}
	switch parts[2] {
	case "videos", "shorts", "streams":
		return strings.ToLower(parts[1]), parts[2], true
	default:
		return "", "", false
	}
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
	parsed, err := parseYouTubeHandleTabData(raw)
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
	entries, err := ContinuationEntries(parsed.entries, parsed.continuation, func(ctx context.Context, token string) ([]Entry, string, error) {
		return fetchYouTubeHandleTabContinuation(ctx, transport, token, config)
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
}

func parseYouTubeHandleTabData(data []byte) (youtubeHandleTabPage, error) {
	var root value.Value
	if err := json.Unmarshal(data, &root); err != nil {
		return youtubeHandleTabPage{}, fmt.Errorf("%w: decode YouTube handle tab data", ErrInvalidMetadata)
	}
	if _, ok := root.Object(); !ok {
		return youtubeHandleTabPage{}, fmt.Errorf("%w: YouTube handle tab root", ErrInvalidMetadata)
	}
	var page youtubeHandleTabPage
	nodes := 0
	err := walkOrderedJSON(root, 0, &nodes, func(key string, object *value.Object) {
		switch key {
		case "videoRenderer", "gridVideoRenderer", "reelItemRenderer":
			if entry, ok := youtubeHandleTabVideoEntry(object); ok {
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
		case "channelMetadataRenderer":
			if page.title == "" {
				page.title = objectString(object, "title")
			}
			if page.channelID == "" {
				page.channelID = objectString(object, "externalId")
			}
		case "c4TabbedHeaderRenderer", "pageHeaderRenderer":
			if page.title == "" {
				page.title = rendererText(object.Lookup("title"))
			}
			if page.channelID == "" {
				page.channelID = objectString(object, "channelId")
			}
		case "alertRenderer":
			if page.alert == "" {
				page.alert = rendererText(object.Lookup("text"))
			}
		}
	})
	if err != nil {
		return youtubeHandleTabPage{}, err
	}
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

func fetchYouTubeHandleTabContinuation(ctx context.Context, transport Transport, token string, config youtubePlaylistConfig) ([]Entry, string, error) {
	if token = validYouTubeContinuationToken(token); token == "" {
		return nil, "", fmt.Errorf("%w: invalid YouTube handle tab continuation", ErrInvalidPlaylist)
	}
	version := config.ClientVersion
	if version == "" {
		version = youtubeDefaultClientVersion
	}
	payload := map[string]any{"context": map[string]any{"client": map[string]any{"clientName": "WEB", "clientVersion": version, "hl": "en", "timeZone": "UTC", "utcOffsetMinutes": 0, "visitorData": config.VisitorData}}, "continuation": token}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("%w: encode YouTube handle tab continuation", ErrInvalidMetadata)
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
		return nil, "", categorizeYouTubeHandleTabError(err)
	}
	parsed, err := parseYouTubeHandleTabData(response)
	if err != nil {
		return nil, "", err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return nil, "", youtubeHandleTabAlertError(parsed.alert)
	}
	return parsed.entries, parsed.continuation, nil
}

func categorizeYouTubeHandleTabError(err error) error {
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
