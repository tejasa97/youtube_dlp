package extractor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestYouTubeAnonymousClientProfilesAreExactAndIsolated(t *testing.T) {
	seen := make(map[string]struct{})
	for _, profile := range youtubeAnonymousFormatRecoveryClients {
		if !profile.valid() {
			t.Fatalf("invalid anonymous profile %#v", profile)
		}
		if profile.RequireAuth || profile.RequirePremium {
			t.Fatalf("anonymous profile %s must not require auth/premium", profile.Name)
		}
		if profile.ClientName == "WEB_REMIX" {
			t.Fatal("WEB_REMIX must not appear in anonymous video recovery")
		}
		key := profile.Name + "\x00" + profile.ClientName + "\x00" + profile.ClientID
		if _, ok := seen[key]; ok {
			t.Fatalf("duplicate anonymous profile %s", profile.Name)
		}
		seen[key] = struct{}{}
	}
	if len(youtubeAnonymousFormatRecoveryClients) > MaxYouTubeClientAttempts {
		t.Fatalf("anonymous profiles exceed attempt budget")
	}
}

func TestYouTubeAuthenticatedClientProfilesRequireExactAuthBoundary(t *testing.T) {
	for _, profile := range youtubeAuthenticatedFormatRecoveryClients {
		if !profile.valid() || !profile.RequireAuth || !profile.SupportsCookies {
			t.Fatalf("authenticated profile %#v", profile)
		}
		if profile.ClientName == "WEB_REMIX" || profile.ClientName == "ANDROID" {
			t.Fatalf("incompatible auth profile %s", profile.Name)
		}
		if profile.origin() != youtubeAuthOrigin {
			t.Fatalf("origin=%q", profile.origin())
		}
	}
}

func TestYouTubeClientRotationOrderIsDeterministic(t *testing.T) {
	wantAnon := []string{"android", "android_vr", "web_safari", "ios", "mweb"}
	if len(youtubeAnonymousFormatRecoveryClients) != len(wantAnon) {
		t.Fatalf("anon len=%d", len(youtubeAnonymousFormatRecoveryClients))
	}
	for i, name := range wantAnon {
		if youtubeAnonymousFormatRecoveryClients[i].Name != name {
			t.Fatalf("anon[%d]=%s want %s", i, youtubeAnonymousFormatRecoveryClients[i].Name, name)
		}
	}
	wantAuth := []string{"tv_downgraded", "web_creator"}
	for i, name := range wantAuth {
		if youtubeAuthenticatedFormatRecoveryClients[i].Name != name {
			t.Fatalf("auth[%d]=%s want %s", i, youtubeAuthenticatedFormatRecoveryClients[i].Name, name)
		}
	}
}

