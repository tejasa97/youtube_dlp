package ytdlp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/extractor"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/protocol/hls"
)

type capturedTwitchProductRequest struct {
	url     string
	headers http.Header
	body    []byte
}

type twitchProductRoundTripper struct {
	mu         sync.Mutex
	requests   []capturedTwitchProductRequest
	statuses   map[string]int
	media      map[string][]byte
	metadata   []byte
	hostileHLS bool
	entered    chan struct{}
	release    chan struct{}
}

func (transport *twitchProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport.entered != nil && transport.release != nil && twitchProductMediaRequest(request) {
		select {
		case transport.entered <- struct{}{}:
		default:
		}
		select {
		case <-transport.release:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	}
	var payload []byte
	if request.Body != nil && request.URL.Host == "gql.twitch.tv" {
		payload, _ = io.ReadAll(request.Body)
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, capturedTwitchProductRequest{
		url: request.URL.String(), headers: request.Header.Clone(), body: append([]byte(nil), payload...),
	})
	status := transport.statuses[request.URL.String()]
	if status == 0 {
		status = http.StatusOK
	}
	media := append([]byte(nil), transport.media[request.URL.String()]...)
	transport.mu.Unlock()

	body := []byte(nil)
	switch request.URL.Host {
	case "gql.twitch.tv":
		switch {
		case bytes.Contains(payload, []byte(`"operationName":"StreamMetadata"`)):
			body = transport.metadata
			if body == nil {
				body = twitchProductFixture("metadata.json")
			}
		case bytes.Contains(payload, []byte(`"operationName":"VideoMetadata"`)):
			body = twitchProductFixture("vod_metadata.json")
		case bytes.Contains(payload, []byte(`"operationName":"FilterableVideoTower_Videos"`)):
			body = twitchProductPlaylistPage(payload, "cursor-page1-final", "videos_page1.json", "videos_page2.json")
		case bytes.Contains(payload, []byte(`"operationName":"ClipsCards__User"`)):
			body = twitchProductPlaylistPage(payload, "cursor-clips-page1-final", "clips_page1.json", "clips_page2.json")
		case bytes.Contains(payload, []byte(`"operationName":"ChannelCollectionsContent"`)):
			body = twitchProductPlaylistPage(payload, "cursor-collections-page1-final", "collections_page1.json", "collections_page2.json")
		case bytes.Contains(payload, []byte(`"operationName":"CollectionSideBar"`)):
			body = twitchProductFixture("collection_direct.json")
		case bytes.Contains(payload, []byte(`"operationName":"ShareClipRenderStatus"`)):
			body = twitchProductFixture("clip_metadata.json")
		case bytes.Contains(payload, []byte("streamPlaybackAccessToken")):
			body = twitchProductFixture("access_token.json")
		case bytes.Contains(payload, []byte("videoPlaybackAccessToken")):
			body = twitchProductFixture("vod_access_token.json")
		}
	case "usher.ttvnw.net":
		if strings.HasSuffix(request.URL.Path, ".m3u8") {
			if transport.hostileHLS {
				body = []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100\nhttps://evil.invalid/media.m3u8\n")
			} else {
				body = []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100\nhttps://edge.ttvnw.net/media.m3u8?sig=variant\n")
			}
		}
	case "edge.ttvnw.net":
		if strings.HasSuffix(request.URL.Path, ".m3u8") {
			body = []byte("#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4?sig=init\"\n#EXTINF:1,\nseg-1.ts?sig=one\n#EXTINF:1,\nseg-2.ts?sig=two\n#EXT-X-ENDLIST\n")
		} else {
			body = transport.media[request.URL.String()]
			if body == nil {
				switch request.URL.Path {
				case "/init.mp4":
					body = []byte("init-")
				case "/seg-1.ts":
					body = []byte("one-")
				case "/seg-2.ts":
					body = []byte("two")
				}
			}
		}
	case "clips-media.twitch.tv":
		body = []byte("clip-exact-bytes")
	case "static-cdn.jtvnw.net":
		if request.URL.Path == "/storyboards/spec.json" {
			body = twitchProductFixture("storyboard_spec.json")
		} else {
			body = []byte("thumbnail-exact-bytes")
		}
	}
	if body == nil && media != nil {
		body = media
	}
	if status == http.StatusOK && body == nil {
		status = http.StatusNotFound
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}

func twitchProductPlaylistPage(payload []byte, cursor, first, second string) []byte {
	if bytes.Contains(payload, []byte(`"cursor":"`+cursor+`"`)) {
		return twitchProductFixture(second)
	}
	return twitchProductFixture(first)
}

func twitchProductMediaRequest(request *http.Request) bool {
	return request.URL.Host == "edge.ttvnw.net" || request.URL.Host == "clips-media.twitch.tv"
}

func twitchProductFixture(name string) []byte {
	data, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "twitch", name))
	if err != nil {
		panic(err)
	}
	return data
}

