package extractor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

var microsoftFixtureRoot = filepath.Join("..", "..", "conformance", "extractors", "risk", "microsoft")

func loadMicrosoftFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(microsoftFixtureRoot, name))
	if err != nil {
		t.Fatalf("read microsoft fixture %s: %v", name, err)
	}
	return data
}

// --- microsoft test transport --------------------------------------------

type microsoftTransport struct {
	mu              sync.Mutex
	responsesBody   map[string][]byte
	responsesStatus map[string]int
	requests        []string
	cookieSeen      []string
	authSeen        []string
	proxySeen       []string
	refererSeen     []string
	isolation       atomic.Int32
	waitCh          chan struct{}
	requestCh       chan string
}

func newMicrosoftTransport() *microsoftTransport {
	return &microsoftTransport{
		responsesBody:   make(map[string][]byte),
		responsesStatus: make(map[string]int),
	}
}

func (t *microsoftTransport) set(method, rawURL string, status int, body []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.responsesBody[method+" "+rawURL] = body
	t.responsesStatus[method+" "+rawURL] = status
}

func (t *microsoftTransport) get(method, rawURL string) (int, []byte, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	body, ok := t.responsesBody[method+" "+rawURL]
	status, hasStatus := t.responsesStatus[method+" "+rawURL]
	if !hasStatus {
		status = http.StatusOK
	}
	return status, body, ok
}

func (t *microsoftTransport) trackHeaders(r *http.Request) {
	if r.Header.Get("Cookie") != "" {
		t.mu.Lock()
		t.cookieSeen = append(t.cookieSeen, r.Header.Get("Cookie"))
		t.mu.Unlock()
	}
	if r.Header.Get("Authorization") != "" {
		t.mu.Lock()
		t.authSeen = append(t.authSeen, r.Header.Get("Authorization"))
		t.mu.Unlock()
	}
	if r.Header.Get("Proxy-Authorization") != "" {
		t.mu.Lock()
		t.proxySeen = append(t.proxySeen, r.Header.Get("Proxy-Authorization"))
		t.mu.Unlock()
	}
	if r.Header.Get("Referer") != "" {
		t.mu.Lock()
		t.refererSeen = append(t.refererSeen, r.Header.Get("Referer"))
		t.mu.Unlock()
	}
}

func (t *microsoftTransport) Do(ctx context.Context, r *http.Request) (*http.Response, error) {
	t.trackHeaders(r)
	t.mu.Lock()
	requestKey := r.Method + " " + r.URL.String()
	t.requests = append(t.requests, requestKey)
	waitCh := t.waitCh
	requestCh := t.requestCh
	t.mu.Unlock()
	if requestCh != nil {
		select {
		case requestCh <- requestKey:
		default:
		}
	}
	if waitCh != nil {
		select {
		case <-waitCh:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return t.buildResponse(r.Method, r.URL.String())
}

func (t *microsoftTransport) buildResponse(method, rawURL string) (*http.Response, error) {
	status, body, ok := t.get(method, rawURL)
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(nil)),
		}, nil
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}, nil
}

func (t *microsoftTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	_, body, ok := t.get("GET", rawURL)
	if !ok {
		return nil, nil, &HTTPStatusError{Code: http.StatusNotFound}
	}
	return body, make(http.Header), nil
}

