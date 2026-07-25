package extractor

// Shared YouTube browse helpers: authenticated/anonymous continuations and
// playlist info assembly for channel, handle, alias, and channel-search routes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// youtubeBrowseAuth carries the exact-origin WEB SID boundary for browse and
// search continuations. Once set, anonymous fallback is refused.
type youtubeBrowseAuth struct {
	config *youtubeWEBAuthConfig
	apiKey string
	now    func() time.Time
}

func youtubeBrowseAuthFromPage(page []byte, transport Transport) *youtubeBrowseAuth {
	pageConfig := discoverYouTubePageConfig(page)
	auth := pageConfig.webAuthConfig("", "")
	if !auth.LoggedIn || !auth.valid() {
		return nil
	}
	// Only engage authenticated continuations when the transport can honor the
	// no-redirect cookie boundary. Otherwise remain anonymous.
	if _, ok := transport.(youtubeAuthenticatedTransport); !ok {
		return nil
	}
	apiKey := pageConfig.APIKey
	if !validYouTubeWEBAPIKey(apiKey) {
		apiKey = ""
	}
	cloned := auth
	return &youtubeBrowseAuth{config: &cloned, apiKey: apiKey, now: time.Now}
}

func youtubeRendererPlaylistInfo(id, title, webpageURL string, tabs []youtubeAdvertisedTab) value.Info {
	fields := []value.Field{
		{Key: "id", Value: value.String(id)},
		{Key: "title", Value: value.String(title)},
		{Key: "webpage_url", Value: value.String(webpageURL)},
	}
	if len(tabs) > 0 {
		tabValues := make([]value.Value, 0, len(tabs))
		for _, tab := range tabs {
			tabObject := value.NewObject(
				value.Field{Key: "id", Value: value.String(tab.ID)},
				value.Field{Key: "title", Value: value.String(tab.Title)},
			)
			if tab.URL != "" {
				tabObject.Set("url", value.String(tab.URL))
			}
			if tab.Count > 0 {
				tabObject.Set("approx_count", value.Int(int64(tab.Count)))
			}
			tabValues = append(tabValues, value.ObjectValue(tabObject))
		}
		fields = append(fields, value.Field{Key: "channel_tabs", Value: value.List(tabValues...)})
	}
	return value.NewInfo(value.NewObject(fields...))
}

func youtubeRendererAlertError(alert, subject string) error {
	lower := strings.ToLower(alert)
	if strings.Contains(lower, "private") || strings.Contains(lower, "sign in") ||
		strings.Contains(lower, "login") || strings.Contains(lower, "members only") ||
		strings.Contains(lower, "members-only") || strings.Contains(lower, "member only") ||
		strings.Contains(lower, "member-only") {
		return fmt.Errorf("%w: %s access denied", ErrAuthentication, subject)
	}
	return fmt.Errorf("%w: %s unavailable", ErrUnavailable, subject)
}

