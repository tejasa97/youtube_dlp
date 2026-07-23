package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type youtubeAuthFixtureTransport struct {
	cookies   []*http.Cookie
	cookieErr error
	response  *http.Response
	err       error
	request   *http.Request
}

func (transport *youtubeAuthFixtureTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	return transport.DoNoRedirect(context.Background(), request)
}

func (transport *youtubeAuthFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("not used")
}

func (transport *youtubeAuthFixtureTransport) Cookies(string) ([]*http.Cookie, error) {
	return transport.cookies, transport.cookieErr
}

func (transport *youtubeAuthFixtureTransport) DoNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.request = request.Clone(request.Context())
	if transport.err != nil {
		return nil, transport.err
	}
	if transport.response == nil {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"dQw4w9WgXcQ"}}`)), Header: make(http.Header)}, nil
	}
	response := *transport.response
	if response.Body == nil {
		response.Body = io.NopCloser(bytes.NewReader(nil))
	}
	return &response, nil
}

func youtubeAuthCookies() []*http.Cookie {
	return []*http.Cookie{{Name: "LOGIN_INFO", Value: "logged-in"}, {Name: "SAPISID", Value: "sapi"}, {Name: "__Secure-1PAPISID", Value: "one"}, {Name: "__Secure-3PAPISID", Value: "three"}}
}

func youtubeAuthConfig() youtubeWEBAuthConfig {
	return youtubeWEBAuthConfig{ClientName: "WEB", ClientID: "1", ClientVersion: "2.20250701.00.00", VisitorData: "visitor", UserAgent: "Mozilla/5.0", DelegatedSessionID: "page-id", UserSessionID: "user-session", LoggedIn: true}
}

func TestYouTubeSIDAuthorizationExact(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	authorization, err := youtubeSIDAuthorization(youtubeAuthCookies(), "user-session", now)
	if err != nil {
		t.Fatal(err)
	}
	const want = "SAPISIDHASH 1700000000_ae5cae00e99095c972960bc0cfd02a7b5efe0f03_u SAPISID1PHASH 1700000000_6690bab64b6483233af4b11f1f833451b6b01296_u SAPISID3PHASH 1700000000_128673da7f556980dac382a8f21b0514473af17d_u"
	if authorization != want {
		t.Fatalf("authorization = %q, want %q", authorization, want)
	}
}

func TestYouTubeSIDAuthorizationFallbackAndFailures(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cookies := []*http.Cookie{{Name: "LOGIN_INFO", Value: "logged"}, {Name: "__Secure-3PAPISID", Value: "three"}}
	got, err := youtubeSIDAuthorization(cookies, "", now)
	if err != nil {
		t.Fatal(err)
	}
	const want = "SAPISIDHASH 1700000000_7f5cf0ef7325a890a89cc0dc432ce24c7bae42dc SAPISID3PHASH 1700000000_7f5cf0ef7325a890a89cc0dc432ce24c7bae42dc"
	if got != want {
		t.Fatalf("fallback authorization = %q, want %q", got, want)
	}
	for _, cookies := range [][]*http.Cookie{nil, {{Name: "SAPISID", Value: "sapi"}}, {{Name: "LOGIN_INFO", Value: "logged"}}, {{Name: "LOGIN_INFO", Value: "logged"}, {Name: "SAPISID", Value: "bad\rvalue"}}} {
		if _, err := youtubeSIDAuthorization(cookies, "", now); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("cookies %v: error = %v, want ErrAuthentication", cookies, err)
		}
	}
}

func TestYouTubeWEBAuthHeaders(t *testing.T) {
	config := youtubeAuthConfig()
	headers, err := youtubeWEBAuthHeaders(config, "SAPISIDHASH safe")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"Authorization": "SAPISIDHASH safe", "Origin": youtubeAuthOrigin, "X-Origin": youtubeAuthOrigin, "X-Goog-Pageid": "page-id", "X-Goog-Authuser": "0", "X-Youtube-Bootstrap-Logged-In": "true"} {
		if got := headers.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	config.DelegatedSessionID, config.SessionIndex, config.LoggedIn = "", "3", false
	headers, err = youtubeWEBAuthHeaders(config, "SAPISIDHASH safe")
	if err != nil || headers.Get("X-Goog-PageId") != "" || headers.Get("X-Goog-AuthUser") != "3" || headers.Get("X-Youtube-Bootstrap-Logged-In") != "" {
		t.Fatalf("headers = %#v, err = %v", headers, err)
	}
	config.SessionIndex = "x\r1"
	if _, err := youtubeWEBAuthHeaders(config, "safe"); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("bad config error = %v, want ErrAuthentication", err)
	}
	config = youtubeAuthConfig()
	config.ClientName = "ANDROID"
	if _, err := youtubeWEBAuthHeaders(config, "safe"); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("non-WEB config error = %v, want ErrAuthentication", err)
	}
	config = youtubeAuthConfig()
	config.UserAgent = ""
	if headers, err := youtubeWEBAuthHeaders(config, "safe"); err != nil || headers.Get("User-Agent") != "" {
		t.Fatalf("optional user-agent headers = %#v, error = %v", headers, err)
	}
}

func TestRequestAuthenticatedYouTubeWEBPlayer(t *testing.T) {
	transport := &youtubeAuthFixtureTransport{cookies: youtubeAuthCookies()}
	player, err := requestAuthenticatedYouTubeWEBPlayer(context.Background(), transport, "dQw4w9WgXcQ", youtubeAuthConfig(), func() time.Time { return time.Unix(1_700_000_000, 0) })
	if err != nil {
		t.Fatal(err)
	}
	if player.VideoDetails.VideoID != "dQw4w9WgXcQ" || transport.request == nil {
		t.Fatalf("player = %#v request = %#v", player.VideoDetails, transport.request)
	}
	if transport.request.URL.String() != youtubeAuthenticatedWEBPlayerURL || transport.request.Method != http.MethodPost {
		t.Fatalf("request = %s %s", transport.request.Method, transport.request.URL)
	}
	if transport.request.Header.Get("Cookie") != "" || transport.request.Header.Get("Authorization") == "" || transport.request.Header.Get("Origin") != youtubeAuthOrigin || transport.request.Header.Get("X-Origin") != youtubeAuthOrigin {
		t.Fatalf("unsafe request headers: %#v", transport.request.Header)
	}
	var payload struct {
		Context struct {
			Client struct {
				ClientName       string `json:"clientName"`
				ClientVersion    string `json:"clientVersion"`
				HL               string `json:"hl"`
				TimeZone         string `json:"timeZone"`
				UTCOffsetMinutes int    `json:"utcOffsetMinutes"`
			} `json:"client"`
		} `json:"context"`
		VideoID         string `json:"videoId"`
		PlaybackContext struct {
			ContentPlaybackContext struct {
				HTML5Preference string `json:"html5Preference"`
			} `json:"contentPlaybackContext"`
		} `json:"playbackContext"`
		ContentCheckOK bool `json:"contentCheckOk"`
		RacyCheckOK    bool `json:"racyCheckOk"`
	}
	if err := json.NewDecoder(transport.request.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.VideoID != "dQw4w9WgXcQ" || !payload.ContentCheckOK || !payload.RacyCheckOK || payload.Context.Client.ClientName != "WEB" || payload.Context.Client.ClientVersion != "2.20250701.00.00" || payload.Context.Client.HL != "en" || payload.Context.Client.TimeZone != "UTC" || payload.Context.Client.UTCOffsetMinutes != 0 || payload.PlaybackContext.ContentPlaybackContext.HTML5Preference != "HTML5_PREF_WANTS" {
		t.Fatalf("unexpected player payload: %#v", payload)
	}
}

func TestRequestAuthenticatedYouTubeWEBPlayerFailuresAreCategorizedAndSecretFree(t *testing.T) {
	newTransport := func(status int, body string) *youtubeAuthFixtureTransport {
		return &youtubeAuthFixtureTransport{cookies: youtubeAuthCookies(), response: &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header)}}
	}
	clock := func() time.Time { return time.Unix(1_700_000_000, 0) }
	for _, test := range []struct {
		name      string
		transport *youtubeAuthFixtureTransport
		config    youtubeWEBAuthConfig
		body      string
		want      error
	}{
		{"missing cookies", &youtubeAuthFixtureTransport{}, youtubeAuthConfig(), "", ErrAuthentication},
		{"redirect", newTransport(http.StatusFound, ""), youtubeAuthConfig(), "", ErrAuthentication},
		{"unauthorized", newTransport(http.StatusUnauthorized, ""), youtubeAuthConfig(), "", ErrAuthentication},
		{"forbidden", newTransport(http.StatusForbidden, ""), youtubeAuthConfig(), "", ErrAuthentication},
		{"malformed", newTransport(http.StatusOK, "{"), youtubeAuthConfig(), "", ErrAuthentication},
		{"missing video id", newTransport(http.StatusOK, `{}`), youtubeAuthConfig(), "", ErrAuthentication},
		{"mismatch", newTransport(http.StatusOK, `{"videoDetails":{"videoId":"aaaaaaaaaaa"}}`), youtubeAuthConfig(), "", ErrAuthentication},
		{"missing playability", newTransport(http.StatusOK, `{"videoDetails":{"videoId":"dQw4w9WgXcQ"}}`), youtubeAuthConfig(), "", ErrAuthentication},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := requestAuthenticatedYouTubeWEBPlayer(context.Background(), test.transport, "dQw4w9WgXcQ", test.config, clock)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && (bytes.Contains([]byte(err.Error()), []byte("sapi")) || bytes.Contains([]byte(err.Error()), []byte("logged-in"))) {
				t.Fatalf("secret leaked in error: %v", err)
			}
		})
	}
	t.Run("oversized", func(t *testing.T) {
		transport := newTransport(http.StatusOK, strings.Repeat(" ", int(maxExtractorJSONBytes)+1))
		_, err := requestAuthenticatedYouTubeWEBPlayer(context.Background(), transport, "dQw4w9WgXcQ", youtubeAuthConfig(), clock)
		if !errors.Is(err, ErrAuthentication) {
			t.Fatalf("error = %v, want ErrAuthentication", err)
		}
	})
}

func TestRequestAuthenticatedYouTubeWEBPlayerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := requestAuthenticatedYouTubeWEBPlayer(ctx, &youtubeAuthFixtureTransport{cookies: youtubeAuthCookies(), err: context.Canceled}, "dQw4w9WgXcQ", youtubeAuthConfig(), time.Now)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRequestAuthenticatedYouTubeWEBNext(t *testing.T) {
	transport := &youtubeAuthFixtureTransport{cookies: youtubeAuthCookies(), response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"responseContext":{"visitorData":"visitor"},"contents":{}}`)),
	}}
	var response struct {
		ResponseContext struct {
			VisitorData string `json:"visitorData"`
		} `json:"responseContext"`
		Contents map[string]any `json:"contents"`
	}
	body := []byte(`{"continuation":"continuation-token","context":{"caller":"comments"}}`)
	err := requestAuthenticatedYouTubeWEBNext(context.Background(), transport, youtubeAuthenticatedWEBNextURL+"&key=web-api-key", body, youtubeAuthConfig(), func() time.Time { return time.Unix(1_700_000_000, 0) }, &response)
	if err != nil {
		t.Fatal(err)
	}
	if transport.request == nil || transport.request.Method != http.MethodPost || transport.request.URL.String() != youtubeAuthenticatedWEBNextURL+"&key=web-api-key" {
		t.Fatalf("request = %#v", transport.request)
	}
	for key, want := range map[string]string{
		"Content-Type":                  "application/json",
		"Origin":                        youtubeAuthOrigin,
		"X-Origin":                      youtubeAuthOrigin,
		"X-Youtube-Client-Name":         "1",
		"X-Youtube-Client-Version":      "2.20250701.00.00",
		"X-Youtube-Bootstrap-Logged-In": "true",
	} {
		if got := transport.request.Header.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if transport.request.Header.Get("Authorization") == "" || transport.request.Header.Get("Cookie") != "" {
		t.Fatalf("unsafe auth headers: %#v", transport.request.Header)
	}
	var gotBody map[string]any
	if err := json.NewDecoder(transport.request.Body).Decode(&gotBody); err != nil {
		t.Fatal(err)
	}
	if gotBody["continuation"] != "continuation-token" || response.ResponseContext.VisitorData != "visitor" || response.Contents == nil {
		t.Fatalf("body = %#v, response = %#v", gotBody, response)
	}
}

