package extractor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

type vkFixtureTransport struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   map[string][]byte
	statuses map[string]int
	repeat   bool
}

func (transport *vkFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	response, err := transport.Do(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return body, response.Header.Clone(), err
}

func (transport *vkFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.DoWithoutCredentialsNoRedirect(ctx, request)
}

func (transport *vkFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.serve(ctx, request)
}

func (transport *vkFixtureTransport) DoWithoutCredentialsNoRedirectWithReferer(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.serve(ctx, request)
}

func (transport *vkFixtureTransport) serve(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if request.Header.Get(header) != "" {
			return nil, errors.New("ambient credential leaked")
		}
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Clone(ctx))
	transport.mu.Unlock()
	key := request.Method + " " + request.URL.String()
	var body []byte
	if request.Method == http.MethodPost && request.Body != nil {
		body, _ = io.ReadAll(request.Body)
	}
	if strings.HasSuffix(request.URL.Path, "/al_video.php") && request.Method == http.MethodPost {
		form, _ := url.ParseQuery(string(body))
		switch form.Get("act") {
		case "show":
			body = transport.bodies["video"]
		case "load_videos_silent":
			if transport.repeat || form.Get("offset") == "0" {
				body = transport.bodies["user1"]
			} else {
				body = transport.bodies["user2"]
			}
		}
	} else if strings.HasSuffix(request.URL.Path, "/wkview.php") {
		body = transport.bodies["wall"]
	} else if strings.Contains(request.URL.Path, "/public_video_stream/record/") {
		body = transport.bodies["record"]
	} else if strings.HasSuffix(request.URL.Path, "/public_video_stream") {
		body = transport.bodies["live"]
	} else {
		body = transport.bodies[key]
	}
	status := transport.statuses[key]
	if status == 0 {
		status = http.StatusOK
	}
	if body == nil {
		status = http.StatusNotFound
		body = []byte("not found")
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}

func newVKFixtureTransport(t *testing.T) *vkFixtureTransport {
	t.Helper()
	read := func(name string) []byte {
		body, err := os.ReadFile(filepath.Join("testdata", "vk", name))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	return &vkFixtureTransport{bodies: map[string][]byte{
		"video":  read("video_api.json"),
		"user1":  read("user_page1.json"),
		"user2":  read("user_page2.json"),
		"wall":   read("wall_page.json"),
		"record": read("vkplay_record.json"),
		"live":   read("vkplay_live.json"),
		"GET https://vk.com/video/@fixture_group":                             read("user_page.html"),
		"GET https://vkvideo.ru/playlist/-123_77":                             read("user_page_playlist.html"),
		"GET https://vk.com/video_ext.php?oid=-123&id=101&hash=embed-fixture": read("video_embed.html"),
	}}
}

func fixtureRequests(transport *vkFixtureTransport) []*http.Request {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]*http.Request(nil), transport.requests...)
}

func TestVKRouteMatrixAndOverlap(t *testing.T) {
	registry := NewRegistry(NewVKUserVideos(), NewVKWallPost(), NewVK(), NewVKAudio(), NewVKPlay(), NewVKPlayLive())
	cases := []struct {
		raw, name string
	}{
		{"https://vk.com/video-123_101", "vk"},
		{"http://m.vk.com/clip-123_101?list=abc123", "vk"},
		{"https://new.vk.com/videos-123?z=video-123_101%2Fclub123", "vk"},
		{"https://vkvideo.ru/clips-123?z=clip-123_101", "vk"},
		{"https://vk.com/video_ext.php?oid=-123&id=101&hash=embed-fixture", "vk"},
		{"https://vkvideo.ru/video_ext.php?oid=-123&id=101&hash=embed-fixture", "vk"},
		{"https://vksport.vkvideo.ru/video_ext.php?oid=-123&id=101&hash=embed-fixture", "vk"},
		{"https://vksport.vkvideo.ru/video-123_101", "vk"},
		{"https://vk.com/video/@fixture_group", "vk_uservideos"},
		{"https://vkvideo.ru/playlist/-123_77", "vk_uservideos"},
		{"https://m.vk.com/public?w=wall-123_77", "vk_wallpost"},
		{"https://vk.com/wall-123_77", "vk_wallpost"},
		{"https://vkplay.live/caster/record/f5e6e3b5-dc52-4d14-965d-0680dd2882da/records", "vkplay"},
		{"https://live.vkplay.ru/caster/record/f5e6e3b5-dc52-4d14-965d-0680dd2882da", "vkplay"},
		{"https://live.vkvideo.ru/caster", "vkplay_live"},
	}
	for _, test := range cases {
		selected, err := registry.Select(test.raw)
		if err != nil {
			t.Fatalf("Select(%q): %v", test.raw, err)
		}
		if selected.Name() != test.name {
			t.Fatalf("Select(%q)=%q, want %q", test.raw, selected.Name(), test.name)
		}
	}
	for _, raw := range []string{
		"https://evilvk.com/video-123_101", "https://vk.com:443/video-123_101", "https://user:pass@vk.com/video-123_101",
		"https://vk.com/video-123_101?list=a&list=b", "https://vk.com/video-123_101?unsafe=1", "https://vk.com/video-123_101#fragment",
		"https://vk.com./video-123_101",
		"https://vk.com/video_ext.php?oid=-123&id=101&unsafe=1", "https://vk.com/video/@fixture_group?z=video-123_101",
		"https://daxab.com/embed/-123_101", "https://daxab.com/ext.php?oid=-123&id=101",
		"https://vkplay.live/caster/record/not-a-uuid", "http://vkplay.live/caster",
		"https://live.vkplay.ru/caster?query=1", "https://live.vkvideo.ru:443/caster",
	} {
		parsed, _ := url.Parse(raw)
		if NewVK().Suitable(parsed) || NewVKUserVideos().Suitable(parsed) || NewVKWallPost().Suitable(parsed) || NewVKPlay().Suitable(parsed) || NewVKPlayLive().Suitable(parsed) {
			t.Fatalf("unexpected VK route match for %q", raw)
		}
	}
}

