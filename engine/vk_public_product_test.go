package engine

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/downloader"
	"github.com/tejasa97/youtube_dlp/internal/extractor"
	"github.com/tejasa97/youtube_dlp/internal/network"
)

type vkProductRequest struct {
	method, host, path, rawQuery string
	body                         string
	headers                      http.Header
}

type vkProductRoundTripper struct {
	mu           sync.Mutex
	requests     []vkProductRequest
	bodies       map[string][]byte
	statuses     map[string]int
	blockPath    string
	redirectPath string
	started      chan string
	liveOffline  bool
	hostile      int
}

func (transport *vkProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	var requestBody []byte
	if request.Method == http.MethodPost && request.Body != nil {
		requestBody, _ = io.ReadAll(request.Body)
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, vkProductRequest{method: request.Method, host: request.URL.Hostname(), path: request.URL.Path, rawQuery: request.URL.RawQuery, body: string(requestBody), headers: request.Header.Clone()})
	status := transport.statuses[request.Method+" "+request.URL.Path]
	body := transport.bodies[request.Method+" "+request.URL.Path]
	if request.URL.Path == "/record/init.mp4" && request.URL.RawQuery == "sig=init-signed&expires=1700000000" {
		body = []byte("vkplay-hls-init-")
	}
	block := transport.blockPath != "" && request.URL.Path == transport.blockPath
	started := transport.started
	transport.mu.Unlock()
	if request.URL.Hostname() == "evil.example" {
		transport.mu.Lock()
		transport.hostile++
		transport.mu.Unlock()
		return nil, errors.New("hostile target reached")
	}
	if request.Method == http.MethodPost {
		form, _ := url.ParseQuery(string(requestBody))
		switch {
		case request.URL.Path == "/al_video.php" && form.Get("act") == "load_videos_silent":
			if form.Get("offset") == "0" {
				body = transport.bodies["POST /al_video_page1"]
			} else {
				body = transport.bodies["POST /al_video_page2"]
			}
		case request.URL.Path == "/wkview.php":
			body = transport.bodies["POST /wkview.php"]
		}
	}
	if request.URL.Path == "/v1/blog/caster/public_video_stream" && transport.liveOffline {
		body = transport.bodies["GET /v1/blog/caster/public_video_stream_offline"]
	}
	if started != nil {
		if block {
			select {
			case started <- request.Method + " " + request.URL.Path:
			default:
			}
		}
	}
	if block {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}
	if transport.redirectPath == request.URL.Path {
		header := make(http.Header)
		header.Set("Location", "https://vkplay.live/redirected")
		return &http.Response{StatusCode: http.StatusFound, Header: header, Body: io.NopCloser(strings.NewReader("redirect")), Request: request}, nil
	}
	if status == 0 {
		status = http.StatusOK
	}
	if body == nil {
		status = http.StatusNotFound
		body = []byte("missing fixture")
	}
	body = append([]byte(nil), body...)
	if request.URL.Path == "/record/indexed.mp4" {
		if rawRange := request.Header.Get("Range"); rawRange != "" {
			var start, end int
			if _, err := fmt.Sscanf(rawRange, "bytes=%d-%d", &start, &end); err != nil || start < 0 || end < start || end >= len(body) {
				return &http.Response{StatusCode: http.StatusRequestedRangeNotSatisfiable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("range")), Request: request}, nil
			}
			status = http.StatusPartialContent
			header := make(http.Header)
			header.Set("Content-Length", itoa(end-start+1))
			header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(body)))
			header.Set("Content-Type", vkProductContentType(request.URL.Path))
			return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(body[start : end+1])), Request: request}, nil
		}
	}
	header := make(http.Header)
	header.Set("Content-Length", itoa(len(body)))
	header.Set("Content-Type", vkProductContentType(request.URL.Path))
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}

