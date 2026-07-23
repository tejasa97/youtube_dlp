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
	if parsed == nil {
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
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 3 || parts[0] != "channel" || !youtubeChannelIDPattern.MatchString(parts[1]) {
		return "", "", false
	}
	switch parts[2] {
	case "videos", "shorts", "streams":
		return parts[1], parts[2], true
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
	parsed, err := parseYouTubeChannelTabData(raw)
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
	entries, err := ContinuationEntries(parsed.entries, parsed.continuation, func(ctx context.Context, token string) ([]Entry, string, error) {
		return fetchYouTubeChannelContinuation(ctx, transport, token, config)
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
}

func parseYouTubeChannelTabData(data []byte) (youtubeChannelTabPage, error) {
	var root value.Value
	if err := json.Unmarshal(data, &root); err != nil {
		return youtubeChannelTabPage{}, fmt.Errorf("%w: decode YouTube channel tab data", ErrInvalidMetadata)
	}
	if _, ok := root.Object(); !ok {
		return youtubeChannelTabPage{}, fmt.Errorf("%w: YouTube channel tab root", ErrInvalidMetadata)
	}
	var page youtubeChannelTabPage
	nodes := 0
	err := walkOrderedJSON(root, 0, &nodes, func(key string, object *value.Object) {
		switch key {
		case "videoRenderer", "gridVideoRenderer":
			if entry, ok := youtubeChannelVideoEntry(object); ok {
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
		case "c4TabbedHeaderRenderer":
			if page.title == "" {
				page.title = rendererText(object.Lookup("title"))
			}
		case "alertRenderer":
			if page.alert == "" {
				page.alert = rendererText(object.Lookup("text"))
			}
		}
	})
	if err != nil {
		return youtubeChannelTabPage{}, err
	}
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

func fetchYouTubeChannelContinuation(ctx context.Context, transport Transport, token string, config youtubePlaylistConfig) ([]Entry, string, error) {
	if token = validYouTubeContinuationToken(token); token == "" {
		return nil, "", fmt.Errorf("%w: invalid YouTube continuation", ErrInvalidPlaylist)
	}
	version := config.ClientVersion
	if version == "" {
		version = youtubeDefaultClientVersion
	}
	payload := map[string]any{"context": map[string]any{"client": map[string]any{"clientName": "WEB", "clientVersion": version, "hl": "en", "timeZone": "UTC", "utcOffsetMinutes": 0, "visitorData": config.VisitorData}}, "continuation": token}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("%w: encode YouTube continuation", ErrInvalidMetadata)
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
		return nil, "", categorizeYouTubeChannelError(err)
	}
	parsed, err := parseYouTubeChannelTabData(response)
	if err != nil {
		return nil, "", err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return nil, "", youtubePlaylistAlertError(parsed.alert)
	}
	return parsed.entries, parsed.continuation, nil
}

func categorizeYouTubeChannelError(err error) error {
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
			return ErrYouTubeChannelRateLimited
		}
	}
	return fmt.Errorf("%w: request failed", ErrYouTubeChannelNetwork)
}
