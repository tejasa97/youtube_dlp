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

const (
	youtubeInitialDataMarker       = "ytInitialData"
	youtubeConfigMarker            = "ytcfg.set"
	youtubeDefaultClientVersion    = "2.20260708.00.00"
	youtubePlaylistContinuationURL = "https://www.youtube.com/youtubei/v1/browse"
	youtubeMaxJSONDepth            = 128
	youtubeMaxJSONNodes            = 1_000_000
	youtubeMaxContinuationCommands = 32
	youtubeMaxContinuationBytes    = 16 << 10
)

var youtubePlaylistIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

type youtubePlaylistConfig struct {
	APIKey        string
	ClientVersion string
	VisitorData   string
}

func youtubePlaylistID(rawURL string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Path != "/playlist" {
		return "", false
	}
	id := parsed.Query().Get("list")
	return id, youtubePlaylistIDPattern.MatchString(id)
}

func extractYouTubePlaylist(ctx context.Context, request Request, playlistID string) (Extraction, error) {
	canonical := "https://www.youtube.com/playlist?" + url.Values{"list": {playlistID}}.Encode()
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	rawData, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: YouTube initial data: %v", ErrInvalidMetadata, err)
	}
	parsed, err := parseYouTubePlaylistData(rawData)
	if err != nil {
		return Extraction{}, err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return Extraction{}, youtubePlaylistAlertError(parsed.alert)
	}
	if parsed.title == "" {
		return Extraction{}, fmt.Errorf("%w: missing playlist metadata", ErrInvalidMetadata)
	}
	config := extractYouTubePlaylistConfig(page)
	sequence, err := ContinuationEntries(parsed.entries, parsed.continuation, func(ctx context.Context, token string) ([]Entry, string, error) {
		return fetchYouTubePlaylistContinuation(ctx, request.Transport, token, config)
	})
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(playlistID)},
		value.Field{Key: "title", Value: value.String(parsed.title)},
		value.Field{Key: "description", Value: value.String(parsed.description)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	return Playlist(info, sequence)
}