func twitchProductClient(t *testing.T, roundTripper *twitchProductRoundTripper) *Client {
	t.Helper()
	return NewClient(withTransportFactory(func(config network.Config) (*network.Client, error) {
		config.RoundTripper = roundTripper
		config.DefaultHeaders = http.Header{
			"Authorization":       {"Bearer ambient-secret"},
			"Cookie":              {"ambient=secret"},
			"Proxy-Authorization": {"Basic ambient-secret"},
			"Referer":             {"https://evil.invalid/ambient"},
		}
		return network.New(config)
	}))
}

func twitchProductRequest(rawURL, outputDir string) Request {
	return Request{
		URL: rawURL, OutputDir: outputDir, OutputTemplate: "%(id)s.%(ext)s",
		OutputTemplates: OutputTemplates{OutputTemplateThumbnail: "%(id)s-thumb.%(ext)s"},
		Overwrite:       true, Format: "best",
	}
}

func (transport *twitchProductRoundTripper) requestsSnapshot() []capturedTwitchProductRequest {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	requests := make([]capturedTwitchProductRequest, len(transport.requests))
	copy(requests, transport.requests)
	return requests
}

func containsTwitchProductRequest(requests []capturedTwitchProductRequest, rawURL string) bool {
	for _, request := range requests {
		if request.url == rawURL {
			return true
		}
	}
	return false
}

func assertTwitchProductAmbientHeaders(t *testing.T, requests []capturedTwitchProductRequest) {
	t.Helper()
	for _, request := range requests {
		for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
			if got := request.headers.Get(key); got != "" {
				t.Fatalf("%s leaked on %s: %q", key, request.url, got)
			}
		}
	}
}

func assertTwitchProductSignedMediaQueries(t *testing.T, requests []capturedTwitchProductRequest) {
	t.Helper()
	for _, request := range requests {
		parsed, err := url.Parse(request.url)
		if err != nil {
			t.Fatalf("invalid captured URL %q: %v", request.url, err)
		}
		if parsed.Host != "usher.ttvnw.net" && parsed.Host != "edge.ttvnw.net" &&
			(parsed.Host != "clips-media.twitch.tv" || !strings.HasSuffix(parsed.Path, ".mp4")) {
			continue
		}
		if parsed.Query().Get("sig") == "" {
			t.Fatalf("signed media query was lost: %q", request.url)
		}
	}
}

func assertTwitchProductNoSecrets(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	for _, secret := range []string{"ambient-secret", "ambient=secret", "fixture-clip-signature-do-not-log", "fixture-signature-do-not-log"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error exposed secret %q: %v", secret, err)
		}
	}
}