func vkProductContentType(path string) string {
	switch {
	case strings.HasSuffix(path, ".m3u8"):
		return "application/vnd.apple.mpegurl"
	case strings.HasSuffix(path, ".vtt"):
		return "text/vtt"
	case strings.HasSuffix(path, ".jpg"):
		return "image/jpeg"
	case strings.HasSuffix(path, ".mp4"):
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}

func readVKProductFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "internal", "extractor", "testdata", "vk", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func newVKProductTransport(t *testing.T) *vkProductRoundTripper {
	t.Helper()
	return &vkProductRoundTripper{bodies: map[string][]byte{
		"POST /al_video.php":        readVKProductFixture(t, "video_api.json"),
		"POST /al_video_page1":      readVKProductFixture(t, "user_page1.json"),
		"POST /al_video_page2":      readVKProductFixture(t, "user_page2.json"),
		"POST /wkview.php":          readVKProductFixture(t, "wall_page.json"),
		"GET /video_ext.php":        readVKProductFixture(t, "video_embed.html"),
		"GET /video/@fixture_group": readVKProductFixture(t, "user_page.html"),
		"GET /v1/blog/caster/public_video_stream/record/f5e6e3b5-dc52-4d14-965d-0680dd2882da": readVKProductFixture(t, "vkplay_record.json"),
		"GET /v1/blog/caster/public_video_stream":                                             readVKProductFixture(t, "vkplay_live.json"),
		"GET /v1/blog/caster/public_video_stream_offline":                                     readVKProductFixture(t, "vkplay_live_offline.json"),
		"GET /video/master.m3u8":                                                              readVKProductFixture(t, "vk_hls_master.m3u8"),
		"GET /video/media.m3u8":                                                               readVKProductFixture(t, "vk_hls_media.m3u8"),
		"GET /record/master.m3u8":                                                             readVKProductFixture(t, "vk_hls_master.m3u8"),
		"GET /record/media.m3u8":                                                              readVKProductFixture(t, "vk_hls_media.m3u8"),
		"GET /record/manifest.mpd":                                                            readVKProductFixture(t, "vk_dash_record.mpd"),
		"GET /record/init.mp4":                                                                []byte("vkplay-dash-init"),
		"GET /record/one.m4s":                                                                 []byte("vkplay-dash-one"),
		"GET /record/two.m4s":                                                                 []byte("vkplay-dash-two"),
		"GET /live/master.m3u8":                                                               readVKProductFixture(t, "vk_hls_master.m3u8"),
		"GET /live/media.m3u8":                                                                readVKProductFixture(t, "vk_hls_media.m3u8"),
		"GET /video/init.mp4":                                                                 []byte("vk-hls-init-"),
		"GET /live/init.mp4":                                                                  []byte("vk-live-hls-init-"),
		"GET /video/key.bin":                                                                  []byte("0123456789abcdef"),
		"GET /record/key.bin":                                                                 []byte("0123456789abcdef"),
		"GET /live/key.bin":                                                                   []byte("0123456789abcdef"),
		"GET /video/one.bin":                                                                  vkProductEncryptedSegment("vk-hls-one-"),
		"GET /video/two.bin":                                                                  vkProductEncryptedSegment("vk-hls-two"),
		"GET /record/one.bin":                                                                 vkProductEncryptedSegment("vkplay-hls-one-"),
		"GET /record/two.bin":                                                                 vkProductEncryptedSegment("vkplay-hls-two"),
		"GET /live/one.bin":                                                                   vkProductEncryptedSegment("vk-live-hls-one-"),
		"GET /live/two.bin":                                                                   vkProductEncryptedSegment("vk-live-hls-two"),
		"GET /fixture/track.m4a":                                                              []byte("vk-wall-audio"),
		"GET /video/fixture-360.mp4":                                                          []byte("vk-direct-bytes"),
		"GET /record/high.mp4":                                                                []byte("vkplay-direct-bytes"),
		"GET /embed/fixture-720.mp4":                                                          []byte("vk-embed-bytes"),
		"GET /video/en.vtt":                                                                   []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nfixture\n"),
		"GET /impg/fixture-thumb.jpg":                                                         []byte("vk-thumb-bytes"),
		"GET /impg/embed-thumb.jpg":                                                           []byte("vk-embed-thumb-bytes"),
		"GET /video/hostile-master.m3u8":                                                      readVKProductFixture(t, "vk_hls_hostile_variant.m3u8"),
		"GET /video/hostile-segment-master.m3u8":                                              readVKProductFixture(t, "vk_hls_hostile_segment_master.m3u8"),
		"GET /video/hostile-segment-variant.m3u8":                                             readVKProductFixture(t, "vk_hls_hostile_segment_variant.m3u8"),
		"GET /video/hostile-dash.mpd":                                                         readVKProductFixture(t, "vk_dash_hostile.mpd"),
	}, started: make(chan string, 8)}
}

func vkProductEncryptedSegment(plaintext string) []byte {
	key := []byte("0123456789abcdef")
	iv := []byte("abcdef0123456789")
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	input := append([]byte(plaintext), bytes.Repeat([]byte{byte(padding)}, padding)...)
	ciphertext := make([]byte, len(input))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, input)
	return ciphertext
}

func vkProductSIDX(referenceSizes ...int) []byte {
	boxSize := 32 + 12*len(referenceSizes)
	box := make([]byte, boxSize)
	binary.BigEndian.PutUint32(box[0:4], uint32(boxSize))
	copy(box[4:8], []byte("sidx"))
	// version 0, flags 0, reference ID 1, timescale 1000, zero EPT/offset
	binary.BigEndian.PutUint32(box[12:16], 1)
	binary.BigEndian.PutUint32(box[16:20], 1000)
	binary.BigEndian.PutUint16(box[30:32], uint16(len(referenceSizes)))
	for index, size := range referenceSizes {
		offset := 32 + index*12
		binary.BigEndian.PutUint32(box[offset:offset+4], uint32(size))
		binary.BigEndian.PutUint32(box[offset+4:offset+8], 1000)
	}
	return box
}

