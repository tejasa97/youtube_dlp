package ytdlp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/network"
)

const niconicoProductFixtureRoot = "../../conformance/extractors/risk/niconico"

func readNiconicoProductFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(niconicoProductFixtureRoot, name))
	if err != nil {
		t.Fatalf("read Niconico product fixture %s: %v", name, err)
	}
	return body
}

type niconicoProductRoundTripper struct {
	mu              sync.Mutex
	requests        []niconicoProductRequest
	segmentStatus   int
	redirectPath    string
	hostileHLSChild bool
	entered         chan struct{}
	block           <-chan struct{}
}

type niconicoProductRequest struct {
	method  string
	host    string
	path    string
	query   string
	headers http.Header
	body    []byte
}

func (transport *niconicoProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	if request.Body != nil {
		body, _ = io.ReadAll(request.Body)
		_ = request.Body.Close()
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, niconicoProductRequest{
		method: request.Method, host: request.URL.Host, path: request.URL.Path,
		query: request.URL.RawQuery, headers: request.Header.Clone(), body: append([]byte(nil), body...),
	})
	transport.mu.Unlock()

	status := http.StatusOK
	responseBody := []byte{}
	responseHeaders := make(http.Header)
	if request.URL.Path == transport.redirectPath {
		status = http.StatusFound
		responseHeaders.Set("Location", "https://evil.example/redirected")
		responseBody = []byte("redirect")
	}
	if strings.HasSuffix(request.URL.Path, "-0.ts") && transport.entered != nil {
		select {
		case transport.entered <- struct{}{}:
		default:
		}
	}
	if strings.HasSuffix(request.URL.Path, "-0.ts") && transport.block != nil {
		select {
		case <-transport.block:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	}
	switch {
	case request.URL.Host == "nvapi.nicovideo.jp" && strings.HasPrefix(request.URL.Path, "/api/watch/v3_guest/"):
		videoID := strings.TrimPrefix(request.URL.Path, "/api/watch/v3_guest/")
		responseBody = niconicoProductWatchFixture(videoID)
	case request.URL.Host == "nvapi.nicovideo.jp" && strings.HasPrefix(request.URL.Path, "/v1/watch/") && strings.HasSuffix(request.URL.Path, "/access-rights/hls"):
		responseBody = readNiconicoProductFixtureUnchecked("access_rights.json")
	case request.URL.Host == "nvapi.nicovideo.jp" && request.URL.Path == "/v2/mylists/99":
		if request.URL.Query().Get("pageSize") == "1" {
			responseBody = []byte(`{"meta":{"status":200},"data":{"mylist":{"name":"Fixture mylist","description":"Fixture list","owner":{"id":"7","name":"Owner"}}}}`)
		} else {
			responseBody = niconicoProductCollectionFixture("mylist", niconicoProductPage(request))
		}
	case request.URL.Host == "nvapi.nicovideo.jp" && request.URL.Path == "/v2/series/88":
		if request.URL.Query().Get("pageSize") == "1" {
			responseBody = []byte(`{"meta":{"status":200},"data":{"detail":{"title":"Fixture series","description":"Fixture series description","owner":{"id":"8","name":"Series owner"}}}}`)
		} else {
			responseBody = niconicoProductCollectionFixture("series", niconicoProductPage(request))
		}
	case request.URL.Host == "nvapi.nicovideo.jp" && request.URL.Path == "/v2/users/7/videos":
		responseBody = niconicoProductCollectionFixture("user", niconicoProductPage(request))
	case request.URL.Host == "delivery.domand.nicovideo.jp" && request.URL.Path == "/fixture/master.m3u8":
		responseBody = readNiconicoProductFixtureUnchecked("master.m3u8")
	case request.URL.Host == "delivery.domand.nicovideo.jp" && strings.HasSuffix(request.URL.Path, "v360.m3u8"):
		responseBody = readNiconicoProductFixtureUnchecked("video-v360.m3u8")
	case request.URL.Host == "delivery.domand.nicovideo.jp" && strings.HasSuffix(request.URL.Path, "v720.m3u8"):
		responseBody = readNiconicoProductFixtureUnchecked("video-v720.m3u8")
		if transport.hostileHLSChild {
			responseBody = bytes.Replace(responseBody, []byte("v720-0.ts?token=signed%2Fvalue&expires=1700000000"), []byte("https://evil.example/hostile.ts"), 1)
		}
	case request.URL.Host == "delivery.domand.nicovideo.jp" && strings.HasSuffix(request.URL.Path, "a64.m3u8"):
		responseBody = readNiconicoProductFixtureUnchecked("audio-a64.m3u8")
	case request.URL.Host == "delivery.domand.nicovideo.jp" && strings.HasSuffix(request.URL.Path, "a128.m3u8"):
		responseBody = readNiconicoProductFixtureUnchecked("audio-a128.m3u8")
	case request.URL.Host == "delivery.domand.nicovideo.jp" && strings.HasSuffix(request.URL.Path, "v360-0.ts"):
		if transport.segmentStatus != 0 {
			status = transport.segmentStatus
		}
		responseBody = []byte("fixture-video-360")
	case request.URL.Host == "delivery.domand.nicovideo.jp" && strings.HasSuffix(request.URL.Path, "v720-0.ts"):
		if transport.segmentStatus != 0 {
			status = transport.segmentStatus
		}
		responseBody = []byte("fixture-video-720")
	case request.URL.Host == "delivery.domand.nicovideo.jp" && strings.HasSuffix(request.URL.Path, "a64-0.ts"):
		if transport.segmentStatus != 0 {
			status = transport.segmentStatus
		}
		responseBody = []byte("fixture-audio-64")
	case request.URL.Host == "delivery.domand.nicovideo.jp" && strings.HasSuffix(request.URL.Path, "a128-0.ts"):
		if transport.segmentStatus != 0 {
			status = transport.segmentStatus
		}
		responseBody = []byte("fixture-audio-128")
	case request.URL.Host == "img.cdn.nimg.jp":
		responseBody = []byte("fixture-thumbnail")
	case request.URL.Host == "www.nicovideo.jp" && (strings.HasPrefix(request.URL.Path, "/search/") || strings.HasPrefix(request.URL.Path, "/tag/")):
		responseBody = niconicoProductSearchFixture(niconicoProductPage(request))
	default:
		status = http.StatusNotFound
		responseBody = []byte("not found")
	}
	responseHeaders.Set("Content-Length", itoaNiconico(len(responseBody)))
	return &http.Response{
		StatusCode: status, Body: io.NopCloser(bytes.NewReader(responseBody)),
		Header: responseHeaders, Request: request,
	}, nil
}

func readNiconicoProductFixtureUnchecked(name string) []byte {
	body, _ := os.ReadFile(filepath.Join(niconicoProductFixtureRoot, name))
	return body
}

func niconicoProductPage(request *http.Request) int {
	page, _ := strconv.Atoi(request.URL.Query().Get("page"))
	if page < 1 {
		return 1
	}
	return page
}

func niconicoProductWatchFixture(videoID string) []byte {
	body := readNiconicoProductFixtureUnchecked("watch_guest.json")
	return bytes.Replace(body, []byte(`"id":"sm9"`), []byte(`"id":"`+videoID+`"`), 1)
}

func niconicoProductCollectionFixture(kind string, page int) []byte {
	name := kind + "_page_" + strconv.Itoa(page) + ".json"
	if page < 1 || page > 2 {
		if kind == "mylist" {
			return []byte(`{"meta":{"status":200},"data":{"mylist":{"items":[]}}}`)
		}
		if kind == "user" {
			return []byte(`{"meta":{"status":200},"data":{"totalCount":101,"items":[]}}`)
		}
		return []byte(`{"meta":{"status":200},"data":{"items":[]}}`)
	}
	return readNiconicoProductFixtureUnchecked(name)
}

func niconicoProductSearchFixture(page int) []byte {
	switch page {
	case 1:
		return readNiconicoProductFixtureUnchecked("search_page_1.html")
	case 2:
		return readNiconicoProductFixtureUnchecked("search_page_2.html")
	default:
		return readNiconicoProductFixtureUnchecked("search_empty.html")
	}
}

func niconicoProductAmbientHeaders() http.Header {
	return http.Header{
		"Authorization":       {"Bearer ambient-secret"},
		"Cookie":              {"session=ambient-secret"},
		"Proxy-Authorization": {"Basic ambient-secret"},
		"Referer":             {"https://ambient.example/secret"},
	}
}

func newNiconicoProductClient(roundTripper *niconicoProductRoundTripper) *Client {
	return NewClient(withTransportFactory(func(config network.Config) (*network.Client, error) {
		config.RoundTripper = roundTripper
		config.DefaultHeaders = niconicoProductAmbientHeaders()
		return network.New(config)
	}))
}

func runNiconicoProductFixture(t *testing.T, client *Client, request Request) (Result, string) {
	t.Helper()
	result, err := client.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("registered product Run: %v", err)
	}
	return result, result.Extractor
}