func (t *microsoftTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, r *http.Request) (*http.Response, error) {
	t.isolation.Add(1)
	t.trackHeaders(r)
	t.mu.Lock()
	requestKey := r.Method + " " + r.URL.String()
	t.requests = append(t.requests, requestKey)
	waitCh := t.waitCh
	requestCh := t.requestCh
	t.mu.Unlock()
	if requestCh != nil {
		select {
		case requestCh <- requestKey:
		default:
		}
	}
	if r.Response != nil && r.Response.StatusCode >= 300 && r.Response.StatusCode < 400 {
		return nil, fmt.Errorf("%w: redirected", ErrInvalidMetadata)
	}
	if waitCh != nil {
		select {
		case <-waitCh:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return t.buildResponse(r.Method, r.URL.String())
}

func (t *microsoftTransport) DoWithoutCookies(ctx context.Context, r *http.Request) (*http.Response, error) {
	return t.Do(ctx, r)
}

func (t *microsoftTransport) DoWithScopedAuthorizationNoRedirect(ctx context.Context, r *http.Request) (*http.Response, error) {
	return t.DoWithoutCredentialsNoRedirect(ctx, r)
}

func (t *microsoftTransport) DoWithScopedAuthenticationNoRedirect(ctx context.Context, r *http.Request) (*http.Response, error) {
	return t.DoWithoutCredentialsNoRedirect(ctx, r)
}

func (t *microsoftTransport) DoWithoutCredentialsNoRedirectWithReferer(ctx context.Context, r *http.Request) (*http.Response, error) {
	return t.DoWithoutCredentialsNoRedirect(ctx, r)
}

func (t *microsoftTransport) ReadPageProfileWithoutCredentialsNoRedirect(ctx context.Context, rawURL, profile string) ([]byte, http.Header, error) {
	return t.ReadPage(ctx, rawURL)
}

func (t *microsoftTransport) DoProfile(ctx context.Context, r *http.Request, profile string) (*http.Response, error) {
	return t.Do(ctx, r)
}

func (t *microsoftTransport) ReadPageProfile(ctx context.Context, rawURL, profile string) ([]byte, http.Header, error) {
	return t.ReadPage(ctx, rawURL)
}

func (t *microsoftTransport) DoProfiledNoRedirect(ctx context.Context, r *http.Request, profile string) (*http.Response, error) {
	return t.Do(ctx, r)
}

func (t *microsoftTransport) DoProfiledPageNoRedirect(ctx context.Context, r *http.Request, profile string) (*http.Response, error) {
	return t.Do(ctx, r)
}

// --- helpers ---------------------------------------------------------------

func mustList(t *testing.T, v value.Value) []value.Value {
	t.Helper()
	list, ok := v.ListValue()
	if !ok {
		t.Fatalf("not a list: %v", v)
	}
	return list
}

// --- Route matrix and precedence ------------------------------------------

func TestMicrosoftFamilyRouteMatrix(t *testing.T) {
	registry := NewRegistry(
		NewMicrosoftEmbed(),
		NewMicrosoftMedius(),
		NewMicrosoftLearnPlaylist(),
		NewMicrosoftLearnEpisode(),
		NewMicrosoftLearnSession(),
		NewMicrosoftBuild(),
	)
	cases := []struct {
		raw  string
		want string
	}{
		{"https://www.microsoft.com/en-us/videoplayer/embed/RWL07e", "microsoft_embed"},
		{"https://microsoft.com/videoplayer/embed/RWL07e", "microsoft_embed"},
		{"https://medius.microsoft.com/Embed/video-nc/9640d86c-f513-4889-959e-5dace86e7d2b", "microsoft_medius"},
		{"https://medius.microsoft.com/Embed/VideoDetails/78493569-9b3b-4a85-a409-ee76e789e25c", "microsoft_medius"},
		{"https://medius.microsoft.com/Embed/Video?id=0dc69bda-079b-4070-a7db-a8da1a06a9c7", "microsoft_medius"},
		{"https://learn.microsoft.com/en-us/shows/bash-for-beginners", "microsoft_learn_playlist"},
		{"https://learn.microsoft.com/events/build-2022", "microsoft_learn_playlist"},
		{"https://learn.microsoft.com/en-us/shows/bash-for-beginners/what-is-the-difference", "microsoft_learn_episode"},
		{"https://learn.microsoft.com/en-us/events/build-2022/ts01-rapidly", "microsoft_learn_session"},
		{"https://build.microsoft.com/en-US/sessions", "microsoft_build"},
		{"https://build.microsoft.com/en-US/sessions/b49feb31-afcd-4217-a538-d3ca1d171198", "microsoft_build"},
	}
	for _, test := range cases {
		selected, err := registry.Select(test.raw)
		if err != nil {
			t.Fatalf("Select(%q): %v", test.raw, err)
		}
		if selected.Name() != test.want {
			t.Fatalf("Select(%q) = %s, want %s", test.raw, selected.Name(), test.want)
		}
	}
}

func TestMicrosoftFamilyRouteRejectsUnsafe(t *testing.T) {
	registry := NewRegistry(
		NewMicrosoftEmbed(),
		NewMicrosoftMedius(),
		NewMicrosoftLearnPlaylist(),
		NewMicrosoftLearnEpisode(),
		NewMicrosoftLearnSession(),
		NewMicrosoftBuild(),
	)
	hostiles := []string{
		"http://www.microsoft.com/en-us/videoplayer/embed/RWL07e",
		"http://medius.microsoft.com/Embed/video-nc/9640d86c-f513-4889-959e-5dace86e7d2b",
		"http://learn.microsoft.com/en-us/shows/bash-for-beginners",
		"http://build.microsoft.com/en-US/sessions",
		"https://example.com/en-us/videoplayer/embed/RWL07e",
		"https://medius.microsoft.com.attacker.example/Embed/video-nc/9640d86c-f513-4889-959e-5dace86e7d2b",
		"https://learn.microsoft.com.attacker.example/en-us/shows/bash-for-beginners",
		"https://user:pass@www.microsoft.com/en-us/videoplayer/embed/RWL07e",
		"https://www.microsoft.com:8080/en-us/videoplayer/embed/RWL07e",
		"https://www.microsoft.com/en-us/videoplayer/embed/RWL07e#x",
		"https://www.microsoft.com/en-us/videoplayer/embed/%2e%2e",
		"https://www.microsoft.com/en-us/videoplayer/embed/",
		"https://www.microsoft.com/en-us/videoplayer/embed/!!!",
		"https://medius.microsoft.com/Embed/video-nc/notauuid",
		"https://medius.microsoft.com/Embed/VideoDetails/notauuid",
		"https://medius.microsoft.com/Embed/Video?id=notauuid",
		"https://medius.microsoft.com/Embed/Video?id=&extra=x",
		"https://medius.microsoft.com/Embed/Video?source=video-nc&id=9640d86c-f513-4889-959e-5dace86e7d2b",
		"https://medius.microsoft.com/Embed/Video?id=9640d86c-f513-4889-959e-5dace86e7d2b&id=9640d86c-f513-4889-959e-5dace86e7d2c",
		"https://medius.microsoft.com/Embed/Video?id=%26bad=%2f",
		"https://medius.microsoft.com/Embed/Video?id=9640d86c-f513-4889-959e-5dace86e7d2b%20extra",
		"https://medius.microsoft.com/Embed/video-nc/9640d86c-f513-4889-959e-5dace86e7d2b/extra",
		"https://medius.microsoft.com/Embed/video-nc/9640d86c-f513-4889-959e-5dace86e7d2b?x=1",
		"https://medius.microsoft.com/Embed/VideoDetails/9640d86c-f513-4889-959e-5dace86e7d2b/extra",
		"https://medius.microsoft.com/Embed/VideoDetails/9640d86c-f513-4889-959e-5dace86e7d2b?x=1",
		"https://learn.microsoft.com/en-us/shows/",
		"https://build.microsoft.com/sessions",
		"https://build.microsoft.com/9640d86c-f513-4889-959e-5dace86e7d2b",
		"https://build.microsoft.com/sessions/9640d86c-f513-4889-959e-5dace86e7d2b",
		"https://build.microsoft.com/en-US/sessions/9640d86c-f513-4889-959e-5dace86e7d2b?evil=1",
		"https://build.microsoft.com/en-US/sessions?evil=1",
		"https://build.microsoft.com/en-US/sessions?source=other",
		"https://build.microsoft.com/en-US/sessions?source=sessions&extra=1",
		"https://127.0.0.1/videoplayer/embed/RWL07e",
	}
	for _, raw := range hostiles {
		if _, err := registry.Select(raw); err == nil {
			t.Fatalf("Select(%q) should be rejected", raw)
		}
	}
}

// --- Host policy -----------------------------------------------------------

func TestMicrosoftRoleHostAllowlist(t *testing.T) {
	cases := []struct {
		role   microsoftMediaRole
		url    string
		accept bool
	}{
		{microsoftRoleManifest, "https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/ism/manifest.ism", true},
		{microsoftRoleManifest, "https://mediusimg.event.microsoft.com/video-uuid/manifest.ism", true},
		{microsoftRoleManifest, "https://mediusdownload.event.microsoft.com/asset/manifest.ism", true},
		{microsoftRoleManifest, "https://learn.microsoft.com/video/media/manifest.ism", true},
		{microsoftRoleManifest, "https://attacker.example.com/manifest.ism", false},
		{microsoftRoleManifest, "http://insecure.example.com/manifest.ism", false},
		{microsoftRoleMedia, "https://mediusdownload.event.microsoft.com/asset/video.mp4", true},
		{microsoftRoleMedia, "https://learn.microsoft.com/video/media/audio.mp3", true},
		{microsoftRoleMedia, "https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/mp4/high.mp4", true},
		{microsoftRoleMedia, "https://attacker.example.com/video.mp4", false},
		{microsoftRoleCaption, "https://mediusimg.event.microsoft.com/captions/en.vtt", true},
		{microsoftRoleCaption, "https://learn.microsoft.com/captions/en.vtt", true},
		{microsoftRoleCaption, "https://attacker.example.com/captions.vtt", false},
		{microsoftRoleThumbnail, "https://img-prod-cms-rt-microsoft-com.akamaized.net/cms/api/am/imageFileData/x", true},
		{microsoftRoleThumbnail, "https://mediusimg.event.microsoft.com/video-uuid/thumbnail.jpg", true},
		{microsoftRoleThumbnail, "https://learn.microsoft.com/thumb/640.jpg", true},
		{microsoftRoleThumbnail, "https://attacker.example.com/thumb.jpg", false},
	}
	for _, test := range cases {
		parsed, err := url.Parse(test.url)
		if err != nil {
			t.Fatalf("parse %q: %v", test.url, err)
		}
		if got := microsoftHostAllowed(parsed, test.role); got != test.accept {
			t.Fatalf("role=%s url=%q got=%v want=%v", test.role, test.url, got, test.accept)
		}
	}
}

// --- MicrosoftEmbed success / metadata / format / subtitle / thumbnail ----

func TestMicrosoftEmbedSuccess(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/api/videos/RWL07e",
		http.StatusOK, loadMicrosoftFixture(t, "embed_metadata.json"))
	ext := NewMicrosoftEmbed()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://www.microsoft.com/en-us/videoplayer/embed/RWL07e",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if extraction.IsPlaylist() {
		t.Fatalf("expected media")
	}
	if id, _ := extraction.Info.Lookup("id").StringValue(); id != "RWL07e" {
		t.Fatalf("id=%q", id)
	}
	if title, _ := extraction.Info.Lookup("title").StringValue(); title != "Microsoft for Public Health and Social Services" {
		t.Fatalf("title=%q", title)
	}
	timestamp, ok := extraction.Info.Lookup("timestamp").Int()
	if !ok || timestamp != 1631658316 {
		t.Fatalf("timestamp=%d ok=%v", timestamp, ok)
	}
	formats, _ := extraction.Info.Lookup("formats").ListValue()
	if len(formats) != 5 {
		t.Fatalf("formats=%d want 5", len(formats))
	}
	protocolOrder := []string{"ism", "m3u8_native", "http_dash_segments", "https", "https"}
	for i, f := range formats {
		obj, _ := f.Object()
		if isolated, _ := obj.Lookup("_credential_isolated").Bool(); !isolated {
			t.Fatalf("format %d missing credential_isolated", i)
		}
		protocol, _ := obj.Lookup("protocol").StringValue()
		if protocol != protocolOrder[i] {
			t.Fatalf("format %d protocol=%q want %q", i, protocol, protocolOrder[i])
		}
	}
	subsRaw, ok := extraction.Info.Lookup("subtitles").Object()
	if !ok {
		t.Fatalf("no subtitles")
	}
	expectedLangs := []string{"en", "es", "fr"}
	for _, lang := range expectedLangs {
		got, _ := subsRaw.Lookup(lang).ListValue()
		if len(got) != 1 {
			t.Fatalf("subtitle %s missing", lang)
		}
		obj, _ := got[0].Object()
		if isolated, _ := obj.Lookup("_credential_isolated").Bool(); !isolated {
			t.Fatalf("subtitle %s missing credential_isolated", lang)
		}
	}
}