func assertTwitchProductPagePair(t *testing.T, requests []capturedTwitchProductRequest, operationName, cursor string, start, wantPages int) {
	t.Helper()
	var pages []capturedTwitchProductRequest
	for _, request := range requests[start:] {
		if bytes.Contains(request.body, []byte(`"operationName":"`+operationName+`"`)) {
			pages = append(pages, request)
		}
	}
	if len(pages) != wantPages {
		t.Fatalf("%s pages=%d want=%d", operationName, len(pages), wantPages)
	}
	if wantPages > 0 && bytes.Contains(pages[0].body, []byte(`"cursor"`)) {
		t.Fatalf("%s first page reused a cursor: %s", operationName, pages[0].body)
	}
	if wantPages > 1 && !bytes.Contains(pages[1].body, []byte(`"cursor":"`+cursor+`"`)) {
		t.Fatalf("%s second page missing fresh cursor: %s", operationName, pages[1].body)
	}
}

func twitchProductResultSignature(t *testing.T, result Result) string {
	t.Helper()
	var builder strings.Builder
	var info any
	if err := json.Unmarshal(result.InfoJSON, &info); err != nil {
		t.Fatalf("invalid result metadata: %v", err)
	}
	normalized, err := json.Marshal(normalizeTwitchProductResultValue(info))
	if err != nil {
		t.Fatalf("normalize result metadata: %v", err)
	}
	builder.Write(normalized)
	if result.Filename != "" {
		data, err := os.ReadFile(result.Filename)
		if err != nil {
			t.Fatal(err)
		}
		builder.WriteByte('|')
		builder.Write(data)
	}
	for _, child := range result.Entries {
		builder.WriteByte('{')
		builder.WriteString(twitchProductResultSignature(t, child))
		builder.WriteByte('}')
	}
	return builder.String()
}

func normalizeTwitchProductResultValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "filepath" {
				delete(value, key)
				continue
			}
			value[key] = normalizeTwitchProductResultValue(child)
		}
		return value
	case []any:
		for index, child := range value {
			value[index] = normalizeTwitchProductResultValue(child)
		}
		return value
	case string:
		parsed, err := url.Parse(value)
		if err == nil && (parsed.Hostname() == "usher.ttvnw.net" || parsed.Hostname() == "edge.ttvnw.net") {
			query := parsed.Query()
			query.Del("p")
			parsed.RawQuery = query.Encode()
			return parsed.String()
		}
	}
	return value
}

func TestProductTwitchVODHLSExactBytesAndIsolation(t *testing.T) {
	roundTripper := &twitchProductRoundTripper{}
	client := twitchProductClient(t, roundTripper)
	result, err := client.Run(context.Background(), twitchProductRequest("https://www.twitch.tv/videos/1234567890", t.TempDir()))
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil || string(data) != "init-one-two" {
		t.Fatalf("bytes=%q err=%v", data, err)
	}
	requests := roundTripper.requestsSnapshot()
	assertTwitchProductAmbientHeaders(t, requests)
	assertTwitchProductSignedMediaQueries(t, requests)
	if !containsTwitchProductRequest(requests, "https://static-cdn.jtvnw.net/storyboards/spec.json") {
		t.Fatal("storyboard request was not exercised")
	}
}

func TestProductTwitchLiveAndRerunHLSExactBytes(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata []byte
		live     bool
	}{
		{name: "live", live: true},
		{name: "rerun", metadata: bytes.Replace(twitchProductFixture("metadata.json"), []byte(`"type": "live"`), []byte(`"type": "rerun"`), 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTripper := &twitchProductRoundTripper{metadata: test.metadata}
			client := twitchProductClient(t, roundTripper)
			result, err := client.Run(context.Background(), twitchProductRequest("https://www.twitch.tv/fixture_channel", t.TempDir()))
			if err != nil || !result.Downloaded {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			wantLive := []byte(`"is_live":true`)
			if !test.live {
				wantLive = []byte(`"is_live":false`)
			}
			if !bytes.Contains(result.InfoJSON, wantLive) {
				t.Fatalf("live metadata=%s", result.InfoJSON)
			}
			data, err := os.ReadFile(result.Filename)
			if err != nil || string(data) != "init-one-two" {
				t.Fatalf("bytes=%q err=%v", data, err)
			}
			assertTwitchProductAmbientHeaders(t, roundTripper.requestsSnapshot())
			assertTwitchProductSignedMediaQueries(t, roundTripper.requestsSnapshot())
		})
	}
}

