package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/network"
)

// microsoftProductRoundTripper intercepts every request the product
// layer makes on behalf of a Microsoft extractor and serves a
// deterministic synthetic response based on the URL path. It records
// every captured request so tests can assert credential isolation and
// dispatch behavior.
type microsoftProductRoundTripper struct {
	mu         sync.Mutex
	manifests  map[string][]byte
	segments   map[string][]byte
	pages      map[string][]byte
	apiJSON    map[string][]byte
	requests   []capturedMicrosoftProductRequest
	statuses   map[string]int    // "METHOD path" -> status code
	redirectTo map[string]string // path -> synthetic path to fetch instead
}

type capturedMicrosoftProductRequest struct {
	method  string
	path    string
	headers http.Header
}

func (rt *microsoftProductRoundTripper) setManifest(path string, body []byte) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.manifests == nil {
		rt.manifests = make(map[string][]byte)
	}
	rt.manifests[path] = body
}

func (rt *microsoftProductRoundTripper) setSegment(path string, body []byte) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.segments == nil {
		rt.segments = make(map[string][]byte)
	}
	rt.segments[path] = body
}

func (rt *microsoftProductRoundTripper) setPage(path string, body []byte) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.pages == nil {
		rt.pages = make(map[string][]byte)
	}
	rt.pages[path] = body
}

func (rt *microsoftProductRoundTripper) setAPI(path string, body []byte) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.apiJSON == nil {
		rt.apiJSON = make(map[string][]byte)
	}
	rt.apiJSON[path] = body
}

func (rt *microsoftProductRoundTripper) requestPaths() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	paths := make([]string, 0, len(rt.requests))
	for _, r := range rt.requests {
		paths = append(paths, r.method+" "+r.path)
	}
	return paths
}

func (rt *microsoftProductRoundTripper) requestHeaders(method, path string) []http.Header {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var out []http.Header
	for _, r := range rt.requests {
		if r.method == method && r.path == path {
			out = append(out, r.headers)
		}
	}
	return out
}

func (rt *microsoftProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.requests = append(rt.requests, capturedMicrosoftProductRequest{
		method: request.Method, path: request.URL.Path, headers: request.Header.Clone(),
	})
	manifests := rt.manifests
	segments := rt.segments
	pages := rt.pages
	apiJSON := rt.apiJSON
	statuses := rt.statuses
	rt.mu.Unlock()

	respond := func(status int, contentType string, body []byte) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Content-Type", contentType)
		header.Set("Content-Length", itoa(len(body)))
		return &http.Response{
			StatusCode: status,
			Header:     header,
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    request,
		}, nil
	}
	if v, ok := statuses[request.Method+" "+request.URL.Path]; ok {
		return respond(v, "text/plain", []byte("error"))
	}
	if b, ok := manifests[request.URL.Path]; ok {
		return respond(http.StatusOK, contentTypeFor(request.URL.Path), b)
	}
	if b, ok := apiJSON[request.URL.Path]; ok {
		return respond(http.StatusOK, "application/json", b)
	}
	if b, ok := pages[request.URL.Path]; ok {
		return respond(http.StatusOK, "text/html", b)
	}
	// Match segments by suffix since ISM/HLS/DASH segments are resolved
	// relative to the manifest URL. The synthetic suffix may use
	// "{bitrate}" as a placeholder for the resolved bitrate token.
	for suffix, body := range segments {
		// Substitute the "{bitrate}" placeholder with a regex class so the
		// resolved bitrate token can match any digits.
		pattern := regexp.QuoteMeta(suffix)
		pattern = strings.ReplaceAll(pattern, regexp.QuoteMeta("{bitrate}"), "[0-9]+")
		matched, err := regexp.MatchString(pattern+"$", request.URL.Path)
		if err == nil && matched {
			return respond(http.StatusOK, contentTypeFor(request.URL.Path), body)
		}
	}
	return respond(http.StatusNotFound, "text/plain", []byte("not found"))
}