// fetchYouTubeBrowseContinuation posts a browse continuation, optionally under
// the authenticated WEB SID boundary. Visitor rotation is returned to the
// stateful entry iterator.
func fetchYouTubeBrowseContinuation(
	ctx context.Context,
	transport Transport,
	token, visitorData string,
	config youtubePlaylistConfig,
	policy youtubeRendererPolicy,
	subject string,
	categorize func(error) error,
	auth *youtubeBrowseAuth,
) ([]Entry, string, string, error) {
	if token = validYouTubeContinuationToken(token); token == "" {
		return nil, "", visitorData, fmt.Errorf("%w: invalid YouTube %s continuation", ErrInvalidPlaylist, subject)
	}
	version := config.ClientVersion
	if version == "" {
		version = youtubeDefaultClientVersion
	}
	if auth != nil {
		if auth.config == nil || !auth.config.LoggedIn {
			return nil, "", visitorData, ErrAuthentication
		}
		version = auth.config.ClientVersion
	}
	requestVisitor := visitorData
	if auth != nil && auth.config.VisitorData != "" {
		requestVisitor = auth.config.VisitorData
	}
	payload := map[string]any{
		"context": map[string]any{"client": map[string]any{
			"clientName": "WEB", "clientVersion": version, "hl": "en",
			"timeZone": "UTC", "utcOffsetMinutes": 0, "visitorData": requestVisitor,
		}},
		"continuation": token,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", visitorData, fmt.Errorf("%w: encode YouTube %s continuation", ErrInvalidMetadata, subject)
	}
	endpoint, _ := url.Parse(youtubePlaylistContinuationURL)
	query := endpoint.Query()
	query.Set("prettyPrint", "false")
	apiKey := config.APIKey
	if auth != nil && auth.apiKey != "" {
		apiKey = auth.apiKey
	}
	if apiKey != "" {
		query.Set("key", apiKey)
	}
	endpoint.RawQuery = query.Encode()

	var response json.RawMessage
	if auth != nil {
		authConfig := *auth.config
		authConfig.VisitorData = requestVisitor
		now := auth.now
		if now == nil {
			now = time.Now
		}
		if err := requestAuthenticatedYouTubeWEBBrowse(ctx, transport, endpoint.String(), body, authConfig, now, &response); err != nil {
			return nil, "", visitorData, categorizeAuthenticatedBrowseError(err, categorize)
		}
	} else {
		headers := make(http.Header)
		headers.Set("Content-Type", "application/json")
		headers.Set("Origin", "https://www.youtube.com")
		headers.Set("X-Youtube-Client-Name", "1")
		headers.Set("X-Youtube-Client-Version", version)
		if err := RequestJSON(ctx, transport, http.MethodPost, endpoint.String(), body, headers, &response); err != nil {
			return nil, "", visitorData, categorize(err)
		}
	}
	parsed, err := parseYouTubeRendererData(response, policy)
	if err != nil {
		return nil, "", visitorData, err
	}
	if parsed.alert != "" && len(parsed.entries) == 0 {
		return nil, "", visitorData, youtubeRendererAlertError(parsed.alert, subject)
	}
	if parsed.visitorData == "" {
		parsed.visitorData = visitorData
	}
	return parsed.entries, parsed.continuation, parsed.visitorData, nil
}

func categorizeAuthenticatedBrowseError(err error, fallback func(error) error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrAuthentication) || errors.Is(err, ErrUnavailable) ||
		errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) {
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
			if fallback != nil {
				return fallback(err)
			}
			return err
		}
	}
	// After authenticated state is engaged, do not fall back to anonymous.
	return ErrAuthentication
}

// requestAuthenticatedYouTubeWEBBrowse mirrors the /next helper for browse and
// search continuations under the same exact-origin SID rules.
func requestAuthenticatedYouTubeWEBBrowse(ctx context.Context, transport Transport, endpoint string, body []byte, config youtubeWEBAuthConfig, now func() time.Time, target any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !config.LoggedIn || now == nil || target == nil || !validYouTubeWEBBrowseOrSearchEndpoint(endpoint) {
		return ErrAuthentication
	}
	authTransport, ok := transport.(youtubeAuthenticatedTransport)
	if !ok {
		return ErrAuthentication
	}
	cookies, err := authTransport.Cookies(youtubeAuthOrigin)
	if err != nil {
		return ErrAuthentication
	}
	authorization, err := youtubeSIDAuthorization(cookies, config.UserSessionID, now())
	if err != nil {
		return err
	}
	headers, err := youtubeWEBAuthHeaders(config, authorization)
	if err != nil {
		return err
	}
	err = requestJSON(ctx, authTransport.DoNoRedirect, http.MethodPost, endpoint, body, headers, target)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		if (status.Code >= http.StatusMultipleChoices && status.Code < http.StatusBadRequest) ||
			status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden {
			return ErrAuthentication
		}
		return err
	}
	if errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) {
		return err
	}
	return ErrAuthentication
}