func TestProductTwitchClipDirectExactBytesAndIsolation(t *testing.T) {
	roundTripper := &twitchProductRoundTripper{}
	client := twitchProductClient(t, roundTripper)
	request := twitchProductRequest("https://clips.twitch.tv/CulturedFixtureSlug-abc_123", t.TempDir())
	request.Thumbnails.WriteAll = true
	result, err := client.Run(context.Background(), request)
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil || string(data) != "clip-exact-bytes" {
		t.Fatalf("bytes=%q err=%v", data, err)
	}
	requests := roundTripper.requestsSnapshot()
	assertTwitchProductAmbientHeaders(t, requests)
	assertTwitchProductSignedMediaQueries(t, requests)
	if !containsTwitchProductRequest(requests, "https://clips-media.twitch.tv/default.jpg") {
		t.Fatal("clip thumbnail request was not exercised")
	}
}

func TestProductTwitchPlayerEmbedReentryExactBytes(t *testing.T) {
	roundTripper := &twitchProductRoundTripper{}
	client := twitchProductClient(t, roundTripper)
	result, err := client.Run(context.Background(), twitchProductRequest("https://player.twitch.tv/?video=v1234567890", t.TempDir()))
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil || string(data) != "init-one-two" {
		t.Fatalf("bytes=%q err=%v", data, err)
	}
	assertTwitchProductAmbientHeaders(t, roundTripper.requestsSnapshot())
	assertTwitchProductSignedMediaQueries(t, roundTripper.requestsSnapshot())
}

func TestProductTwitchRetainedPlaylistFamiliesReenterAndDownload(t *testing.T) {
	for _, test := range []struct {
		name          string
		rawURL        string
		operationName string
		cursor        string
		pages         int
		entries       int
	}{
		{name: "videos", rawURL: "https://www.twitch.tv/fixture_channel/videos", operationName: "FilterableVideoTower_Videos", cursor: "cursor-page1-final", pages: 2, entries: 3},
		{name: "clips", rawURL: "https://www.twitch.tv/fixture_channel/clips", operationName: "ClipsCards__User", cursor: "cursor-clips-page1-final", pages: 2, entries: 3},
		{name: "collections", rawURL: "https://www.twitch.tv/fixture_channel/videos?filter=collections", operationName: "ChannelCollectionsContent", cursor: "cursor-collections-page1-final", pages: 2, entries: 3},
		{name: "direct_collection", rawURL: "https://www.twitch.tv/collections/FixtureCollection01", operationName: "CollectionSideBar", pages: 1, entries: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTripper := &twitchProductRoundTripper{}
			client := twitchProductClient(t, roundTripper)
			var signatures []string
			for run := 0; run < 2; run++ {
				before := roundTripper.requestsSnapshot()
				result, err := client.Run(context.Background(), twitchProductRequest(test.rawURL, t.TempDir()))
				if err != nil || len(result.Entries) != test.entries {
					t.Fatalf("run %d result entries=%d err=%v", run, len(result.Entries), err)
				}
				for index, child := range result.Entries {
					if !child.Downloaded && len(child.Entries) == 0 {
						t.Fatalf("run %d child %d was not executed: %+v", run, index, child)
					}
				}
				signatures = append(signatures, twitchProductResultSignature(t, result))
				after := roundTripper.requestsSnapshot()
				assertTwitchProductPagePair(t, after, test.operationName, test.cursor, len(before), test.pages)
				assertTwitchProductAmbientHeaders(t, after[len(before):])
				assertTwitchProductSignedMediaQueries(t, after[len(before):])
			}
			if signatures[0] != signatures[1] {
				t.Fatalf("reentry signatures differ:\nfirst=%s\nsecond=%s", signatures[0], signatures[1])
			}
		})
	}
}