func contentTypeFor(path string) string {
	switch {
	case strings.HasSuffix(path, ".ism") || strings.Contains(path, "manifest.ism"):
		return "application/vnd.ms-sstr+xml"
	case strings.HasSuffix(path, ".m3u8"):
		return "application/vnd.apple.mpegurl"
	case strings.HasSuffix(path, ".mpd"):
		return "application/dash+xml"
	case strings.HasSuffix(path, ".vtt"):
		return "text/vtt"
	case strings.HasSuffix(path, ".ts"):
		return "video/mp2t"
	case strings.HasSuffix(path, ".m4s"):
		return "video/iso.segment"
	case strings.HasSuffix(path, ".mp4"):
		return "video/mp4"
	case strings.HasSuffix(path, ".mp3"):
		return "audio/mpeg"
	case strings.HasSuffix(path, ".jpg"), strings.HasSuffix(path, ".png"):
		return "image/jpeg"
	}
	return "application/octet-stream"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := make([]byte, 0, 16)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}
	if neg {
		digits = append(digits, '-')
	}
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	return string(digits)
}

const (
	microsoftISMManifest = `<?xml version="1.0" encoding="UTF-8"?>
<SmoothStreamingMedia MajorVersion="2" MinorVersion="2" Duration="60000000" TimeScale="10000000">
  <StreamIndex Type="video" Name="video" Chunks="2" TimeScale="10000000" Url="QualityLevels({bitrate})/Fragments(video={start time})">
    <QualityLevel Index="0" Bitrate="1500000" FourCC="H264" MaxWidth="1280" MaxHeight="720" />
    <c d="30000000" />
    <c d="30000000" />
  </StreamIndex>
</SmoothStreamingMedia>`

	microsoftISMVideoSegment = `synthetic-microsoft-ism-video-fragment`
	microsoftISMAudioSegment = `synthetic-microsoft-ism-audio-fragment`
)

func microsoftISMSegmentStub(rt *microsoftProductRoundTripper, prefix string) {
	for _, fragment := range []struct {
		path string
		body string
	}{
		{prefix + "/manifest.ism/QualityLevels(1500000)/Fragments(video=0)", microsoftISMVideoSegment},
		{prefix + "/manifest.ism/QualityLevels(1500000)/Fragments(video=30000000)", microsoftISMVideoSegment},
		{prefix + "/manifest.ism/QualityLevels(128000)/Fragments(audio=0)", microsoftISMAudioSegment},
		{prefix + "/manifest.ism/QualityLevels(128000)/Fragments(audio=30000000)", microsoftISMAudioSegment},
	} {
		rt.setSegment(fragment.path, []byte(fragment.body))
	}
}

