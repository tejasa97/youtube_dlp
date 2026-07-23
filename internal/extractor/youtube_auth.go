package extractor

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const youtubeAuthenticatedWEBPlayerURL = "https://www.youtube.com/youtubei/v1/player?prettyPrint=false"
const youtubeAuthenticatedWEBNextURL = "https://www.youtube.com/youtubei/v1/next?prettyPrint=false"
const youtubeAuthOrigin = "https://www.youtube.com"

// youtubeAuthenticatedTransport is deliberately narrower than Transport. An
// authenticated Innertube request must both read the operation cookie jar and
// refuse redirects, so credentials cannot be sent to another origin.
type youtubeAuthenticatedTransport interface {
	Transport
	Cookies(string) ([]*http.Cookie, error)
	DoNoRedirect(context.Context, *http.Request) (*http.Response, error)
}

// youtubeWEBAuthConfig contains only the bounded webpage configuration needed
// for a WEB player retry. Values originate in ytcfg; it is not a general
// Innertube client configuration surface.
type youtubeWEBAuthConfig struct {
	ClientName    string
	ClientID      string
	ClientVersion string
	VisitorData   string
	UserAgent     string

	DelegatedSessionID string
	UserSessionID      string
	SessionIndex       string
	LoggedIn           bool
}

func (config youtubeWEBAuthConfig) valid() bool {
	// Keep this recovery WEB-only rather than accepting a page-selected
	// arbitrary Innertube identity.
	if config.ClientName != "WEB" || config.ClientID != "1" || config.ClientVersion == "" {
		return false
	}
	for _, value := range []string{config.ClientName, config.ClientID, config.ClientVersion, config.VisitorData, config.UserAgent, config.DelegatedSessionID, config.UserSessionID, config.SessionIndex} {
		if !youtubeSafeHeaderValue(value) {
			return false
		}
	}
	if config.SessionIndex != "" {
		if _, err := strconv.ParseUint(config.SessionIndex, 10, 32); err != nil {
			return false
		}
	}
	return true
}

func youtubeSafeHeaderValue(value string) bool {
	return len(value) <= 512 && !strings.ContainsAny(value, "\r\n\x00")
}

// youtubeSIDAuthorization exactly follows yt-dlp's YouTube SID scheme. A
// LOGIN_INFO cookie and at least one SID are required before we claim an
// authenticated browser session.
func youtubeSIDAuthorization(cookies []*http.Cookie, userSessionID string, now time.Time) (string, error) {
	if !youtubeSafeHeaderValue(userSessionID) {
		return "", ErrAuthentication
	}
	values := make(map[string]string, 4)
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" {
			continue
		}
		if _, known := values[cookie.Name]; known {
			continue // cookiejar order defines the applicable cookie; retain it.
		}
		if !youtubeSafeHeaderValue(cookie.Value) {
			return "", ErrAuthentication
		}
		values[cookie.Name] = cookie.Value
	}
	if _, hasLoginInfo := values["LOGIN_INFO"]; !hasLoginInfo {
		return "", ErrAuthentication
	}
	sapisid := values["SAPISID"]
	threePSAPISID := values["__Secure-3PAPISID"]
	if sapisid == "" {
		sapisid = threePSAPISID
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	var authorizations []string
	for _, candidate := range []struct {
		scheme string
		sid    string
	}{
		{"SAPISIDHASH", sapisid},
		{"SAPISID1PHASH", values["__Secure-1PAPISID"]},
		{"SAPISID3PHASH", threePSAPISID},
	} {
		if candidate.sid == "" {
			continue
		}
		prefix := ""
		suffix := ""
		if userSessionID != "" {
			prefix = userSessionID + " "
			suffix = "_u"
		}
		sum := sha1.Sum([]byte(prefix + timestamp + " " + candidate.sid + " " + youtubeAuthOrigin))
		authorizations = append(authorizations, candidate.scheme+" "+timestamp+"_"+hex.EncodeToString(sum[:])+suffix)
	}
	if len(authorizations) == 0 {
		return "", ErrAuthentication
	}
	return strings.Join(authorizations, " "), nil
}