func vkProductVideoAPIWithFormat(t *testing.T, formatKey, rawURL string) []byte {
	t.Helper()
	body := readVKProductFixture(t, "video_api.json")
	var envelope struct {
		Payload []json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Payload) < 3 {
		t.Fatalf("video fixture payload: %v", err)
	}
	var options map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Payload[len(envelope.Payload)-1], &options); err != nil {
		t.Fatalf("video fixture options: %v", err)
	}
	var player map[string]json.RawMessage
	if err := json.Unmarshal(options["player"], &player); err != nil {
		t.Fatalf("video fixture player: %v", err)
	}
	var params []map[string]json.RawMessage
	if err := json.Unmarshal(player["params"], &params); err != nil || len(params) == 0 {
		t.Fatalf("video fixture params: %v", err)
	}
	params[0][formatKey] = json.RawMessage(strconv.Quote(rawURL))
	player["params"], _ = json.Marshal(params)
	options["player"], _ = json.Marshal(player)
	envelope.Payload[len(envelope.Payload)-1], _ = json.Marshal(options)
	result, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func newVKProductClient(t *testing.T, transport *vkProductRoundTripper) *Client {
	t.Helper()
	return newBroadTestClient(withTransportFactory(func(config network.Config) (*network.Client, error) {
		config.RoundTripper = transport
		config.DefaultHeaders = http.Header{
			"Authorization":       {"Bearer ambient-secret"},
			"Cookie":              {"session=ambient-secret"},
			"Proxy-Authorization": {"Basic ambient-secret"},
			"Referer":             {"https://ambient.example/page"},
		}
		return network.New(config)
	}))
}

func assertVKProductIsolation(t *testing.T, transport *vkProductRoundTripper) {
	t.Helper()
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, request := range transport.requests {
		wantHost := ""
		switch {
		case request.method == http.MethodPost && (request.path == "/al_video.php" || request.path == "/wkview.php"),
			request.path == "/video_ext.php", request.path == "/video/@fixture_group":
			wantHost = "vk.com"
		case strings.HasPrefix(request.path, "/v1/blog/caster/public_video_stream"):
			wantHost = "api.vkplay.live"
		case request.path == "/video/en.vtt":
			wantHost = "sub.vkuservideo.net"
		case strings.HasPrefix(request.path, "/video/") && request.path != "/video/@fixture_group":
			wantHost = "media.vkuservideo.net"
		case strings.HasPrefix(request.path, "/embed/"):
			wantHost = "media.vkuservideo.net"
		case strings.HasPrefix(request.path, "/record/indexed"):
			wantHost = "media.vkuservideo.net"
		case strings.HasPrefix(request.path, "/impg/"):
			wantHost = "sun9.userapi.com"
		case strings.HasPrefix(request.path, "/sub/"):
			wantHost = "sub.vkuservideo.net"
		case strings.HasPrefix(request.path, "/record/") || strings.HasPrefix(request.path, "/live/"):
			wantHost = "cdn.vkplay.live"
		case strings.HasPrefix(request.path, "/fixture/"):
			wantHost = "audio.vkuseraudio.net"
		}
		if wantHost == "" || request.host != wantHost {
			t.Fatalf("unexpected VK product host %q for %s %s; want %q", request.host, request.method, request.path, wantHost)
		}
		for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
			if got := request.headers.Get(header); got != "" {
				t.Fatalf("%s leaked on %s %s", header, request.method, request.path)
			}
		}
		wantReferer := ""
		if request.method == http.MethodPost {
			switch request.path {
			case "/al_video.php":
				wantReferer = "https://vk.com/al_video.php"
			case "/wkview.php":
				wantReferer = "https://vk.com/wkview.php"
			}
		}
		if got := request.headers.Get("Referer"); got != wantReferer {
			t.Fatalf("Referer on %s %s=%q want %q", request.method, request.path, got, wantReferer)
		}
	}
}

func assertVKProductError(t *testing.T, err error, category ErrorCategory, sentinel error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error", sentinel)
	}
	if !IsCategory(err, category) {
		t.Fatalf("error category=%v want %s; err=%v", err, category, err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error=%v does not contain sentinel %v", err, sentinel)
	}
	assertVKProductSafeError(t, err)
}

func assertVKProductSafeError(t *testing.T, err error) {
	t.Helper()
	for _, secret := range []string{"ambient-secret", "sig=", "signed", "token=", "Bearer", "session="} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error contains secret material %q: %v", secret, err)
		}
	}
}