func TestValidYouTubeWEBNextEndpoint(t *testing.T) {
	valid := []string{
		youtubeAuthenticatedWEBNextURL,
		youtubeAuthenticatedWEBNextURL + "&key=key",
		"https://www.youtube.com/youtubei/v1/next?key=key&prettyPrint=false",
	}
	for _, endpoint := range valid {
		if !validYouTubeWEBNextEndpoint(endpoint) {
			t.Errorf("endpoint rejected: %q", endpoint)
		}
	}
	invalid := []string{
		"http://www.youtube.com/youtubei/v1/next?prettyPrint=false",
		"https://youtube.com/youtubei/v1/next?prettyPrint=false",
		"https://www.youtube.com:443/youtubei/v1/next?prettyPrint=false",
		"https://user@www.youtube.com/youtubei/v1/next?prettyPrint=false",
		"https://www.youtube.com/youtubei/v1/next?prettyPrint=false#fragment",
		"https://www.youtube.com/youtubei/v1/%6eext?prettyPrint=false",
		"https://www.youtube.com/youtubei/v1/player?prettyPrint=false",
		"https://www.youtube.com/youtubei/v1/next",
		"https://www.youtube.com/youtubei/v1/next?prettyPrint=true",
		"https://www.youtube.com/youtubei/v1/next?prettyPrint=false&prettyPrint=false",
		"https://www.youtube.com/youtubei/v1/next?prettyPrint=false&key=",
		"https://www.youtube.com/youtubei/v1/next?prettyPrint=false&other=value",
		"https://www.youtube.com/youtubei/v1/next?prettyPrint=false&key=one&key=two",
	}
	for _, endpoint := range invalid {
		if validYouTubeWEBNextEndpoint(endpoint) {
			t.Errorf("endpoint accepted: %q", endpoint)
		}
	}
	if validYouTubeWEBAPIKey(strings.Repeat("k", 513)) || validYouTubeWEBAPIKey("bad\rkey") {
		t.Fatal("unsafe API key accepted")
	}
}