func validYouTubeWEBBrowseOrSearchEndpoint(rawEndpoint string) bool {
	endpoint, err := url.Parse(rawEndpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Fragment != "" || endpoint.RawPath != "" {
		return false
	}
	if endpoint.Host != "www.youtube.com" || endpoint.Hostname() != "www.youtube.com" || endpoint.Port() != "" {
		return false
	}
	switch endpoint.Path {
	case "/youtubei/v1/browse", "/youtubei/v1/search":
	default:
		return false
	}
	query, err := url.ParseQuery(endpoint.RawQuery)
	if err != nil || len(query) == 0 || len(query["prettyPrint"]) != 1 || query.Get("prettyPrint") != "false" {
		return false
	}
	if len(query) > 2 {
		return false
	}
	for name, values := range query {
		switch name {
		case "prettyPrint":
			if len(values) != 1 || values[0] != "false" {
				return false
			}
		case "key":
			if len(values) != 1 || !validYouTubeWEBAPIKey(values[0]) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func fetchYouTubeSearchContinuationAuth(
	ctx context.Context,
	transport Transport,
	token string,
	config youtubePlaylistConfig,
	auth *youtubeBrowseAuth,
) ([]Entry, string, error) {
	if token = validYouTubeContinuationToken(token); token == "" {
		return nil, "", fmt.Errorf("%w: invalid YouTube search continuation", ErrInvalidPlaylist)
	}
	version := config.ClientVersion
	if version == "" {
		version = youtubeDefaultClientVersion
	}
	visitorData := config.VisitorData
	if auth != nil {
		if auth.config == nil || !auth.config.LoggedIn {
			return nil, "", ErrAuthentication
		}
		version = auth.config.ClientVersion
		if auth.config.VisitorData != "" {
			visitorData = auth.config.VisitorData
		}
	}
	payload := map[string]any{
		"context": map[string]any{"client": map[string]any{
			"clientName": "WEB", "clientVersion": version, "hl": "en",
			"timeZone": "UTC", "utcOffsetMinutes": 0, "visitorData": visitorData,
		}},
		"continuation": token,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("%w: encode YouTube search continuation", ErrInvalidMetadata)
	}
	endpoint, _ := url.Parse(youtubeSearchAPIURL)
	values := endpoint.Query()
	values.Set("prettyPrint", "false")
	apiKey := config.APIKey
	if auth != nil && auth.apiKey != "" {
		apiKey = auth.apiKey
	}
	if apiKey != "" {
		values.Set("key", apiKey)
	}
	endpoint.RawQuery = values.Encode()
	var response json.RawMessage
	if auth != nil {
		authConfig := *auth.config
		authConfig.VisitorData = visitorData
		now := auth.now
		if now == nil {
			now = time.Now
		}
		if err := requestAuthenticatedYouTubeWEBBrowse(ctx, transport, endpoint.String(), body, authConfig, now, &response); err != nil {
			return nil, "", categorizeAuthenticatedBrowseError(err, categorizeYouTubeSearchError)
		}
	} else {
		headers := make(http.Header)
		headers.Set("Content-Type", "application/json")
		headers.Set("Origin", "https://www.youtube.com")
		headers.Set("X-Youtube-Client-Name", "1")
		headers.Set("X-Youtube-Client-Version", version)
		if err := RequestJSON(ctx, transport, http.MethodPost, endpoint.String(), body, headers, &response); err != nil {
			return nil, "", categorizeYouTubeSearchError(err)
		}
	}
	page, err := parseYouTubeRendererData(response, youtubeRendererPolicy{kinds: youtubeRendererSearchAll})
	if err != nil {
		return nil, "", err
	}
	if page.alert != "" && len(page.entries) == 0 {
		return nil, "", youtubeSearchAlertError(page.alert)
	}
	return page.entries, page.continuation, nil
}
