package ytdlp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

type bilibiliProductRoundTripper struct {
	mu       sync.Mutex
	pages    map[string][]byte
	api      map[string][]byte
	media    map[string][]byte
	statuses map[string]int
	requests []capturedBilibiliRequest
	entered  chan struct{}
	release  chan struct{}
}

type capturedBilibiliRequest struct {
	url     string
	headers http.Header
}

func (transport *bilibiliProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport.entered != nil && transport.release != nil && transport.isMedia(request.URL.String()) {
		select {
		case transport.entered <- struct{}{}:
		default:
		}
		<-transport.release
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, capturedBilibiliRequest{url: request.URL.String(), headers: request.Header.Clone()})
	status := transport.statuses[request.URL.String()]
	if status == 0 {
		status = http.StatusOK
	}
	body, ok := transport.pages[request.URL.String()]
	if !ok {
		body, ok = transport.api[request.URL.String()]
	}
	if !ok {
		body, ok = transport.media[request.URL.String()]
	}
	transport.mu.Unlock()
	if !ok && status == http.StatusOK {
		status = http.StatusNotFound
	}
	headers := make(http.Header)
	if strings.HasSuffix(request.URL.Path, ".m4a") || strings.HasSuffix(request.URL.Path, ".mp4") {
		headers.Set("Content-Type", "application/octet-stream")
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}

func (transport *bilibiliProductRoundTripper) isMedia(rawURL string) bool {
	return strings.Contains(rawURL, "upos-sz-mirror.bilivideo.com") || strings.Contains(rawURL, "bstarstatic.com/video") || strings.Contains(rawURL, "bstarstatic.com/audio")
}

func bilibiliProductDomesticPage(videoURL, audioURL string) []byte {
	return []byte(fmt.Sprintf(`<script>window.__INITIAL_STATE__={"videoData":{"bvid":"BV1prod0001","aid":1,"cid":2,"title":"Product","desc":"fixture","duration":4,"pic":"https://i0.hdslb.com/bfs/archive/product.jpg","owner":{"name":"Fixture","mid":9},"pages":[{"page":1,"part":"Part 1","duration":4}]}};</script><script>window.__playinfo__={"code":0,"data":{"timelength":4000,"dash":{"video":[{"id":80,"baseUrl":"%s","mimeType":"video/mp4","bandwidth":800000,"width":640,"height":360,"codecs":"avc1"}],"audio":[{"id":30280,"baseUrl":"%s","mimeType":"audio/mp4","bandwidth":64000,"codecs":"mp4a"}]}}};</script>`, videoURL, audioURL))
}

func bilibiliProductBangumiPage() []byte {
	return []byte(`<script>window.__INITIAL_STATE__={"videoInfo":{"aid":1,"bvid":"BV1prod0001","cid":2,"title":"Bangumi Product","duration":4,"cover":"https://i0.hdslb.com/bfs/bangumi/product.jpg"},"seasonInfo":{"season_id":9,"title":"Season"}};</script>`)
}

func bilibiliProductBangumiAPI(videoURL, audioURL string) []byte {
	return []byte(fmt.Sprintf(`{"code":0,"data":{"result":{"video_info":{"timelength":4000,"dash":{"video":[{"id":80,"baseUrl":"%s","mimeType":"video/mp4","codecs":"avc1"}],"audio":[{"id":30280,"baseUrl":"%s","mimeType":"audio/mp4","codecs":"mp4a"}]}}}}}`, videoURL, audioURL))
}

func bilibiliProductInternationalPage(title string, ugc bool) []byte {
	if ugc {
		return []byte(fmt.Sprintf(`<script>window.__INITIAL_STATE__={"UgcVideo":{"videoData":{"title":"%s","desc":"intl description","cover":"https://v.bstarstatic.com/cover.jpg","formatted_pub_date":"2022-11-08 17:42:04"}}};</script>`, title))
	}
	return []byte(fmt.Sprintf(`<script>window.__INITIAL_STATE__={"OgvVideo":{"epDetail":{"title_display":"E2 - %s","desc":"intl description","cover":"https://v.bstarstatic.com/cover.jpg","formatted_pub_date":"2022-11-08 17:42:04"}}};</script>`, title))
}

func bilibiliProductInternationalAPI(videoURL, audioURL string) []byte {
	return []byte(fmt.Sprintf(`{"code":0,"data":{"playurl":{"video":[{"video_resource":{"url":%q,"width":640,"height":360,"bandwidth":400000,"codecs":"avc1","size":17},"stream_info":{"desc_words":"360P"}}],"audio_resource":[{"url":%q,"bandwidth":64000,"codecs":"mp4a","size":17}]}}}`, videoURL, audioURL))
}

func bilibiliProductOp(t *testing.T, transport *network.Client, rawURL, format string) (*operation, string) {
	t.Helper()
	root := t.TempDir()
	request := Request{URL: rawURL, OutputDir: root, Format: format, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	rootExtractor := ""
	capabilities := mediaformat.PlannerCapabilities{CanMergeFormats: true}
	return &operation{client: NewClient(), request: request, transport: transport, registry: productRegistry(), compatibility: compatibility, rootExtractor: &rootExtractor, plannerCapabilities: &capabilities}, root
}

func newBilibiliProductOp(t *testing.T, roundTripper *bilibiliProductRoundTripper, rawURL, format string) (*operation, string) {
	transport, err := network.New(network.Config{RoundTripper: roundTripper})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(transport.CloseIdleConnections)
	return bilibiliProductOp(t, transport, rawURL, format)
}

func assertBilibiliNoArtifacts(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("unexpected artifacts: %v", names)
	}
}

func TestProductBilibiliDomesticDASHExactBytes(t *testing.T) {
	videoURL := "https://upos-sz-mirror.bilivideo.com/product-video.mp4?sign=video"
	audioURL := "https://upos-sz-mirror.bilivideo.com/product-audio.m4a?sign=audio"
	rt := &bilibiliProductRoundTripper{
		pages: map[string][]byte{"https://www.bilibili.com/video/BV1prod0001": bilibiliProductDomesticPage(videoURL, audioURL)},
		media: map[string][]byte{videoURL: []byte("domestic-video-bytes"), audioURL: []byte("domestic-audio-bytes")},
	}
	op, _ := newBilibiliProductOp(t, rt, "https://www.bilibili.com/video/BV1prod0001", "bv*")
	result, err := op.process(context.Background(), op.request.URL, "", nil, make(map[string]bool), 0)
	if err != nil || !result.Downloaded || result.Extractor != "bilibili" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil || string(data) != "domestic-video-bytes" {
		t.Fatalf("downloaded=%q err=%v", data, err)
	}
}

func TestProductBilibiliBangumiExactBytesAndScopedReferer(t *testing.T) {
	videoURL := "https://upos-sz-mirror.bilivideo.com/bangumi-video.mp4?token=video"
	audioURL := "https://upos-sz-mirror.bilivideo.com/bangumi-audio.m4a?token=audio"
	pageURL := "https://www.bilibili.com/bangumi/play/ep1"
	apiURL := "https://api.bilibili.com/pgc/player/web/v2/playurl?ep_id=1&fnval=12240"
	rt := &bilibiliProductRoundTripper{
		pages: map[string][]byte{pageURL: bilibiliProductBangumiPage()},
		api:   map[string][]byte{apiURL: bilibiliProductBangumiAPI(videoURL, audioURL)},
		media: map[string][]byte{videoURL: []byte("bangumi-video-bytes"), audioURL: []byte("bangumi-audio-bytes")},
	}
	op, _ := newBilibiliProductOp(t, rt, pageURL, "bv*")
	result, err := op.process(context.Background(), pageURL, "", nil, make(map[string]bool), 0)
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil || string(data) != "bangumi-video-bytes" {
		t.Fatalf("downloaded=%q err=%v", data, err)
	}
	for _, request := range rt.requests {
		if referer := request.headers.Get("Referer"); referer != "" && referer != "https://www.bilibili.com/" {
			t.Fatalf("unexpected referer on %s: %q", request.url, referer)
		}
	}
}

func TestProductBilibiliAudioExactBytes(t *testing.T) {
	rawURL := "https://www.bilibili.com/audio/au1"
	mediaURL := "https://upos-sz-mirror.bilivideo.com/song.m4a?sign=song"
	rt := &bilibiliProductRoundTripper{
		api: map[string][]byte{
			"https://www.bilibili.com/audio/music-service-c/web/url?sid=1":       []byte(fmt.Sprintf(`{"code":0,"data":{"cdns":[%q],"size":17}}`, mediaURL)),
			"https://www.bilibili.com/audio/music-service-c/web/song/info?sid=1": []byte(`{"code":0,"data":{"title":"Song","duration":4,"author":"Artist","cover":"https://i0.hdslb.com/bfs/song.jpg"}}`),
		},
		media: map[string][]byte{mediaURL: []byte("audio-exact-bytes")},
	}
	transport, err := network.New(network.Config{RoundTripper: rt, DefaultHeaders: http.Header{"Authorization": {"Bearer ambient"}, "Cookie": {"session=ambient"}, "Proxy-Authorization": {"Basic ambient"}, "Referer": {"https://evil.invalid/ambient"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	op, _ := bilibiliProductOp(t, transport, rawURL, "ba*")
	result, err := op.process(context.Background(), rawURL, "", nil, make(map[string]bool), 0)
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil || string(data) != "audio-exact-bytes" {
		t.Fatalf("downloaded=%q err=%v", data, err)
	}
	for _, request := range rt.requests {
		for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
			if got := request.headers.Get(key); got != "" {
				t.Fatalf("%s leaked on %s: %q", key, request.url, got)
			}
		}
		wantReferer := ""
		if request.url == mediaURL {
			wantReferer = rawURL
		}
		if got := request.headers.Get("Referer"); got != wantReferer {
			t.Fatalf("referer on %s=%q want=%q", request.url, got, wantReferer)
		}
	}
}

func TestProductBilibiliInternationalExactBytes(t *testing.T) {
	rawURL := "https://www.bilibili.tv/en/play/1/2"
	mediaURL := "https://v.bstarstatic.com/product-video.mp4?token=intl&sig=video"
	audioURL := "https://v.bstarstatic.com/product-audio.m4a?token=intl&sig=audio"
	apiURL := "https://api.bilibili.tv/intl/gateway/web/playurl?platform=web&ep_id=2"
	rt := &bilibiliProductRoundTripper{
		pages: map[string][]byte{rawURL: bilibiliProductInternationalPage("Intl Product", false)},
		api:   map[string][]byte{apiURL: bilibiliProductInternationalAPI(mediaURL, audioURL)},
		media: map[string][]byte{mediaURL: []byte("intl-exact-bytes")},
	}
	op, _ := newBilibiliProductOp(t, rt, rawURL, "bv*")
	result, err := op.process(context.Background(), rawURL, "", nil, make(map[string]bool), 0)
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(result.Filename)
	if err != nil || string(data) != "intl-exact-bytes" {
		t.Fatalf("downloaded=%q err=%v", data, err)
	}

	ugcURL := "https://www.bilibili.tv/en/video/2045730385"
	ugcMediaURL := "https://v.bstarstatic.com/ugc-video.mp4?token=ugc&sig=video"
	ugcAudioURL := "https://v.bstarstatic.com/ugc-audio.m4a?token=ugc&sig=audio"
	ugcAPIURL := "https://api.bilibili.tv/intl/gateway/web/playurl?platform=web&aid=2045730385"
	rt.pages = map[string][]byte{ugcURL: bilibiliProductInternationalPage("UGC Product", true)}
	rt.api = map[string][]byte{ugcAPIURL: bilibiliProductInternationalAPI(ugcMediaURL, ugcAudioURL)}
	rt.media = map[string][]byte{ugcMediaURL: []byte("ugc-exact-bytes")}
	op, _ = newBilibiliProductOp(t, rt, ugcURL, "bv*")
	result, err = op.process(context.Background(), ugcURL, "", nil, make(map[string]bool), 0)
	if err != nil || !result.Downloaded {
		t.Fatalf("ugc result=%+v err=%v", result, err)
	}
	data, err = os.ReadFile(result.Filename)
	if err != nil || string(data) != "ugc-exact-bytes" {
		t.Fatalf("ugc downloaded=%q err=%v", data, err)
	}
}

func TestProductBilibiliTransparentPlayerAndPlaylistReentry(t *testing.T) {
	videoURL := "https://upos-sz-mirror.bilivideo.com/reentry-video.mp4"
	audioURL := "https://upos-sz-mirror.bilivideo.com/reentry-audio.m4a"
	child := "https://www.bilibili.com/video/BV1prod0001"
	dynamicURL := "https://t.bilibili.com/1"
	dynamicAPI := "https://api.bilibili.com/x/polymer/web-dynamic/v1/detail?id=1"
	rt := &bilibiliProductRoundTripper{
		pages: map[string][]byte{child: bilibiliProductDomesticPage(videoURL, audioURL), "https://www.bilibili.com/video/av1": bilibiliProductDomesticPage(videoURL, audioURL)},
		api:   map[string][]byte{dynamicAPI: []byte(`{"code":0,"data":{"item":{"modules":{"module_dynamic":{"major":{"archive":{"jump_url":"https://www.bilibili.com/video/av1"}}}}}}}`)},
		media: map[string][]byte{videoURL: []byte("reentry-video-bytes")},
	}
	op, _ := newBilibiliProductOp(t, rt, dynamicURL, "bv*")
	result, err := op.process(context.Background(), dynamicURL, "", nil, make(map[string]bool), 0)
	if err != nil || !result.Downloaded {
		t.Fatalf("dynamic result=%+v err=%v", result, err)
	}
	if data, err := os.ReadFile(result.Filename); err != nil || string(data) != "reentry-video-bytes" {
		t.Fatalf("dynamic bytes=%q err=%v", data, err)
	}

	playerURL := "https://player.bilibili.com/player.html?aid=1&cid=2"
	op, _ = newBilibiliProductOp(t, rt, playerURL, "bv*")
	result, err = op.process(context.Background(), playerURL, "", nil, make(map[string]bool), 0)
	if err != nil || !result.Downloaded {
		t.Fatalf("player result=%+v err=%v", result, err)
	}
	if data, err := os.ReadFile(result.Filename); err != nil || string(data) != "reentry-video-bytes" {
		t.Fatalf("player bytes=%q err=%v", data, err)
	}

	listURL := "https://space.bilibili.com/1/lists/2"
	listAPI := "https://api.bilibili.com/x/polymer/web-space/seasons_archives_list?mid=1&season_id=2&page_num=1&page_size=30"
	rt.pages = map[string][]byte{child: bilibiliProductDomesticPage(videoURL, audioURL)}
	rt.api = map[string][]byte{listAPI: []byte(`{"code":0,"data":{"meta":{"name":"Collection Name"},"archives":[{"aid":1,"bvid":"BV1prod0001","title":"Row","page":{"total":1,"page_size":30}}],"page":{"total":1,"page_size":30}}}`)}
	op, _ = newBilibiliProductOp(t, rt, listURL, "bv*")
	op.request.Playlist.Items = "1"
	result, err = op.process(context.Background(), listURL, "", nil, make(map[string]bool), 0)
	if err != nil || len(result.Entries) != 1 {
		t.Fatalf("playlist result=%+v err=%v", result, err)
	}
}

type bilibiliPlaylistProductCase struct {
	name      string
	rawURL    string
	format    string
	wantBytes string
	setup     func(*bilibiliProductRoundTripper)
}

func TestProductBilibiliRetainedPlaylistFamilies(t *testing.T) {
	child := "https://www.bilibili.com/video/BV1prod0001"
	domesticVideoURL := "https://upos-sz-mirror.bilivideo.com/playlist-video.mp4"
	domesticAudioURL := "https://upos-sz-mirror.bilivideo.com/playlist-audio.m4a"
	intlChild := "https://www.bilibili.tv/en/play/1/2"
	intlVideoURL := "https://v.bstarstatic.com/playlist-video.mp4?token=intl&sig=playlist"
	sectionAPI := "https://api.bilibili.com/pgc/web/season/section?season_id=9"
	collectionAPI := "https://api.bilibili.com/x/polymer/web-space/seasons_archives_list?mid=1&season_id=2&page_num=1&page_size=30"
	seriesAPI := "https://api.bilibili.com/x/series/archives?mid=1&series_id=2&pn=1&ps=30"
	categoryAPI := "https://api.bilibili.com/x/web-interface/newlist?rid=26&type=1&ps=20&jsonp=jsonp&Search_key=kichiku%3A+mad&pn=1"
	audioURLAPI := "https://www.bilibili.com/audio/music-service-c/web/url?sid=1"
	audioInfoAPI := "https://www.bilibili.com/audio/music-service-c/web/song/info?sid=1"
	audioAlbumAPI := "https://www.bilibili.com/audio/music-service-c/web/song/of-menu?sid=1&pn=1&ps=100"
	intlSeasonAPI := "https://api.bilibili.tv/intl/gateway/web/v2/ogv/play/season_info?season_id=1&platform=web"
	intlEpisodesAPI := "https://api.bilibili.tv/intl/gateway/web/v2/ogv/play/episodes?season_id=1&platform=web"
	intlPlayURLAPI := "https://api.bilibili.tv/intl/gateway/web/playurl?platform=web&ep_id=2"
	playlistDomesticPage := bilibiliProductDomesticPage(domesticVideoURL, domesticAudioURL)
	playlistDomesticMedia := func(transport *bilibiliProductRoundTripper, bytes []byte) {
		transport.pages[child] = playlistDomesticPage
		transport.media[domesticVideoURL] = bytes
	}
	playlistBangumiSection := []byte(`{"code":0,"result":{"main_section":{"episodes":[{"id":1,"aid":1,"bvid":"BV1prod0001","cid":2,"title":"Episode","cover":"https://i0.hdslb.com/bfs/episode.jpg"}]}}}`)
	playlistArchive := `{"aid":1,"bvid":"BV1prod0001","title":"Row","pic":"https://i0.hdslb.com/bfs/row.jpg","duration":4,"stat":{"view":2}}`
	cases := []bilibiliPlaylistProductCase{
		{
			name:   "bangumi media",
			rawURL: "https://www.bilibili.com/bangumi/media/md9", format: "bv*", wantBytes: "bangumi-media-child",
			setup: func(transport *bilibiliProductRoundTripper) {
				transport.pages["https://www.bilibili.com/bangumi/media/md9"] = []byte(`<script>window.__INITIAL_STATE__={"mediaInfo":{"season_id":9,"title":"Season"}};</script>`)
				transport.api[sectionAPI] = playlistBangumiSection
				playlistDomesticMedia(transport, []byte("bangumi-media-child"))
				transport.pages["https://www.bilibili.com/bangumi/play/ep1"] = bilibiliProductBangumiPage()
				transport.api["https://api.bilibili.com/pgc/player/web/v2/playurl?ep_id=1&fnval=12240"] = bilibiliProductBangumiAPI(domesticVideoURL, domesticAudioURL)
			},
		},
		{
			name:   "bangumi season",
			rawURL: "https://www.bilibili.com/bangumi/play/ss9", format: "bv*", wantBytes: "bangumi-season-child",
			setup: func(transport *bilibiliProductRoundTripper) {
				transport.pages["https://www.bilibili.com/bangumi/play/ss9"] = []byte(`<script>window.__INITIAL_STATE__={"mediaInfo":{"title":"Season"}};</script>`)
				transport.api[sectionAPI] = playlistBangumiSection
				playlistDomesticMedia(transport, []byte("bangumi-season-child"))
				transport.pages["https://www.bilibili.com/bangumi/play/ep1"] = bilibiliProductBangumiPage()
				transport.api["https://api.bilibili.com/pgc/player/web/v2/playurl?ep_id=1&fnval=12240"] = bilibiliProductBangumiAPI(domesticVideoURL, domesticAudioURL)
			},
		},
		{
			name:   "collection list",
			rawURL: "https://space.bilibili.com/1/lists/2", format: "bv*", wantBytes: "collection-child",
			setup: func(transport *bilibiliProductRoundTripper) {
				transport.api[collectionAPI] = []byte(`{"code":0,"data":{"meta":{"name":"Collection Name","description":"Collection Description","mid":1,"ptime":1700000000,"cover":"https://i0.hdslb.com/bfs/collection.jpg"},"archives":[` + playlistArchive + `],"page":{"total":1,"page_size":30}}}`)
				playlistDomesticMedia(transport, []byte("collection-child"))
			},
		},
		{
			name:   "series list",
			rawURL: "https://space.bilibili.com/1/lists/2?type=series", format: "bv*", wantBytes: "series-child",
			setup: func(transport *bilibiliProductRoundTripper) {
				transport.api["https://api.bilibili.com/x/series/series?series_id=2"] = []byte(`{"code":0,"data":{"meta":{"name":"Series Name","description":"Series Description","mid":1,"ctime":1700000000,"mtime":1700000001}}}`)
				transport.api[seriesAPI] = []byte(`{"code":0,"data":{"meta":{"name":"Series Name"},"archives":[` + playlistArchive + `],"page":{"total":1,"size":30}}}`)
				playlistDomesticMedia(transport, []byte("series-child"))
			},
		},
		{
			name:   "category",
			rawURL: "https://www.bilibili.com/v/kichiku/mad", format: "bv*", wantBytes: "category-child",
			setup: func(transport *bilibiliProductRoundTripper) {
				transport.api[categoryAPI] = []byte(`{"code":0,"data":{"page":{"count":1,"size":20},"archives":[` + playlistArchive + `]}}`)
				playlistDomesticMedia(transport, []byte("category-child"))
			},
		},
		{
			name:   "audio album",
			rawURL: "https://www.bilibili.com/audio/am1", format: "ba*", wantBytes: "audio-album-child",
			setup: func(transport *bilibiliProductRoundTripper) {
				audioURL := "https://upos-sz-mirror.bilivideo.com/album-song.m4a?sign=album"
				transport.api[audioAlbumAPI] = []byte(`{"code":0,"data":{"data":[{"id":1,"title":"Song"}]}}`)
				transport.api[audioURLAPI] = []byte(fmt.Sprintf(`{"code":0,"data":{"cdns":[%q],"size":17}}`, audioURL))
				transport.api[audioInfoAPI] = []byte(`{"code":0,"data":{"title":"Song","duration":4,"author":"Artist"}}`)
				transport.media[audioURL] = []byte("audio-album-child")
			},
		},
		{
			name:   "international series",
			rawURL: "https://www.bilibili.tv/en/play/1", format: "bv*", wantBytes: "intl-series-child",
			setup: func(transport *bilibiliProductRoundTripper) {
				transport.api[intlSeasonAPI] = []byte(`{"code":0,"data":{"season":{"title":"Series","horizontal_cover":"https://v.bstarstatic.com/cover.jpg"}}}`)
				transport.api[intlEpisodesAPI] = []byte(`{"code":0,"data":{"sections":[{"episodes":[{"episode_id":2,"title":"Episode"}]}]}}`)
				transport.pages[intlChild] = bilibiliProductInternationalPage("Intl Product", false)
				transport.api[intlPlayURLAPI] = bilibiliProductInternationalAPI(intlVideoURL, "https://v.bstarstatic.com/playlist-audio.m4a?token=intl&sig=playlist")
				transport.media[intlVideoURL] = []byte("intl-series-child")
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			transport := &bilibiliProductRoundTripper{pages: make(map[string][]byte), api: make(map[string][]byte), media: make(map[string][]byte)}
			test.setup(transport)
			op, _ := newBilibiliProductOp(t, transport, test.rawURL, test.format)
			op.request.Playlist.Items = "1"
			result, err := op.process(context.Background(), test.rawURL, "", nil, make(map[string]bool), 0)
			if err != nil || len(result.Entries) != 1 || !result.Entries[0].Downloaded {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			data, err := os.ReadFile(result.Entries[0].Filename)
			if err != nil || string(data) != test.wantBytes {
				t.Fatalf("child bytes=%q err=%v want=%q", data, err, test.wantBytes)
			}
		})
	}
}

func TestProductBilibiliCredentialIsolation(t *testing.T) {
	pageURL := "https://www.bilibili.com/bangumi/play/ep1"
	apiURL := "https://api.bilibili.com/pgc/player/web/v2/playurl?ep_id=1&fnval=12240"
	videoURL := "https://upos-sz-mirror.bilivideo.com/isolation-video.mp4"
	audioURL := "https://upos-sz-mirror.bilivideo.com/isolation-audio.m4a"
	thumbnailURL := "https://i0.hdslb.com/bfs/bangumi/product.jpg"
	rt := &bilibiliProductRoundTripper{pages: map[string][]byte{pageURL: bilibiliProductBangumiPage()}, api: map[string][]byte{apiURL: bilibiliProductBangumiAPI(videoURL, audioURL)}, media: map[string][]byte{videoURL: []byte("isolated"), thumbnailURL: []byte("thumbnail")}}
	transport, err := network.New(network.Config{RoundTripper: rt, DefaultHeaders: http.Header{"Authorization": {"Bearer ambient-secret"}, "Cookie": {"session=ambient-secret"}, "Proxy-Authorization": {"Basic ambient-secret"}, "Referer": {"https://evil.invalid/ambient"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	op, _ := bilibiliProductOp(t, transport, pageURL, "bv*")
	op.request.Thumbnails.Write = true
	result, err := op.process(context.Background(), pageURL, "", nil, make(map[string]bool), 0)
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	seen := make(map[string]bool)
	for _, request := range rt.requests {
		role := ""
		wantReferer := ""
		switch {
		case request.url == pageURL:
			role = "page"
		case request.url == apiURL:
			role, wantReferer = "api", "https://www.bilibili.com/"
		case request.url == videoURL || request.url == audioURL:
			role = "media"
		case request.url == thumbnailURL:
			role = "thumbnail"
		default:
			t.Fatalf("unexpected captured Bilibili request: %s", request.url)
		}
		seen[role] = true
		for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
			if got := request.headers.Get(key); got != "" {
				t.Fatalf("%s leaked on %s (%s): %q", key, request.url, role, got)
			}
		}
		if got := request.headers.Get("Referer"); got != wantReferer {
			t.Fatalf("referer on %s (%s)=%q want %q", request.url, role, got, wantReferer)
		}
	}
	for _, role := range []string{"page", "api", "media", "thumbnail"} {
		if !seen[role] {
			t.Fatalf("missing credential-isolation role %s; captured=%v", role, rt.requests)
		}
	}
}

func TestProductBilibiliEnteredCancellationLeavesNoArtifacts(t *testing.T) {
	videoURL := "https://upos-sz-mirror.bilivideo.com/cancel-video.mp4"
	audioURL := "https://upos-sz-mirror.bilivideo.com/cancel-audio.m4a"
	rt := &bilibiliProductRoundTripper{pages: map[string][]byte{"https://www.bilibili.com/video/BV1prod0001": bilibiliProductDomesticPage(videoURL, audioURL)}, media: map[string][]byte{videoURL: []byte("cancel")}, entered: make(chan struct{}, 1), release: make(chan struct{})}
	op, root := newBilibiliProductOp(t, rt, "https://www.bilibili.com/video/BV1prod0001", "bv*")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := op.process(ctx, op.request.URL, "", nil, make(map[string]bool), 0); done <- err }()
	select {
	case <-rt.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("media request never entered")
	}
	cancel()
	close(rt.release)
	if err := <-done; err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	assertBilibiliNoArtifacts(t, root)
}

func TestProductBilibiliMediaFailureLeavesNoArtifacts(t *testing.T) {
	videoURL := "https://upos-sz-mirror.bilivideo.com/failure-video.mp4"
	audioURL := "https://upos-sz-mirror.bilivideo.com/failure-audio.m4a"
	rt := &bilibiliProductRoundTripper{pages: map[string][]byte{"https://www.bilibili.com/video/BV1prod0001": bilibiliProductDomesticPage(videoURL, audioURL)}, statuses: map[string]int{videoURL: http.StatusInternalServerError}}
	op, root := newBilibiliProductOp(t, rt, "https://www.bilibili.com/video/BV1prod0001", "bv*")
	_, err := op.process(context.Background(), op.request.URL, "", nil, make(map[string]bool), 0)
	if err == nil || !IsCategory(err, ErrorNetwork) {
		t.Fatalf("error=%v", err)
	}
	assertBilibiliNoArtifacts(t, root)
}
