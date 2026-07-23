package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