func youtubeWEBAuthHeaders(config youtubeWEBAuthConfig, authorization string) (http.Header, error) {
	if !config.valid() || authorization == "" || !youtubeSafeHeaderValue(authorization) {
		return nil, ErrAuthentication
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if config.UserAgent != "" {
		headers.Set("User-Agent", config.UserAgent)
	}
	headers.Set("X-Youtube-Client-Name", config.ClientID)
	headers.Set("X-Youtube-Client-Version", config.ClientVersion)
	headers.Set("Origin", youtubeAuthOrigin)
	headers.Set("X-Origin", youtubeAuthOrigin)
	headers.Set("Authorization", authorization)
	if config.VisitorData != "" {
		headers.Set("X-Goog-Visitor-Id", config.VisitorData)
	}
	if config.DelegatedSessionID != "" {
		headers.Set("X-Goog-PageId", config.DelegatedSessionID)
	}
	if config.SessionIndex != "" {
		headers.Set("X-Goog-AuthUser", config.SessionIndex)
	} else if config.DelegatedSessionID != "" {
		headers.Set("X-Goog-AuthUser", "0")
	}
	if config.LoggedIn {
		headers.Set("X-Youtube-Bootstrap-Logged-In", "true")
	}
	return headers, nil
}

// requestAuthenticatedYouTubeWEBPlayer issues exactly one authenticated WEB
// player request. It intentionally accepts no endpoint or caller headers.
func requestAuthenticatedYouTubeWEBPlayer(ctx context.Context, transport Transport, videoID string, config youtubeWEBAuthConfig, now func() time.Time) (youtubePlayerResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !youtubeIDPattern.MatchString(videoID) || !config.LoggedIn || now == nil {
		return youtubePlayerResponse{}, ErrAuthentication
	}
	authTransport, ok := transport.(youtubeAuthenticatedTransport)
	if !ok {
		return youtubePlayerResponse{}, ErrAuthentication
	}
	endpoint, err := url.Parse(youtubeAuthenticatedWEBPlayerURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host != "www.youtube.com" || endpoint.User != nil || endpoint.Port() != "" {
		return youtubePlayerResponse{}, ErrAuthentication
	}
	cookies, err := authTransport.Cookies(youtubeAuthOrigin)
	if err != nil {
		return youtubePlayerResponse{}, ErrAuthentication
	}
	authorization, err := youtubeSIDAuthorization(cookies, config.UserSessionID, now())
	if err != nil {
		return youtubePlayerResponse{}, ErrAuthentication
	}
	headers, err := youtubeWEBAuthHeaders(config, authorization)
	if err != nil {
		return youtubePlayerResponse{}, err
	}
	clientContext := map[string]any{
		"clientName": config.ClientName, "clientVersion": config.ClientVersion,
		"hl": "en", "timeZone": "UTC", "utcOffsetMinutes": 0,
	}
	if config.VisitorData != "" {
		clientContext["visitorData"] = config.VisitorData
	}
	payload, err := json.Marshal(map[string]any{
		"context": map[string]any{"client": clientContext},
		"videoId": videoID,
		"playbackContext": map[string]any{
			"contentPlaybackContext": map[string]any{"html5Preference": "HTML5_PREF_WANTS"},
		},
		"contentCheckOk": true,
		"racyCheckOk":    true,
	})
	if err != nil {
		return youtubePlayerResponse{}, fmt.Errorf("%w: encode authenticated player request", ErrAuthentication)
	}
	var player youtubePlayerResponse
	err = requestJSON(ctx, authTransport.DoNoRedirect, http.MethodPost, endpoint.String(), payload, headers, &player)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return youtubePlayerResponse{}, err
		}
		var status *HTTPStatusError
		if errors.As(err, &status) && (status.Code >= 300 && status.Code < 400 || status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden) {
			return youtubePlayerResponse{}, ErrAuthentication
		}
		if errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) {
			return youtubePlayerResponse{}, ErrAuthentication
		}
		return youtubePlayerResponse{}, fmt.Errorf("%w: authenticated player request failed", ErrAuthentication)
	}
	// A successful authenticated retry is useful only when it positively binds
	// the response to the requested video. Treat an absent ID as an auth
	// failure rather than allowing a partial/error response into format merge.
	if player.VideoDetails.VideoID == "" {
		return youtubePlayerResponse{}, ErrAuthentication
	}
	if player.VideoDetails.VideoID != videoID {
		return youtubePlayerResponse{}, ErrAuthentication
	}
	if player.PlayabilityStatus.Status == "" {
		return youtubePlayerResponse{}, ErrAuthentication
	}
	player.clientName = config.ClientName
	player.visitorData = config.VisitorData
	return player, nil
}

// requestAuthenticatedYouTubeWEBNext issues exactly one authenticated WEB
// continuation request. The endpoint is deliberately constrained to the
// YouTube /next API: comments may carry an optional webpage API key, but must
// not be allowed to redirect credentials or turn this into a general-purpose
// authenticated HTTP primitive.
func requestAuthenticatedYouTubeWEBNext(ctx context.Context, transport Transport, endpoint string, body []byte, config youtubeWEBAuthConfig, now func() time.Time, target any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !config.LoggedIn || now == nil || target == nil || !validYouTubeWEBNextEndpoint(endpoint) {
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
		return ErrAuthentication
	}
	headers, err := youtubeWEBAuthHeaders(config, authorization)
	if err != nil {
		return ErrAuthentication
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
		if (status.Code >= http.StatusMultipleChoices && status.Code < http.StatusBadRequest) || status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden {
			return ErrAuthentication
		}
		// Continuation retry/categorization needs the actual HTTP status for
		// availability, throttling, timeout, and server failures.
		return err
	}
	if errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) {
		return err
	}
	// Transport errors can contain request credentials in implementations or
	// upstream error text. Do not expose them through the extractor boundary.
	return ErrAuthentication
}

func validYouTubeWEBNextEndpoint(rawEndpoint string) bool {
	endpoint, err := url.Parse(rawEndpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host != "www.youtube.com" || endpoint.Hostname() != "www.youtube.com" || endpoint.Port() != "" || endpoint.User != nil || endpoint.Fragment != "" || endpoint.Path != "/youtubei/v1/next" || endpoint.RawPath != "" {
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

func validYouTubeWEBAPIKey(value string) bool {
	// API keys are request material, never headers. This is a resource and
	// control-character bound only; their format is controlled by YouTube.
	return value != "" && len(value) <= 512 && !strings.ContainsAny(value, "\r\n\x00")
}