func TestProductVKRegisteredAPIAndDirectPlaybackWithAllSidecarsIsolated(t *testing.T) {
	transport := newVKProductTransport(t)
	root := t.TempDir()
	request := Request{URL: "https://vksport.vkvideo.ru/video-123_101", OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true, Format: "url360",
		Subtitles: SubtitleOptions{WriteManual: true, Languages: []string{"en"}}, Thumbnails: ThumbnailOptions{Write: true}}
	client := newVKProductClient(t, transport)
	result, err := client.Run(context.Background(), request)
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil || string(data) != "vk-direct-bytes" {
		t.Fatalf("output=%q err=%v", data, err)
	}
	for _, path := range []string{"/al_video.php", "/video/fixture-360.mp4", "/video/en.vtt", "/impg/fixture-thumb.jpg"} {
		if !vkProductSawPath(transport, path) {
			t.Fatalf("missing request %s", path)
		}
	}
	if !vkProductSawQuery(transport, "/video/fixture-360.mp4", "sig=direct%2Bsigned&expires=1700000000") {
		t.Fatal("signed direct query was not preserved")
	}
	if !vkProductSawHostPath(transport, "vk.com", "/al_video.php") {
		t.Fatal("VK alias did not canonicalize API request")
	}
	assertVKProductIsolation(t, transport)
}

func TestProductVKEmbedPageAndHLSExactBytesAreIsolated(t *testing.T) {
	transport := newVKProductTransport(t)
	root := t.TempDir()
	request := Request{URL: "https://vk.com/video_ext.php?oid=-123&id=101&hash=embed-fixture", OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true, Format: "url720", Thumbnails: ThumbnailOptions{Write: true}}
	client := newVKProductClient(t, transport)
	result, err := client.Run(context.Background(), request)
	if err != nil || !result.Downloaded {
		t.Fatalf("embed result=%+v err=%v", result, err)
	}
	data, _ := os.ReadFile(result.Filename)
	if string(data) != "vk-embed-bytes" {
		t.Fatalf("embed output=%q", data)
	}
	if !vkProductSawPath(transport, "/video_ext.php") || !vkProductSawPath(transport, "/embed/fixture-720.mp4") {
		t.Fatalf("embed paths missing")
	}
	assertVKProductIsolation(t, transport)

	hlsTransport := newVKProductTransport(t)
	hlsRoot := t.TempDir()
	hlsRequest := Request{URL: "https://vk.com/video-123_101", OutputDir: hlsRoot, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true, Format: "hls"}
	hlsClient := newVKProductClient(t, hlsTransport)
	hlsResult, err := hlsClient.Run(context.Background(), hlsRequest)
	if err != nil || !hlsResult.Downloaded {
		t.Fatalf("HLS result=%+v err=%v", hlsResult, err)
	}
	hlsBytes, _ := os.ReadFile(hlsResult.Filename)
	if string(hlsBytes) != "vk-hls-init-vk-hls-one-vk-hls-two" {
		t.Fatalf("HLS output=%q", hlsBytes)
	}
	for _, path := range []string{"/video/master.m3u8", "/video/media.m3u8", "/video/one.bin", "/video/two.bin"} {
		if !vkProductSawPath(hlsTransport, path) {
			t.Fatalf("missing HLS path %s", path)
		}
	}
	for path, query := range map[string]string{
		"/video/master.m3u8": "sig=hls-signed&expires=1700000000",
		"/video/media.m3u8":  "sig=variant-signed&expires=1700000000",
		"/video/init.mp4":    "sig=init-signed&expires=1700000000",
		"/video/key.bin":     "sig=key-signed&expires=1700000000",
		"/video/one.bin":     "sig=segment-one&expires=1700000000",
		"/video/two.bin":     "sig=segment-two&expires=1700000000",
	} {
		if !vkProductSawQuery(hlsTransport, path, query) {
			t.Fatalf("missing signed HLS query %s?%s", path, query)
		}
	}
	assertVKProductIsolation(t, hlsTransport)
}