func fetchYouTubePlaylistContinuation(ctx context.Context, transport Transport, token string, config youtubePlaylistConfig) ([]Entry, string, error) {
	clientVersion := config.ClientVersion
	if clientVersion == "" {
		clientVersion = youtubeDefaultClientVersion
	}
	type clientContext struct {
		ClientName       string `json:"clientName"`
		ClientVersion    string `json:"clientVersion"`
		HL               string `json:"hl"`
		TimeZone         string `json:"timeZone"`
		UTCOffsetMinutes int    `json:"utcOffsetMinutes"`
		VisitorData      string `json:"visitorData,omitempty"`
	}
	type requestBody struct {
		Context struct {
			Client clientContext `json:"client"`
		} `json:"context"`
		Continuation string `json:"continuation"`
	}
	payload := requestBody{Continuation: token}
	payload.Context.Client = clientContext{
		ClientName: "WEB", ClientVersion: clientVersion, HL: "en", TimeZone: "UTC",
		UTCOffsetMinutes: 0, VisitorData: config.VisitorData,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("%w: encode playlist continuation", ErrInvalidMetadata)
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
	headers.Set("X-Youtube-Client-Version", clientVersion)
	var response json.RawMessage
	if err := RequestJSON(ctx, transport, http.MethodPost, endpoint.String(), body, headers, &response); err != nil {
		var status *HTTPStatusError
		if errors.As(err, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden) {
			return nil, "", ErrAuthentication
		}
		return nil, "", err
	}
	parsed, err := parseYouTubePlaylistData(response)
	if err != nil {
		return nil, "", err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return nil, "", youtubePlaylistAlertError(parsed.alert)
	}
	return parsed.entries, parsed.continuation, nil
}

func extractYouTubePlaylistConfig(page []byte) youtubePlaylistConfig {
	raw, err := extractJSONObject(page, youtubeConfigMarker)
	if err != nil {
		return youtubePlaylistConfig{ClientVersion: youtubeDefaultClientVersion}
	}
	var config struct {
		APIKey        string `json:"INNERTUBE_API_KEY"`
		ClientVersion string `json:"INNERTUBE_CLIENT_VERSION"`
		VisitorData   string `json:"VISITOR_DATA"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return youtubePlaylistConfig{ClientVersion: youtubeDefaultClientVersion}
	}
	if config.ClientVersion == "" {
		config.ClientVersion = youtubeDefaultClientVersion
	}
	return youtubePlaylistConfig(config)
}

type youtubePlaylistPage struct {
	entries      []Entry
	continuation string
	title        string
	description  string
	alert        string
}

func parseYouTubePlaylistData(data []byte) (youtubePlaylistPage, error) {
	var root value.Value
	if err := json.Unmarshal(data, &root); err != nil {
		return youtubePlaylistPage{}, fmt.Errorf("%w: decode YouTube playlist data", ErrInvalidMetadata)
	}
	if _, ok := root.Object(); !ok {
		return youtubePlaylistPage{}, fmt.Errorf("%w: YouTube playlist root", ErrInvalidMetadata)
	}
	var page youtubePlaylistPage
	appendEntry := func(entry Entry, ok bool) {
		if !ok {
			return
		}
		page.entries = append(page.entries, entry)
	}
	nodes := 0
	err := walkOrderedJSON(root, 0, &nodes, func(key string, object *value.Object) {
		switch key {
		case "playlistVideoRenderer", "playlistPanelVideoRenderer":
			appendEntry(youtubePlaylistEntry(object))
		case "lockupViewModel":
			appendEntry(youtubePlaylistLockupEntry(object))
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
		case "playlistMetadataRenderer":
			if page.title == "" {
				page.title = objectString(object, "title")
				page.description = objectString(object, "description")
			}
		case "playlistHeaderRenderer":
			if page.title == "" {
				page.title = rendererText(object.Lookup("title"))
				page.description = rendererText(object.Lookup("descriptionText"))
			}
		case "alertRenderer":
			if page.alert == "" {
				page.alert = rendererText(object.Lookup("text"))
			}
		}
	})
	if err != nil {
		return youtubePlaylistPage{}, err
	}
	return page, nil
}

func youtubeContinuationViewModelToken(viewModel *value.Object) string {
	command, ok := viewModel.Lookup("continuationCommand").Object()
	if !ok {
		return ""
	}
	innertube, ok := command.Lookup("innertubeCommand").Object()
	if !ok {
		return ""
	}
	if token := validYouTubeContinuationToken(objectString(innertube, "continuationCommand", "token")); token != "" {
		return token
	}
	executor, ok := innertube.Lookup("commandExecutorCommand").Object()
	if !ok {
		return ""
	}
	commands, ok := executor.Lookup("commands").ListValue()
	if !ok || len(commands) > youtubeMaxContinuationCommands {
		return ""
	}
	for _, item := range commands {
		if candidate, ok := item.Object(); ok {
			if token := validYouTubeContinuationToken(objectString(candidate, "continuationCommand", "token")); token != "" {
				return token
			}
		}
	}
	return ""
}

func validYouTubeContinuationToken(token string) string {
	if token == "" || len(token) > youtubeMaxContinuationBytes || strings.ContainsAny(token, "\x00\r\n") {
		return ""
	}
	return token
}

func youtubePlaylistLockupEntry(viewModel *value.Object) (Entry, bool) {
	if objectString(viewModel, "contentType") != "LOCKUP_CONTENT_TYPE_VIDEO" {
		return Entry{}, false
	}
	videoID := objectString(viewModel, "contentId")
	if !youtubeIDPattern.MatchString(videoID) {
		return Entry{}, false
	}
	title := objectString(viewModel, "metadata", "lockupMetadataViewModel", "title", "content")
	return Entry{
		URL: "https://www.youtube.com/watch?v=" + videoID, ExtractorKey: "youtube",
		ID: videoID, Title: title,
	}, true
}

func youtubePlaylistEntry(renderer *value.Object) (Entry, bool) {
	videoID := objectString(renderer, "videoId")
	if !youtubeIDPattern.MatchString(videoID) {
		return Entry{}, false
	}
	return Entry{
		URL: "https://www.youtube.com/watch?v=" + videoID, ExtractorKey: "youtube",
		ID: videoID, Title: rendererText(renderer.Lookup("title")),
	}, true
}

func walkOrderedJSON(item value.Value, depth int, nodes *int, visit func(string, *value.Object)) error {
	*nodes++
	if depth > youtubeMaxJSONDepth || *nodes > youtubeMaxJSONNodes {
		return fmt.Errorf("%w: YouTube playlist data exceeds traversal limit", ErrInvalidMetadata)
	}
	if object, ok := item.Object(); ok {
		for _, field := range object.Fields() {
			if child, ok := field.Value.Object(); ok {
				visit(field.Key, child)
			}
			if err := walkOrderedJSON(field.Value, depth+1, nodes, visit); err != nil {
				return err
			}
		}
		return nil
	}
	if list, ok := item.ListValue(); ok {
		for _, child := range list {
			if err := walkOrderedJSON(child, depth+1, nodes, visit); err != nil {
				return err
			}
		}
	}
	return nil
}

func objectString(object *value.Object, path ...string) string {
	if object == nil || len(path) == 0 {
		return ""
	}
	current := value.ObjectValue(object)
	for _, key := range path {
		nested, ok := current.Object()
		if !ok {
			return ""
		}
		current = nested.Lookup(key)
	}
	result, _ := current.StringValue()
	return result
}

func rendererText(item value.Value) string {
	object, ok := item.Object()
	if !ok {
		return ""
	}
	if text := objectString(object, "simpleText"); text != "" {
		return text
	}
	runs, _ := object.Lookup("runs").ListValue()
	var result strings.Builder
	for _, run := range runs {
		if runObject, ok := run.Object(); ok {
			result.WriteString(objectString(runObject, "text"))
		}
	}
	return result.String()
}

func youtubePlaylistAlertError(alert string) error {
	lower := strings.ToLower(alert)
	if strings.Contains(lower, "private") || strings.Contains(lower, "sign in") || strings.Contains(lower, "login") {
		return fmt.Errorf("%w: playlist access denied", ErrAuthentication)
	}
	return fmt.Errorf("%w: playlist unavailable", ErrUnavailable)
}