func TestRequestAuthenticatedYouTubeWEBNextErrors(t *testing.T) {
	clock := func() time.Time { return time.Unix(1_700_000_000, 0) }
	newTransport := func(status int, body string) *youtubeAuthFixtureTransport {
		return &youtubeAuthFixtureTransport{cookies: youtubeAuthCookies(), response: &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}}
	}
	request := func(transport Transport, endpoint string, target any) error {
		return requestAuthenticatedYouTubeWEBNext(context.Background(), transport, endpoint, []byte(`{"continuation":"private-continuation"}`), youtubeAuthConfig(), clock, target)
	}
	for _, status := range []int{http.StatusFound, http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var result map[string]any
			if err := request(newTransport(status, ""), youtubeAuthenticatedWEBNextURL, &result); !errors.Is(err, ErrAuthentication) {
				t.Fatalf("error = %v, want ErrAuthentication", err)
			}
		})
	}
	for _, status := range []int{http.StatusRequestTimeout, http.StatusNotFound, http.StatusGone, http.StatusTooManyRequests, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var result map[string]any
			err := request(newTransport(status, ""), youtubeAuthenticatedWEBNextURL, &result)
			var want *HTTPStatusError
			if !errors.As(err, &want) || want.Code != status {
				t.Fatalf("error = %v, want HTTP status %d", err, status)
			}
		})
	}
	for _, test := range []struct {
		name      string
		transport Transport
		endpoint  string
		config    youtubeWEBAuthConfig
		body      string
		want      error
	}{
		{name: "missing authentication capability", transport: &youtubeAuthNoCapability{}, endpoint: youtubeAuthenticatedWEBNextURL, config: youtubeAuthConfig(), want: ErrAuthentication},
		{name: "missing cookies", transport: &youtubeAuthFixtureTransport{}, endpoint: youtubeAuthenticatedWEBNextURL, config: youtubeAuthConfig(), want: ErrAuthentication},
		{name: "invalid endpoint", transport: newTransport(http.StatusOK, `{}`), endpoint: "https://evil.invalid/youtubei/v1/next?prettyPrint=false", config: youtubeAuthConfig(), want: ErrAuthentication},
		{name: "invalid config", transport: newTransport(http.StatusOK, `{}`), endpoint: youtubeAuthenticatedWEBNextURL, config: youtubeWEBAuthConfig{}, want: ErrAuthentication},
		{name: "malformed", transport: newTransport(http.StatusOK, "{"), endpoint: youtubeAuthenticatedWEBNextURL, config: youtubeAuthConfig(), want: ErrInvalidMetadata},
		{name: "transport secret", transport: &youtubeAuthFixtureTransport{cookies: youtubeAuthCookies(), err: errors.New("sapi private-continuation web-api-key")}, endpoint: youtubeAuthenticatedWEBNextURL, config: youtubeAuthConfig(), want: ErrAuthentication},
	} {
		t.Run(test.name, func(t *testing.T) {
			var result map[string]any
			err := requestAuthenticatedYouTubeWEBNext(context.Background(), test.transport, test.endpoint, []byte(`{"continuation":"private-continuation"}`), test.config, clock, &result)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && (strings.Contains(err.Error(), "sapi") || strings.Contains(err.Error(), "private-continuation") || strings.Contains(err.Error(), "web-api-key")) {
				t.Fatalf("secret leaked in error: %v", err)
			}
		})
	}
	var oversized map[string]any
	err := request(newTransport(http.StatusOK, strings.Repeat(" ", int(maxExtractorJSONBytes)+1)), youtubeAuthenticatedWEBNextURL, &oversized)
	if !errors.Is(err, ErrJSONResponseTooLarge) {
		t.Fatalf("oversized error = %v, want ErrJSONResponseTooLarge", err)
	}
}