func TestProductTwitchEnteredCancellationCleansArtifacts(t *testing.T) {
	roundTripper := &twitchProductRoundTripper{entered: make(chan struct{}, 1), release: make(chan struct{})}
	client := twitchProductClient(t, roundTripper)
	root := t.TempDir()
	request := twitchProductRequest("https://www.twitch.tv/fixture_channel", root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.Run(ctx, request)
		done <- err
	}()
	select {
	case <-roundTripper.entered:
		cancel()
	case err := <-done:
		t.Fatalf("operation ended before entered cancellation: %v", err)
	}
	close(roundTripper.release)
	runErr := <-done
	if !errors.Is(runErr, context.Canceled) || !IsCategory(runErr, ErrorCancelled) {
		t.Fatalf("error=%v", runErr)
	}
	assertTwitchProductNoSecrets(t, runErr)
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("artifacts=%v err=%v", entries, err)
	}
}

func TestProductTwitchStatusFailureIsTypedAndSecretSafe(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		want     error
		category ErrorCategory
	}{
		{name: "redirect", status: http.StatusFound, want: extractor.ErrTwitchRedirect, category: ErrorNetwork},
		{name: "client", status: http.StatusBadRequest, want: extractor.ErrTwitchClient, category: ErrorNetwork},
		{name: "rate", status: http.StatusTooManyRequests, want: extractor.ErrTwitchRateLimited, category: ErrorNetwork},
		{name: "legal", status: http.StatusUnavailableForLegalReasons, want: extractor.ErrRegionRestricted, category: ErrorUnsupported},
		{name: "server", status: http.StatusInternalServerError, want: extractor.ErrTwitchServer, category: ErrorNetwork},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTripper := &twitchProductRoundTripper{statuses: map[string]int{"https://gql.twitch.tv/gql": test.status}}
			client := twitchProductClient(t, roundTripper)
			_, err := client.Run(context.Background(), twitchProductRequest("https://www.twitch.tv/fixture_channel", t.TempDir()))
			if err == nil || !errors.Is(err, test.want) || !IsCategory(err, test.category) {
				t.Fatalf("error=%v want=%v category=%v", err, test.want, test.category)
			}
			assertTwitchProductNoSecrets(t, err)
		})
	}
}

func TestProductTwitchHostileHLSFailsClosed(t *testing.T) {
	roundTripper := &twitchProductRoundTripper{hostileHLS: true}
	client := twitchProductClient(t, roundTripper)
	_, err := client.Run(context.Background(), twitchProductRequest("https://www.twitch.tv/fixture_channel", t.TempDir()))
	if err == nil || !errors.Is(err, hls.ErrInvalidPlaylist) || !IsCategory(err, ErrorInternal) {
		t.Fatalf("error=%v", err)
	}
	assertTwitchProductNoSecrets(t, err)
}

func TestProductTwitchAdaptersSelectExactKeys(t *testing.T) {
	registry := productRegistry()
	for _, test := range []struct {
		rawURL string
		key    string
	}{
		{"https://www.twitch.tv/videos/123", "twitch_vod"},
		{"https://www.twitch.tv/fixture/clip/Slug", "twitch_clips"},
		{"https://www.twitch.tv/collections/Collection", "twitch_collection"},
		{"https://www.twitch.tv/fixture/videos", "twitch_videos"},
		{"https://www.twitch.tv/fixture/clips", "twitch_videos_clips"},
		{"https://www.twitch.tv/fixture/videos?filter=collections", "twitch_videos_collections"},
		{"https://www.twitch.tv/fixture", "twitch_stream"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.key {
			t.Fatalf("Select(%q)=%v err=%v want=%q", test.rawURL, selected, err, test.key)
		}
	}
}