func newMicrosoftEmbedOperation(t *testing.T, rt *microsoftProductRoundTripper) (*operation, string) {
	t.Helper()
	transport, err := network.New(network.Config{
		DefaultHeaders: http.Header{
			"Authorization":       {"Bearer ambient-secret"},
			"Cookie":              {"session=ambient-secret"},
			"Proxy-Authorization": {"Basic ambient-secret"},
			"Referer":             {"https://page.example/ambient"},
		},
		RoundTripper: rt,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)
	root := t.TempDir()
	request := Request{
		URL:            "https://www.microsoft.com/en-us/videoplayer/embed/RWL07e",
		OutputDir:      root,
		OutputTemplate: "%(id)s.%(ext)s",
		Overwrite:      true,
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	rootExtractor := ""
	// Empty Format selector selects all formats, which lets the
	// sorter pick the direct HTTPS MP4 without invoking ffmpeg.
	return &operation{
		client:        newBroadTestClient(),
		request:       request,
		transport:     transport,
		registry:      productRuntime(),
		compatibility: compatibility,
		rootExtractor: &rootExtractor,
	}, root
}

func assertMicrosoftCredentialIsolation(t *testing.T, rt *microsoftProductRoundTripper, scopedReferers map[string]string) {
	t.Helper()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	credentialKeys := []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"}
	for _, req := range rt.requests {
		for _, key := range credentialKeys {
			got := req.headers.Get(key)
			want := ""
			if key == "Referer" {
				want = scopedReferers[req.path]
			}
			if got != want {
				t.Fatalf("%s on %s %s = %q, want %q", key, req.method, req.path, got, want)
			}
		}
	}
}

// --- Microsoft Embed: full product pipeline ISM extraction and metadata ----

func TestProductMicrosoftEmbedDirectMP4DownloadStripsAmbientCredentials(t *testing.T) {
	rt := &microsoftProductRoundTripper{}
	// Direct MP4 endpoint serves a deterministic byte payload. The ISM
	// path is exercised by the no-artifacts tests; this test focuses on
	// proving credential isolation on the manifest + direct media path
	// without depending on ffmpeg being available in the test runtime.
	rt.setAPI("/vhs/api/videos/RWL07e", []byte(`{
  "streams":{
    "high_bitrate_MP4":{"url":"https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/mp4/high.mp4","widthPixels":1920,"heightPixels":1080}
  },
  "captions":{},
  "snippet":{"title":"MP4 Download Test","activeStartDate":"/Date(1631658316000)/","minimumAge":0,"thumbnails":[]}
}`))
	rt.setManifest("/vhs/mp4/high.mp4", []byte("microsoft-mp4-direct-bytes"))

	operation, root := newMicrosoftEmbedOperation(t, rt)
	result, runErr := operation.process(context.Background(), operation.request.URL, "", nil, make(map[string]bool), 0)
	if runErr != nil {
		t.Fatalf("process: %v", runErr)
	}
	if !result.Downloaded || result.Filename == "" {
		t.Fatalf("root result was not downloaded: %+v", result)
	}
	if result.Bytes != int64(len("microsoft-mp4-direct-bytes")) {
		t.Fatalf("downloaded bytes = %d", result.Bytes)
	}
	// Verify the direct MP4 request was issued and downloaded.
	if len(rt.requestHeaders("GET", "/vhs/mp4/high.mp4")) == 0 {
		t.Fatalf("expected direct MP4 request, got paths: %v", rt.requestPaths())
	}
	// Verify no ambient credentials leaked to any request.
	assertMicrosoftCredentialIsolation(t, rt, nil)
	got, err := os.ReadFile(result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte("microsoft-mp4-direct-bytes"); !bytes.Equal(got, want) {
		t.Fatalf("output bytes = %q, want %q", got, want)
	}
	if !strings.HasPrefix(result.Filename, root+string(os.PathSeparator)) {
		t.Fatalf("output filename %q is outside %q", result.Filename, root)
	}
}

func TestProductMicrosoftEmbedSidecarsStripAmbientCredentials(t *testing.T) {
	rt := &microsoftProductRoundTripper{}
	rt.setAPI("/vhs/api/videos/RWL07e", []byte(`{
  "streams":{"high_bitrate_MP4":{"url":"https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/mp4/high.mp4"}},
  "captions":{"en":{"url":"https://mediusimg.event.microsoft.com/captions/en.vtt"}},
  "snippet":{"title":"Sidecar Isolation Test","minimumAge":0,"thumbnails":[{"url":"https://img-prod-cms-rt-microsoft-com.akamaized.net/thumb.jpg","width":640,"height":360}]}
}`))
	rt.setManifest("/vhs/mp4/high.mp4", []byte("microsoft-sidecar-media"))
	rt.setManifest("/captions/en.vtt", []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nfixture\n"))
	rt.setManifest("/thumb.jpg", []byte("synthetic-microsoft-thumbnail"))
	operation, _ := newMicrosoftEmbedOperation(t, rt)
	operation.request.Subtitles = SubtitleOptions{WriteManual: true, Languages: []string{"en"}}
	operation.request.Thumbnails = ThumbnailOptions{Write: true}
	result, runErr := operation.process(context.Background(), operation.request.URL, "", nil, make(map[string]bool), 0)
	if runErr != nil || !result.Downloaded {
		t.Fatalf("result = %+v, error = %v", result, runErr)
	}
	for _, sidecarPath := range []string{"/captions/en.vtt", "/thumb.jpg"} {
		if len(rt.requestHeaders("GET", sidecarPath)) == 0 {
			t.Fatalf("expected sidecar request %s, got paths: %v", sidecarPath, rt.requestPaths())
		}
	}
	assertMicrosoftCredentialIsolation(t, rt, nil)
}

func TestProductMicrosoftEmbedHLSAndDASHDownloadStripsAmbientCredentials(t *testing.T) {
	for _, test := range []struct {
		name       string
		streamKey  string
		streamURL  string
		format     string
		configure  func(*microsoftProductRoundTripper)
		wantOutput string
	}{
		{
			name: "HLS", streamKey: "apple_HTTP_Live_Streaming",
			streamURL: "https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/hls/master.m3u8", format: "hls",
			configure: func(rt *microsoftProductRoundTripper) {
				rt.setManifest("/vhs/hls/master.m3u8", []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nmedia.m3u8\n"))
				rt.setManifest("/vhs/hls/media.m3u8", []byte("#EXTM3U\n#EXTINF:1,\none.bin\n#EXTINF:1,\ntwo.bin\n#EXT-X-ENDLIST\n"))
				rt.setSegment("/vhs/hls/one.bin", []byte("microsoft-hls-one-"))
				rt.setSegment("/vhs/hls/two.bin", []byte("microsoft-hls-two"))
			},
			wantOutput: "microsoft-hls-one-microsoft-hls-two",
		},
		{
			name: "DASH", streamKey: "mPEG_DASH",
			streamURL: "https://prod-video-cms-rt-microsoft-com.akamaized.net/vhs/dash/manifest.mpd", format: "dash",
			configure: func(rt *microsoftProductRoundTripper) {
				rt.setManifest("/vhs/dash/manifest.mpd", []byte(`<MPD type="static" mediaPresentationDuration="PT2S"><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="fixture" bandwidth="1000"><SegmentTemplate duration="1" initialization="init.bin" media="$Number$.bin"/></Representation></AdaptationSet></Period></MPD>`))
				rt.setSegment("/vhs/dash/init.bin", []byte("microsoft-dash-init-"))
				rt.setSegment("/vhs/dash/1.bin", []byte("microsoft-dash-one-"))
				rt.setSegment("/vhs/dash/2.bin", []byte("microsoft-dash-two"))
			},
			wantOutput: "microsoft-dash-init-microsoft-dash-one-microsoft-dash-two",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt := &microsoftProductRoundTripper{}
			rt.setAPI("/vhs/api/videos/RWL07e", []byte(fmt.Sprintf(`{
  "streams":{%q:{"url":%q}},
  "captions":{},
  "snippet":{"title":"%s Download Test","minimumAge":0,"thumbnails":[]}
}`, test.streamKey, test.streamURL, test.name)))
			test.configure(rt)
			operation, _ := newMicrosoftEmbedOperation(t, rt)
			operation.request.Format = test.format
			result, runErr := operation.process(context.Background(), operation.request.URL, "", nil, make(map[string]bool), 0)
			if runErr != nil {
				t.Fatalf("process: %v", runErr)
			}
			if !result.Downloaded || result.Filename == "" || result.Bytes != int64(len(test.wantOutput)) {
				t.Fatalf("result = %+v", result)
			}
			got, readErr := os.ReadFile(result.Filename)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != test.wantOutput {
				t.Fatalf("output = %q, want %q", got, test.wantOutput)
			}
			assertMicrosoftCredentialIsolation(t, rt, nil)
		})
	}
}

// --- Microsoft Learn Session transparent reentry to Medius ---------------

func TestProductMicrosoftLearnSessionReentryDownload(t *testing.T) {
	rt := &microsoftProductRoundTripper{}
	mediusUUID := "9640d86c-f513-4889-959e-5dace86e7d2b"
	rt.setPage("/en-us/events/build-2022/"+mediusUUID, []byte(`<!DOCTYPE html><html><head>
<meta name="externalVideoUrl" content="https://medius.microsoft.com/Embed/video-nc/`+mediusUUID+`">
</head><body></body></html>`))
	rt.setPage("/Embed/video-nc/"+mediusUUID, []byte(`<!DOCTYPE html><html><head>
<meta property="og:title" content="Medius reentry test">
<meta property="og:image" content="https://mediusimg.event.microsoft.com/thumb.jpg">
<script>
var StreamUrl = "https://mediusimg.event.microsoft.com/medius/manifest.ism/manifest";
const captionsConfiguration = {"languageList":[{"src":"https://mediusimg.event.microsoft.com/captions/en.vtt","srclang":"en","kind":"English"}]};
</script>
</head><body></body></html>`))
	rt.setManifest("/medius/manifest.ism/manifest", []byte(microsoftISMManifest))
	microsoftISMSegmentStub(rt, "/medius")

	transport, err := network.New(network.Config{
		DefaultHeaders: http.Header{
			"Authorization":       {"Bearer ambient-secret"},
			"Cookie":              {"session=ambient-secret"},
			"Proxy-Authorization": {"Basic ambient-secret"},
			"Referer":             {"https://page.example/ambient"},
		},
		RoundTripper: rt,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)

	root := t.TempDir()
	request := Request{
		URL:            "https://learn.microsoft.com/en-us/events/build-2022/" + mediusUUID,
		OutputDir:      root,
		OutputTemplate: "%(id)s.%(ext)s",
		Overwrite:      true,
		Format:         "ism",
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	rootExtractor := ""
	operation := &operation{
		client:        newBroadTestClient(),
		request:       request,
		transport:     transport,
		registry:      productRuntime(),
		compatibility: compatibility,
		rootExtractor: &rootExtractor,
	}
	result, runErr := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if runErr != nil {
		t.Fatalf("process: %v", runErr)
	}
	if !result.Downloaded || result.Filename == "" {
		t.Fatalf("root result was not downloaded: %+v", result)
	}
	wantBytes := []byte(strings.Repeat(microsoftISMVideoSegment, 2))
	gotBytes, readErr := os.ReadFile(result.Filename)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(gotBytes, wantBytes) {
		t.Fatalf("Learn output bytes = %q, want %q", gotBytes, wantBytes)
	}
	// Verify the explicit Learn-session Referer propagated ONLY to the
	// Medius fetch and nowhere else.
	mediusHeaders := rt.requestHeaders("GET", "/Embed/video-nc/"+mediusUUID)
	wantReferer := request.URL
	if len(mediusHeaders) != 1 {
		t.Fatalf("Medius discovery requests = %d, want 1", len(mediusHeaders))
	}
	if mediusHeaders[0].Get("Referer") != wantReferer {
		t.Fatalf("Medius discovery Referer = %q, want %q", mediusHeaders[0].Get("Referer"), wantReferer)
	}
	assertMicrosoftCredentialIsolation(t, rt, map[string]string{
		"/Embed/video-nc/" + mediusUUID: wantReferer,
	})
}

// --- Microsoft Build: playlist then reentry to Medius ---------------------

func TestProductMicrosoftBuildPlaylistToMediusReentry(t *testing.T) {
	rt := &microsoftProductRoundTripper{}
	mediusUUID1 := "9640d86c-f513-4889-959e-5dace86e7d2b"
	mediusUUID2 := "aee55fb5-fcf9-4b38-b764-a3527cb57554"
	// Build API response goes to a different path; no LearnSession page needed.
	rt.setAPI("/api/session/all/en-US", []byte(fmt.Sprintf(`[
  {"sessionId":"%s","title":"Build Session 1","startDateTime":"2024-05-21T16:00:00Z","onDemand":"https://medius.microsoft.com/Embed/video-nc/%s"},
  {"sessionId":"%s","title":"Build Session 2","startDateTime":"2024-05-21T16:00:00Z","onDemand":"https://medius.microsoft.com/Embed/video-nc/%s"}
]`, mediusUUID1, mediusUUID1, mediusUUID2, mediusUUID2)))
	for _, u := range []string{mediusUUID1, mediusUUID2} {
		rt.setPage("/Embed/video-nc/"+u, []byte(fmt.Sprintf(`<!DOCTYPE html><html><head>
<meta property="og:title" content="Build session %s">
<meta property="og:image" content="https://mediusimg.event.microsoft.com/thumb.jpg">
<script>
var StreamUrl = "https://mediusimg.event.microsoft.com/ism/manifest.ism/manifest";
const captionsConfiguration = {"languageList":[{"src":"https://mediusimg.event.microsoft.com/captions/en.vtt","srclang":"en","kind":"English"}]};
</script></head><body></body></html>`, u)))
		rt.setManifest("/ism/manifest.ism/manifest", []byte(microsoftISMManifest))
		microsoftISMSegmentStub(rt, "/ism")
	}

	transport, err := network.New(network.Config{
		DefaultHeaders: http.Header{
			"Authorization":       {"Bearer ambient-secret"},
			"Cookie":              {"session=ambient-secret"},
			"Proxy-Authorization": {"Basic ambient-secret"},
			"Referer":             {"https://page.example/ambient"},
		},
		RoundTripper: rt,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)

	root := t.TempDir()
	request := Request{
		URL:            "https://build.microsoft.com/en-US/sessions",
		OutputDir:      root,
		OutputTemplate: "%(id)s.%(ext)s",
		Overwrite:      true,
		Format:         "ism",
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	rootExtractor := ""
	operation := &operation{
		client:        newBroadTestClient(),
		request:       request,
		transport:     transport,
		registry:      productRuntime(),
		compatibility: compatibility,
		rootExtractor: &rootExtractor,
	}
	result, runErr := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if runErr != nil {
		t.Fatalf("process: %v", runErr)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(result.Entries), result)
	}
	if result.SuppressedFailures != 0 {
		t.Fatalf("suppressed failures = %d, want 0", result.SuppressedFailures)
	}
	wantBytes := []byte(strings.Repeat(microsoftISMVideoSegment, 2))
	for i, child := range result.Entries {
		if !child.Downloaded || child.Filename == "" {
			t.Fatalf("child %d was not downloaded: %+v", i, child)
		}
		gotBytes, readErr := os.ReadFile(child.Filename)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(gotBytes, wantBytes) {
			t.Fatalf("child %d bytes = %q, want %q", i, gotBytes, wantBytes)
		}
	}
	buildReferer := request.URL
	assertMicrosoftCredentialIsolation(t, rt, map[string]string{
		"/Embed/video-nc/" + mediusUUID1: buildReferer,
		"/Embed/video-nc/" + mediusUUID2: buildReferer,
	})
}

// --- Failure / cancellation leaves zero artifacts -------------------------

func TestProductMicrosoftEmbedNoArtifactsOnFailure(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		status       int
		wantCategory ErrorCategory
	}{
		{"API-302", http.StatusFound, ErrorInternal},
		{"API-401", http.StatusUnauthorized, ErrorAuthentication},
		{"API-403", http.StatusForbidden, ErrorUnsupported},
		{"API-404", http.StatusNotFound, ErrorUnsupported},
		{"API-410", http.StatusGone, ErrorUnsupported},
		{"API-451", http.StatusUnavailableForLegalReasons, ErrorUnsupported},
		{"API-500", http.StatusInternalServerError, ErrorNetwork},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			rt := &microsoftProductRoundTripper{statuses: map[string]int{
				"GET /vhs/api/videos/RWL07e": scenario.status,
			}}
			transport, err := network.New(network.Config{RoundTripper: rt})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.CloseIdleConnections()
			root := t.TempDir()
			request := Request{
				URL:            "https://www.microsoft.com/en-us/videoplayer/embed/RWL07e",
				OutputDir:      root,
				OutputTemplate: "%(id)s.%(ext)s",
				Overwrite:      true,
				Format:         "ism",
			}
			compatibility, err := prepareCompatibility(request)
			if err != nil {
				t.Fatal(err)
			}
			rootExtractor := ""
			operation := &operation{
				client:        newBroadTestClient(),
				request:       request,
				transport:     transport,
				registry:      productRuntime(),
				compatibility: compatibility,
				rootExtractor: &rootExtractor,
			}
			_, runErr := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
			if runErr == nil {
				t.Fatalf("expected failure")
			}
			if !IsCategory(runErr, scenario.wantCategory) {
				t.Fatalf("category for HTTP %d = %v, want %s", scenario.status, runErr, scenario.wantCategory)
			}
			if paths := rt.requestPaths(); len(paths) != 1 || paths[0] != "GET /vhs/api/videos/RWL07e" {
				t.Fatalf("requests = %v", paths)
			}
			entries, err := listDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("artifacts remain after failure: %v", entries)
			}
		})
	}
}

func TestProductMicrosoftEmbedCancellationLeavesNoArtifacts(t *testing.T) {
	rt := &microsoftProductRoundTripper{}
	operation, root := newMicrosoftEmbedOperation(t, rt)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, runErr := operation.process(ctx, operation.request.URL, "", nil, make(map[string]bool), 0)
	if !IsCategory(runErr, ErrorCancelled) {
		t.Fatalf("cancellation error = %v", runErr)
	}
	if paths := rt.requestPaths(); len(paths) != 0 {
		t.Fatalf("canceled operation made requests: %v", paths)
	}
	entries, err := listDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("artifacts remain after cancellation: %v", entries)
	}
}

func listDir(dir string) ([]os.DirEntry, error) {
	return os.ReadDir(dir)
}
