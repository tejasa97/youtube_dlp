package ytdlp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/extractor"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

type tedProductRoundTripper struct {
	mu        sync.Mutex
	responses map[string]tedProductResponse
	requests  []http.Request
	wait      chan struct{}
	started   chan struct{}
	waitURL   string
	startOnce sync.Once
}

type tedProductResponse struct {
	status      int
	body        []byte
	contentType string
}

func (transport *tedProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.requests = append(transport.requests, *request.Clone(request.Context()))
	response, ok := transport.responses[request.URL.String()]
	wait := transport.wait
	transport.mu.Unlock()
	waitForRequest := wait != nil && (transport.waitURL == "" || request.URL.String() == transport.waitURL)
	if transport.started != nil && waitForRequest {
		transport.startOnce.Do(func() { close(transport.started) })
	}
	if waitForRequest {
		select {
		case <-wait:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	}
	if !ok {
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not found")), Request: request}, nil
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	header := make(http.Header)
	header.Set("Content-Type", response.contentType)
	header.Set("Content-Length", fmt.Sprintf("%d", len(response.body)))
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(response.body)), Request: request}, nil
}

func tedProductFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "risk", "ted", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func tedProductResponses(t *testing.T, includeMedia bool) map[string]tedProductResponse {
	t.Helper()
	responses := map[string]tedProductResponse{
		"https://www.ted.com/talks/fixture_talk":                                         {body: tedProductFixture(t, "talk_page.html"), contentType: "text/html"},
		"https://hls.ted.com/talks/fixture/master.m3u8?sig=master-1":                     {body: tedProductFixture(t, "master.m3u8"), contentType: "application/vnd.apple.mpegurl"},
		"https://hls.ted.com/talks/fixture/800k.m3u8?sig=variant-800":                    {body: tedProductFixture(t, "800k.m3u8"), contentType: "application/vnd.apple.mpegurl"},
		"https://download.ted.com/talks/fixture/800k.m3u8?sig=variant-800":               {body: tedProductFixture(t, "800k.m3u8"), contentType: "application/vnd.apple.mpegurl"},
		"https://download.ted.com/talks/fixture/captions/en.m3u8?sig=caption-1":          {body: tedProductFixture(t, "en.m3u8"), contentType: "application/vnd.apple.mpegurl"},
		"https://download.ted.com/talks/fixture/captions/en-0.vtt?sig=caption-segment-0": {body: tedProductFixture(t, "en-0.vtt"), contentType: "text/vtt"},
		"https://pi.tedcdn.com/images/fixture-talk.jpg?sig=thumb-1":                      {body: []byte("fixture-thumbnail-bytes"), contentType: "image/jpeg"},
	}
	if includeMedia {
		responses["https://download.ted.com/talks/fixture/500k.mp4?sig=direct-500"] = tedProductResponse{body: []byte("fixture-direct-media-bytes"), contentType: "video/mp4"}
		responses["https://download.ted.com/talks/fixture/seg-0.ts?sig=segment-0"] = tedProductResponse{body: []byte("fixture-hls-one-"), contentType: "video/mp2t"}
		responses["https://download.ted.com/talks/fixture/seg-1.ts?sig=segment-1"] = tedProductResponse{body: []byte("fixture-hls-two"), contentType: "video/mp2t"}
	}
	return responses
}

func newTedProductOperation(t *testing.T, transportRoundTripper *tedProductRoundTripper, request Request) (*operation, string) {
	t.Helper()
	ambient, err := network.New(network.Config{
		DefaultHeaders: http.Header{
			"Authorization":       {"Bearer ambient-secret"},
			"Cookie":              {"session=ambient-secret"},
			"Proxy-Authorization": {"Basic ambient-secret"},
			"Referer":             {"https://ambient.example/page"},
		},
		RoundTripper: transportRoundTripper,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ambient.CloseIdleConnections)
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	rootExtractor := ""
	return &operation{
		client: NewClient(), request: request, transport: ambient,
		registry: productRegistry(), compatibility: compatibility,
		rootExtractor: &rootExtractor,
	}, request.OutputDir
}

func assertTedProductIsolation(t *testing.T, transport *tedProductRoundTripper) {
	t.Helper()
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for index, request := range transport.requests {
		for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
			if got := request.Header.Get(key); got != "" {
				t.Fatalf("request %d %s leaked %q", index, key, got)
			}
		}
	}
}