func niconicoProductRequests(roundTripper *niconicoProductRoundTripper) []niconicoProductRequest {
	roundTripper.mu.Lock()
	defer roundTripper.mu.Unlock()
	requests := make([]niconicoProductRequest, len(roundTripper.requests))
	copy(requests, roundTripper.requests)
	return requests
}

func assertNiconicoProductRequestIsolation(t *testing.T, roundTripper *niconicoProductRoundTripper) []niconicoProductRequest {
	t.Helper()
	requests := niconicoProductRequests(roundTripper)
	for _, request := range requests {
		for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
			if got := request.headers.Get(name); got != "" {
				t.Fatalf("ambient %s leaked on %s %s: %q", name, request.method, request.path, got)
			}
		}
	}
	return requests
}

func assertNiconicoProductPlaylistChildren(t *testing.T, result Result, rootExtractor string, wantRoot string) {
	t.Helper()
	if rootExtractor != wantRoot {
		t.Fatalf("root extractor=%q; want %q", rootExtractor, wantRoot)
	}
	wantIDs := []string{"sm10", "sm11", "sm12"}
	if len(result.Entries) != len(wantIDs) {
		t.Fatalf("playlist children=%d; want %d: %+v", len(result.Entries), len(wantIDs), result.Entries)
	}
	for index, wantID := range wantIDs {
		child := result.Entries[index]
		if child.Extractor != "niconico" || !child.Downloaded || child.Filename == "" {
			t.Fatalf("child %d key/download/path = %q/%t/%q; want niconico/downloaded", index, child.Extractor, child.Downloaded, child.Filename)
		}
		data, err := os.ReadFile(child.Filename)
		if err != nil {
			t.Fatalf("read child %d output: %v", index, err)
		}
		if got, want := string(data), "fixture-video-720"; got != want {
			t.Fatalf("child %d bytes=%q; want %q", index, got, want)
		}
		if child.Bytes != int64(len(data)) {
			t.Fatalf("child %d byte count=%d; want %d", index, child.Bytes, len(data))
		}
		var metadata map[string]any
		if err := json.Unmarshal(child.InfoJSON, &metadata); err != nil {
			t.Fatalf("child %d metadata: %v", index, err)
		}
		if got, _ := metadata["id"].(string); got != wantID {
			t.Fatalf("child %d metadata id=%q; want %q", index, got, wantID)
		}
	}
}

