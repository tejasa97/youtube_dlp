package extractor

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

var youtubeChannelIDPattern = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)

const youtubeMaxTabEntryTitleBytes = 4096

var (
	ErrYouTubeChannelRateLimited = errors.New("YouTube channel rate limited")
	ErrYouTubeChannelNetwork     = errors.New("YouTube channel network failure")
)

// YouTubeChannelTab handles only explicit, public channel tab URLs. Register
// it before YouTube: the video extractor intentionally remains responsible for
// watch pages and all non-tab YouTube routes.
type YouTubeChannelTab struct{}

func NewYouTubeChannelTab() YouTubeChannelTab { return YouTubeChannelTab{} }
func (YouTubeChannelTab) Name() string        { return "youtube_channel_tab" }

func (YouTubeChannelTab) Suitable(parsed *url.URL) bool {
	_, _, ok := youtubeChannelTabTarget(parsed)
	return ok
}

func (YouTubeChannelTab) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	channelID, tab, ok := youtubeChannelTabTarget(parsed)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: unsupported YouTube channel tab", ErrUnsupported)
	}
	return extractYouTubeChannelTab(ctx, request.Transport, channelID, tab)
}

// youtubeChannelTabTarget is the one strict route policy used by Suitable and
// Extract. It accepts exact public web hosts only and rejects the broad class
// of video URLs that may happen to contain a channel-looking path.
func youtubeChannelTabTarget(parsed *url.URL) (channelID, tab string, ok bool) {
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
	raw := strings.ToLower(parsed.RawQuery)
	if strings.Contains(raw, "%2f") || strings.Contains(raw, "%5c") || strings.Contains(raw, "%00") {
		return "", "", false
	}
	// These are deliberately exact routes. Refusing RawPath prevents an
	// encoded spelling from becoming an alternate canonical form.
	if parsed.RawPath != "" {
		return "", "", false
	}
	parts := strings.Split(parsed.Path, "/")
	if len(parts) != 4 || parts[0] != "" || parts[1] != "channel" || !youtubeChannelIDPattern.MatchString(parts[2]) {
		return "", "", false
	}
	switch parts[3] {
	case "videos", "shorts", "streams", "playlists":
		return parts[2], parts[3], true
	}
	return "", "", false
}

func extractYouTubeChannelTab(ctx context.Context, transport Transport, channelID, tab string) (Extraction, error) {
	canonical := "https://www.youtube.com/channel/" + channelID + "/" + tab
	page, _, err := transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, categorizeYouTubeChannelError(err)
	}
	raw, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: YouTube channel tab initial data", ErrInvalidMetadata)
	}
	parsed, err := parseYouTubeChannelTabData(raw, tab)
	if err != nil {
		return Extraction{}, err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return Extraction{}, youtubePlaylistAlertError(parsed.alert)
	}
	if parsed.title == "" {
		return Extraction{}, fmt.Errorf("%w: missing YouTube channel metadata", ErrInvalidMetadata)
	}
	config := extractYouTubePlaylistConfig(page)
	visitorData := parsed.visitorData
	if visitorData == "" {
		visitorData = config.VisitorData
	}
	entries, err := StatefulContinuationEntries(parsed.entries, parsed.continuation, visitorData, func(ctx context.Context, token, visitorData string) ([]Entry, string, string, error) {
		return fetchYouTubeChannelContinuation(ctx, transport, token, visitorData, config, tab)
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(channelID)},
		value.Field{Key: "title", Value: value.String(parsed.title)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	)), entries)
}

type youtubeChannelTabPage struct {
	entries                    []Entry
	continuation, title, alert string
	visitorData                string
}