type youtubeAuthNoCapability struct{}

func (*youtubeAuthNoCapability) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("not used")
}
func (*youtubeAuthNoCapability) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("not used")
}

func TestRequestAuthenticatedYouTubeWEBNextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var target map[string]any
	err := requestAuthenticatedYouTubeWEBNext(ctx, &youtubeAuthFixtureTransport{cookies: youtubeAuthCookies(), err: context.Canceled}, youtubeAuthenticatedWEBNextURL, []byte(`{}`), youtubeAuthConfig(), time.Now, &target)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func FuzzValidYouTubeWEBNextEndpoint(f *testing.F) {
	f.Add(youtubeAuthenticatedWEBNextURL)
	f.Add(youtubeAuthenticatedWEBNextURL + "&key=key")
	f.Add("https://evil.invalid/youtubei/v1/next?prettyPrint=false")
	f.Fuzz(func(t *testing.T, endpoint string) {
		if validYouTubeWEBNextEndpoint(endpoint) {
			parsed, err := url.Parse(endpoint)
			if err != nil || parsed.Scheme != "https" || parsed.Host != "www.youtube.com" || parsed.Path != "/youtubei/v1/next" || parsed.User != nil || parsed.Fragment != "" {
				t.Fatalf("unsafe accepted endpoint %q", endpoint)
			}
		}
	})
}

func FuzzYouTubeWEBAuthConfig(f *testing.F) {
	f.Add("WEB", "1", "2.20250701.00.00", "page", "user", "0")
	f.Fuzz(func(t *testing.T, clientName, clientID, version, pageID, userID, sessionIndex string) {
		config := youtubeWEBAuthConfig{ClientName: clientName, ClientID: clientID, ClientVersion: version, UserAgent: "ua", DelegatedSessionID: pageID, UserSessionID: userID, SessionIndex: sessionIndex, LoggedIn: true}
		headers, err := youtubeWEBAuthHeaders(config, "SAPISIDHASH safe")
		if err == nil {
			for _, key := range []string{"Authorization", "Origin", "X-Origin"} {
				if headers.Get(key) == "" || strings.ContainsAny(headers.Get(key), "\r\n\x00") {
					t.Fatalf("unsafe %s header", key)
				}
			}
		}
	})
}