func assertNiconicoProductChildAccessRights(t *testing.T, requests []niconicoProductRequest, wantRuns int) {
	t.Helper()
	for _, videoID := range []string{"sm10", "sm11", "sm12"} {
		path := "/v1/watch/" + videoID + "/access-rights/hls"
		matches := 0
		for _, request := range requests {
			if request.path != path {
				continue
			}
			matches++
			if request.method != http.MethodPost || request.headers.Get("X-Access-Right-Key") != "fixture-access-key==" {
				t.Fatalf("access-rights request for %s = %#v; want isolated POST with fixture key", videoID, request)
			}
			if request.query != "actionTrackId=fixture-watch-track" {
				t.Fatalf("access-rights action track for %s=%q; want watch.Client.WatchTrackID", videoID, request.query)
			}
			var decoded struct {
				Outputs [][]string `json:"outputs"`
			}
			if err := json.Unmarshal(request.body, &decoded); err != nil {
				t.Fatalf("access-rights body for %s: %v", videoID, err)
			}
			if len(decoded.Outputs) != 4 || decoded.Outputs[0][0] != "v360" || decoded.Outputs[0][1] != "a64" || decoded.Outputs[3][0] != "v720" || decoded.Outputs[3][1] != "a128" {
				t.Fatalf("access-rights outputs for %s=%#v", videoID, decoded.Outputs)
			}
		}
		if matches != wantRuns {
			t.Fatalf("access-rights requests for %s=%d; want %d", videoID, matches, wantRuns)
		}
	}
}