func TestProductVKPlayRecordingHLSAndDirectBytesAreIsolated(t *testing.T) {
	transport := newVKProductTransport(t)
	root := t.TempDir()
	request := Request{URL: "https://live.vkvideo.ru/caster/record/f5e6e3b5-dc52-4d14-965d-0680dd2882da/records", OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true, Format: "hls"}
	client := newVKProductClient(t, transport)
	result, err := client.Run(context.Background(), request)
	if err != nil || !result.Downloaded {
		t.Fatalf("VK Play HLS result=%+v err=%v", result, err)
	}
	if !vkProductSawHostPath(transport, "api.vkplay.live", "/v1/blog/caster/public_video_stream/record/f5e6e3b5-dc52-4d14-965d-0680dd2882da") {
		t.Fatal("VK Play recording alias did not use canonical API")
	}
	data, _ := os.ReadFile(result.Filename)
	if string(data) != "vkplay-hls-init-vkplay-hls-one-vkplay-hls-two" {
		t.Fatalf("VK Play HLS output=%q", data)
	}
	assertVKProductIsolation(t, transport)
	for path, query := range map[string]string{
		"/record/master.m3u8": "sig=record-hls",
		"/record/media.m3u8":  "sig=variant-signed&expires=1700000000",
		"/record/init.mp4":    "sig=init-signed&expires=1700000000",
		"/record/key.bin":     "sig=key-signed&expires=1700000000",
		"/record/one.bin":     "sig=segment-one&expires=1700000000",
		"/record/two.bin":     "sig=segment-two&expires=1700000000",
	} {
		if !vkProductSawQuery(transport, path, query) {
			t.Fatalf("missing signed VK Play HLS query %s?%s", path, query)
		}
	}

	dashTransport := newVKProductTransport(t)
	dashRoot := t.TempDir()
	dashRequest := Request{URL: request.URL, OutputDir: dashRoot, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true, Format: "dash"}
	dashClient := newVKProductClient(t, dashTransport)
	dashResult, err := dashClient.Run(context.Background(), dashRequest)
	if err != nil || !dashResult.Downloaded {
		t.Fatalf("VK Play DASH result=%+v err=%v", dashResult, err)
	}
	dashBytes, _ := os.ReadFile(dashResult.Filename)
	if string(dashBytes) != "vkplay-dash-initvkplay-dash-onevkplay-dash-two" {
		t.Fatalf("unexpected DASH output=%q", dashBytes)
	}
	for path, query := range map[string]string{
		"/record/manifest.mpd": "sig=record-dash",
		"/record/init.mp4":     "sig=init-signed",
		"/record/one.m4s":      "sig=dash-one",
		"/record/two.m4s":      "sig=dash-two",
	} {
		if !vkProductSawQuery(dashTransport, path, query) {
			t.Fatalf("missing signed VK Play DASH query %s?%s", path, query)
		}
	}
	assertVKProductIsolation(t, dashTransport)

	directTransport := newVKProductTransport(t)
	directRoot := t.TempDir()
	directRequest := Request{URL: request.URL, OutputDir: directRoot, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true, Format: "high"}
	directClient := newVKProductClient(t, directTransport)
	result, err = directClient.Run(context.Background(), directRequest)
	if err != nil || !result.Downloaded {
		t.Fatalf("VK Play direct result=%+v err=%v", result, err)
	}
	data, _ = os.ReadFile(result.Filename)
	if string(data) != "vkplay-direct-bytes" {
		t.Fatalf("unexpected direct fixture output=%q", data)
	}
	assertVKProductIsolation(t, directTransport)
}

