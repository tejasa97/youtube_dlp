package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/downloader"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/protocol/hls"
)

const (
	radioFranceFixtureAudioBytes   = "fixture-radiofrance-audio"
	radioFranceFixtureSegmentBytes = "fixture-radiofrance-segment"
)

type radioFranceProductRoundTripper struct {
	mu             sync.Mutex
	requests       map[string][]http.Header
	fixtures       map[string][]byte
	liveFixture    string
	manifestStatus int
	segmentStatus  int
	directStatus   int
	blockSegment   bool
	segmentEntered chan struct{}
}

func readRadioFranceProductFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("../conformance/extractors/risk/radiofrance/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func newRadioFranceProductRoundTripper(t *testing.T) *radioFranceProductRoundTripper {
	t.Helper()
	return &radioFranceProductRoundTripper{
		fixtures: map[string][]byte{
			"episode.html":                readRadioFranceProductFixture(t, "episode.html"),
			"episode_child.html":          readRadioFranceProductFixture(t, "episode_child.html"),
			"episode_schedule_child.html": readRadioFranceProductFixture(t, "episode_schedule_child.html"),
			"live.json":                   readRadioFranceProductFixture(t, "live.json"),
			"live_hls_only.json":          readRadioFranceProductFixture(t, "live_hls_only.json"),
			"master.m3u8":                 readRadioFranceProductFixture(t, "master.m3u8"),
			"path_podcast.json":           readRadioFranceProductFixture(t, "path_podcast.json"),
			"expressions_page2.json":      readRadioFranceProductFixture(t, "expressions_page2.json"),
			"schedule.html":               readRadioFranceProductFixture(t, "schedule.html"),
		},
	}
}

func (transport *radioFranceProductRoundTripper) liveBody() []byte {
	key := transport.liveFixture
	if key == "" {
		key = "live.json"
	}
	return transport.fixtures[key]
}

func (transport *radioFranceProductRoundTripper) recordRequest(request *http.Request) string {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if transport.requests == nil {
		transport.requests = make(map[string][]http.Header)
	}
	key := request.Method + " " + request.URL.Host + request.URL.Path
	if request.URL.RawQuery != "" {
		key += "?" + request.URL.RawQuery
	}
	transport.requests[key] = append(transport.requests[key], request.Header.Clone())
	return key
}

func (transport *radioFranceProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	key := transport.recordRequest(request)

	switch {
	case request.URL.Host == "www.radiofrance.fr" && strings.Contains(request.URL.Path, "/podcasts/science-en-questions/"):
		return productHTMLResponse(transport.fixtures["episode.html"]), nil
	case request.URL.Host == "www.radiofrance.fr" && strings.Contains(request.URL.Path, "/podcasts/le-billet-vert/episode-alpha-100001"):
		return productHTMLResponse(transport.fixtures["episode_child.html"]), nil
	case request.URL.Host == "www.radiofrance.fr" && strings.Contains(request.URL.Path, "/podcasts/fixture-grid/episode-morning-300001"):
		return productHTMLResponse(transport.fixtures["episode_schedule_child.html"]), nil
	case request.URL.Host == "www.radiofrance.fr" && strings.HasSuffix(request.URL.Path, "/api/live"):
		return productJSONResponse(transport.liveBody()), nil
	case request.URL.Host == "www.radiofrance.fr" && request.URL.Path == "/api/v2.1/path":
		return productJSONResponse(transport.fixtures["path_podcast.json"]), nil
	case request.URL.Host == "www.radiofrance.fr" && strings.Contains(request.URL.Path, "/expressions"):
		return productJSONResponse(transport.fixtures["expressions_page2.json"]), nil
	case request.URL.Host == "www.radiofrance.fr" && strings.Contains(request.URL.Path, "grille-programmes"):
		return productHTMLResponse(transport.fixtures["schedule.html"]), nil
	case request.URL.Host == "icecast.radiofrance.fr" && strings.HasSuffix(request.URL.Path, ".m3u8"):
		status := transport.manifestStatus
		if status == 0 {
			status = http.StatusOK
		}
		body := transport.fixtures["master.m3u8"]
		if status != http.StatusOK {
			body = nil
		}
		return productResponse(status, "application/vnd.apple.mpegurl", body), nil
	case request.URL.Host == "icecast.radiofrance.fr" && strings.HasSuffix(request.URL.Path, ".aac"):
		if transport.blockSegment {
			if transport.segmentEntered != nil {
				select {
				case transport.segmentEntered <- struct{}{}:
				default:
				}
			}
			<-request.Context().Done()
			return nil, request.Context().Err()
		}
		status := transport.segmentStatus
		if status == 0 {
			status = http.StatusOK
		}
		body := []byte(radioFranceFixtureSegmentBytes)
		if status != http.StatusOK {
			body = nil
		}
		return productResponse(status, "audio/aac", body), nil
	case request.URL.Host == "audio-mp3.radiofrance.fr":
		status := transport.directStatus
		if status == 0 {
			status = http.StatusOK
		}
		body := []byte(radioFranceFixtureAudioBytes)
		if status != http.StatusOK {
			body = nil
		}
		return productResponse(status, "audio/mpeg", body), nil
	default:
		return nil, errors.New("unexpected Radio France product request: " + request.URL.String() + " key=" + key)
	}
}