func TestMicrosoftEmbedAkamaiTimestampFormats(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"/Date(1631658316000)/", 1631658316},
		{"1631658316", 1631658316},
		{"", 0},
		{"not-a-date", 0},
	}
	for _, test := range cases {
		got := microsoftAkamaiTimestamp(test.in)
		if got != test.want {
			t.Fatalf("microsoftAkamaiTimestamp(%q) = %d, want %d", test.in, got, test.want)
		}
	}
}

func TestMicrosoftEmbedRejectsQuery(t *testing.T) {
	ext := NewMicrosoftEmbed()
	parsed, err := url.Parse("https://www.microsoft.com/en-us/videoplayer/embed/RWL07e?evil=1")
	if err != nil {
		t.Fatal(err)
	}
	if ext.Suitable(parsed) {
		t.Fatalf("Suitable should reject query")
	}
}

// --- MicrosoftMedius routes ------------------------------------------------

func TestMicrosoftMediusVideoNCSuccess(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://medius.microsoft.com/Embed/video-nc/9640d86c-f513-4889-959e-5dace86e7d2b",
		http.StatusOK, loadMicrosoftFixture(t, "medius_webpage.html"))
	ext := NewMicrosoftMedius()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://medius.microsoft.com/Embed/video-nc/9640d86c-f513-4889-959e-5dace86e7d2b",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if id, _ := extraction.Info.Lookup("id").StringValue(); id != "9640d86c-f513-4889-959e-5dace86e7d2b" {
		t.Fatalf("id=%q", id)
	}
	formats, _ := extraction.Info.Lookup("formats").ListValue()
	if len(formats) != 1 {
		t.Fatalf("formats=%d", len(formats))
	}
	obj, _ := formats[0].Object()
	if isolated, _ := obj.Lookup("_credential_isolated").Bool(); !isolated {
		t.Fatalf("format not isolated")
	}
	if protocol, _ := obj.Lookup("protocol").StringValue(); protocol != "ism" {
		t.Fatalf("protocol=%q", protocol)
	}
	subsRaw, ok := extraction.Info.Lookup("subtitles").Object()
	if !ok {
		t.Fatalf("no subtitles")
	}
	if len(mustList(t, subsRaw.Lookup("en"))) == 0 || len(mustList(t, subsRaw.Lookup("es"))) == 0 {
		t.Fatalf("missing en/es subs")
	}
}

func TestMicrosoftMediusVideoQueryRoute(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://medius.microsoft.com/Embed/video-nc/0dc69bda-079b-4070-a7db-a8da1a06a9c7",
		http.StatusOK, loadMicrosoftFixture(t, "medius_webpage_video_query.html"))
	ext := NewMicrosoftMedius()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://medius.microsoft.com/Embed/Video?id=0dc69bda-079b-4070-a7db-a8da1a06a9c7",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if id, _ := extraction.Info.Lookup("id").StringValue(); id != "0dc69bda-079b-4070-a7db-a8da1a06a9c7" {
		t.Fatalf("id=%q", id)
	}
}