func TestYouTubeAnonymousRecoveryRotatesExactClientIdentities(t *testing.T) {
	formatBody := func(client string) []byte {
		return []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"fixture0001","title":"` + client + `"},"streamingData":{"formats":[{"itag":18,"url":"https://example.test/` + client + `","mimeType":"video/mp4"}]}}`)
	}
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{}},
		responses: map[string][]byte{
			"3":  []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
			"28": []byte(`{"playabilityStatus":{"status":"ERROR"}}`),
			"1":  formatBody("safari"),
			"5":  formatBody("ios"),
			"2":  formatBody("mweb"),
		},
	}
	recovered, err := recoverYouTubeFormats(context.Background(), transport, "fixture0001", "visitor", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 3 {
		t.Fatalf("recovered=%d", len(recovered))
	}
	var names []string
	for _, player := range recovered {
		names = append(names, player.clientName)
		if player.visitorData != "visitor" {
			t.Fatalf("visitor leaked/changed: %q", player.visitorData)
		}
	}
	if strings.Join(names, ",") != "WEB,IOS,MWEB" {
		t.Fatalf("names=%v", names)
	}
	for _, request := range transport.requests {
		if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" {
			t.Fatal("anonymous recovery leaked credentials")
		}
		if request.Header.Get("Origin") == "https://music.youtube.com" {
			t.Fatal("WEB_REMIX origin crossed into video recovery")
		}
	}
}

func TestYouTubeAnonymousRecoveryCancelsBetweenAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{},
		responses: map[string][]byte{
			"3": []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
		},
	}
	cancel()
	_, err := recoverYouTubeFormats(ctx, transport, "fixture0001", "visitor", "", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeAuthenticatedRecoveryFallsBackToTVWithoutAnonymous(t *testing.T) {
	webFail := []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"fixture"},"videoDetails":{"videoId":"dQw4w9WgXcQ"}}`)
	tvOK := []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"dQw4w9WgXcQ","title":"tv"},"streamingData":{"formats":[{"itag":18,"url":"https://example.test/tv","mimeType":"video/mp4"}]}}`)
	transport := &youtubeAuthRotationTransport{
		cookies: youtubeAuthCookies(),
		byClient: map[string][]byte{
			"1": webFail,
			"7": tvOK,
		},
	}
	page := []byte(`ytcfg.set({
		"INNERTUBE_API_KEY":"fixture-key",
		"INNERTUBE_CONTEXT_CLIENT_NAME":1,
		"INNERTUBE_CLIENT_VERSION":"2.fixture",
		"VISITOR_DATA":"auth-visitor",
		"LOGGED_IN":true,
		"INNERTUBE_CONTEXT":{"client":{"clientName":"WEB","clientVersion":"2.fixture","visitorData":"auth-visitor","userAgent":"fixture-agent"}},
		"DELEGATED_SESSION_ID":"page-id",
		"SESSION_INDEX":0,
		"USER_SESSION_ID":"user-session"
	});`)
	config := discoverYouTubePageConfig(page)
	recovered, err := recoverAuthenticatedYouTubeFormats(context.Background(), transport, "dQw4w9WgXcQ", config, "auth-visitor", "page-id||user-session", false, func() time.Time {
		return time.Unix(1_700_000_000, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].clientName != "TVHTML5" || recovered[0].clientID != 7 {
		t.Fatalf("recovered=%+v", recovered)
	}
	for i, request := range transport.requests {
		if request.Header.Get("Origin") != youtubeAuthOrigin || request.Header.Get("Authorization") == "" {
			t.Fatalf("auth headers missing: %v", request.Header)
		}
		if request.Header.Get("Cookie") != "" {
			t.Fatal("cookie header must stay jar-managed")
		}
		if i < len(transport.bodies) {
			body := string(transport.bodies[i])
			if strings.Contains(body, "WEB_REMIX") || strings.Contains(body, `"clientName":"ANDROID"`) {
				t.Fatalf("identity leakage in body: %s", body)
			}
		}
	}
	if transport.sawAnonymous {
		t.Fatal("authenticated recovery must not use anonymous cookie-isolated path")
	}
}

func TestYouTubeAuthenticatedRecoveryRejectsPremiumClientWithoutSignal(t *testing.T) {
	transport := &youtubeAuthRotationTransport{
		cookies: youtubeAuthCookies(),
		byClient: map[string][]byte{
			"1":  []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"},"videoDetails":{"videoId":"dQw4w9WgXcQ"}}`),
			"7":  []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"},"videoDetails":{"videoId":"dQw4w9WgXcQ"}}`),
			"62": []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"dQw4w9WgXcQ","title":"creator"},"streamingData":{"formats":[{"itag":18,"url":"https://example.test/c","mimeType":"video/mp4"}]}}`),
		},
	}
	page := []byte(`ytcfg.set({
		"INNERTUBE_CONTEXT_CLIENT_NAME":1,"INNERTUBE_CLIENT_VERSION":"2.fixture","VISITOR_DATA":"auth-visitor","LOGGED_IN":true,
		"INNERTUBE_CONTEXT":{"client":{"clientName":"WEB","clientVersion":"2.fixture","visitorData":"auth-visitor","userAgent":"fixture-agent"}},
		"DELEGATED_SESSION_ID":"page-id","SESSION_INDEX":0,"USER_SESSION_ID":"user-session"
	});`)
	_, err := recoverAuthenticatedYouTubeFormats(context.Background(), transport, "dQw4w9WgXcQ", discoverYouTubePageConfig(page), "auth-visitor", "page-id||user-session", false, func() time.Time {
		return time.Unix(1_700_000_000, 0)
	})
	if !errors.Is(err, ErrAuthentication) && !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
	for _, id := range transport.clientIDs {
		if id == "62" {
			t.Fatal("web_creator must not run without premium signal")
		}
	}
}

func TestYouTubePremiumSubscriberSignal(t *testing.T) {
	if youtubePremiumSubscriber(nil) {
		t.Fatal("nil")
	}
	if youtubePremiumSubscriber([]byte(`{"topbar":{}}`)) {
		t.Fatal("empty")
	}
	premium := []byte(`{"topbar":{"desktopTopbarRenderer":{"logo":{"topbarLogoRenderer":{"iconImage":{"iconType":"YOUTUBE_PREMIUM_LOGO"}}}}}}`)
	if !youtubePremiumSubscriber(premium) {
		t.Fatal("premium logo")
	}
	tooltip := []byte(`{"topbar":{"desktopTopbarRenderer":{"logo":{"topbarLogoRenderer":{"tooltipText":{"simpleText":"YouTube Premium"}}}}}}`)
	if !youtubePremiumSubscriber(tooltip) {
		t.Fatal("premium tooltip")
	}
}

func TestYouTubeRejectsWEBRemixProfileInVideoRecovery(t *testing.T) {
	profile := youtubeClientProfile{
		Name: "music", ClientName: "WEB_REMIX", ClientID: "67",
		ClientVersion: "1.0", UserAgent: "ua",
	}
	if profile.valid() {
		t.Fatal("WEB_REMIX must be invalid for video recovery profiles")
	}
}

func TestYouTubeAuthenticatedPlayerRejectsRemixOrigin(t *testing.T) {
	profile := youtubeAuthenticatedFormatRecoveryClients[0]
	profile.Origin = "https://music.youtube.com"
	_, err := requestAuthenticatedYouTubePlayer(context.Background(), &youtubeAuthRotationTransport{cookies: youtubeAuthCookies()}, "dQw4w9WgXcQ", profile, youtubeAuthSession{LoggedIn: true, UserSessionID: "user-session"}, time.Now)
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("err=%v", err)
	}
}

type youtubeAuthRotationTransport struct {
	cookies      []*http.Cookie
	byClient     map[string][]byte
	requests     []*http.Request
	bodies       [][]byte
	clientIDs    []string
	sawAnonymous bool
	memoryTransport
}

func (transport *youtubeAuthRotationTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("authenticated recovery must use the no-redirect transport")
}

func (transport *youtubeAuthRotationTransport) DoWithoutCookies(context.Context, *http.Request) (*http.Response, error) {
	transport.sawAnonymous = true
	return nil, errors.New("authenticated recovery must not use anonymous isolation")
}

func (transport *youtubeAuthRotationTransport) Cookies(rawURL string) ([]*http.Cookie, error) {
	if rawURL != youtubeAuthOrigin {
		return nil, fmt.Errorf("unexpected cookie scope %q", rawURL)
	}
	return append([]*http.Cookie(nil), transport.cookies...), nil
}

func (transport *youtubeAuthRotationTransport) DoNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	cloned := request.Clone(request.Context())
	transport.requests = append(transport.requests, cloned)
	transport.bodies = append(transport.bodies, body)
	clientID := request.Header.Get("X-Youtube-Client-Name")
	transport.clientIDs = append(transport.clientIDs, clientID)
	response, ok := transport.byClient[clientID]
	if !ok {
		return nil, fmt.Errorf("unexpected authenticated client %q", clientID)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(response)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func TestYouTubeClientProfileJSONRoundTripRejectsMalformed(t *testing.T) {
	_, err := requestYouTubePlayer(context.Background(), &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{},
		responses:       map[string][]byte{"3": []byte(`{`)},
	}, "fixture0001", "visitor", "", youtubeAnonymousFormatRecoveryClients[0], nil)
	if err == nil {
		t.Fatal("expected malformed rejection")
	}
	if strings.Contains(err.Error(), "visitor") && strings.Contains(err.Error(), "Authorization") {
		t.Fatalf("secretful error: %v", err)
	}
}

func TestYouTubeContradictoryPlayerVideoIDRejected(t *testing.T) {
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{},
		responses: map[string][]byte{
			"3": []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"other000001","title":"x"},"streamingData":{"formats":[{"itag":18,"url":"https://example.test/x","mimeType":"video/mp4"}]}}`),
		},
	}
	_, err := recoverYouTubeFormats(context.Background(), transport, "fixture0001", "visitor", "", nil)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeAuthSessionRejectsIncompleteLoggedOut(t *testing.T) {
	session := youtubeAuthSession{LoggedIn: false, UserSessionID: "x"}
	if session.valid() {
		t.Fatal("logged out session must be invalid")
	}
}
