package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/youtubepot"
)

func TestYouTubeAnonymousClientProfilesAreExactAndIsolated(t *testing.T) {
	seen := make(map[string]struct{})
	for _, profile := range youtubeAnonymousFormatRecoveryClients {
		if !profile.valid() {
			t.Fatalf("invalid anonymous profile %#v", profile)
		}
		if profile.RequireAuth {
			t.Fatalf("anonymous profile %s must not require auth", profile.Name)
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
	for _, premium := range []bool{false, true} {
		for _, ageGated := range []bool{false, true} {
			for _, profile := range youtubeAuthenticatedFormatRecoveryClients(premium, ageGated) {
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
	}
	if youtubeWebCreatorClient.GVSPolicy.Required != true || !youtubeWebCreatorClient.GVSPolicy.NotRequiredForPremium {
		t.Fatal("web_creator must require GVS with premium exception only")
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
	wantAuth := []string{"tv_downgraded", "web_safari"}
	gotAuth := youtubeAuthenticatedFormatRecoveryClients(false, false)
	if len(gotAuth) != len(wantAuth) {
		t.Fatalf("auth len=%d", len(gotAuth))
	}
	for i, name := range wantAuth {
		if gotAuth[i].Name != name {
			t.Fatalf("auth[%d]=%s want %s", i, gotAuth[i].Name, name)
		}
	}
	wantAge := []string{"tv_downgraded", "web_safari", "web_creator"}
	gotAge := youtubeAuthenticatedFormatRecoveryClients(false, true)
	if len(gotAge) != len(wantAge) {
		t.Fatalf("age auth len=%d", len(gotAge))
	}
	for i, name := range wantAge {
		if gotAge[i].Name != name {
			t.Fatalf("age auth[%d]=%s want %s", i, gotAge[i].Name, name)
		}
	}
	wantPremium := []string{"tv_downgraded", "web_creator"}
	gotPremium := youtubeAuthenticatedFormatRecoveryClients(true, false)
	if len(gotPremium) != len(wantPremium) {
		t.Fatalf("premium len=%d", len(gotPremium))
	}
	for i, name := range wantPremium {
		if gotPremium[i].Name != name {
			t.Fatalf("premium[%d]=%s want %s", i, gotPremium[i].Name, name)
		}
	}
	// Premium ignores ageGated for the default premium client set.
	gotPremiumAge := youtubeAuthenticatedFormatRecoveryClients(true, true)
	if len(gotPremiumAge) != len(wantPremium) {
		t.Fatalf("premium+age must stay exact premium defaults, got %d", len(gotPremiumAge))
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

func youtubeAuthRecoveryPage() []byte {
	return []byte(`ytcfg.set({
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
}

func TestYouTubeAuthenticatedRecoveryFallsBackToTVWithoutAnonymous(t *testing.T) {
	webFail := []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"fixture"},"videoDetails":{"videoId":"fixture0001"}}`)
	tvOK := []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"fixture0001","title":"tv"},"streamingData":{"formats":[{"itag":18,"url":"https://example.test/tv","mimeType":"video/mp4"}]}}`)
	transport := &youtubeAuthRotationTransport{
		cookies: youtubeAuthCookies(),
		byClient: map[string][]byte{
			"1": webFail,
			"7": tvOK,
		},
	}
	config := discoverYouTubePageConfig(youtubeAuthRecoveryPage())
	recovered, err := recoverAuthenticatedYouTubeFormats(context.Background(), transport, "fixture0001", config, "auth-visitor", "page-id||user-session", false, false, nil, func() time.Time {
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

func TestYouTubeAuthenticatedNonPremiumOmitsWebCreatorWithoutAgeGate(t *testing.T) {
	fail := []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"Sign in"},"videoDetails":{"videoId":"fixture0001"}}`)
	safariOK := []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"fixture0001","title":"safari"},"streamingData":{"formats":[{"itag":18,"url":"https://example.test/s","mimeType":"video/mp4"}]}}`)
	seq := &youtubeAuthSequenceTransport{
		cookies: youtubeAuthCookies(),
		responses: []youtubeAuthSequencedResponse{
			{clientID: "1", body: fail},
			{clientID: "7", body: fail},
			{clientID: "1", body: safariOK},
		},
	}
	recovered, err := recoverAuthenticatedYouTubeFormats(context.Background(), seq, "fixture0001", discoverYouTubePageConfig(youtubeAuthRecoveryPage()), "auth-visitor", "page-id||user-session", false, false, nil, func() time.Time {
		return time.Unix(1_700_000_000, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].clientName != "WEB" || recovered[0].clientID != 1 {
		t.Fatalf("recovered=%+v", recovered)
	}
	for _, id := range seq.clientIDs {
		if id == "62" {
			t.Fatal("web_creator must not run without attributable age-gate signal")
		}
	}
	if len(seq.clientIDs) != 3 || seq.clientIDs[0] != "1" || seq.clientIDs[1] != "7" || seq.clientIDs[2] != "1" {
		t.Fatalf("client order=%v want WEB,tv,web_safari", seq.clientIDs)
	}
}

func TestYouTubeAuthenticatedNonPremiumAllowsWebCreatorForAgeGate(t *testing.T) {
	ageFail := []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"Sign in to confirm your age"},"videoDetails":{"videoId":"fixture0001"}}`)
	creatorOK := []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"fixture0001","title":"creator"},"streamingData":{"formats":[{"itag":18,"url":"https://example.test/c","mimeType":"video/mp4"}]}}`)
	seq := &youtubeAuthSequenceTransport{
		cookies: youtubeAuthCookies(),
		responses: []youtubeAuthSequencedResponse{
			{clientID: "1", body: ageFail},
			{clientID: "7", body: ageFail},
			{clientID: "1", body: ageFail},
			{clientID: "62", body: creatorOK},
		},
	}
	recovered, err := recoverAuthenticatedYouTubeFormats(context.Background(), seq, "fixture0001", discoverYouTubePageConfig(youtubeAuthRecoveryPage()), "auth-visitor", "page-id||user-session", false, true, nil, func() time.Time {
		return time.Unix(1_700_000_000, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].clientName != "WEB_CREATOR" || recovered[0].clientID != 62 {
		t.Fatalf("recovered=%+v", recovered)
	}
	if len(seq.clientIDs) != 4 || seq.clientIDs[3] != "62" {
		t.Fatalf("client order=%v want WEB,tv,web_safari,web_creator", seq.clientIDs)
	}
}

func TestYouTubePlayabilityAgeGatedSignals(t *testing.T) {
	if youtubePlayabilityAgeGated(youtubePlayabilityStatus{Status: "LOGIN_REQUIRED", Reason: "Sign in"}) {
		t.Fatal("ordinary login must not be age-gated")
	}
	if !youtubePlayabilityAgeGated(youtubePlayabilityStatus{Status: "LOGIN_REQUIRED", Reason: "Sign in to confirm your age"}) {
		t.Fatal("confirm your age")
	}
	if !youtubePlayabilityAgeGated(youtubePlayabilityStatus{Status: "AGE_VERIFICATION_REQUIRED"}) {
		t.Fatal("age verification status")
	}
	if !youtubePlayabilityAgeGated(youtubePlayabilityStatus{DesktopLegacyAgeGateReason: json.RawMessage(`1`)}) {
		t.Fatal("desktop legacy age gate")
	}
}

func TestYouTubeTruthfulJSONMatchesPythonTruthyShapes(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"null", false},
		{"false", false},
		{"true", true},
		{"0", false},
		{"0.0", false},
		{"1", true},
		{`""`, false},
		{`"0"`, true},
		{`"false"`, true},
		{`"age"`, true},
		{"{}", false},
		{`{"x":1}`, true},
		{"[]", false},
		{"[0]", true},
		{"[null]", true},
		{"{", false},    // malformed
		{"[1,]", false}, // malformed
		{strings.Repeat("1", youtubeMaxTruthfulJSONBytes+1), false},
	}
	for _, test := range cases {
		got := youtubeTruthfulJSON(json.RawMessage(test.raw))
		if got != test.want {
			t.Fatalf("%q => %t want %t", test.raw, got, test.want)
		}
	}
}

func TestYouTubeAuthenticatedPremiumWebCreatorSkipsGVSToken(t *testing.T) {
	fail := []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"},"videoDetails":{"videoId":"fixture0001"}}`)
	creatorGVS := []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"fixture0001","title":"creator"},"streamingData":{"adaptiveFormats":[{"itag":137,"url":"https://example.test/v","mimeType":"video/mp4"}]}}`)
	director, err := youtubepot.New(youtubepot.Config{
		Policy: youtubepot.FetchAlways,
		Providers: []youtubepot.Provider{youtubepot.ProviderFunc{ProviderName: "reject", Function: func(context.Context, youtubepot.Request) (youtubepot.Response, error) {
			return youtubepot.Response{}, youtubepot.ErrRejected
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := &youtubeAuthRotationTransport{
		cookies: youtubeAuthCookies(),
		byClient: map[string][]byte{
			"1":  fail,
			"7":  fail,
			"62": creatorGVS,
		},
	}
	recovered, err := recoverAuthenticatedYouTubeFormats(context.Background(), transport, "fixture0001", discoverYouTubePageConfig(youtubeAuthRecoveryPage()), "auth-visitor", "page-id||user-session", true, false, director, func() time.Time {
		return time.Unix(1_700_000_000, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].clientName != "WEB_CREATOR" {
		t.Fatalf("recovered=%+v", recovered)
	}
	if len(transport.clientIDs) != 3 || transport.clientIDs[2] != "62" {
		t.Fatalf("premium order=%v want WEB,tv,web_creator", transport.clientIDs)
	}
}

func TestYouTubeAuthenticatedWebCreatorFailsClosedWithoutRequiredGVSToken(t *testing.T) {
	fail := []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"Sign in to confirm your age"},"videoDetails":{"videoId":"fixture0001"}}`)
	creatorGVS := []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"fixture0001","title":"creator"},"streamingData":{"adaptiveFormats":[{"itag":137,"url":"https://example.test/v","mimeType":"video/mp4"}]}}`)
	director, err := youtubepot.New(youtubepot.Config{
		Policy: youtubepot.FetchAlways,
		Providers: []youtubepot.Provider{youtubepot.ProviderFunc{ProviderName: "reject", Function: func(context.Context, youtubepot.Request) (youtubepot.Response, error) {
			return youtubepot.Response{}, youtubepot.ErrRejected
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := &youtubeAuthRotationTransport{
		cookies: youtubeAuthCookies(),
		byClient: map[string][]byte{
			"1":  fail,
			"7":  fail,
			"62": creatorGVS,
		},
	}
	_, err = recoverAuthenticatedYouTubeFormats(context.Background(), transport, "fixture0001", discoverYouTubePageConfig(youtubeAuthRecoveryPage()), "auth-visitor", "page-id||user-session", false, true, director, func() time.Time {
		return time.Unix(1_700_000_000, 0)
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "GVS PO token required for web_creator") {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeAuthenticatedRecoveryCancelsBetweenAttempts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transport := &youtubeAuthRotationTransport{
		cookies: youtubeAuthCookies(),
		byClient: map[string][]byte{
			"1": []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"},"videoDetails":{"videoId":"fixture0001"}}`),
		},
	}
	cancel()
	_, err := recoverAuthenticatedYouTubeFormats(ctx, transport, "fixture0001", discoverYouTubePageConfig(youtubeAuthRecoveryPage()), "auth-visitor", "page-id||user-session", false, false, nil, func() time.Time {
		return time.Unix(1_700_000_000, 0)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeAuthenticatedContradictoryIdentityRejected(t *testing.T) {
	fail := []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"},"videoDetails":{"videoId":"fixture0001"}}`)
	transport := &youtubeAuthRotationTransport{
		cookies: youtubeAuthCookies(),
		byClient: map[string][]byte{
			"1":  fail,
			"7":  []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"other0000001","title":"tv"},"streamingData":{"formats":[{"itag":18,"url":"https://example.test/tv","mimeType":"video/mp4"}]}}`),
			"62": fail,
		},
	}
	_, err := recoverAuthenticatedYouTubeFormats(context.Background(), transport, "fixture0001", discoverYouTubePageConfig(youtubeAuthRecoveryPage()), "auth-visitor", "page-id||user-session", false, false, nil, func() time.Time {
		return time.Unix(1_700_000_000, 0)
	})
	if !errors.Is(err, ErrAuthentication) && !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeAuthenticatedSelectedClientSABRMetadata(t *testing.T) {
	fail := []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"},"videoDetails":{"videoId":"fixture0001"}}`)
	tvOK := []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"fixture0001","title":"tv","lengthSeconds":"42"},"streamingData":{"formats":[{"itag":18,"url":"https://example.test/tv","mimeType":"video/mp4"}]}}`)
	transport := &youtubeAuthRotationTransport{
		cookies: youtubeAuthCookies(),
		byClient: map[string][]byte{
			"1": fail,
			"7": tvOK,
		},
	}
	recovered, err := recoverAuthenticatedYouTubeFormats(context.Background(), transport, "fixture0001", discoverYouTubePageConfig(youtubeAuthRecoveryPage()), "auth-visitor", "page-id||user-session", false, false, nil, func() time.Time {
		return time.Unix(1_700_000_000, 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].clientName != "TVHTML5" || recovered[0].clientID != 7 || recovered[0].clientVersion != "5.20260707" {
		t.Fatalf("recovered=%+v", recovered)
	}
	selected := recovered[0]
	selected.StreamingData.Formats = nil
	selected.StreamingData.ServerABRURL = "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=fixture"
	selected.StreamingData.AdaptiveFormats = []youtubeFormat{{
		Itag: 137, MimeType: `video/mp4; codecs="avc1.640028"`, Bitrate: 1000,
		Width: 1920, Height: 1080, LastModified: "1",
	}}
	selected.PlayerConfig.MediaCommonConfig.MediaUstreamerRequestConfig.VideoPlaybackUstreamerConfig = "dGVzdA=="
	values, err := buildYouTubeSABRFormats(context.Background(), []youtubePlayerResponse{selected}, "https://www.youtube.com/watch?v=fixture0001", "fixture0001", 42, true, "not_live")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) == 0 {
		t.Fatal("expected SABR formats from selected client")
	}
	object, _ := values[0].Object()
	if client, _ := object.Lookup("_youtube_client").StringValue(); client != "TVHTML5" {
		t.Fatalf("client=%q", client)
	}
	if id, _ := object.Lookup("_youtube_sabr_client_id").Int(); id != 7 {
		t.Fatalf("client_id=%d", id)
	}
	if ver, _ := object.Lookup("_youtube_sabr_client_version").StringValue(); ver != "5.20260707" {
		t.Fatalf("version=%q", ver)
	}
	if ua, _ := object.Lookup("_youtube_sabr_user_agent").StringValue(); ua != youtubeTVDowngradedClient.UserAgent {
		t.Fatalf("ua=%q", ua)
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
	profile := youtubeTVDowngradedClient
	profile.Origin = "https://music.youtube.com"
	_, err := requestAuthenticatedYouTubePlayer(context.Background(), &youtubeAuthRotationTransport{cookies: youtubeAuthCookies()}, "fixture0001", profile, youtubeAuthSession{LoggedIn: true, UserSessionID: "user-session"}, time.Now)
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

type youtubeAuthSequencedResponse struct {
	clientID string
	body     []byte
}

type youtubeAuthSequenceTransport struct {
	cookies      []*http.Cookie
	responses    []youtubeAuthSequencedResponse
	requests     []*http.Request
	bodies       [][]byte
	clientIDs    []string
	sawAnonymous bool
	next         int
	memoryTransport
}

func (transport *youtubeAuthSequenceTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("authenticated recovery must use the no-redirect transport")
}

func (transport *youtubeAuthSequenceTransport) DoWithoutCookies(context.Context, *http.Request) (*http.Response, error) {
	transport.sawAnonymous = true
	return nil, errors.New("authenticated recovery must not use anonymous isolation")
}

func (transport *youtubeAuthSequenceTransport) Cookies(rawURL string) ([]*http.Cookie, error) {
	if rawURL != youtubeAuthOrigin {
		return nil, fmt.Errorf("unexpected cookie scope %q", rawURL)
	}
	return append([]*http.Cookie(nil), transport.cookies...), nil
}

func (transport *youtubeAuthSequenceTransport) DoNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
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
	if transport.next >= len(transport.responses) {
		return nil, fmt.Errorf("unexpected authenticated client %q at step %d", clientID, transport.next)
	}
	expected := transport.responses[transport.next]
	transport.next++
	if expected.clientID != clientID {
		return nil, fmt.Errorf("client order mismatch: got %q want %q", clientID, expected.clientID)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(expected.body)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
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