func TestMicrosoftMediusVideoQueryRejects(t *testing.T) {
	ext := NewMicrosoftMedius()
	cases := []string{
		"https://medius.microsoft.com/Embed/Video?id=notauuid",
		"https://medius.microsoft.com/Embed/Video?id=&extra=x",
		"https://medius.microsoft.com/Embed/Video?source=video-nc&id=9640d86c-f513-4889-959e-5dace86e7d2b",
		"https://medius.microsoft.com/Embed/Video?id=9640d86c-f513-4889-959e-5dace86e7d2b&id=9640d86c-f513-4889-959e-5dace86e7d2c",
		"https://medius.microsoft.com/Embed/Video?id=%26bad=%2f",
		"https://medius.microsoft.com/Embed/Video",
		"https://medius.microsoft.com/Embed/Video?",
	}
	for _, raw := range cases {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if ext.Suitable(parsed) {
			t.Fatalf("Suitable(%q) should be false", raw)
		}
	}
}

func TestMicrosoftMediusVideoDetailsSuccess(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://medius.microsoft.com/Embed/video-nc/78493569-9b3b-4a85-a409-ee76e789e25c",
		http.StatusOK, loadMicrosoftFixture(t, "medius_webpage_videodetails.html"))
	ext := NewMicrosoftMedius()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://medius.microsoft.com/Embed/VideoDetails/78493569-9b3b-4a85-a409-ee76e789e25c",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if title, _ := extraction.Info.Lookup("title").StringValue(); !strings.Contains(title, "Anomaly Detection") {
		t.Fatalf("title=%q", title)
	}
}

func TestMicrosoftMediusRejectsExtraSegments(t *testing.T) {
	ext := NewMicrosoftMedius()
	for _, raw := range []string{
		"https://medius.microsoft.com/Embed/video-nc/9640d86c-f513-4889-959e-5dace86e7d2b/extra",
		"https://medius.microsoft.com/Embed/VideoDetails/9640d86c-f513-4889-959e-5dace86e7d2b/extra",
	} {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if ext.Suitable(parsed) {
			t.Fatalf("Suitable(%q) should be false", raw)
		}
	}
}

func TestMicrosoftMediusLegacyCaptionsStopsAtBlock(t *testing.T) {
	page := []byte(`<!DOCTYPE html><html><head><meta property="og:title" content="block-scoped">
<meta property="og:image" content="https://mediusimg.event.microsoft.com/video-x/thumbnail.jpg">
</head>
<body><script>var StreamUrl = "https://mediusimg.event.microsoft.com/video-x/manifest.ism/manifest";</script>
<script>var file = {
  'https://mediusimg.event.microsoft.com/video-x/captions/in.vtt?token=1'
};</script>
<script>var unrelated = 'https://mediusimg.event.microsoft.com/video-x/captions/out.vtt?token=2';</script>
</body></html>`)
	transport := newMicrosoftTransport()
	transport.set("GET", "https://medius.microsoft.com/Embed/video-nc/00000000-0000-4000-8000-000000000111",
		http.StatusOK, page)
	ext := NewMicrosoftMedius()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://medius.microsoft.com/Embed/video-nc/00000000-0000-4000-8000-000000000111",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	subsRaw, ok := extraction.Info.Lookup("subtitles").Object()
	if !ok {
		t.Fatalf("missing subtitles object")
	}
	in := mustList(t, subsRaw.Lookup("in"))
	if len(in) == 0 {
		t.Fatalf("in-block caption missing under lang=in")
	}
	if outVal, ok := subsRaw.Lookup("out").ListValue(); ok && len(outVal) > 0 {
		t.Fatalf("out-of-block caption leaked: %v", outVal)
	}
}

func TestMicrosoftMediusMissingStreamURL(t *testing.T) {
	page := []byte(`<!DOCTYPE html><html><head><meta property="og:title" content="Broken"></head>
<body><p>no stream</p></body></html>`)
	transport := newMicrosoftTransport()
	transport.set("GET", "https://medius.microsoft.com/Embed/video-nc/00000000-0000-4000-8000-000000000001",
		http.StatusOK, page)
	ext := NewMicrosoftMedius()
	_, err := ext.Extract(context.Background(), Request{
		URL:       "https://medius.microsoft.com/Embed/video-nc/00000000-0000-4000-8000-000000000001",
		Transport: transport,
	})
	if err == nil {
		t.Fatalf("expected error for missing stream URL")
	}
}

// --- MicrosoftLearnPlaylist (lazy) ----------------------------------------

func TestMicrosoftLearnPlaylistLazyIteratorNoAPIBeforeIteration(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://learn.microsoft.com/en-us/shows/bash-for-beginners",
		http.StatusOK, []byte(`<!DOCTYPE html><html><head><meta property="og:title" content="Bash for Beginners"></head><body></body></html>`))
	transport.set("GET",
		"https://learn.microsoft.com/api/contentbrowser/search/shows/bash-for-beginners/episodes?locale=en-us&%24skip=0",
		http.StatusOK, loadMicrosoftFixture(t, "learn_playlist_page1.json"))
	ext := NewMicrosoftLearnPlaylist()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://learn.microsoft.com/en-us/shows/bash-for-beginners",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	transport.mu.Lock()
	for _, req := range transport.requests {
		if strings.Contains(req, "/api/contentbrowser/search/") {
			transport.mu.Unlock()
			t.Fatalf("API requested before iteration: %s", req)
		}
	}
	transport.mu.Unlock()
	_ = extraction
}

func TestMicrosoftLearnPlaylistTwoIndependentIterations(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://learn.microsoft.com/en-us/shows/bash-for-beginners",
		http.StatusOK, []byte(`<!DOCTYPE html><html><head><meta property="og:title" content="Bash for Beginners"></head><body></body></html>`))
	transport.set("GET",
		"https://learn.microsoft.com/api/contentbrowser/search/shows/bash-for-beginners/episodes?locale=en-us&%24skip=0",
		http.StatusOK, loadMicrosoftFixture(t, "learn_playlist_page1.json"))
	transport.set("GET",
		"https://learn.microsoft.com/api/contentbrowser/search/shows/bash-for-beginners/episodes?locale=en-us&%24skip=4",
		http.StatusOK, loadMicrosoftFixture(t, "learn_playlist_page2.json"))
	ext := NewMicrosoftLearnPlaylist()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://learn.microsoft.com/en-us/shows/bash-for-beginners",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	first, err := CollectEntries(context.Background(), extraction.Entries, microsoftMaxEntries)
	if err != nil || len(first) != 6 {
		t.Fatalf("first iteration: %d err=%v", len(first), err)
	}
	second, err := CollectEntries(context.Background(), extraction.Entries, microsoftMaxEntries)
	if err != nil || len(second) != 6 {
		t.Fatalf("second iteration: %d err=%v", len(second), err)
	}
	for i := range first {
		if first[i].URL != second[i].URL {
			t.Fatalf("iter %d url mismatch: %q vs %q", i, first[i].URL, second[i].URL)
		}
	}
}