func newRadioFranceProductTransport(t *testing.T, roundTripper *radioFranceProductRoundTripper) *network.Client {
	t.Helper()
	transport, err := network.New(network.Config{
		RoundTripper:   roundTripper,
		DefaultHeaders: credentialIsolationHeaders(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)
	return transport
}

func assertRadioFranceAmbientCredentialsAbsent(t *testing.T, requests map[string][]http.Header, keys ...string) {
	t.Helper()
	for _, key := range keys {
		headers := requests[key]
		if len(headers) == 0 {
			t.Fatalf("missing request %s in %#v", key, requests)
		}
		for _, header := range headers {
			for _, name := range credentialHeaders {
				if got := header.Get(name); got != "" {
					t.Fatalf("%s leaked %s=%q", key, name, got)
				}
			}
		}
	}
}

func assertRadioFranceHTTPStatusError(t *testing.T, err error, code int) {
	t.Helper()
	var statusErr *downloader.HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.Code != code {
		t.Fatalf("error=%v want *downloader.HTTPStatusError code=%d", err, code)
	}
}

func assertRadioFranceOutputDirEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("artifacts remain in %s: %v", root, entries)
	}
}

func runRadioFranceProductDownload(t *testing.T, transport *network.Client, request Request) (Result, error) {
	t.Helper()
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
	return operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
}