func TestVKVideoAPIAndEmbedPreserveSignedAssetsAndMetadata(t *testing.T) {
	transport := newVKFixtureTransport(t)
	result, err := NewVK().Extract(context.Background(), Request{URL: "https://vk.com/video-123_101", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsPlaylist() {
		t.Fatal("video is playlist")
	}
	if id, _ := result.Info.Lookup("id").StringValue(); id != "-123_101" {
		t.Fatalf("id=%q", id)
	}
	if title, _ := result.Info.Lookup("title").StringValue(); title != "VK Fixture Video" {
		t.Fatalf("title=%q", title)
	}
	formats, _ := result.Info.Lookup("formats").ListValue()
	if len(formats) != 2 {
		t.Fatalf("formats=%d", len(formats))
	}
	direct, _ := formats[0].Object()
	if raw, _ := direct.Lookup("url").StringValue(); !strings.Contains(raw, "sig=direct%2Bsigned") {
		t.Fatalf("signed direct URL was rewritten: %q", raw)
	}
	for _, format := range formats {
		object, _ := format.Object()
		if isolated, _ := object.Lookup("_credential_isolated").Bool(); !isolated {
			t.Fatal("format is not credential isolated")
		}
	}
	if _, ok := result.Info.Lookup("chapters").ListValue(); !ok {
		t.Fatal("chapters missing")
	}
	subs, ok := result.Info.Lookup("subtitles").Object()
	if !ok || len(mustVKList(t, subs.Lookup("en"))) != 1 {
		t.Fatal("subtitle missing")
	}
	thumbs, _ := result.Info.Lookup("thumbnails").ListValue()
	if len(thumbs) != 1 {
		t.Fatal("thumbnail missing")
	}
	for _, raw := range []string{
		"https://vk.com/video_ext.php?oid=-123&id=101&hash=embed-fixture",
		"https://vkvideo.ru/video_ext.php?oid=-123&id=101&hash=embed-fixture",
		"https://vksport.vkvideo.ru/video-123_101",
	} {
		embed, embedErr := NewVK().Extract(context.Background(), Request{URL: raw, Transport: transport})
		if embedErr != nil {
			t.Fatal(embedErr)
		}
		if id, _ := embed.Info.Lookup("id").StringValue(); id != "-123_101" {
			t.Fatalf("embed id=%q", id)
		}
	}
	for _, request := range fixtureRequests(transport) {
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" || request.Header.Get("Proxy-Authorization") != "" {
			t.Fatalf("ambient credentials leaked on %s %s", request.Method, request.URL)
		}
	}
}

func TestVKUserVideosLazyReusableAndRepeatedPageDetection(t *testing.T) {
	transport := newVKFixtureTransport(t)
	extraction, err := NewVKUserVideos().Extract(context.Background(), Request{URL: "https://vk.com/video/@fixture_group", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtureRequests(transport)) != 1 {
		t.Fatalf("playlist API was eager: %d requests", len(fixtureRequests(transport)))
	}
	first, err := CollectEntries(context.Background(), extraction.Entries, 10)
	if err != nil || len(first) != 3 {
		t.Fatalf("first=%v err=%v", first, err)
	}
	second, err := CollectEntries(context.Background(), extraction.Entries, 10)
	if err != nil || len(second) != 3 {
		t.Fatalf("second=%v err=%v", second, err)
	}
	for index := range first {
		if first[index].ID != second[index].ID {
			t.Fatalf("reusable entry %d=%q/%q", index, first[index].ID, second[index].ID)
		}
	}
	transport.repeat = true
	repeated, err := NewVKUserVideos().Extract(context.Background(), Request{URL: "https://vk.com/video/@fixture_group", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), repeated.Entries, 10)
	if !errors.Is(err, ErrVKRepeatedPage) {
		t.Fatalf("repeated page err=%v", err)
	}
}

func TestVKWallPostTransparentVideoAndOpaqueAudio(t *testing.T) {
	transport := newVKFixtureTransport(t)
	extraction, err := NewVKWallPost().Extract(context.Background(), Request{URL: "https://vk.com/wall-123_77", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), extraction.Entries, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	if entries[0].ExtractorKey != "vk_audio" || entries[1].ExtractorKey != "vk" || !entries[1].Transparent || entries[1].Referer != "https://vk.com/wall-123_77" {
		t.Fatalf("wall entries=%+v", entries)
	}
	audio, err := NewVKAudio().Extract(context.Background(), Request{URL: entries[0].URL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formatList, _ := audio.Info.Lookup("formats").ListValue()
	format, _ := formatList[0].Object()
	if isolated, _ := format.Lookup("_credential_isolated").Bool(); !isolated {
		t.Fatal("wall audio format is not isolated")
	}
	if referer := fixtureRequests(transport)[0].Header.Get("Referer"); referer != "https://vk.com/wkview.php" {
		t.Fatalf("wall API Referer=%q", referer)
	}
}

func TestVKPlayRecordingAndLiveHLS(t *testing.T) {
	transport := newVKFixtureTransport(t)
	record, err := NewVKPlay().Extract(context.Background(), Request{URL: "https://vkplay.live/caster/record/f5e6e3b5-dc52-4d14-965d-0680dd2882da", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if formats, _ := record.Info.Lookup("formats").ListValue(); len(formats) != 3 {
		t.Fatalf("record formats=%d", len(formats))
	}
	live, err := NewVKPlayLive().Extract(context.Background(), Request{URL: "https://vkplay.live/caster", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := live.Info.Lookup("formats").ListValue()
	if len(formats) != 1 {
		t.Fatalf("live formats=%d, want one HLS", len(formats))
	}
	format, _ := formats[0].Object()
	if protocol, _ := format.Lookup("protocol").StringValue(); protocol != "m3u8_native" {
		t.Fatalf("live protocol=%q", protocol)
	}
}

func TestVKFailuresAndCancellationAreTypedAndSecretSafe(t *testing.T) {
	transport := newVKFixtureTransport(t)
	transport.statuses = map[string]int{"POST https://vk.com/al_video.php": http.StatusTooManyRequests}
	_, err := NewVK().Extract(context.Background(), Request{URL: "https://vk.com/video-123_101", Transport: transport})
	if !errors.Is(err, ErrVKRateLimited) || strings.Contains(err.Error(), "signed") {
		t.Fatalf("rate-limit error=%v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	transport = newVKFixtureTransport(t)
	_, err = NewVK().Extract(cancelled, Request{URL: "https://vk.com/video-123_101", Transport: transport})
	if !errors.Is(err, context.Canceled) || len(fixtureRequests(transport)) != 0 {
		t.Fatalf("cancellation err=%v requests=%d", err, len(fixtureRequests(transport)))
	}
}

func TestVKRequiresCredentialIsolatedTransport(t *testing.T) {
	plain := vkPlainTransport{body: []byte(`{"payload":["0","","{}"]}`)}
	_, err := NewVK().Extract(context.Background(), Request{URL: "https://vk.com/video-123_101", Transport: plain})
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("err=%v", err)
	}
}

type vkPlainTransport struct{ body []byte }

func (transport vkPlainTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return transport.body, make(http.Header), nil
}
func (transport vkPlainTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(transport.body)), Header: make(http.Header)}, nil
}

func mustVKList(t *testing.T, raw value.Value) []value.Value {
	t.Helper()
	list, ok := raw.ListValue()
	if !ok {
		t.Fatalf("not a list: %v", raw)
	}
	return list
}
