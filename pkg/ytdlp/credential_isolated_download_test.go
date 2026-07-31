package ytdlp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/fragment"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/youtubelive"
)

var credentialHeaders = []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"}

func credentialIsolationHeaders() http.Header {
	return http.Header{
		"Authorization":       {"Bearer fixture-secret"},
		"Cookie":              {"session=fixture-secret"},
		"Proxy-Authorization": {"Basic fixture-secret"},
		"Referer":             {"https://page.example/fixture"},
	}
}

func newCredentialIsolationTestTransport(t *testing.T, roundTripper http.RoundTripper) *network.Client {
	t.Helper()
	transport, err := network.New(network.Config{
		DefaultHeaders: credentialIsolationHeaders(),
		RoundTripper:   roundTripper,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)
	return transport
}

func assertCredentialHeadersAbsent(t *testing.T, requests map[string][]http.Header, requiredPaths ...string) {
	t.Helper()
	for _, path := range requiredPaths {
		headers := requests[path]
		if len(headers) == 0 {
			t.Errorf("no request captured for %s", path)
			continue
		}
		for _, header := range headers {
			for _, key := range credentialHeaders {
				if got := header.Get(key); got != "" {
					t.Errorf("%s leaked on %s: %q", key, path, got)
				}
			}
		}
	}
}

func TestCredentialIsolatedHLSManifestAndSegmentRequests(t *testing.T) {
	var mu sync.Mutex
	requests := make(map[string][]http.Header)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests[request.URL.Path] = append(requests[request.URL.Path], request.Header.Clone())
		mu.Unlock()
		switch request.URL.Path {
		case "/manifest.m3u8":
			_, _ = io.WriteString(writer, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n")
		case "/segment.ts":
			_, _ = io.WriteString(writer, "isolated-segment")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	transport, err := network.New(network.Config{DefaultHeaders: credentialIsolationHeaders()})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	root := t.TempDir()
	destination := filepath.Join(root, "output.ts")
	operation := &operation{transport: transport, request: Request{OutputDir: root}}
	path, _, err := operation.downloadSelection(context.Background(), mediaformat.Selection{
		URL: server.URL + "/manifest.m3u8", Protocol: "m3u8_native", Ext: "ts",
		Headers: credentialIsolationHeaders(), CredentialIsolated: true,
	}, root, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != destination {
		t.Fatalf("path = %q, want %q", path, destination)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "isolated-segment" {
		t.Fatalf("downloaded data = %q", data)
	}
	mu.Lock()
	defer mu.Unlock()
	assertCredentialHeadersAbsent(t, requests, "/manifest.m3u8", "/segment.ts")
}

type credentialCancellationBody struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (body *credentialCancellationBody) Read(buffer []byte) (int, error) {
	body.once.Do(func() { close(body.entered) })
	<-body.release
	copy(buffer, []byte("generic-partial"))
	return len("generic-partial"), nil
}

func (body *credentialCancellationBody) Close() error { return nil }

func TestCredentialIsolatedDirectCancellationRollsBackPartialArtifacts(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	transport := newCredentialIsolationTestTransport(t, roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"ETag": {`"generic"`}},
			Body:       &credentialCancellationBody{entered: entered, release: release},
			Request:    request,
		}, nil
	}))
	root := t.TempDir()
	destination := filepath.Join(root, "generic.bin")
	operation := &operation{transport: transport, request: Request{OutputDir: root}}
	selection := mediaformat.Selection{
		URL: "https://media.example.invalid/generic.mp4", Ext: "mp4", CredentialIsolated: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := operation.downloadSelection(ctx, selection, root, destination, nil)
		done <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("context canceled before direct body entered")
	}
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	for _, path := range []string{destination, destination + ".part", destination + ".part.json"} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("artifact %s remains: %v", path, err)
		}
	}
}