func assertNiconicoProductPages(t *testing.T, requests []niconicoProductRequest, host, pathPrefix string, wantCount, wantRuns int) {
	t.Helper()
	pages := map[string]int{}
	for _, request := range requests {
		if request.host != host || !strings.HasPrefix(request.path, pathPrefix) {
			continue
		}
		query := request.query
		if strings.Contains(query, "page=1") {
			pages["1"]++
		}
		if strings.Contains(query, "page=2") {
			pages["2"]++
		}
	}
	if pages["1"] != wantRuns || pages["2"] != wantRuns {
		t.Fatalf("pages for %s%s = %#v; want page 1 and 2 fetched %d times in requests %#v", host, pathPrefix, pages, wantRuns, requests)
	}
	count := 0
	for _, request := range requests {
		if request.host == host && strings.HasPrefix(request.path, pathPrefix) {
			count++
		}
	}
	if count != wantCount*wantRuns {
		t.Fatalf("requests for %s%s=%d; want %d", host, pathPrefix, count, wantCount*wantRuns)
	}
}

func assertNiconicoProductPlaylistResultsEqual(t *testing.T, first, second Result) {
	t.Helper()
	if len(first.Entries) != len(second.Entries) {
		t.Fatalf("reusable playlist lengths=%d/%d", len(first.Entries), len(second.Entries))
	}
	for index := range first.Entries {
		left, right := first.Entries[index], second.Entries[index]
		if left.Extractor != right.Extractor || left.Bytes != right.Bytes {
			t.Fatalf("reusable child %d identity=%q/%q bytes=%d/%d", index, left.Extractor, right.Extractor, left.Bytes, right.Bytes)
		}
		leftInfo, err := os.ReadFile(left.Filename)
		if err != nil {
			t.Fatalf("read first reusable child %d: %v", index, err)
		}
		rightInfo, err := os.ReadFile(right.Filename)
		if err != nil {
			t.Fatalf("read second reusable child %d: %v", index, err)
		}
		if !bytes.Equal(leftInfo, rightInfo) || !bytes.Equal(first.Entries[index].InfoJSON, second.Entries[index].InfoJSON) {
			t.Fatalf("reusable child %d changed bytes or metadata", index)
		}
	}
}

func assertNiconicoProductError(t *testing.T, err error, category ErrorCategory, want error) {
	t.Helper()
	if err == nil || !IsCategory(err, category) || !errors.Is(err, want) {
		t.Fatalf("error=%v; want category=%s sentinel=%v", err, category, want)
	}
	for _, secret := range []string{"signed%2Fvalue", "fixture-access-key", "fixture-watch-track", "ambient-secret", "token="} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("secret %q leaked in error %q", secret, err)
		}
	}
}

func itoaNiconico(value int) string {
	if value == 0 {
		return "0"
	}
	return string([]byte(fmtNiconicoInt(value)))
}