func TestMicrosoftLearnPlaylistPartialConsumption(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://learn.microsoft.com/en-us/shows/bash-for-beginners",
		http.StatusOK, []byte(`<!DOCTYPE html><html><head><meta property="og:title" content="Bash for Beginners"></head><body></body></html>`))
	transport.set("GET",
		"https://learn.microsoft.com/api/contentbrowser/search/shows/bash-for-beginners/episodes?locale=en-us&%24skip=0",
		http.StatusOK, loadMicrosoftFixture(t, "learn_playlist_page1.json"))
	transport.set("GET",
		"https://learn.microsoft.com/api/contentbrowser/search/shows/bash-for-beginners/episodes?locale=en-us&%24skip=4",
		http.StatusOK, loadMicrosoftFixture(t, "learn_playlist_page2.json"))
	ext := NewMicrosoftLearnPlaylist()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://learn.microsoft.com/en-us/shows/bash-for-beginners",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	iterator := extraction.Entries.Iterator()
	partial := []Entry{}
	for i := 0; i < 2; i++ {
		entry, ok, err := iterator.Next(context.Background())
		if err != nil || !ok {
			t.Fatalf("partial iteration failed: %v ok=%v", err, ok)
		}
		partial = append(partial, entry)
	}
	if len(partial) != 2 {
		t.Fatalf("partial=%d", len(partial))
	}
	full, err := CollectEntries(context.Background(), extraction.Entries, microsoftMaxEntries)
	if err != nil || len(full) != 6 {
		t.Fatalf("full second iteration: %d err=%v", len(full), err)
	}
}

func TestMicrosoftLearnPlaylistInvalidRowsSkipByServerCount(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://learn.microsoft.com/en-us/shows/bash-for-beginners",
		http.StatusOK, []byte(`<!DOCTYPE html><html><head><meta property="og:title" content="Bash for Beginners"></head><body></body></html>`))
	transport.set("GET",
		"https://learn.microsoft.com/api/contentbrowser/search/shows/bash-for-beginners/episodes?locale=en-us&%24skip=0",
		http.StatusOK, loadMicrosoftFixture(t, "learn_playlist_invalid_page1.json"))
	transport.set("GET",
		"https://learn.microsoft.com/api/contentbrowser/search/shows/bash-for-beginners/episodes?locale=en-us&%24skip=6",
		http.StatusOK, loadMicrosoftFixture(t, "learn_playlist_invalid_page2.json"))
	ext := NewMicrosoftLearnPlaylist()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://learn.microsoft.com/en-us/shows/bash-for-beginners",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	collected, err := CollectEntries(context.Background(), extraction.Entries, microsoftMaxEntries)
	if err != nil {
		t.Fatalf("CollectEntries: %v", err)
	}
	if len(collected) != 5 {
		t.Fatalf("collected=%d want 5 (3 deduped + 2 from page 2)", len(collected))
	}
	foundSkip6 := false
	transport.mu.Lock()
	for _, req := range transport.requests {
		if strings.Contains(req, "%24skip=6") {
			foundSkip6 = true
		}
		if strings.Contains(req, "%24skip=3") {
			transport.mu.Unlock()
			t.Fatalf("iterator advanced by deduped count, used $skip=3")
		}
	}
	transport.mu.Unlock()
	if !foundSkip6 {
		t.Fatalf("iterator did not advance to $skip=6")
	}
}

func TestMicrosoftLearnPlaylistRepeatedCursorTerminates(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://learn.microsoft.com/en-us/shows/repeat",
		http.StatusOK, []byte(`<!DOCTYPE html><html><head></head><body></body></html>`))
	transport.set("GET",
		"https://learn.microsoft.com/api/contentbrowser/search/shows/repeat/episodes?locale=en-us&%24skip=0",
		http.StatusOK, loadMicrosoftFixture(t, "learn_playlist_dedupe.json"))
	ext := NewMicrosoftLearnPlaylist()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://learn.microsoft.com/en-us/shows/repeat",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	collected, err := CollectEntries(context.Background(), extraction.Entries, microsoftMaxEntries)
	if err != nil {
		t.Fatalf("CollectEntries: %v", err)
	}
	if len(collected) != 2 {
		t.Fatalf("collected=%d want 2 deduped", len(collected))
	}
}

func TestMicrosoftLearnPlaylistEmptyTerminalPage(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://learn.microsoft.com/en-us/shows/empty",
		http.StatusOK, []byte(`<!DOCTYPE html><html><head></head><body></body></html>`))
	transport.set("GET",
		"https://learn.microsoft.com/api/contentbrowser/search/shows/empty/episodes?locale=en-us&%24skip=0",
		http.StatusOK, loadMicrosoftFixture(t, "learn_playlist_empty.json"))
	ext := NewMicrosoftLearnPlaylist()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://learn.microsoft.com/en-us/shows/empty",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	collected, err := CollectEntries(context.Background(), extraction.Entries, microsoftMaxEntries)
	if err != nil {
		t.Fatalf("CollectEntries: %v", err)
	}
	if len(collected) != 0 {
		t.Fatalf("collected=%d want 0", len(collected))
	}
}