func TestProductVKPlayDASHBaseURLIndexAndRepresentationHopsAreIsolated(t *testing.T) {
	transport := newVKProductTransport(t)
	initBytes := []byte("vkplay-index-init-")
	mediaOne := []byte("vkplay-index-one-")
	mediaTwo := []byte("vkplay-index-two")
	index := vkProductSIDX(len(mediaOne), len(mediaTwo))
	resource := append(append(append([]byte{}, initBytes...), index...), append(mediaOne, mediaTwo...)...)
	indexStart := len(initBytes)
	indexEnd := indexStart + len(index) - 1
	transport.bodies["GET /record/indexed.mpd"] = []byte(fmt.Sprintf(
		`<MPD mediaPresentationDuration="PT2S"><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="indexed" bandwidth="1000"><BaseURL>indexed.mp4?sig=base-signed&amp;expires=1700000000</BaseURL><SegmentBase indexRange="%d-%d"><Initialization range="0-%d"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`,
		indexStart, indexEnd, len(initBytes)-1))
	transport.bodies["GET /record/indexed.mp4"] = resource
	transport.bodies["POST /al_video.php"] = vkProductVideoAPIWithFormat(t, "dash", "https://media.vkuservideo.net/record/indexed.mpd?sig=index-master&expires=1700000000")
	root := t.TempDir()
	request := Request{URL: "https://vk.com/video-123_101", OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true, Format: "dash"}
	result, err := newVKProductClient(t, transport).Run(context.Background(), request)
	if err != nil || !result.Downloaded {
		t.Fatalf("indexed DASH result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil || string(data) != string(append(append(append([]byte{}, initBytes...), mediaOne...), mediaTwo...)) {
		t.Fatalf("indexed DASH output=%q err=%v", data, err)
	}
	for path, query := range map[string]string{
		"/record/indexed.mpd": "sig=index-master&expires=1700000000",
		"/record/indexed.mp4": "sig=base-signed&expires=1700000000",
	} {
		if !vkProductSawQuery(transport, path, query) {
			t.Fatalf("missing signed indexed DASH query %s?%s", path, query)
		}
	}
	if !vkProductSawRange(transport, "/record/indexed.mp4", fmt.Sprintf("bytes=%d-%d", indexStart, indexEnd)) {
		t.Fatal("DASH index range was not fetched")
	}
	if !vkProductSawRange(transport, "/record/indexed.mp4", fmt.Sprintf("bytes=0-%d", len(initBytes)-1)) {
		t.Fatal("DASH initialization range was not fetched")
	}
	assertVKProductIsolation(t, transport)
}

func TestProductVKPlayLiveHLSFailureAndCancellation(t *testing.T) {
	transport := newVKProductTransport(t)
	root := t.TempDir()
	request := Request{URL: "https://live.vkplay.ru/caster", OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true, Format: "live_hls"}
	client := newVKProductClient(t, transport)
	result, err := client.Run(context.Background(), request)
	if err != nil || !result.Downloaded {
		t.Fatalf("live result=%+v err=%v", result, err)
	}
	if !vkProductSawHostPath(transport, "api.vkplay.live", "/v1/blog/caster/public_video_stream") {
		t.Fatal("VK Play live alias did not use canonical API")
	}
	data, _ := os.ReadFile(result.Filename)
	if string(data) != "vk-live-hls-init-vk-live-hls-one-vk-live-hls-two" {
		t.Fatalf("live output=%q", data)
	}
	if !vkProductSawQuery(transport, "/live/master.m3u8", "sig=live-hls") {
		t.Fatal("live signed query was not preserved")
	}
	for path, query := range map[string]string{
		"/live/media.m3u8": "sig=variant-signed&expires=1700000000",
		"/live/init.mp4":   "sig=init-signed&expires=1700000000",
		"/live/key.bin":    "sig=key-signed&expires=1700000000",
		"/live/one.bin":    "sig=segment-one&expires=1700000000",
		"/live/two.bin":    "sig=segment-two&expires=1700000000",
	} {
		if !vkProductSawQuery(transport, path, query) {
			t.Fatalf("missing signed live HLS query %s?%s", path, query)
		}
	}
	for _, path := range []string{"/v1/blog/caster/public_video_stream", "/live/master.m3u8", "/live/media.m3u8", "/live/one.bin", "/live/two.bin"} {
		if !vkProductSawPath(transport, path) {
			t.Fatalf("missing live path %s", path)
		}
	}
	assertVKProductIsolation(t, transport)

	offlineTransport := newVKProductTransport(t)
	offlineTransport.liveOffline = true
	offlineRoot := t.TempDir()
	offlineRequest := request
	offlineRequest.OutputDir = offlineRoot
	offlineClient := newVKProductClient(t, offlineTransport)
	_, err = offlineClient.Run(context.Background(), offlineRequest)
	assertVKProductError(t, err, ErrorUnsupported, extractor.ErrVKNotLive)
	if entries, _ := os.ReadDir(offlineRoot); len(entries) != 0 {
		t.Fatalf("unexpected offline artifacts=%v", entries)
	}
	assertVKProductIsolation(t, offlineTransport)

	statusTransport := newVKProductTransport(t)
	statusTransport.statuses = map[string]int{"GET /v1/blog/caster/public_video_stream": http.StatusTooManyRequests}
	statusClient := newVKProductClient(t, statusTransport)
	_, err = statusClient.Run(context.Background(), request)
	assertVKProductError(t, err, ErrorNetwork, extractor.ErrVKRateLimited)
	assertVKProductIsolation(t, statusTransport)

	redirectTransport := newVKProductTransport(t)
	redirectTransport.redirectPath = "/v1/blog/caster/public_video_stream"
	redirectRoot := t.TempDir()
	redirectRequest := request
	redirectRequest.OutputDir = redirectRoot
	redirectClient := newVKProductClient(t, redirectTransport)
	_, err = redirectClient.Run(context.Background(), redirectRequest)
	assertVKProductError(t, err, ErrorNetwork, extractor.ErrVKInvalidStatus)
	if entries, _ := os.ReadDir(redirectRoot); len(entries) != 0 {
		t.Fatalf("redirect artifacts=%v", entries)
	}
	assertVKProductIsolation(t, redirectTransport)

	blockingTransport := newVKProductTransport(t)
	blockingTransport.blockPath = "/live/one.bin"
	blockingRoot := t.TempDir()
	blockingRequest := Request{URL: request.URL, OutputDir: blockingRoot, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true, Format: "live_hls"}
	blockingClient := newVKProductClient(t, blockingTransport)
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := blockingClient.Run(ctx, blockingRequest)
		resultCh <- err
	}()
	select {
	case entered := <-blockingTransport.started:
		if entered != "GET /live/one.bin" {
			t.Fatalf("cancellation entered %q", entered)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("live media request did not enter flight")
	}
	cancel()
	assertVKProductError(t, <-resultCh, ErrorCancelled, context.Canceled)
	if entries, _ := os.ReadDir(blockingRoot); len(entries) != 0 {
		t.Fatalf("live cancellation artifacts=%v", entries)
	}
	assertVKProductIsolation(t, blockingTransport)
}

func TestProductVKManifestHopPolicyFailsClosedBeforeHostileNetwork(t *testing.T) {
	cases := []struct {
		name, format, url, fixture string
	}{
		{"hls-master-variant", "hls", "https://media.vkuservideo.net/video/hostile-master.m3u8?sig=master-signed", "vk_hls_hostile_variant.m3u8"},
		{"hls-segment", "hls", "https://media.vkuservideo.net/video/hostile-segment-master.m3u8?sig=master-signed", "vk_hls_hostile_segment_master.m3u8"},
		{"dash-reference", "dash", "https://media.vkuservideo.net/video/hostile-dash.mpd?sig=dash-master", "vk_dash_hostile.mpd"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			transport := newVKProductTransport(t)
			transport.bodies["POST /al_video.php"] = vkProductVideoAPIWithFormat(t, test.format, test.url)
			transport.bodies["GET /video/hostile-master.m3u8"] = readVKProductFixture(t, test.fixture)
			root := t.TempDir()
			request := Request{URL: "https://vk.com/video-123_101", OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true, Format: test.format}
			client := newVKProductClient(t, transport)
			_, err := client.Run(context.Background(), request)
			assertVKProductError(t, err, ErrorSecurity, extractor.ErrVKUnsafeAsset)
			transport.mu.Lock()
			hostile := transport.hostile
			transport.mu.Unlock()
			if hostile != 0 {
				t.Fatalf("hostile network requests=%d", hostile)
			}
			if entries, _ := os.ReadDir(root); len(entries) != 0 {
				t.Fatalf("hostile artifacts=%v", entries)
			}
			assertVKProductIsolation(t, transport)
		})
	}
}