func fmtNiconicoInt(value int) []byte {
	if value == 0 {
		return []byte{'0'}
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := make([]byte, 0, 16)
	for value > 0 {
		digits = append(digits, byte('0'+value%10))
		value /= 10
	}
	if negative {
		digits = append(digits, '-')
	}
	for left, right := 0, len(digits)-1; left < right; left, right = left+1, right-1 {
		digits[left], digits[right] = digits[right], digits[left]
	}
	return digits
}

func TestProductNiconicoRegisteredWatchHLSDownloadIsolatedAndSigned(t *testing.T) {
	roundTripper := &niconicoProductRoundTripper{}
	root := t.TempDir()
	request := Request{
		URL: "https://www.nicovideo.jp/watch/sm9", OutputDir: root,
		OutputTemplate: "%(id)s.%(ext)s", Format: "bestvideo", Overwrite: true,
		Thumbnails: ThumbnailOptions{WriteAll: true},
	}
	client := newNiconicoProductClient(roundTripper)
	result, err := client.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("registered product Run: %v", err)
	}
	if result.Extractor != "niconico" || !result.Downloaded || result.Filename == "" {
		t.Fatalf("root=%q result=%+v", result.Extractor, result)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "fixture-video-720"; got != want {
		t.Fatalf("download=%q want=%q", got, want)
	}

	requests := assertNiconicoProductRequestIsolation(t, roundTripper)
	var accessBody []byte
	guestRequests := 0
	for _, request := range requests {
		if request.host == "nvapi.nicovideo.jp" && request.path == "/api/watch/v3_guest/sm9" {
			guestRequests++
			actionTrackID := request.query
			if !strings.HasPrefix(actionTrackID, "actionTrackId=AAAAAAAAAA_") {
				t.Fatalf("guest action track query=%q; want AAAAAAAAAA_<unix milliseconds>", actionTrackID)
			}
			milliseconds, parseErr := strconv.ParseInt(strings.TrimPrefix(actionTrackID, "actionTrackId=AAAAAAAAAA_"), 10, 64)
			if parseErr != nil || milliseconds <= 0 {
				t.Fatalf("guest action track query=%q; want numeric unix milliseconds", actionTrackID)
			}
		}
		if request.path == "/v1/watch/sm9/access-rights/hls" {
			accessBody = append([]byte(nil), request.body...)
		}
		if request.path == "/fixture/master.m3u8" && request.query != "token=signed%2Fvalue&expires=1700000000" {
			t.Fatalf("signed manifest query changed: %q", request.query)
		}
		if request.host == "delivery.domand.nicovideo.jp" && request.query != "token=signed%2Fvalue&expires=1700000000" {
			t.Fatalf("signed media query changed on %s: %q", request.path, request.query)
		}
		if request.path == "/v1/watch/sm9/access-rights/hls" && request.query != "actionTrackId=fixture-watch-track" {
			t.Fatalf("access-right action track query changed: %q", request.query)
		}
	}
	if guestRequests != 1 {
		t.Fatalf("guest metadata requests=%d; want 1", guestRequests)
	}
	var decoded struct {
		Outputs [][]string `json:"outputs"`
	}
	if err := json.Unmarshal(accessBody, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Outputs) != 4 || decoded.Outputs[0][0] != "v360" || decoded.Outputs[0][1] != "a64" || decoded.Outputs[3][0] != "v720" || decoded.Outputs[3][1] != "a128" {
		t.Fatalf("access outputs=%#v", decoded.Outputs)
	}
}

func TestProductNiconicoRegisteredBestAudioDownloadIsExactAndIsolated(t *testing.T) {
	roundTripper := &niconicoProductRoundTripper{}
	client := newNiconicoProductClient(roundTripper)
	request := Request{
		URL: "https://www.nicovideo.jp/watch/sm9", OutputDir: t.TempDir(),
		OutputTemplate: "%(id)s.%(ext)s", Format: "bestaudio", Overwrite: true,
	}
	result, rootExtractor := runNiconicoProductFixture(t, client, request)
	if rootExtractor != "niconico" || !result.Downloaded || result.Filename == "" {
		t.Fatalf("root=%q result=%+v", rootExtractor, result)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "fixture-audio-128"; got != want {
		t.Fatalf("download=%q want=%q", got, want)
	}
	requests := assertNiconicoProductRequestIsolation(t, roundTripper)
	accessRights := 0
	media := 0
	for _, request := range requests {
		if request.path == "/v1/watch/sm9/access-rights/hls" {
			accessRights++
			if request.query != "actionTrackId=fixture-watch-track" {
				t.Fatalf("access-right query=%q", request.query)
			}
		}
		if request.path == "/fixture/audio/a128.m3u8" {
			media++
		}
	}
	if accessRights != 1 || media != 1 {
		t.Fatalf("access-rights/media requests=%d/%d; requests=%#v", accessRights, media, requests)
	}
}

func TestProductNiconicoRegisteredSearchChildReentryIsolation(t *testing.T) {
	for _, test := range []struct {
		name string
		url  string
		root string
		path string
	}{
		{name: "search URL", url: "https://www.nicovideo.jp/search/sm9", root: "niconico_search_url", path: "/search/"},
		{name: "tag URL", url: "https://www.nicovideo.jp/tag/fixture", root: "niconico_tag", path: "/tag/"},
		{name: "pseudo search", url: "nicosearch:fixture term", root: "niconico_search", path: "/search/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTripper := &niconicoProductRoundTripper{}
			client := newNiconicoProductClient(roundTripper)
			request := Request{
				URL: test.url, OutputDir: t.TempDir(), OutputTemplate: "%(id)s.%(ext)s", Format: "bestvideo",
				Overwrite: true,
			}
			first, rootExtractor := runNiconicoProductFixture(t, client, request)
			assertNiconicoProductPlaylistChildren(t, first, rootExtractor, test.root)
			second, secondRoot := runNiconicoProductFixture(t, client, request)
			assertNiconicoProductPlaylistChildren(t, second, secondRoot, test.root)
			assertNiconicoProductPlaylistResultsEqual(t, first, second)
			requests := assertNiconicoProductRequestIsolation(t, roundTripper)
			assertNiconicoProductChildAccessRights(t, requests, 2)
			assertNiconicoProductPages(t, requests, "www.nicovideo.jp", test.path, 2, 2)
		})
	}
}

func TestProductNiconicoHLSFailureAndCancellationLeaveNoArtifacts(t *testing.T) {
	for _, test := range []struct {
		name             string
		expectedCategory ErrorCategory
		expectedSentinel error
		configure        func(*niconicoProductRoundTripper, context.CancelFunc) (<-chan struct{}, <-chan struct{})
	}{
		{
			name:             "segment failure",
			expectedCategory: ErrorNetwork,
			expectedSentinel: errNiconicoMediaStatus,
			configure: func(roundTripper *niconicoProductRoundTripper, _ context.CancelFunc) (<-chan struct{}, <-chan struct{}) {
				roundTripper.segmentStatus = http.StatusBadGateway
				return nil, nil
			},
		},
		{
			name:             "in-flight cancellation",
			expectedCategory: ErrorCancelled,
			expectedSentinel: context.Canceled,
			configure: func(roundTripper *niconicoProductRoundTripper, cancel context.CancelFunc) (<-chan struct{}, <-chan struct{}) {
				entered := make(chan struct{}, 1)
				block := make(chan struct{})
				roundTripper.entered, roundTripper.block = entered, block
				return entered, block
			},
		},
		{
			name:             "manifest redirect",
			expectedCategory: ErrorSecurity,
			expectedSentinel: errNiconicoMediaRedirect,
			configure: func(roundTripper *niconicoProductRoundTripper, _ context.CancelFunc) (<-chan struct{}, <-chan struct{}) {
				roundTripper.redirectPath = "/fixture/video/v720.m3u8"
				return nil, nil
			},
		},
		{
			name:             "hostile HLS child",
			expectedCategory: ErrorSecurity,
			expectedSentinel: errNiconicoMediaHost,
			configure: func(roundTripper *niconicoProductRoundTripper, _ context.CancelFunc) (<-chan struct{}, <-chan struct{}) {
				roundTripper.hostileHLSChild = true
				return nil, nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTripper := &niconicoProductRoundTripper{}
			client := newNiconicoProductClient(roundTripper)
			root := t.TempDir()
			request := Request{URL: "https://www.nicovideo.jp/watch/sm9", OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Format: "bestvideo", Overwrite: true, Downloader: DownloaderOptions{Attempts: 1}}
			ctx, cancel := context.WithCancel(context.Background())
			entered, _ := test.configure(roundTripper, cancel)
			if entered == nil {
				_, err := client.Run(ctx, request)
				if err == nil {
					t.Fatalf("failure unexpectedly succeeded; requests=%#v", niconicoProductRequests(roundTripper))
				}
				assertNiconicoProductError(t, err, test.expectedCategory, test.expectedSentinel)
			} else {
				done := make(chan error, 1)
				go func() {
					_, runErr := client.Run(ctx, request)
					done <- runErr
				}()
				<-entered
				cancel()
				assertNiconicoProductError(t, <-done, test.expectedCategory, test.expectedSentinel)
			}
			cancel()
			assertNiconicoProductRequestIsolation(t, roundTripper)
			files, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 0 {
				t.Fatalf("artifacts remain after %s: %v", test.name, files)
			}
		})
	}
}

func TestProductNiconicoPlaylistChildReentryUsesRegisteredKeys(t *testing.T) {
	for _, test := range []struct {
		name  string
		url   string
		root  string
		path  string
		count int
	}{
		{name: "mylist", url: "https://www.nicovideo.jp/mylist/99", root: "niconico_playlist", path: "/v2/mylists/99", count: 3},
		{name: "series", url: "https://www.nicovideo.jp/series/88", root: "niconico_series", path: "/v2/series/88", count: 3},
		{name: "user videos", url: "https://www.nicovideo.jp/user/7/video", root: "niconico_user", path: "/v2/users/7/videos", count: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTripper := &niconicoProductRoundTripper{}
			client := newNiconicoProductClient(roundTripper)
			request := Request{
				URL: test.url, OutputDir: t.TempDir(), OutputTemplate: "%(id)s.%(ext)s", Format: "bestvideo",
				Overwrite: true,
			}
			first, rootExtractor := runNiconicoProductFixture(t, client, request)
			assertNiconicoProductPlaylistChildren(t, first, rootExtractor, test.root)
			second, secondRoot := runNiconicoProductFixture(t, client, request)
			assertNiconicoProductPlaylistChildren(t, second, secondRoot, test.root)
			assertNiconicoProductPlaylistResultsEqual(t, first, second)
			requests := assertNiconicoProductRequestIsolation(t, roundTripper)
			assertNiconicoProductChildAccessRights(t, requests, 2)
			assertNiconicoProductPages(t, requests, "nvapi.nicovideo.jp", test.path, test.count, 2)
		})
	}
}