func TestMicrosoftLearnPlaylistCancellationDuringContinuation(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://learn.microsoft.com/en-us/shows/cancel",
		http.StatusOK, []byte(`<!DOCTYPE html><html><head></head><body></body></html>`))
	transport.set("GET",
		"https://learn.microsoft.com/api/contentbrowser/search/shows/cancel/episodes?locale=en-us&%24skip=0",
		http.StatusOK, loadMicrosoftFixture(t, "learn_playlist_page1.json"))
	transport.set("GET",
		"https://learn.microsoft.com/api/contentbrowser/search/shows/cancel/episodes?locale=en-us&%24skip=4",
		http.StatusOK, loadMicrosoftFixture(t, "learn_playlist_page2.json"))
	ext := NewMicrosoftLearnPlaylist()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://learn.microsoft.com/en-us/shows/cancel",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	iterator := extraction.Entries.Iterator()
	for i := 0; i < 4; i++ {
		if _, ok, nextErr := iterator.Next(context.Background()); nextErr != nil || !ok {
			t.Fatalf("first page entry %d: ok=%v err=%v", i, ok, nextErr)
		}
	}
	requestCh := make(chan string, 1)
	waitCh := make(chan struct{})
	transport.mu.Lock()
	transport.requestCh = requestCh
	transport.waitCh = waitCh
	transport.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, _, nextErr := iterator.Next(ctx)
		errCh <- nextErr
	}()
	select {
	case requestKey := <-requestCh:
		if !strings.Contains(requestKey, "%24skip=4") {
			t.Fatalf("continuation request = %q", requestKey)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("continuation request did not start")
	}
	cancel()
	if nextErr := <-errCh; !errors.Is(nextErr, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", nextErr)
	}
}

// --- MicrosoftLearnEpisode ------------------------------------------------

func TestMicrosoftLearnEpisodeSuccess(t *testing.T) {
	transport := newMicrosoftTransport()
	uuid := "d44e1a03-a0e5-45c2-9496-5c9fa08dc94c"
	transport.set("GET", "https://learn.microsoft.com/en-us/shows/bash-for-beginners/what-is-the-difference",
		http.StatusOK, []byte(`<!DOCTYPE html><html><head><meta property="og:title" content="What is the Difference">
<meta property="og:description" content="Synthetic Learn episode">
<meta name="entryId" content="`+uuid+`"></head><body></body></html>`))
	transport.set("GET", "https://learn.microsoft.com/api/video/public/v1/entries/"+uuid,
		http.StatusOK, loadMicrosoftFixture(t, "learn_episode_metadata.json"))
	ext := NewMicrosoftLearnEpisode()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://learn.microsoft.com/en-us/shows/bash-for-beginners/what-is-the-difference",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if id, _ := extraction.Info.Lookup("id").StringValue(); id != uuid {
		t.Fatalf("id=%q", id)
	}
	formats, _ := extraction.Info.Lookup("formats").ListValue()
	if len(formats) != 7 {
		t.Fatalf("formats=%d", len(formats))
	}
	for _, f := range formats {
		obj, _ := f.Object()
		if isolated, _ := obj.Lookup("_credential_isolated").Bool(); !isolated {
			t.Fatalf("format not credential isolated: %v", obj)
		}
	}
	subsRaw, ok := extraction.Info.Lookup("subtitles").Object()
	if !ok || len(mustList(t, subsRaw.Lookup("en"))) == 0 {
		t.Fatalf("missing english subs")
	}
	if thumbnails := mustList(t, extraction.Info.Lookup("thumbnails")); len(thumbnails) != 2 {
		t.Fatalf("thumbnails=%d", len(thumbnails))
	}
	if timestamp, ok := extraction.Info.Lookup("timestamp").Int(); !ok || timestamp == 0 {
		t.Fatalf("timestamp missing")
	}
}

// --- MicrosoftLearnSession ------------------------------------------------

func TestMicrosoftLearnSessionReentryValidatesMediusSuitability(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://learn.microsoft.com/en-us/events/build-2022/ts01",
		http.StatusOK, []byte(`<!DOCTYPE html><html><head>
<meta name="externalVideoUrl" content="https://attacker.example.com/embed/evil">
</head><body></body></html>`))
	ext := NewMicrosoftLearnSession()
	_, err := ext.Extract(context.Background(), Request{
		URL:       "https://learn.microsoft.com/en-us/events/build-2022/ts01",
		Transport: transport,
	})
	if err == nil {
		t.Fatalf("expected failure for non-Medius externalVideoUrl")
	}
}

// --- MicrosoftBuild -------------------------------------------------------

func TestMicrosoftBuildExactRoutes(t *testing.T) {
	ext := NewMicrosoftBuild()
	hostiles := []string{
		"https://build.microsoft.com/sessions",
		"https://build.microsoft.com/sessions/9640d86c-f513-4889-959e-5dace86e7d2b",
		"https://build.microsoft.com/9640d86c-f513-4889-959e-5dace86e7d2b",
		"https://build.microsoft.com/en-US/sessions/9640d86c-f513-4889-959e-5dace86e7d2b?evil=1",
		"https://build.microsoft.com/en-US/sessions?evil=1",
		"https://build.microsoft.com/en-US/sessions?source=other",
		"https://build.microsoft.com/en-US/sessions?source=sessions&extra=1",
		"https://build.microsoft.com/en-US/sessions?source=sessions&source=other",
	}
	for _, raw := range hostiles {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if ext.Suitable(parsed) {
			t.Fatalf("Suitable(%q) should be false", raw)
		}
	}
}

func TestMicrosoftBuildPlaylistOnDemandValidatesMediusSuitability(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://api-v2.build.microsoft.com/api/session/all/en-US",
		http.StatusOK, []byte(`[{"sessionId":"00000000-0000-4000-8000-000000000000","title":"x","onDemand":"https://attacker.example.com/Embed/video-nc/00000000-0000-4000-8000-000000000000"}]`))
	ext := NewMicrosoftBuild()
	_, err := ext.Extract(context.Background(), Request{
		URL:       "https://build.microsoft.com/en-US/sessions",
		Transport: transport,
	})
	if err == nil {
		t.Fatalf("expected error for non-Medius onDemand")
	}
}

func TestMicrosoftBuildSessionsPlaylist(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://api-v2.build.microsoft.com/api/session/all/en-US",
		http.StatusOK, loadMicrosoftFixture(t, "build_sessions.json"))
	ext := NewMicrosoftBuild()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://build.microsoft.com/en-US/sessions?source=sessions",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extraction.IsPlaylist() {
		t.Fatalf("expected playlist")
	}
	collected, err := CollectEntries(context.Background(), extraction.Entries, microsoftMaxEntries)
	if err != nil {
		t.Fatalf("CollectEntries: %v", err)
	}
	if len(collected) != 2 {
		t.Fatalf("collected=%d", len(collected))
	}
	for _, entry := range collected {
		if entry.ExtractorKey != "microsoft_medius" || !entry.Transparent {
			t.Fatalf("entry=%+v", entry)
		}
		if !strings.Contains(entry.URL, "medius.microsoft.com/Embed/video-nc/") {
			t.Fatalf("entry url=%q", entry.URL)
		}
	}
}