func TestProductRegistryRoutesRadioFranceFamily(t *testing.T) {
	registry := productRegistry()
	for _, test := range []struct{ rawURL, want string }{
		{"http://maison.radiofrance.fr/radiovisions/one-one", "radiofrance"},
		{"https://www.radiofrance.fr/franceculture/podcasts/science-en-questions/la-physique-d-einstein-8440487", "franceculture"},
		{"https://www.radiofrance.fr/franceinfo/podcasts/le-billet-vert", "radiofrance_podcast"},
		{"https://www.radiofrance.fr/personnes/thomas-pesquet", "radiofrance_profile"},
		{"https://www.radiofrance.fr/franceinter/grille-programmes?date=17-02-2023", "radiofrance_program_schedule"},
		{"https://www.radiofrance.fr/franceinter/", "radiofrance_live"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q) = %v err=%v want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func TestProductFranceCultureDirectAudioDownloadCredentialIsolation(t *testing.T) {
	roundTripper := newRadioFranceProductRoundTripper(t)
	transport := newRadioFranceProductTransport(t, roundTripper)

	root := t.TempDir()
	request := Request{
		URL:            "https://www.radiofrance.fr/franceculture/podcasts/science-en-questions/la-physique-d-einstein-8440487",
		OutputDir:      root,
		OutputTemplate: "%(id)s.%(ext)s",
		Overwrite:      true,
	}
	result, err := runRadioFranceProductDownload(t, transport, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || result.Filename == "" {
		t.Fatalf("result=%+v", result)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil || string(data) != radioFranceFixtureAudioBytes {
		t.Fatalf("bytes=%q err=%v", data, err)
	}
	roundTripper.mu.Lock()
	defer roundTripper.mu.Unlock()
	assertRadioFranceAmbientCredentialsAbsent(t, roundTripper.requests,
		"GET www.radiofrance.fr/franceculture/podcasts/science-en-questions/la-physique-d-einstein-8440487",
		"GET audio-mp3.radiofrance.fr/fixture/episode-8440487.mp3",
	)
}

func TestProductRadioFranceLiveHLSDownloadCredentialIsolation(t *testing.T) {
	roundTripper := newRadioFranceProductRoundTripper(t)
	roundTripper.liveFixture = "live_hls_only.json"
	transport := newRadioFranceProductTransport(t, roundTripper)

	root := t.TempDir()
	request := Request{
		URL:            "https://www.radiofrance.fr/franceinter/",
		OutputDir:      root,
		OutputTemplate: "%(id)s.%(ext)s",
		Format:         "hls-0",
		Overwrite:      true,
	}
	result, err := runRadioFranceProductDownload(t, transport, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || result.Filename == "" {
		t.Fatalf("result=%+v", result)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil || string(data) != radioFranceFixtureSegmentBytes {
		t.Fatalf("bytes=%q err=%v", data, err)
	}
	roundTripper.mu.Lock()
	defer roundTripper.mu.Unlock()
	manifestKey := "GET icecast.radiofrance.fr/franceinter/live.m3u8"
	segmentKey := "GET icecast.radiofrance.fr/franceinter/segment-000.aac"
	directKey := "GET icecast.radiofrance.fr/franceinter/live.aac"
	if len(roundTripper.requests[manifestKey]) == 0 {
		t.Fatalf("missing HLS manifest request: %#v", roundTripper.requests)
	}
	if len(roundTripper.requests[segmentKey]) == 0 {
		t.Fatalf("missing HLS segment request: %#v", roundTripper.requests)
	}
	if len(roundTripper.requests[directKey]) != 0 {
		t.Fatalf("direct AAC must not download when Format=hls-0: %#v", roundTripper.requests)
	}
	assertRadioFranceAmbientCredentialsAbsent(t, roundTripper.requests,
		"GET www.radiofrance.fr/franceinter/api/live",
		manifestKey,
		segmentKey,
	)
}

func TestProductRadioFranceLiveDirectAudioDownloadCredentialIsolation(t *testing.T) {
	roundTripper := newRadioFranceProductRoundTripper(t)
	transport := newRadioFranceProductTransport(t, roundTripper)

	root := t.TempDir()
	request := Request{
		URL:            "https://www.radiofrance.fr/franceinter/",
		OutputDir:      root,
		OutputTemplate: "%(id)s.%(ext)s",
		Format:         "direct-1",
		Overwrite:      true,
	}
	result, err := runRadioFranceProductDownload(t, transport, request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || result.Filename == "" {
		t.Fatalf("result=%+v", result)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil || string(data) != radioFranceFixtureSegmentBytes {
		t.Fatalf("bytes=%q err=%v", data, err)
	}
	roundTripper.mu.Lock()
	defer roundTripper.mu.Unlock()
	manifestKey := "GET icecast.radiofrance.fr/franceinter/live.m3u8"
	directKey := "GET icecast.radiofrance.fr/franceinter/live.aac"
	if len(roundTripper.requests[directKey]) == 0 {
		t.Fatalf("missing direct AAC request: %#v", roundTripper.requests)
	}
	if len(roundTripper.requests[manifestKey]) != 0 {
		t.Fatalf("HLS manifest must not download when Format=direct-1: %#v", roundTripper.requests)
	}
	assertRadioFranceAmbientCredentialsAbsent(t, roundTripper.requests,
		"GET www.radiofrance.fr/franceinter/api/live",
		directKey,
	)
}

func TestProductRadioFrancePodcastPlaylistChildDownload(t *testing.T) {
	roundTripper := newRadioFranceProductRoundTripper(t)
	transport := newRadioFranceProductTransport(t, roundTripper)

	root := t.TempDir()
	request := Request{
		URL:            "https://www.radiofrance.fr/franceinfo/podcasts/le-billet-vert",
		Playlist:       PlaylistOptions{Items: "1"},
		OutputDir:      root,
		OutputTemplate: "%(id)s.%(ext)s",
		Overwrite:      true,
	}
	result, err := runRadioFranceProductDownload(t, transport, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || !result.Entries[0].Downloaded || result.Entries[0].Filename == "" {
		t.Fatalf("result=%+v", result)
	}
	data, err := os.ReadFile(result.Entries[0].Filename)
	if err != nil || string(data) != radioFranceFixtureAudioBytes {
		t.Fatalf("bytes=%q err=%v", data, err)
	}
	roundTripper.mu.Lock()
	defer roundTripper.mu.Unlock()
	for key := range roundTripper.requests {
		if strings.Contains(key, "api/v2.1/path") {
			assertRadioFranceAmbientCredentialsAbsent(t, roundTripper.requests, key)
			break
		}
	}
	assertRadioFranceAmbientCredentialsAbsent(t, roundTripper.requests,
		"GET www.radiofrance.fr/franceinfo/podcasts/le-billet-vert/episode-alpha-100001",
		"GET audio-mp3.radiofrance.fr/fixture/episode-100001.mp3",
	)
}

func TestProductRadioFranceSchedulePlaylistChildDownload(t *testing.T) {
	roundTripper := newRadioFranceProductRoundTripper(t)
	transport := newRadioFranceProductTransport(t, roundTripper)

	root := t.TempDir()
	request := Request{
		URL:            "https://www.radiofrance.fr/franceinter/grille-programmes?date=17-02-2023",
		Playlist:       PlaylistOptions{Items: "1"},
		OutputDir:      root,
		OutputTemplate: "%(id)s.%(ext)s",
		Overwrite:      true,
	}
	result, err := runRadioFranceProductDownload(t, transport, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || !result.Entries[0].Downloaded || result.Entries[0].Filename == "" {
		t.Fatalf("result=%+v", result)
	}
	data, err := os.ReadFile(result.Entries[0].Filename)
	if err != nil || string(data) != radioFranceFixtureAudioBytes {
		t.Fatalf("bytes=%q err=%v", data, err)
	}
	if !strings.Contains(string(result.Entries[0].InfoJSON), `"series_id":"concept-1"`) {
		t.Fatalf("missing schedule series overlay: %s", result.Entries[0].InfoJSON)
	}
	if !strings.Contains(string(result.Entries[0].InfoJSON), `"series":"Fixture Concept"`) {
		t.Fatalf("missing schedule series title overlay: %s", result.Entries[0].InfoJSON)
	}
	roundTripper.mu.Lock()
	defer roundTripper.mu.Unlock()
	assertRadioFranceAmbientCredentialsAbsent(t, roundTripper.requests,
		"GET www.radiofrance.fr/franceinter/grille-programmes?date=17-02-2023",
		"GET www.radiofrance.fr/franceinter/podcasts/fixture-grid/episode-morning-300001",
		"GET audio-mp3.radiofrance.fr/fixture/episode-300001.mp3",
	)
}

func TestProductRadioFranceCancellationAndFailureCleanup(t *testing.T) {
	const (
		manifestKey = "GET icecast.radiofrance.fr/franceinter/live.m3u8"
		segmentKey  = "GET icecast.radiofrance.fr/franceinter/segment-000.aac"
	)
	t.Run("cancel-zero-artifacts", func(t *testing.T) {
		roundTripper := newRadioFranceProductRoundTripper(t)
		roundTripper.liveFixture = "live_hls_only.json"
		roundTripper.blockSegment = true
		roundTripper.segmentEntered = make(chan struct{}, 1)
		transport := newRadioFranceProductTransport(t, roundTripper)
		root := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		request := Request{
			URL:            "https://www.radiofrance.fr/franceinter/",
			OutputDir:      root,
			OutputTemplate: "%(id)s.%(ext)s",
			Format:         "hls-0",
		}
		compatibility, err := prepareCompatibility(request)
		if err != nil {
			t.Fatal(err)
		}
		rootExtractor := ""
		operation := &operation{
			client: newBroadTestClient(), request: request, transport: transport,
			registry: productRuntime(), compatibility: compatibility, rootExtractor: &rootExtractor,
		}
		done := make(chan error, 1)
		go func() {
			_, runErr := operation.process(ctx, request.URL, "", nil, make(map[string]bool), 0)
			done <- runErr
		}()
		select {
		case <-roundTripper.segmentEntered:
		case runErr := <-done:
			t.Fatalf("process finished before segment blocked: %v", runErr)
		}
		cancel()
		runErr := <-done
		if !IsCategory(runErr, ErrorCancelled) || !errors.Is(runErr, context.Canceled) {
			t.Fatalf("cancel error=%v want category=%s identity=%v", runErr, ErrorCancelled, context.Canceled)
		}
		roundTripper.mu.Lock()
		if len(roundTripper.requests[manifestKey]) == 0 {
			t.Fatalf("missing manifest request: %#v", roundTripper.requests)
		}
		if len(roundTripper.requests[segmentKey]) == 0 {
			t.Fatalf("missing segment request: %#v", roundTripper.requests)
		}
		roundTripper.mu.Unlock()
		assertRadioFranceOutputDirEmpty(t, root)
	})
	t.Run("manifest-failure-zero-artifacts", func(t *testing.T) {
		roundTripper := newRadioFranceProductRoundTripper(t)
		roundTripper.liveFixture = "live_hls_only.json"
		roundTripper.manifestStatus = http.StatusInternalServerError
		transport := newRadioFranceProductTransport(t, roundTripper)
		root := t.TempDir()
		request := Request{
			URL:            "https://www.radiofrance.fr/franceinter/",
			OutputDir:      root,
			OutputTemplate: "%(id)s.%(ext)s",
			Format:         "hls-0",
		}
		_, runErr := runRadioFranceProductDownload(t, transport, request)
		if !IsCategory(runErr, ErrorInternal) || !errors.Is(runErr, hls.ErrInvalidPlaylist) {
			t.Fatalf("error=%v want category=%s sentinel=%v", runErr, ErrorInternal, hls.ErrInvalidPlaylist)
		}
		roundTripper.mu.Lock()
		if len(roundTripper.requests[manifestKey]) == 0 {
			t.Fatalf("missing manifest request: %#v", roundTripper.requests)
		}
		if len(roundTripper.requests[segmentKey]) != 0 {
			t.Fatalf("segment must not be requested after manifest failure: %#v", roundTripper.requests)
		}
		roundTripper.mu.Unlock()
		assertRadioFranceOutputDirEmpty(t, root)
	})
	t.Run("segment-failure-zero-artifacts", func(t *testing.T) {
		roundTripper := newRadioFranceProductRoundTripper(t)
		roundTripper.liveFixture = "live_hls_only.json"
		roundTripper.segmentStatus = http.StatusInternalServerError
		transport := newRadioFranceProductTransport(t, roundTripper)
		root := t.TempDir()
		request := Request{
			URL:            "https://www.radiofrance.fr/franceinter/",
			OutputDir:      root,
			OutputTemplate: "%(id)s.%(ext)s",
			Format:         "hls-0",
		}
		_, runErr := runRadioFranceProductDownload(t, transport, request)
		if !IsCategory(runErr, ErrorNetwork) {
			t.Fatalf("error=%v want category=%s", runErr, ErrorNetwork)
		}
		roundTripper.mu.Lock()
		if len(roundTripper.requests[manifestKey]) == 0 {
			t.Fatalf("missing manifest request: %#v", roundTripper.requests)
		}
		if len(roundTripper.requests[segmentKey]) == 0 {
			t.Fatalf("missing segment request: %#v", roundTripper.requests)
		}
		roundTripper.mu.Unlock()
		assertRadioFranceOutputDirEmpty(t, root)
	})
	t.Run("direct-media-failure-zero-artifacts", func(t *testing.T) {
		roundTripper := newRadioFranceProductRoundTripper(t)
		roundTripper.directStatus = http.StatusInternalServerError
		transport := newRadioFranceProductTransport(t, roundTripper)
		root := t.TempDir()
		request := Request{
			URL:            "https://www.radiofrance.fr/franceculture/podcasts/science-en-questions/la-physique-d-einstein-8440487",
			OutputDir:      root,
			OutputTemplate: "%(id)s.%(ext)s",
		}
		_, runErr := runRadioFranceProductDownload(t, transport, request)
		if !IsCategory(runErr, ErrorNetwork) {
			t.Fatalf("error=%v want category=%s", runErr, ErrorNetwork)
		}
		assertRadioFranceHTTPStatusError(t, runErr, http.StatusInternalServerError)
		assertRadioFranceOutputDirEmpty(t, root)
	})
}

func productHTMLResponse(body []byte) *http.Response {
	return productResponse(http.StatusOK, "text/html", body)
}

func productResponse(status int, contentType string, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}