func TestProductTedDirectDownloadPreservesBytesAndSignedQuery(t *testing.T) {
	root := t.TempDir()
	roundTripper := &tedProductRoundTripper{responses: tedProductResponses(t, true)}
	operation, _ := newTedProductOperation(t, roundTripper, Request{
		URL: "https://www.ted.com/talks/fixture_talk", OutputDir: root,
		OutputTemplate: "%(id)s.%(ext)s", Format: "h264-500k", Overwrite: true,
	})
	result, err := operation.process(context.Background(), operation.request.URL, "", nil, make(map[string]bool), 0)
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	got, err := os.ReadFile(result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("fixture-direct-media-bytes")) {
		t.Fatalf("bytes=%q", got)
	}
	assertTedProductIsolation(t, roundTripper)
	if !tedProductSawURL(roundTripper, "https://download.ted.com/talks/fixture/500k.mp4?sig=direct-500") {
		t.Fatalf("signed direct URL was not dispatched")
	}
}

func TestProductTedHLSSubtitleThumbnailAndIsolation(t *testing.T) {
	root := t.TempDir()
	roundTripper := &tedProductRoundTripper{responses: tedProductResponses(t, true)}
	operation, _ := newTedProductOperation(t, roundTripper, Request{
		URL: "https://www.ted.com/talks/fixture_talk", OutputDir: root,
		OutputTemplate: "%(id)s.%(ext)s", Format: "hls", Overwrite: true,
		Subtitles:  SubtitleOptions{WriteManual: true, Languages: []string{"en"}},
		Thumbnails: ThumbnailOptions{Write: true},
	})
	result, err := operation.process(context.Background(), operation.request.URL, "", nil, make(map[string]bool), 0)
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	got, err := os.ReadFile(result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "fixture-hls-one-fixture-hls-two" {
		t.Fatalf("HLS bytes=%q", got)
	}
	if !tedProductSawURL(roundTripper, "https://download.ted.com/talks/fixture/captions/en.m3u8?sig=caption-1") ||
		!tedProductSawURL(roundTripper, "https://download.ted.com/talks/fixture/captions/en-0.vtt?sig=caption-segment-0") ||
		!tedProductSawURL(roundTripper, "https://pi.tedcdn.com/images/fixture-talk.jpg?sig=thumb-1") {
		t.Fatalf("sidecar dispatch missing: %v", tedProductRequestURLs(roundTripper))
	}
	assertTedProductIsolation(t, roundTripper)
}

func TestProductTedEmbedReentryAndPlaylistChildrenReuse(t *testing.T) {
	registry := productRegistry()
	selected, err := registry.Select("https://embed.ted.com/talks/fixture_talk")
	if err != nil || selected.Name() != "ted_embed" {
		t.Fatalf("embed selection=%v err=%v", selected, err)
	}
	transport := &tedFixtureProductTransport{responses: map[string][]byte{
		"https://www.ted.com/series/fixture_series":          tedProductFixture(t, "series_page.html"),
		"https://www.ted.com/playlists/171/fixture_playlist": tedProductFixture(t, "playlist_page.html"),
	}}
	result, err := selected.Extract(context.Background(), extractor.Request{URL: "https://embed.ted.com/talks/fixture_talk"})
	if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "ted_talk" || result.Redirect.URL != "https://www.ted.com/talks/fixture_talk" {
		t.Fatalf("embed result=%#v err=%v", result, err)
	}
	series, err := registry.Select("https://www.ted.com/series/fixture_series")
	if err != nil || series.Name() != "ted_series" {
		t.Fatalf("series selection=%v err=%v", series, err)
	}
	seriesExtraction, err := series.Extract(context.Background(), extractor.Request{URL: "https://www.ted.com/series/fixture_series", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	first, err := extractor.CollectEntries(context.Background(), seriesExtraction.Entries, 10)
	if err != nil || len(first) != 2 {
		t.Fatalf("first children=%#v err=%v", first, err)
	}
	second, err := extractor.CollectEntries(context.Background(), seriesExtraction.Entries, 10)
	if err != nil || len(second) != len(first) || second[0].URL != first[0].URL {
		t.Fatalf("reused children=%#v err=%v", second, err)
	}
	playlistExtractor, err := registry.Select("https://www.ted.com/playlists/171/fixture_playlist")
	if err != nil || playlistExtractor.Name() != "ted_playlist" {
		t.Fatalf("playlist selection=%v err=%v", playlistExtractor, err)
	}
	playlistExtraction, err := playlistExtractor.Extract(context.Background(), extractor.Request{URL: "https://www.ted.com/playlists/171/fixture_playlist", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	playlistFirst, err := extractor.CollectEntries(context.Background(), playlistExtraction.Entries, 10)
	if err != nil || len(playlistFirst) != 2 {
		t.Fatalf("playlist first children=%#v err=%v", playlistFirst, err)
	}
	playlistSecond, err := extractor.CollectEntries(context.Background(), playlistExtraction.Entries, 10)
	if err != nil || len(playlistSecond) != len(playlistFirst) || playlistSecond[1].URL != playlistFirst[1].URL {
		t.Fatalf("playlist reuse=%#v err=%v", playlistSecond, err)
	}

	seriesChildren := []tedProductChildFixture{
		{pageURL: "https://www.ted.com/talks/fixture_season_one", page: "series_one_page.html", id: "1001", mediaURL: "https://download.ted.com/talks/series/season-one.mp4?sig=series-one", media: "fixture-series-season-one-bytes"},
		{pageURL: "https://www.ted.com/talks/fixture_season_two", page: "series_two_page.html", id: "1002", mediaURL: "https://download.ted.com/talks/series/season-two.mp4?sig=series-two", media: "fixture-series-season-two-bytes"},
	}
	seriesFirst, seriesRoot, seriesRequests := runTedProductCollection(t, "https://www.ted.com/series/fixture_series", "series_page.html", seriesChildren)
	seriesSecond, _, seriesRequestsAgain := runTedProductCollection(t, "https://www.ted.com/series/fixture_series", "series_page.html", seriesChildren)
	assertTedProductCollection(t, seriesFirst, seriesRoot, seriesChildren)
	assertTedProductCollection(t, seriesSecond, "", seriesChildren)
	if strings.Join(seriesRequests, "\n") != strings.Join(seriesRequestsAgain, "\n") {
		t.Fatalf("series execution was not deterministic:\nfirst=%v\nsecond=%v", seriesRequests, seriesRequestsAgain)
	}

	playlistChildren := []tedProductChildFixture{
		{pageURL: "https://www.ted.com/talks/fixture_playlist_one", page: "playlist_one_page.html", id: "2001", mediaURL: "https://download.ted.com/talks/playlist/one.mp4?sig=playlist-one", media: "fixture-playlist-one-bytes"},
		{pageURL: "https://www.ted.com/talks/fixture_playlist_two", page: "playlist_two_page.html", id: "2002", mediaURL: "https://download.ted.com/talks/playlist/two.mp4?sig=playlist-two", media: "fixture-playlist-two-bytes"},
	}
	playlistProductFirst, playlistRoot, playlistRequests := runTedProductCollection(t, "https://www.ted.com/playlists/171/fixture_playlist", "playlist_page.html", playlistChildren)
	playlistProductSecond, _, playlistRequestsAgain := runTedProductCollection(t, "https://www.ted.com/playlists/171/fixture_playlist", "playlist_page.html", playlistChildren)
	assertTedProductCollection(t, playlistProductFirst, playlistRoot, playlistChildren)
	assertTedProductCollection(t, playlistProductSecond, "", playlistChildren)
	if strings.Join(playlistRequests, "\n") != strings.Join(playlistRequestsAgain, "\n") {
		t.Fatalf("playlist execution was not deterministic:\nfirst=%v\nsecond=%v", playlistRequests, playlistRequestsAgain)
	}

	embedTransport := &tedProductRoundTripper{responses: tedProductResponses(t, true)}
	embedOperation, _ := newTedProductOperation(t, embedTransport, Request{
		URL: "https://embed.ted.com/talks/fixture_talk", OutputDir: t.TempDir(),
		OutputTemplate: "%(id)s.%(ext)s", Format: "h264-500k", Overwrite: true,
	})
	embedResult, err := embedOperation.process(context.Background(), embedOperation.request.URL, "", nil, make(map[string]bool), 0)
	if err != nil || !embedResult.Downloaded {
		t.Fatalf("registered embed execution result=%+v err=%v", embedResult, err)
	}
	embedBytes, err := os.ReadFile(embedResult.Filename)
	if err != nil || !bytes.Equal(embedBytes, []byte("fixture-direct-media-bytes")) {
		t.Fatalf("embed bytes=%q err=%v", embedBytes, err)
	}
	if !tedProductSawURL(embedTransport, "https://www.ted.com/talks/fixture_talk") {
		t.Fatalf("embed did not re-enter canonical talk: %v", tedProductRequestURLs(embedTransport))
	}
	assertTedProductIsolation(t, embedTransport)
}

type tedProductChildFixture struct {
	pageURL  string
	page     string
	id       string
	mediaURL string
	media    string
}

func runTedProductCollection(t *testing.T, rootURL, rootPage string, children []tedProductChildFixture) (Result, string, []string) {
	t.Helper()
	responses := tedProductResponses(t, true)
	responses[rootURL] = tedProductResponse{body: tedProductFixture(t, rootPage), contentType: "text/html"}
	for _, child := range children {
		responses[child.pageURL] = tedProductResponse{body: tedProductFixture(t, child.page), contentType: "text/html"}
		responses[child.mediaURL] = tedProductResponse{body: []byte(child.media), contentType: "video/mp4"}
	}
	transport := &tedProductRoundTripper{responses: responses}
	root := t.TempDir()
	operation, _ := newTedProductOperation(t, transport, Request{
		URL: rootURL, OutputDir: root, OutputTemplate: "%(id)s.%(ext)s",
		Format: "h264-500k", Overwrite: false,
	})
	result, err := operation.process(context.Background(), rootURL, "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatalf("TED %s execution: %v", rootURL, err)
	}
	assertTedProductIsolation(t, transport)
	return result, root, tedProductRequestURLs(transport)
}

func assertTedProductCollection(t *testing.T, result Result, root string, children []tedProductChildFixture) {
	t.Helper()
	if len(result.Entries) != len(children) {
		t.Fatalf("TED collection entries=%d want %d: %+v", len(result.Entries), len(children), result)
	}
	seen := make(map[string]bool, len(children))
	for index, child := range result.Entries {
		if !child.Downloaded || child.Filename == "" {
			t.Fatalf("TED child %d was not downloaded: %+v", index, child)
		}
		if strings.HasSuffix(child.Filename, string(filepath.Separator)+children[index].id+".mp4") {
			seen[children[index].id] = true
		}
		if root == "" {
			continue
		}
		if child.Filename != filepath.Join(root, children[index].id+".mp4") {
			t.Fatalf("TED child %d filename=%q want distinct id path", index, child.Filename)
		}
		body, err := os.ReadFile(child.Filename)
		if err != nil || string(body) != children[index].media {
			t.Fatalf("TED child %d bytes=%q err=%v", index, body, err)
		}
	}
	if len(seen) != len(children) {
		t.Fatalf("TED child destinations collided: %v", seen)
	}
}

func TestProductTedCancellationAndFailureLeaveNoArtifacts(t *testing.T) {
	root := t.TempDir()
	const directMediaURL = "https://download.ted.com/talks/fixture/500k.mp4?sig=direct-500"
	roundTripper := &tedProductRoundTripper{
		responses: tedProductResponses(t, true), wait: make(chan struct{}), started: make(chan struct{}),
		waitURL: directMediaURL,
	}
	operation, _ := newTedProductOperation(t, roundTripper, Request{
		URL: "https://www.ted.com/talks/fixture_talk", OutputDir: root,
		OutputTemplate: "%(id)s.%(ext)s", Format: "h264-500k", Overwrite: true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := operation.process(ctx, operation.request.URL, "", nil, make(map[string]bool), 0)
		done <- err
	}()
	<-roundTripper.started
	if !tedProductSawURL(roundTripper, directMediaURL) {
		t.Fatalf("cancellation entered the wrong request: %v", tedProductRequestURLs(roundTripper))
	}
	cancel()
	if err := <-done; !IsCategory(err, ErrorCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("cancellation artifacts=%v err=%v", entries, err)
	}

	t.Run("api-failure", func(t *testing.T) {
		root := t.TempDir()
		responses := tedProductResponses(t, true)
		responses["https://www.ted.com/talks/fixture_talk"] = tedProductResponse{status: http.StatusInternalServerError, body: []byte("secret=must-not-leak"), contentType: "text/plain"}
		operation, _ := newTedProductOperation(t, &tedProductRoundTripper{responses: responses}, Request{
			URL: "https://www.ted.com/talks/fixture_talk", OutputDir: root,
			OutputTemplate: "%(id)s.%(ext)s", Format: "h264-500k", Overwrite: true,
		})
		if _, err := operation.process(context.Background(), operation.request.URL, "", nil, make(map[string]bool), 0); err == nil || strings.Contains(err.Error(), "must-not-leak") {
			t.Fatalf("API failure=%v", err)
		}
		assertTedNoArtifacts(t, root)
	})

	t.Run("manifest-failure", func(t *testing.T) {
		root := t.TempDir()
		responses := tedProductResponses(t, true)
		responses["https://hls.ted.com/talks/fixture/master.m3u8?sig=master-1"] = tedProductResponse{status: http.StatusInternalServerError, body: []byte("secret=must-not-leak"), contentType: "text/plain"}
		operation, _ := newTedProductOperation(t, &tedProductRoundTripper{responses: responses}, Request{
			URL: "https://www.ted.com/talks/fixture_talk", OutputDir: root,
			OutputTemplate: "%(id)s.%(ext)s", Format: "hls", Overwrite: true,
		})
		if _, err := operation.process(context.Background(), operation.request.URL, "", nil, make(map[string]bool), 0); err == nil || strings.Contains(err.Error(), "must-not-leak") {
			t.Fatalf("manifest failure=%v", err)
		}
		assertTedNoArtifacts(t, root)
	})

	t.Run("segment-failure", func(t *testing.T) {
		root := t.TempDir()
		responses := tedProductResponses(t, true)
		responses["https://download.ted.com/talks/fixture/seg-0.ts?sig=segment-0"] = tedProductResponse{status: http.StatusInternalServerError, body: []byte("secret=must-not-leak"), contentType: "video/mp2t"}
		operation, _ := newTedProductOperation(t, &tedProductRoundTripper{responses: responses}, Request{
			URL: "https://www.ted.com/talks/fixture_talk", OutputDir: root,
			OutputTemplate: "%(id)s.%(ext)s", Format: "hls", Overwrite: true,
		})
		if _, err := operation.process(context.Background(), operation.request.URL, "", nil, make(map[string]bool), 0); err == nil || strings.Contains(err.Error(), "must-not-leak") {
			t.Fatalf("segment failure=%v", err)
		}
		assertTedNoArtifacts(t, root)
	})

	t.Run("direct-media-failure", func(t *testing.T) {
		root := t.TempDir()
		responses := tedProductResponses(t, true)
		responses["https://download.ted.com/talks/fixture/500k.mp4?sig=direct-500"] = tedProductResponse{status: http.StatusInternalServerError, body: []byte("secret=must-not-leak"), contentType: "text/plain"}
		operation, _ := newTedProductOperation(t, &tedProductRoundTripper{responses: responses}, Request{
			URL: "https://www.ted.com/talks/fixture_talk", OutputDir: root,
			OutputTemplate: "%(id)s.%(ext)s", Format: "h264-500k", Overwrite: true,
		})
		if _, err := operation.process(context.Background(), operation.request.URL, "", nil, make(map[string]bool), 0); err == nil || strings.Contains(err.Error(), "must-not-leak") {
			t.Fatalf("direct media failure=%v", err)
		}
		assertTedNoArtifacts(t, root)
	})
}

func assertTedNoArtifacts(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("artifacts=%v err=%v", entries, err)
	}
}

func TestTedCredentialIsolatedReadPageFailsClosed(t *testing.T) {
	const manifestURL = "https://hls.ted.com/talks/fixture/read-page.m3u8?sig=read-page"
	for _, test := range []struct {
		name       string
		status     int
		body       []byte
		want       error
		wantStatus int
	}{
		{name: "server-status", status: http.StatusServiceUnavailable, body: []byte("secret=must-not-leak"), want: extractor.ErrTedNetwork, wantStatus: http.StatusServiceUnavailable},
		{name: "redirect-status", status: http.StatusFound, body: []byte("Location: https://evil.example/"), want: extractor.ErrTedRedirect, wantStatus: http.StatusFound},
		{name: "oversize", body: bytes.Repeat([]byte("x"), 8<<20+1), want: extractor.ErrJSONResponseTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTripper := &tedProductRoundTripper{responses: map[string]tedProductResponse{
				manifestURL: {status: test.status, body: test.body, contentType: "application/vnd.apple.mpegurl"},
			}}
			ambient, err := network.New(network.Config{
				DefaultHeaders: credentialIsolationHeaders(), RoundTripper: roundTripper,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer ambient.CloseIdleConnections()
			isolated := newTedCredentialIsolatedTransport(ambient, "manifest")
			_, _, err = isolated.ReadPage(context.Background(), manifestURL)
			if !errors.Is(err, test.want) {
				t.Fatalf("ReadPage error=%v want=%v", err, test.want)
			}
			if test.wantStatus != 0 {
				var statusErr *extractor.HTTPStatusError
				if !errors.As(err, &statusErr) || statusErr.Code != test.wantStatus {
					t.Fatalf("ReadPage status error=%v want HTTP %d", err, test.wantStatus)
				}
			}
			if strings.Contains(err.Error(), "must-not-leak") {
				t.Fatalf("ReadPage error leaked response body: %v", err)
			}
		})
	}

}

func TestProductTedHostPolicyRejectsHostileAssets(t *testing.T) {
	t.Run("unknown-policy-before-external-handoff", func(t *testing.T) {
		root := t.TempDir()
		roundTripper := &tedProductRoundTripper{responses: map[string]tedProductResponse{}}
		operation, _ := newTedProductOperation(t, roundTripper, Request{
			OutputDir:  root,
			Downloader: DownloaderOptions{External: &ExternalDownloader{Executable: "must-not-run"}},
		})
		_, _, err := operation.downloadSelection(context.Background(), mediaformat.Selection{
			URL: "https://download.ted.com/talks/fixture/500k.mp4?sig=unknown-policy", Protocol: "https",
			HostPolicy: "unknown", CredentialIsolated: false,
		}, root, filepath.Join(root, "hostile.mp4"), nil)
		if !errors.Is(err, extractor.ErrUnavailable) {
			t.Fatalf("unknown host policy error=%v", err)
		}
		if len(tedProductRequestURLs(roundTripper)) != 0 {
			t.Fatalf("unknown host policy reached network: %v", tedProductRequestURLs(roundTripper))
		}
		assertTedNoArtifacts(t, root)
	})

	t.Run("hostile-manifest", func(t *testing.T) {
		root := t.TempDir()
		roundTripper := &tedProductRoundTripper{responses: map[string]tedProductResponse{}}
		operation, _ := newTedProductOperation(t, roundTripper, Request{OutputDir: root})
		_, _, err := operation.downloadSelection(context.Background(), mediaformat.Selection{
			URL: "https://evil.example/manifest.m3u8", Protocol: "m3u8_native", Ext: "mp4",
			CredentialIsolated: true, HostPolicy: "ted",
		}, root, filepath.Join(root, "hostile.ts"), nil)
		if !errors.Is(err, extractor.ErrUnavailable) {
			t.Fatalf("hostile manifest error=%v", err)
		}
		if len(tedProductRequestURLs(roundTripper)) != 0 {
			t.Fatalf("hostile manifest reached network: %v", tedProductRequestURLs(roundTripper))
		}
		assertTedNoArtifacts(t, root)
	})

	t.Run("hostile-segment", func(t *testing.T) {
		root := t.TempDir()
		manifestURL := "https://hls.ted.com/talks/fixture/hostile.m3u8?sig=hostile"
		roundTripper := &tedProductRoundTripper{responses: map[string]tedProductResponse{
			manifestURL: {body: []byte("#EXTM3U\n#EXTINF:1,\nhttps://evil.example/segment.ts?sig=evil\n#EXT-X-ENDLIST\n"), contentType: "application/vnd.apple.mpegurl"},
		}}
		operation, _ := newTedProductOperation(t, roundTripper, Request{OutputDir: root})
		_, _, err := operation.downloadSelection(context.Background(), mediaformat.Selection{
			URL: manifestURL, Protocol: "m3u8_native", Ext: "mp4", CredentialIsolated: true, HostPolicy: "ted",
		}, root, filepath.Join(root, "hostile-segment.ts"), nil)
		if err == nil {
			t.Fatalf("hostile segment error=%v", err)
		}
		if tedProductSawURL(roundTripper, "https://evil.example/segment.ts?sig=evil") {
			t.Fatal("hostile segment reached network")
		}
		assertTedNoArtifacts(t, root)
	})

	for _, test := range []struct {
		name string
		info value.Info
		run  func(*operation, value.Info) error
	}{
		{
			name: "hostile-subtitle",
			info: value.NewInfo(value.NewObject(
				value.Field{Key: "id", Value: value.String("fixture")}, value.Field{Key: "title", Value: value.String("Fixture")}, value.Field{Key: "ext", Value: value.String("mp4")},
			)),
			run: func(operation *operation, info value.Info) error {
				info.Set("subtitles", value.ObjectValue(value.NewObject(value.Field{Key: "en", Value: value.List(value.ObjectValue(value.NewObject(
					value.Field{Key: "url", Value: value.String("https://evil.example/captions/en.vtt")}, value.Field{Key: "ext", Value: value.String("vtt")},
					value.Field{Key: "_credential_isolated", Value: value.Bool(true)}, value.Field{Key: "_ted_host_policy", Value: value.String("ted")},
				)))})))
				tracks, _, err := selectSubtitles(info, SubtitleOptions{WriteManual: true, Languages: []string{"en"}})
				if err != nil {
					return err
				}
				_, _, err = operation.downloadSubtitles(context.Background(), info, tracks, nil)
				return err
			},
		},
		{
			name: "hostile-thumbnail",
			info: value.NewInfo(value.NewObject(
				value.Field{Key: "id", Value: value.String("fixture")}, value.Field{Key: "title", Value: value.String("Fixture")}, value.Field{Key: "ext", Value: value.String("mp4")},
			)),
			run: func(operation *operation, info value.Info) error {
				info.Set("thumbnails", value.List(value.ObjectValue(value.NewObject(
					value.Field{Key: "id", Value: value.String("0")}, value.Field{Key: "url", Value: value.String("https://evil.example/thumb.jpg")}, value.Field{Key: "ext", Value: value.String("jpg")},
					value.Field{Key: "_credential_isolated", Value: value.Bool(true)}, value.Field{Key: "_ted_host_policy", Value: value.String("ted")},
				))))
				_, _, err := operation.writeThumbnails(context.Background(), &info, false)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			roundTripper := &tedProductRoundTripper{responses: map[string]tedProductResponse{}}
			operation, _ := newTedProductOperation(t, roundTripper, Request{OutputDir: root, Thumbnails: ThumbnailOptions{Write: true}})
			if err := test.run(operation, test.info); !errors.Is(err, extractor.ErrUnavailable) {
				t.Fatalf("hostile asset error=%v", err)
			}
			if len(tedProductRequestURLs(roundTripper)) != 0 {
				t.Fatalf("hostile asset reached network: %v", tedProductRequestURLs(roundTripper))
			}
			assertTedNoArtifacts(t, root)
		})
	}
}

func TestProductTedUnknownThumbnailPolicyFailsClosed(t *testing.T) {
	root := t.TempDir()
	roundTripper := &tedProductRoundTripper{responses: map[string]tedProductResponse{}}
	operation, _ := newTedProductOperation(t, roundTripper, Request{OutputDir: root, Thumbnails: ThumbnailOptions{Write: true}})
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture")}, value.Field{Key: "title", Value: value.String("Fixture")}, value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "thumbnails", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "id", Value: value.String("0")}, value.Field{Key: "url", Value: value.String("https://evil.example/thumb.jpg")}, value.Field{Key: "ext", Value: value.String("jpg")},
			value.Field{Key: "_credential_isolated", Value: value.Bool(true)}, value.Field{Key: "_ted_host_policy", Value: value.String("unknown")},
		)))},
	))
	if _, _, err := operation.writeThumbnails(context.Background(), &info, false); !errors.Is(err, extractor.ErrUnavailable) {
		t.Fatalf("unknown thumbnail policy error=%v", err)
	}
	if len(tedProductRequestURLs(roundTripper)) != 0 {
		t.Fatalf("unknown thumbnail policy reached network: %v", tedProductRequestURLs(roundTripper))
	}
	assertTedNoArtifacts(t, root)
}

func tedProductSawURL(transport *tedProductRoundTripper, rawURL string) bool {
	for _, candidate := range tedProductRequestURLs(transport) {
		if candidate == rawURL {
			return true
		}
	}
	return false
}

func tedProductRequestURLs(transport *tedProductRoundTripper) []string {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	result := make([]string, 0, len(transport.requests))
	for _, request := range transport.requests {
		result = append(result, request.URL.String())
	}
	return result
}

type tedFixtureProductTransport struct {
	responses map[string][]byte
}

func (transport *tedFixtureProductTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected non-isolated TED fixture request")
}

func (transport *tedFixtureProductTransport) ReadPage(_ context.Context, rawURL string) ([]byte, http.Header, error) {
	body, ok := transport.responses[rawURL]
	if !ok {
		return nil, nil, fmt.Errorf("missing TED fixture page")
	}
	return body, make(http.Header), nil
}

func (transport *tedFixtureProductTransport) DoWithoutCredentialsNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	body, ok := transport.responses[request.URL.String()]
	if !ok {
		return nil, fmt.Errorf("missing TED fixture request")
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}