func TestCredentialIsolatedHLSRefusesRedirects(t *testing.T) {
	var manifestTargetCalls atomic.Int64
	var segmentTargetCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/redirect.m3u8":
			http.Redirect(writer, request, "/manifest.m3u8", http.StatusFound)
		case "/manifest.m3u8":
			manifestTargetCalls.Add(1)
			_, _ = io.WriteString(writer, "#EXTM3U\n#EXTINF:1,\nfinal.ts\n#EXT-X-ENDLIST\n")
		case "/segment-manifest.m3u8":
			_, _ = io.WriteString(writer, "#EXTM3U\n#EXTINF:1,\nredirect.ts\n#EXT-X-ENDLIST\n")
		case "/redirect.ts":
			http.Redirect(writer, request, "/final.ts", http.StatusTemporaryRedirect)
		case "/final.ts":
			segmentTargetCalls.Add(1)
			_, _ = io.WriteString(writer, "must-not-download")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	transport, err := network.New(network.Config{DefaultHeaders: credentialIsolationHeaders()})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	for _, test := range []struct {
		name string
		path string
	}{
		{name: "manifest", path: "/redirect.m3u8"},
		{name: "segment", path: "/segment-manifest.m3u8"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			destination := filepath.Join(root, "redirect.ts")
			operation := &operation{transport: transport, request: Request{OutputDir: root}}
			_, _, err := operation.downloadSelection(context.Background(), mediaformat.Selection{
				URL: server.URL + test.path, Protocol: "m3u8_native", CredentialIsolated: true,
			}, root, destination, nil)
			if err == nil {
				t.Fatal("credential-isolated HLS redirect unexpectedly succeeded")
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("destination exists after redirect rejection: %v", statErr)
			}
		})
	}
	if manifestTargetCalls.Load() != 0 || segmentTargetCalls.Load() != 0 {
		t.Fatalf("credential-isolated HLS followed redirects: manifest=%d segment=%d",
			manifestTargetCalls.Load(), segmentTargetCalls.Load())
	}
}

func TestCredentialIsolatedHDSManifestAndFragments(t *testing.T) {
	var mu sync.Mutex
	requests := make(map[string][]http.Header)
	fixture := &hdsTestRoundTripper{}
	roundTripper := hdsTestRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		requests[request.URL.Path] = append(requests[request.URL.Path], request.Header.Clone())
		mu.Unlock()
		return fixture.RoundTrip(request)
	})
	transport := newCredentialIsolationTestTransport(t, roundTripper)
	root := t.TempDir()
	destination := filepath.Join(root, "output.flv")
	operation := &operation{transport: transport, request: Request{OutputDir: root}}
	_, _, err := operation.downloadSelection(context.Background(), mediaformat.Selection{
		URL: "http://cdn.example.invalid/manifest.f4m", Protocol: "f4m",
		TBR: 800, Headers: credentialIsolationHeaders(), CredentialIsolated: true,
	}, root, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	assertCredentialHeadersAbsent(t, requests, "/manifest.f4m", "/media.mp4Seg1-Frag1")
}

func TestCredentialIsolatedDispatchRejectsUnsafeBranches(t *testing.T) {
	for _, test := range []struct {
		name      string
		selection mediaformat.Selection
		external  *ExternalDownloader
	}{
		{name: "external", selection: mediaformat.Selection{URL: "https://media.example/file", Protocol: "https"}, external: &ExternalDownloader{Executable: "must-not-run"}},
		{name: "youtube-live-from-start", selection: mediaformat.Selection{URL: "https://media.example/live", YouTubeLiveFromStart: true, TargetDuration: 1}},
		{name: "youtube-post-live", selection: mediaformat.Selection{URL: "https://media.example/post-live", YouTubePostLive: true, TargetDuration: 1}},
		{name: "youtube-sabr", selection: mediaformat.Selection{URL: "https://media.example/sabr", Protocol: "youtube_sabr_ump", YouTubeSABR: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var networkCalls atomic.Int64
			transport := newCredentialIsolationTestTransport(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
				networkCalls.Add(1)
				return nil, errors.New("unsafe branch reached network")
			}))
			root := t.TempDir()
			destination := filepath.Join(root, "output.bin")
			refreshCalled := false
			operation := &operation{
				transport: transport,
				request:   Request{OutputDir: root, Downloader: DownloaderOptions{External: test.external}},
				youtubeLiveRefresh: func(mediaformat.Selection) youtubelive.LiveRefreshFunc {
					refreshCalled = true
					return nil
				},
			}
			selection := test.selection
			selection.CredentialIsolated = true
			path, size, err := operation.downloadSelection(context.Background(), selection, root, destination, nil)
			if !errors.Is(err, extractor.ErrTransportIsolation) ||
				!IsCategory(categorized("isolated dispatch", err), ErrorUnsupported) {
				t.Fatalf("error = %v", err)
			}
			if path != "" || size != 0 || networkCalls.Load() != 0 || refreshCalled {
				t.Fatalf("path=%q size=%d network=%d refresh=%t", path, size, networkCalls.Load(), refreshCalled)
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("destination exists after rejection: %v", statErr)
			}
		})
	}
}