func TestProductVKWallPostTransparentReentryAndUserPlaylistPaging(t *testing.T) {
	wallTransport := newVKProductTransport(t)
	wallRoot := t.TempDir()
	wallRequest := Request{URL: "https://vk.com/wall-123_77", OutputDir: wallRoot, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true}
	wallClient := newVKProductClient(t, wallTransport)
	wallResult, err := wallClient.Run(context.Background(), wallRequest)
	if err != nil {
		t.Fatalf("wall process err=%v", err)
	}
	if len(wallResult.Entries) != 2 {
		t.Fatalf("wall entries=%d", len(wallResult.Entries))
	}
	if id := vkProductResultID(t, wallResult.Entries[0]); id != "77_900" {
		t.Fatalf("wall audio identity=%q", id)
	}
	if id := vkProductResultID(t, wallResult.Entries[1]); id != "-123_101" {
		t.Fatalf("wall video identity=%q", id)
	}
	audioBytes, _ := os.ReadFile(wallResult.Entries[0].Filename)
	videoBytes, _ := os.ReadFile(wallResult.Entries[1].Filename)
	if string(audioBytes) != "vk-wall-audio" || string(videoBytes) != "vk-direct-bytes" {
		t.Fatalf("wall child bytes audio=%q video=%q", audioBytes, videoBytes)
	}
	wallFirstBytes := [][]byte{append([]byte(nil), audioBytes...), append([]byte(nil), videoBytes...)}
	wallSecondRequest := wallRequest
	wallSecondResult, err := wallClient.Run(context.Background(), wallSecondRequest)
	if err != nil || len(wallSecondResult.Entries) != len(wallResult.Entries) {
		t.Fatalf("second wall result=%+v err=%v", wallSecondResult, err)
	}
	for index, firstChild := range wallResult.Entries {
		secondChild := wallSecondResult.Entries[index]
		if !vkProductMetadataEqual(firstChild.InfoJSON, secondChild.InfoJSON) || vkProductResultID(t, firstChild) != vkProductResultID(t, secondChild) {
			t.Fatalf("wall child %d changed across reuse", index)
		}
		secondBytes, secondErr := os.ReadFile(secondChild.Filename)
		if secondErr != nil || !bytes.Equal(wallFirstBytes[index], secondBytes) {
			t.Fatalf("wall child %d bytes changed: first=%q second=%q error=%v", index, wallFirstBytes[index], secondBytes, secondErr)
		}
	}
	if !vkProductSawPath(wallTransport, "/wkview.php") || !vkProductSawPath(wallTransport, "/fixture/track.m4a") || !vkProductSawPath(wallTransport, "/al_video.php") {
		t.Fatalf("wall transparent reentry paths missing")
	}
	assertVKProductIsolation(t, wallTransport)

	playlistTransport := newVKProductTransport(t)
	playlistRoot := t.TempDir()
	playlistRequest := Request{URL: "https://vk.com/video/@fixture_group", OutputDir: playlistRoot, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true, Format: "url360"}
	playlistClient := newVKProductClient(t, playlistTransport)
	playlistResult, err := playlistClient.Run(context.Background(), playlistRequest)
	if err != nil {
		t.Fatalf("playlist process err=%v", err)
	}
	if len(playlistResult.Entries) != 3 {
		t.Fatalf("playlist entries=%d", len(playlistResult.Entries))
	}
	for index, want := range []string{"-123_101", "-123_102", "-123_103"} {
		if got := vkProductResultID(t, playlistResult.Entries[index]); got != want {
			t.Fatalf("playlist child %d identity=%q want %q", index, got, want)
		}
	}
	playlistFirstBytes := make([][]byte, len(playlistResult.Entries))
	for index, child := range playlistResult.Entries {
		playlistFirstBytes[index], err = os.ReadFile(child.Filename)
		if err != nil {
			t.Fatalf("playlist child %d first bytes: %v", index, err)
		}
	}
	secondResult, err := playlistClient.Run(context.Background(), playlistRequest)
	if err != nil || len(secondResult.Entries) != 3 {
		t.Fatalf("second playlist result=%+v err=%v", secondResult, err)
	}
	for index, firstChild := range playlistResult.Entries {
		secondChild := secondResult.Entries[index]
		if !vkProductMetadataEqual(firstChild.InfoJSON, secondChild.InfoJSON) || vkProductResultID(t, firstChild) != vkProductResultID(t, secondChild) {
			t.Fatalf("playlist child %d changed across reuse", index)
		}
		secondBytes, secondErr := os.ReadFile(secondChild.Filename)
		if secondErr != nil || !bytes.Equal(playlistFirstBytes[index], secondBytes) {
			t.Fatalf("playlist child %d bytes changed: first=%q second=%q error=%v", index, playlistFirstBytes[index], secondBytes, secondErr)
		}
	}
	offsets := map[string]int{}
	playlistTransport.mu.Lock()
	for _, request := range playlistTransport.requests {
		if request.method != http.MethodPost || request.path != "/al_video.php" {
			continue
		}
		form, _ := url.ParseQuery(request.body)
		if form.Get("act") == "load_videos_silent" {
			offsets[form.Get("offset")]++
		}
	}
	playlistTransport.mu.Unlock()
	if offsets["0"] != 2 || offsets["2"] != 2 {
		t.Fatalf("playlist paging offsets=%v", offsets)
	}
	assertVKProductIsolation(t, playlistTransport)
}