func TestMicrosoftBuildSingleUUID(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://api-v2.build.microsoft.com/api/session/all/en-US",
		http.StatusOK, loadMicrosoftFixture(t, "build_sessions.json"))
	ext := NewMicrosoftBuild()
	extraction, err := ext.Extract(context.Background(), Request{
		URL:       "https://build.microsoft.com/en-US/sessions/aee55fb5-fcf9-4b38-b764-a3527cb57554",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extraction.IsURL() {
		t.Fatalf("expected URLResult")
	}
	if extraction.Redirect.ID != "aee55fb5-fcf9-4b38-b764-a3527cb57554" {
		t.Fatalf("redirect id=%q", extraction.Redirect.ID)
	}
}

// --- Failure categories ---------------------------------------------------

func TestMicrosoftEmbedCategorizesFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"unauthorized", http.StatusUnauthorized, ErrAuthentication},
		{"forbidden", http.StatusForbidden, ErrRegionRestricted},
		{"notfound", http.StatusNotFound, ErrUnavailable},
		{"gone", http.StatusGone, ErrUnavailable},
		{"legal", http.StatusUnavailableForLegalReasons, ErrRegionRestricted},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			transport := newMicrosoftTransport()
			transport.set("GET", "https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/api/videos/RWL07e",
				test.status, nil)
			ext := NewMicrosoftEmbed()
			_, err := ext.Extract(context.Background(), Request{
				URL:       "https://www.microsoft.com/en-us/videoplayer/embed/RWL07e",
				Transport: transport,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v want %v", err, test.want)
			}
		})
	}
}

func TestMicrosoftEmbedRejects3xxAsNoRedirectViolation(t *testing.T) {
	for _, status := range []int{300, 301, 302, 303, 307, 308} {
		t.Run(fmt.Sprintf("status-%d", status), func(t *testing.T) {
			transport := newMicrosoftTransport()
			transport.set("GET", "https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/api/videos/RWL07e",
				status, []byte("redirect body"))
			ext := NewMicrosoftEmbed()
			_, err := ext.Extract(context.Background(), Request{
				URL:       "https://www.microsoft.com/en-us/videoplayer/embed/RWL07e",
				Transport: transport,
			})
			if err == nil {
				t.Fatalf("expected error for status %d", status)
			}
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("err=%v want ErrInvalidMetadata sentinel", err)
			}
		})
	}
}