func TestCredentialIsolatedPairDispatchRejectsUnsafeBranches(t *testing.T) {
	for _, test := range []struct {
		name       string
		selections []mediaformat.Selection
	}{
		{name: "youtube-live-pair", selections: []mediaformat.Selection{
			{URL: "https://media.example/video", YouTubeLiveFromStart: true, TargetDuration: 1, VCodec: "avc1", ACodec: "none", CredentialIsolated: true},
			{URL: "https://media.example/audio", YouTubeLiveFromStart: true, TargetDuration: 1, VCodec: "none", ACodec: "mp4a", CredentialIsolated: true},
		}},
		{name: "youtube-sabr-pair", selections: []mediaformat.Selection{
			{Protocol: "youtube_sabr_ump", YouTubeSABR: true, VCodec: "avc1", ACodec: "none", CredentialIsolated: true},
			{Protocol: "youtube_sabr_ump", YouTubeSABR: true, VCodec: "none", ACodec: "mp4a", CredentialIsolated: true},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var networkCalls atomic.Int64
			transport := newCredentialIsolationTestTransport(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
				networkCalls.Add(1)
				return nil, errors.New("unsafe pair reached network")
			}))
			root := t.TempDir()
			destination := filepath.Join(root, "output.mkv")
			branchCalled := false
			operation := &operation{
				transport: transport,
				request:   Request{OutputDir: root},
				youtubeLiveRefresh: func(mediaformat.Selection) youtubelive.LiveRefreshFunc {
					branchCalled = true
					return nil
				},
				sabrMerge: func(context.Context, string, string, string, bool, events.Sink) error {
					branchCalled = true
					return nil
				},
			}
			path, size, err := operation.downloadSelections(context.Background(), test.selections, root, destination, nil)
			if !errors.Is(err, extractor.ErrTransportIsolation) ||
				!IsCategory(categorized("isolated pair dispatch", err), ErrorUnsupported) {
				t.Fatalf("error = %v", err)
			}
			if path != "" || size != 0 || networkCalls.Load() != 0 || branchCalled {
				t.Fatalf("path=%q size=%d network=%d branch=%t", path, size, networkCalls.Load(), branchCalled)
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("destination exists after rejection: %v", statErr)
			}
		})
	}
}

func TestCredentialIsolatedHLSRejectsFFmpegFallback(t *testing.T) {
	var segmentCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.m3u8":
			_, _ = io.WriteString(writer, "#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"key.bin\"\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n")
		case "/segment.ts":
			segmentCalls.Add(1)
			_, _ = io.WriteString(writer, "must-not-download")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	root := t.TempDir()
	destination := filepath.Join(root, "fallback.mp4")
	fallbackCalls := 0
	operation := &operation{
		transport: transport,
		request:   Request{OutputDir: root},
		hlsFallback: func(context.Context, string, string, string, http.Header, bool, events.Sink) (fragment.Result, error) {
			fallbackCalls++
			return fragment.Result{}, errors.New("unsafe fallback invoked")
		},
	}
	path, size, err := operation.downloadSelection(context.Background(), mediaformat.Selection{
		URL: server.URL + "/manifest.m3u8", Protocol: "m3u8_native", CredentialIsolated: true,
	}, root, destination, nil)
	if !errors.Is(err, extractor.ErrTransportIsolation) ||
		!IsCategory(categorized("isolated HLS fallback", err), ErrorUnsupported) {
		t.Fatalf("error = %v", err)
	}
	if path != "" || size != 0 || fallbackCalls != 0 || segmentCalls.Load() != 0 {
		t.Fatalf("path=%q size=%d fallback=%d segments=%d", path, size, fallbackCalls, segmentCalls.Load())
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination exists after fallback rejection: %v", statErr)
	}
}