func TestProductVKFailureAndInFlightCancellationLeaveZeroArtifacts(t *testing.T) {
	failedTransport := newVKProductTransport(t)
	failedTransport.statuses = map[string]int{"GET /video/fixture-360.mp4": http.StatusInternalServerError}
	failedRoot := t.TempDir()
	failedRequest := Request{URL: "https://vk.com/video-123_101", OutputDir: failedRoot, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true, Format: "url360"}
	failedClient := newVKProductClient(t, failedTransport)
	_, err := failedClient.Run(context.Background(), failedRequest)
	if !IsCategory(err, ErrorNetwork) {
		t.Fatalf("direct failure category=%v want %s", err, ErrorNetwork)
	}
	assertVKProductSafeError(t, err)
	var statusErr *downloader.HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusInternalServerError {
		t.Fatalf("direct failure status=%v want HTTP %d", err, http.StatusInternalServerError)
	}
	if entries, _ := os.ReadDir(failedRoot); len(entries) != 0 {
		t.Fatalf("failure artifacts=%v", entries)
	}
	assertVKProductIsolation(t, failedTransport)

	blockingTransport := newVKProductTransport(t)
	blockingTransport.blockPath = "/video/fixture-360.mp4"
	blockingRoot := t.TempDir()
	blockingRequest := Request{URL: "https://vk.com/video-123_101", OutputDir: blockingRoot, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true, Format: "url360"}
	blockingClient := newVKProductClient(t, blockingTransport)
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, err := blockingClient.Run(ctx, blockingRequest)
		resultCh <- err
	}()
	select {
	case entered := <-blockingTransport.started:
		if entered != "GET /video/fixture-360.mp4" {
			t.Fatalf("cancellation entered %q", entered)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("media request did not enter flight")
	}
	cancel()
	assertVKProductError(t, <-resultCh, ErrorCancelled, context.Canceled)
	if entries, _ := os.ReadDir(blockingRoot); len(entries) != 0 {
		t.Fatalf("cancellation artifacts=%v", entries)
	}
	assertVKProductIsolation(t, blockingTransport)
}

func vkProductSawPath(transport *vkProductRoundTripper, path string) bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, request := range transport.requests {
		if request.path == path {
			return true
		}
	}
	return false
}

func vkProductSawQuery(transport *vkProductRoundTripper, path, query string) bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, request := range transport.requests {
		if request.path == path && request.rawQuery == query {
			return true
		}
	}
	return false
}

func vkProductSawRange(transport *vkProductRoundTripper, path, rawRange string) bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, request := range transport.requests {
		if request.path == path && request.headers.Get("Range") == rawRange {
			return true
		}
	}
	return false
}

func vkProductSawHostPath(transport *vkProductRoundTripper, host, path string) bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, request := range transport.requests {
		if request.host == host && request.path == path {
			return true
		}
	}
	return false
}

func vkProductResultID(t *testing.T, result Result) string {
	t.Helper()
	var object struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result.InfoJSON, &object); err != nil {
		t.Fatalf("result metadata: %v", err)
	}
	return object.ID
}

func vkProductMetadataEqual(first, second json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(first, &left) != nil || json.Unmarshal(second, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}