func TestMicrosoftEmbedGenericStatusReturnsTypedError(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/api/videos/RWL07e",
		http.StatusInternalServerError, []byte("boom"))
	ext := NewMicrosoftEmbed()
	_, err := ext.Extract(context.Background(), Request{
		URL:       "https://www.microsoft.com/en-us/videoplayer/embed/RWL07e",
		Transport: transport,
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if errors.Is(err, ErrAuthentication) || errors.Is(err, ErrRegionRestricted) || errors.Is(err, ErrUnavailable) {
		t.Fatalf("generic status must not be a typed auth/region/unavailable sentinel: %v", err)
	}
}

func TestMicrosoftFamilyCancellation(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.waitCh = make(chan struct{})
	close(transport.waitCh)
	ext := NewMicrosoftEmbed()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ext.Extract(ctx, Request{
		URL:       "https://www.microsoft.com/en-us/videoplayer/embed/RWL07e",
		Transport: transport,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
}

func TestMicrosoftEmbedCredentialIsolation(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/api/videos/RWL07e",
		http.StatusOK, loadMicrosoftFixture(t, "embed_metadata.json"))
	ext := NewMicrosoftEmbed()
	_, err := ext.Extract(context.Background(), Request{
		URL:       "https://www.microsoft.com/en-us/videoplayer/embed/RWL07e",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(transport.cookieSeen) != 0 || len(transport.authSeen) != 0 || len(transport.proxySeen) != 0 {
		t.Fatalf("credentials leaked: cookie=%d auth=%d proxy=%d", len(transport.cookieSeen), len(transport.authSeen), len(transport.proxySeen))
	}
	if transport.isolation.Load() == 0 {
		t.Fatalf("no isolated calls recorded")
	}
}

// --- URL validation fuzz --------------------------------------------------

func TestMicrosoftEmbedURLValidationFuzz(t *testing.T) {
	ext := NewMicrosoftEmbed()
	cases := []string{
		"",
		"https://",
		"https://www.microsoft.com/",
		"https://www.microsoft.com/videoplayer",
		"https://www.microsoft.com/videoplayer/embed/",
		"https://www.microsoft.com/videoplayer/embed/RWL07e/extra",
		"https://www.microsoft.com/videoplayer/RWL07e",
		"https://www.microsoft.com/videoplayer/embed/RWL07e/?x",
		"https://www.microsoft.com/en-1/videoplayer/embed/RWL07e",
		"https://www.microsoft.com/videoplayer/embed/RWL07e#frag",
		"http://www.microsoft.com/videoplayer/embed/RWL07e",
		"https://www.microsoft.com:443/videoplayer/embed/RWL07e",
		"https://www.microsoft.com.attacker.example/videoplayer/embed/RWL07e",
		"https://[::1]/videoplayer/embed/RWL07e",
		"https://www.microsoft.com/videoplayer/embed/RWL07e?evil=1",
	}
	for _, raw := range cases {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if ext.Suitable(parsed) {
			t.Fatalf("Suitable(%q) should be false", raw)
		}
	}
}

func TestMicrosoftMediusURLValidationFuzz(t *testing.T) {
	ext := NewMicrosoftMedius()
	cases := []string{
		"",
		"https://medius.microsoft.com/",
		"https://medius.microsoft.com/Embed",
		"https://medius.microsoft.com/Embed/video-nc/",
		"https://medius.microsoft.com/Embed/video-nc/notauuid",
		"https://medius.microsoft.com/Embed/video-nc/00000000-0000-4000-8000-000000000000/extra",
		"https://medius.microsoft.com/Embed/Video?id=notauuid",
		"https://medius.microsoft.com/Embed/VideoDetails/notauuid",
		"https://medius.microsoft.com/Embed/VideoDetails/00000000-0000-4000-8000-000000000000/extra",
		"https://medius.microsoft.com/Embed/VideoDetails/00000000-0000-4000-8000-000000000000?x=1",
		"https://medius.microsoft.com.evil.example/Embed/video-nc/00000000-0000-4000-8000-000000000000",
		"https://medius.microsoft.com/Embed/video-nc/00000000-0000-4000-8000-000000000000#x",
		"https://medius.microsoft.com/Embed/Other/00000000-0000-4000-8000-000000000000",
		"https://medius.microsoft.com/Embed/Video?id=%26bad=%2f",
	}
	for _, raw := range cases {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if ext.Suitable(parsed) {
			t.Fatalf("Suitable(%q) should be false", raw)
		}
	}
}

func TestMicrosoftLearnRoutesFuzz(t *testing.T) {
	cases := []struct {
		raw string
		ext Extractor
	}{
		{"https://learn.microsoft.com/en-us/shows/x", NewMicrosoftLearnPlaylist()},
		{"https://learn.microsoft.com/en-us/events/x", NewMicrosoftLearnPlaylist()},
		{"https://learn.microsoft.com/shows/x", NewMicrosoftLearnPlaylist()},
		{"https://learn.microsoft.com/en-us/shows/x/y", NewMicrosoftLearnEpisode()},
		{"https://learn.microsoft.com/en-us/events/x/y", NewMicrosoftLearnSession()},
		{"https://learn.microsoft.com/shows/x/y", NewMicrosoftLearnEpisode()},
		{"https://learn.microsoft.com/events/x/y", NewMicrosoftLearnSession()},
	}
	for _, test := range cases {
		parsed, err := url.Parse(test.raw)
		if err != nil {
			continue
		}
		if !test.ext.Suitable(parsed) {
			t.Fatalf("%T should be Suitable(%q)", test.ext, test.raw)
		}
	}
	hostiles := []string{
		"https://learn.microsoft.com/en-us/shows/x?evil=1",
		"https://learn.microsoft.com/en-us/events/x?evil=1",
		"https://learn.microsoft.com/en-us/shows/x/y?evil=1",
		"https://learn.microsoft.com/en-us/events/x/y?evil=1",
	}
	for _, raw := range hostiles {
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		ext := NewMicrosoftLearnPlaylist()
		if ext.Suitable(parsed) {
			t.Fatalf("Suitable(%q) should be false", raw)
		}
	}
}

// --- JSON / malformed body ------------------------------------------------

func TestMicrosoftEmbedMalformedJSON(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/api/videos/RWL07e",
		http.StatusOK, []byte("not json"))
	ext := NewMicrosoftEmbed()
	_, err := ext.Extract(context.Background(), Request{
		URL:       "https://www.microsoft.com/en-us/videoplayer/embed/RWL07e",
		Transport: transport,
	})
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestMicrosoftEmbedTrailingJSON(t *testing.T) {
	transport := newMicrosoftTransport()
	body := []byte(`{"streams":{"smooth_Streaming":{"url":"https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/ism/manifest.ism"}}, "snippet":{"title":"OK"}}garbage`)
	transport.set("GET", "https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/api/videos/RWL07e",
		http.StatusOK, body)
	ext := NewMicrosoftEmbed()
	_, err := ext.Extract(context.Background(), Request{
		URL:       "https://www.microsoft.com/en-us/videoplayer/embed/RWL07e",
		Transport: transport,
	})
	if err == nil {
		t.Fatalf("expected trailing JSON error")
	}
}

func TestMicrosoftBuildMalformedJSON(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://api-v2.build.microsoft.com/api/session/all/en-US",
		http.StatusOK, []byte("not-a-json-array"))
	ext := NewMicrosoftBuild()
	_, err := ext.Extract(context.Background(), Request{
		URL:       "https://build.microsoft.com/en-US/sessions",
		Transport: transport,
	})
	if err == nil {
		t.Fatalf("expected malformed JSON error")
	}
}

// --- Sensitive value safety -----------------------------------------------

func TestMicrosoftErrorMessagesDoNotEchoSignedURL(t *testing.T) {
	transport := newMicrosoftTransport()
	transport.set("GET", "https://medius.microsoft.com/Embed/video-nc/9640d86c-f513-4889-959e-5dace86e7d2b",
		http.StatusOK, []byte(`<!DOCTYPE html><html><head><meta property="og:title" content="broken"></head>
<body><script>var StreamUrl = "https://attacker.example.com/secret-path-with-token=value";</script></body></html>`))
	ext := NewMicrosoftMedius()
	_, err := ext.Extract(context.Background(), Request{
		URL:       "https://medius.microsoft.com/Embed/video-nc/9640d86c-f513-4889-959e-5dace86e7d2b",
		Transport: transport,
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if strings.Contains(err.Error(), "secret-path-with-token") {
		t.Fatalf("error leaked signed URL token: %v", err)
	}
}

func TestMicrosoftEmbedRequiresIsolatedTransport(t *testing.T) {
	ext := NewMicrosoftEmbed()
	_, err := ext.Extract(context.Background(), Request{
		URL: "https://www.microsoft.com/en-us/videoplayer/embed/RWL07e",
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("missing transport should be unsupported: %v", err)
	}
}

// --- Host policy boundary for reentry targets ------------------------------

func TestMicrosoftLearnSessionExternalMustBeMediusRoute(t *testing.T) {
	valid, _ := url.Parse("https://medius.microsoft.com/Embed/video-nc/9640d86c-f513-4889-959e-5dace86e7d2b")
	if !microsoftMediusSuitable(valid) {
		t.Fatalf("valid URL should satisfy microsoftMediusSuitable")
	}
	invalid, _ := url.Parse("https://attacker.example.com/Embed/video-nc/uuid")
	if microsoftMediusSuitable(invalid) {
		t.Fatalf("invalid URL must not satisfy microsoftMediusSuitable")
	}
}

func TestMicrosoftBuildOnDemandMustBeMediusRoute(t *testing.T) {
	mediusID := "9640d86c-f513-4889-959e-5dace86e7d2b"
	transport := newMicrosoftTransport()
	transport.set("GET", "https://api-v2.build.microsoft.com/api/session/all/en-US",
		http.StatusOK, []byte(`[{"sessionId":"`+mediusID+`","title":"x","onDemand":"https://medius.microsoft.com/Embed/VideoDetails/`+mediusID+`"}]`))
	extraction, err := NewMicrosoftBuild().Extract(context.Background(), Request{
		URL:       "https://build.microsoft.com/en-US/sessions/" + mediusID,
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !extraction.IsURL() {
		t.Fatalf("expected URLResult")
	}
	if !strings.Contains(extraction.Redirect.URL, "/Embed/VideoDetails/"+mediusID) {
		t.Fatalf("redirect url=%q", extraction.Redirect.URL)
	}
}