func parseYouTubeChannelTabData(data []byte, tab string) (youtubeChannelTabPage, error) {
	var root value.Value
	if err := json.Unmarshal(data, &root); err != nil {
		return youtubeChannelTabPage{}, fmt.Errorf("%w: decode YouTube channel tab data", ErrInvalidMetadata)
	}
	rootObject, ok := root.Object()
	if !ok {
		return youtubeChannelTabPage{}, fmt.Errorf("%w: YouTube channel tab root", ErrInvalidMetadata)
	}
	var page youtubeChannelTabPage
	nodes := 0
	err := walkOrderedJSON(youtubePlaylistContentScope(rootObject), 0, &nodes, func(key string, object *value.Object) {
		switch key {
		case "videoRenderer", "gridVideoRenderer":
			if tab != "playlists" {
				if entry, ok := youtubeChannelVideoEntry(object); ok {
					page.entries = append(page.entries, entry)
				}
			}
		case "playlistRenderer", "gridPlaylistRenderer":
			if tab == "playlists" {
				if entry, ok := youtubeTabPlaylistEntry(object); ok {
					page.entries = append(page.entries, entry)
				}
			}
		case "lockupViewModel":
			if tab == "playlists" {
				if entry, ok := youtubeTabPlaylistLockupEntry(object); ok {
					page.entries = append(page.entries, entry)
				}
			} else if entry, ok := youtubePlaylistLockupEntry(object); ok {
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
		}
	})
	if err != nil {
		return youtubeChannelTabPage{}, err
	}
	metadataNodes := 0
	if err := walkOrderedJSON(rootObject.Lookup("metadata"), 0, &metadataNodes, func(key string, object *value.Object) {
		if key == "channelMetadataRenderer" && page.title == "" {
			page.title = objectString(object, "title")
		}
	}); err != nil {
		return youtubeChannelTabPage{}, err
	}
	headerNodes := 0
	if err := walkOrderedJSON(rootObject.Lookup("header"), 0, &headerNodes, func(key string, object *value.Object) {
		if key == "c4TabbedHeaderRenderer" && page.title == "" {
			page.title = rendererText(object.Lookup("title"))
		}
	}); err != nil {
		return youtubeChannelTabPage{}, err
	}
	alertNodes := 0
	if err := walkOrderedJSON(rootObject.Lookup("alerts"), 0, &alertNodes, func(key string, object *value.Object) {
		if key == "alertRenderer" && page.alert == "" {
			page.alert = rendererText(object.Lookup("text"))
		}
	}); err != nil {
		return youtubeChannelTabPage{}, err
	}
	page.visitorData = objectString(rootObject, "responseContext", "visitorData")
	return page, nil
}

func youtubeChannelVideoEntry(renderer *value.Object) (Entry, bool) {
	videoID := objectString(renderer, "videoId")
	if !youtubeIDPattern.MatchString(videoID) {
		return Entry{}, false
	}
	path := "/watch?v="
	if objectString(renderer, "navigationEndpoint", "reelWatchEndpoint", "videoId") == videoID {
		path = "/shorts/"
	}
	return Entry{URL: "https://www.youtube.com" + path + videoID, ExtractorKey: "youtube", ID: videoID, Title: rendererText(renderer.Lookup("title"))}, true
}

func youtubeTabPlaylistEntry(renderer *value.Object) (Entry, bool) {
	playlistID := objectString(renderer, "playlistId")
	if !youtubePlaylistIDPattern.MatchString(playlistID) {
		return Entry{}, false
	}
	return youtubeTabPlaylistResult(playlistID, rendererText(renderer.Lookup("title"))), true
}

func youtubeTabPlaylistLockupEntry(viewModel *value.Object) (Entry, bool) {
	switch objectString(viewModel, "contentType") {
	case "LOCKUP_CONTENT_TYPE_PLAYLIST", "LOCKUP_CONTENT_TYPE_PODCAST":
	default:
		return Entry{}, false
	}
	playlistID := objectString(viewModel, "contentId")
	if !youtubePlaylistIDPattern.MatchString(playlistID) {
		return Entry{}, false
	}
	title := objectString(viewModel, "metadata", "lockupMetadataViewModel", "title", "content")
	return youtubeTabPlaylistResult(playlistID, title), true
}

func youtubeTabPlaylistResult(playlistID, title string) Entry {
	if len(title) > youtubeMaxTabEntryTitleBytes || strings.ContainsRune(title, 0) {
		title = ""
	}
	return Entry{
		URL: "https://www.youtube.com/playlist?list=" + playlistID, ExtractorKey: "youtube",
		ID: playlistID, Title: title,
	}
}

func fetchYouTubeChannelContinuation(ctx context.Context, transport Transport, token, visitorData string, config youtubePlaylistConfig, tab string) ([]Entry, string, string, error) {
	if token = validYouTubeContinuationToken(token); token == "" {
		return nil, "", visitorData, fmt.Errorf("%w: invalid YouTube continuation", ErrInvalidPlaylist)
	}
	version := config.ClientVersion
	if version == "" {
		version = youtubeDefaultClientVersion
	}
	payload := map[string]any{"context": map[string]any{"client": map[string]any{"clientName": "WEB", "clientVersion": version, "hl": "en", "timeZone": "UTC", "utcOffsetMinutes": 0, "visitorData": visitorData}}, "continuation": token}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", visitorData, fmt.Errorf("%w: encode YouTube continuation", ErrInvalidMetadata)
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
		return nil, "", visitorData, categorizeYouTubeChannelError(err)
	}
	parsed, err := parseYouTubeChannelTabData(response, tab)
	if err != nil {
		return nil, "", visitorData, err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return nil, "", visitorData, youtubePlaylistAlertError(parsed.alert)
	}
	if parsed.visitorData == "" {
		parsed.visitorData = visitorData
	}
	return parsed.entries, parsed.continuation, parsed.visitorData, nil
}

func categorizeYouTubeChannelError(err error) error {
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
			return ErrYouTubeChannelRateLimited
		}
	}
	return fmt.Errorf("%w: request failed", ErrYouTubeChannelNetwork)
}
