package extractor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

type bilibiliFixtureTransport struct {
	mu       sync.Mutex
	pages    map[string][]byte
	api      map[string][]byte
	statuses map[string]int
	calls    []*http.Request
}

func (transport *bilibiliFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	response, err := transport.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return body, response.Header.Clone(), err
}

func (transport *bilibiliFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.DoWithoutCredentialsNoRedirect(ctx, request)
}

func (transport *bilibiliFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.respond(ctx, request)
}

func (transport *bilibiliFixtureTransport) DoWithoutCredentialsNoRedirectWithReferer(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.respond(ctx, request)
}

func (transport *bilibiliFixtureTransport) respond(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.calls = append(transport.calls, request.Clone(ctx))
	status := 200
	if transport.statuses != nil {
		status = transport.statuses[request.URL.String()]
		if status == 0 {
			status = 200
		}
	}
	body, ok := transport.api[request.URL.String()]
	if !ok {
		body, ok = transport.pages[request.URL.String()]
	}
	transport.mu.Unlock()
	if !ok && status == 200 {
		status = http.StatusNotFound
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}

func bilibiliPage(state string) []byte {
	return []byte(`<script>window.__INITIAL_STATE__=` + state + `;</script>`)
}

func bilibiliPlayinfoJSON(videoURL, audioURL string) []byte {
	return []byte(`{"code":0,"data":{"timelength":120000,"dash":{"video":[{"id":80,"baseUrl":"` + videoURL + `","mimeType":"video/mp4","bandwidth":800000,"width":1280,"height":720,"codecs":"avc1.640028"}],"audio":[{"id":30280,"baseUrl":"` + audioURL + `","mimeType":"audio/mp4","bandwidth":64000,"codecs":"mp4a.40.2"}]}}}`)
}

func TestBilibiliHydrationAndAnthology(t *testing.T) {
	canonical := "https://www.bilibili.com/video/BV1fixture01"
	transport := &bilibiliFixtureTransport{pages: map[string][]byte{canonical: readPublicFixture(t, "bilibili", "success.html")}}
	result, err := NewBilibili().Extract(context.Background(), Request{URL: canonical + "?tracking=discarded", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 2 {
		t.Fatalf("formats=%d", len(formats))
	}
	selected, err := mediaformat.Default(result.Info, mediaformat.Options{})
	if err != nil || len(selected) != 2 || selected[0].ID != "dash-video-80" || selected[1].ID != "dash-audio-30280" {
		t.Fatalf("default selection=%#v error=%v", selected, err)
	}
	if selected[0].VCodec != "unknown" || selected[0].ACodec != "none" || selected[1].VCodec != "none" || selected[1].ACodec != "unknown" {
		t.Fatalf("normalized codecs=%#v", selected)
	}
	transport.pages[canonical] = bilibiliPage(`{"videoData":{"bvid":"BV1fixture01","title":"Fixture","pages":[{"page":1,"part":"one"},{"page":2,"part":"two"}]}}`)
	playlist, err := NewBilibili().Extract(context.Background(), Request{URL: canonical, Transport: transport})
	if err != nil || !playlist.IsPlaylist() {
		t.Fatalf("playlist=%v %v", playlist.IsPlaylist(), err)
	}
	entries, err := CollectEntries(context.Background(), playlist.Entries, 3)
	if err != nil || len(entries) != 2 || !entries[0].Transparent {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
}

func TestBilibiliNoPlaylistAnthologyChoice(t *testing.T) {
	const rawURL = "https://www.bilibili.com/video/BV1fixture01"
	page := bilibiliPage(`{"videoData":{"bvid":"BV1fixture01","title":"Fixture","pages":[{"page":1,"part":"one"},{"page":2,"part":"two"}]}}`)
	page = append(page, []byte(`<script>window.__playinfo__=`)...)
	page = append(page, bilibiliPlayinfoJSON(
		"https://upos-sz-mirror.bilivideo.com/choice-video.mp4?sig=video",
		"https://upos-sz-mirror.bilivideo.com/choice-audio.m4a?sig=audio",
	)...)
	page = append(page, []byte(`;</script>`)...)

	t.Run("default-prefers-playlist", func(t *testing.T) {
		transport := &bilibiliFixtureTransport{pages: map[string][]byte{rawURL: page}}
		result, err := NewBilibili().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil || len(entries) != 2 || entries[0].URL != rawURL+"?p=1" {
			t.Fatalf("entries=%#v err=%v", entries, err)
		}
		transport.mu.Lock()
		calls := len(transport.calls)
		transport.mu.Unlock()
		if calls != 1 {
			t.Fatalf("calls=%d want one root page before child re-entry", calls)
		}
	})

	t.Run("no-playlist-prefers-first-video", func(t *testing.T) {
		transport := &bilibiliFixtureTransport{pages: map[string][]byte{rawURL: page}}
		result, err := NewBilibili().Extract(context.Background(), Request{URL: rawURL, Transport: transport, NoPlaylist: true})
		if err != nil || result.IsPlaylist() || result.IsURL() {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		if id, _ := result.Info.Lookup("id").StringValue(); id != "BV1fixture01_p1" {
			t.Fatalf("id=%q want first anthology page", id)
		}
		transport.mu.Lock()
		calls := len(transport.calls)
		transport.mu.Unlock()
		if calls != 1 {
			t.Fatalf("calls=%d want one direct page read", calls)
		}
	})

	t.Run("explicit-page-remains-video", func(t *testing.T) {
		transport := &bilibiliFixtureTransport{pages: map[string][]byte{rawURL + "?p=2": page}}
		result, err := NewBilibili().Extract(context.Background(), Request{URL: rawURL + "?p=2", Transport: transport})
		if err != nil || result.IsPlaylist() || result.IsURL() {
			t.Fatalf("result=%#v err=%v", result, err)
		}
		if id, _ := result.Info.Lookup("id").StringValue(); id != "BV1fixture01_p2" {
			t.Fatalf("id=%q want explicit anthology page", id)
		}
	})

	t.Run("cancellation-prevents-page-read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		transport := &bilibiliFixtureTransport{pages: map[string][]byte{rawURL: page}}
		_, err := NewBilibili().Extract(ctx, Request{URL: rawURL, Transport: transport, NoPlaylist: true})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v want cancellation", err)
		}
		transport.mu.Lock()
		calls := len(transport.calls)
		transport.mu.Unlock()
		if calls != 0 {
			t.Fatalf("cancelled request made %d calls", calls)
		}
	})
}

func TestBilibiliDASHPreservesReportedCodecs(t *testing.T) {
	var play bilibiliPlayinfo
	play.Data.DASH.Audio = []bilibiliDash{{ID: 30280, BaseURL: "https://upos-sz-mirror.bilivideo.com/audio.m4a", MimeType: "audio/mp4", Codecs: "mp4a.40.2"}}
	play.Data.DASH.Video = []bilibiliDash{{ID: 80, BaseURL: "https://upos-sz-mirror.bilivideo.com/video.mp4", MimeType: "video/mp4", Codecs: "avc1.640028", Height: 1080}}
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(bilibiliFormats(play)...)}))
	selected, err := mediaformat.Default(info, mediaformat.Options{})
	if err != nil || len(selected) != 2 || selected[0].VCodec != "avc1.640028" || selected[1].ACodec != "mp4a.40.2" {
		t.Fatalf("default selection=%#v error=%v", selected, err)
	}
}

func TestBilibiliURLPoliciesAndOverlap(t *testing.T) {
	for _, raw := range []string{
		"https://www.bilibili.com/video/BV1abcdef01",
		"https://www.bilibili.com/festival/demo?bvid=BV1abcdef01",
		"https://www.bilibili.com/bangumi/play/ep1",
		"https://www.bilibili.com/bangumi/media/md1",
		"https://www.bilibili.com/bangumi/play/ss1",
		"https://space.bilibili.com/1/lists/2",
		"https://space.bilibili.com/1/lists/2?type=series",
		"https://www.bilibili.com/v/kichiku/mad",
		"https://www.bilibili.com/audio/au1",
		"https://www.bilibili.com/audio/am1",
		"https://player.bilibili.com/player.html?aid=1&cid=2",
		"https://t.bilibili.com/1",
		"https://www.bilibili.tv/en/play/1/2",
		"https://www.bilibili.tv/en/play/1",
	} {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if raw == "https://www.bilibili.com/video/BV1abcdef01" && !NewBilibili().Suitable(parsed) || strings.Contains(raw, "bangumi/play/ep") && !NewBilibiliBangumi().Suitable(parsed) {
			t.Fatalf("route rejected: %s", raw)
		}
	}
	if !NewBilibiliSeriesList().Suitable(mustParseBilibiliURL(t, "https://space.bilibili.com/1/lists/2?type=series")) || NewBilibiliCollectionList().Suitable(mustParseBilibiliURL(t, "https://space.bilibili.com/1/lists/2?type=series")) {
		t.Fatal("collection/series overlap")
	}
	for _, raw := range []string{
		"https://www.bilibili.com.evil.invalid/video/BV1abcdef01",
		"https://user:pass@www.bilibili.com/video/BV1abcdef01",
		"http://www.bilibili.com/bangumi/play/ep1",
		"https://www.bilibili.com/video/BV1abcdef01/extra",
		"https://www.bilibili.com/video/BV1abcdef01?p=1&p=2",
		"https://player.bilibili.com/player.html?aid=1&aid=2",
		"https://www.bilibili.tv/en/play/1/2#fragment",
		"https://space.bilibili.com:443/1/lists/2",
	} {
		parsed, err := url.Parse(raw)
		if err == nil && (NewBilibili().Suitable(parsed) || NewBilibiliBangumi().Suitable(parsed) || NewBilibiliPlayer().Suitable(parsed) || NewBiliIntl().Suitable(parsed) || NewBilibiliCollectionList().Suitable(parsed)) {
			t.Fatalf("unsafe route accepted: %s", raw)
		}
	}
	for _, test := range []struct {
		raw  string
		role bilibiliURLRole
		ok   bool
	}{
		{"https://upos-sz-mirror.bilivideo.com/video.m4s?sign=keep", bilibiliDomesticMedia, true},
		{"https://v.bstarstatic.com/video.mp4?token=keep", bilibiliInternationalMedia, true},
		{"https://evil.invalid/video.mp4", bilibiliDomesticMedia, false},
		{"https://bilivideo.com.evil.invalid/video.mp4", bilibiliDomesticMedia, false},
		{"https://user:secret@upos-sz-mirror.bilivideo.com/video.mp4", bilibiliDomesticMedia, false},
		{"http://upos-sz-mirror.bilivideo.com/video.mp4", bilibiliDomesticMedia, false},
	} {
		if got := validBilibiliRoleURL(test.raw, test.role); got != test.ok {
			t.Errorf("validBilibiliRoleURL(%q)=%v want %v", test.raw, got, test.ok)
		}
	}
}

func TestBilibiliHTTPBoundaryAndCancellation(t *testing.T) {
	raw := "https://www.bilibili.com/video/BV1abcdef01"
	for _, test := range []struct {
		status int
		want   error
	}{
		{http.StatusFound, ErrBilibiliRedirect},
		{http.StatusTooManyRequests, ErrBilibiliRateLimited},
		{http.StatusUnavailableForLegalReasons, ErrRegionRestricted},
		{http.StatusInternalServerError, ErrBilibiliServer},
	} {
		transport := &bilibiliFixtureTransport{statuses: map[string]int{raw: test.status}}
		_, err := NewBilibili().Extract(context.Background(), Request{URL: raw, Transport: transport})
		if !errors.Is(err, test.want) {
			t.Errorf("status %d: %v want %v", test.status, err, test.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewBilibili().Extract(ctx, Request{URL: raw, Transport: &bilibiliFixtureTransport{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

func TestBilibiliBangumiAndReusablePlaylists(t *testing.T) {
	pageURL := "https://www.bilibili.com/bangumi/play/ep1"
	apiURL := "https://api.bilibili.com/pgc/player/web/v2/playurl?ep_id=1&fnval=12240"
	transport := &bilibiliFixtureTransport{
		pages: map[string][]byte{pageURL: bilibiliPage(`{"videoInfo":{"aid":1,"bvid":"BV1bg0001","cid":2,"title":"Episode","duration":120,"cover":"https://i0.hdslb.com/bfs/episode.jpg"},"seasonInfo":{"season_id":9,"title":"Season"}}`)},
		api:   map[string][]byte{apiURL: []byte(`{"code":0,"data":{"result":{"video_info":{"timelength":120000,"dash":{"video":[{"id":80,"baseUrl":"https://upos-sz-mirror.bilivideo.com/video.mp4","mimeType":"video/mp4","codecs":"avc1.640028"}],"audio":[{"id":30280,"baseUrl":"https://upos-sz-mirror.bilivideo.com/audio.m4a","mimeType":"audio/mp4","codecs":"mp4a.40.2"}]}}}}}`)},
	}
	result, err := NewBilibiliBangumi().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if formats, _ := result.Info.Formats(); len(formats) != 2 {
		t.Fatalf("bangumi formats=%d", len(formats))
	}
	mediaURL := "https://www.bilibili.com/bangumi/media/md9"
	seasonURL := "https://www.bilibili.com/bangumi/play/ss9"
	sectionURL := "https://api.bilibili.com/pgc/web/season/section?season_id=9"
	section := []byte(`{"code":0,"result":{"main_section":{"episodes":[{"id":1,"aid":1,"cid":2,"bvid":"BV1bg0001","title":"Episode","cover":"https://i0.hdslb.com/bfs/episode.jpg"}]}}}`)
	transport.pages[mediaURL] = bilibiliPage(`{"mediaInfo":{"season_id":9,"title":"Season"}}`)
	transport.pages[seasonURL] = bilibiliPage(`{"mediaInfo":{"title":"Season"}}`)
	transport.api[sectionURL] = section
	for _, extractor := range []Extractor{NewBilibiliBangumiMedia(), NewBilibiliBangumiSeason()} {
		raw := mediaURL
		if extractor.Name() == NewBilibiliBangumiSeason().Name() {
			raw = seasonURL
		}
		result, err := extractor.Extract(context.Background(), Request{URL: raw, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		first, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil || len(first) != 1 {
			t.Fatalf("first entries=%d err=%v", len(first), err)
		}
		second, err := CollectEntries(context.Background(), result.Entries, 10)
		if err != nil || len(second) != 1 || first[0].URL != second[0].URL {
			t.Fatalf("reusable entries=%v/%v err=%v", first, second, err)
		}
	}
}

func TestBilibiliCollectionSeriesCategoryAndAudioRoutes(t *testing.T) {
	collectionURL := "https://space.bilibili.com/1/lists/2"
	seriesURL := collectionURL + "?type=series"
	collectionAPI := "https://api.bilibili.com/x/polymer/web-space/seasons_archives_list?mid=1&season_id=2&page_num=1&page_size=30"
	seriesAPI := "https://api.bilibili.com/x/series/archives?mid=1&series_id=2&pn=1&ps=30"
	row := `{"aid":1,"bvid":"BV1row0001","title":"Row","pic":"https://i0.hdslb.com/bfs/row.jpg","duration":4,"stat":{"view":2}}`
	transport := &bilibiliFixtureTransport{api: map[string][]byte{
		collectionAPI: []byte(`{"code":0,"data":{"meta":{"name":"Collection Name","description":"Collection Description","mid":1,"ptime":1700000000,"cover":"https://i0.hdslb.com/bfs/collection.jpg"},"archives":[` + row + `],"page":{"total":1,"page_size":30}}}`),
		seriesAPI:     []byte(`{"code":0,"data":{"meta":{"name":"Series Name","description":"Series Description","mid":1,"ctime":1700000000,"mtime":1700000001},"archives":[` + row + `],"page":{"total":1,"size":30}}}`),
		"https://api.bilibili.com/x/series/series?series_id=2": []byte(`{"code":0,"data":{"meta":{"name":"Series Name","description":"Series Description","mid":1,"ctime":1700000000,"mtime":1700000001}}}`),
	}}
	for _, test := range []struct {
		extractor Extractor
		raw       string
	}{{NewBilibiliCollectionList(), collectionURL}, {NewBilibiliSeriesList(), seriesURL}} {
		result, err := test.extractor.Extract(context.Background(), Request{URL: test.raw, Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		wantTitle := "Collection Name"
		if test.extractor.Name() == NewBilibiliSeriesList().Name() {
			wantTitle = "Series Name"
		}
		if title, _ := result.Info.Title(); title != wantTitle {
			t.Fatalf("%s title=%q want=%q", test.extractor.Name(), title, wantTitle)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, 2)
		if err != nil || len(entries) != 1 {
			t.Fatalf("%s entries=%d err=%v", test.extractor.Name(), len(entries), err)
		}
		entries, err = CollectEntries(context.Background(), result.Entries, 2)
		if err != nil || len(entries) != 1 {
			t.Fatalf("%s not reusable: %d %v", test.extractor.Name(), len(entries), err)
		}
	}
	categoryURL := "https://www.bilibili.com/v/kichiku/mad"
	categoryAPI := "https://api.bilibili.com/x/web-interface/newlist?rid=26&type=1&ps=20&jsonp=jsonp&Search_key=kichiku%3A+mad&pn=1"
	transport.api[categoryAPI] = []byte(`{"code":0,"data":{"page":{"count":1,"size":20},"archives":[` + row + `]}}`)
	result, err := NewBilibiliCategory().Extract(context.Background(), Request{URL: categoryURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := CollectEntries(context.Background(), result.Entries, 2); err != nil || len(entries) != 1 {
		t.Fatalf("category entries=%d err=%v", len(entries), err)
	}

	audioURL := "https://www.bilibili.com/audio/au1"
	transport.api["https://www.bilibili.com/audio/music-service-c/web/url?sid=1"] = []byte(`{"code":0,"data":{"cdns":["https://upos-sz-mirror.bilivideo.com/audio.m4a"],"size":4}}`)
	transport.api["https://www.bilibili.com/audio/music-service-c/web/song/info?sid=1"] = []byte(`{"code":0,"data":{"title":"Song","author":"Artist","duration":4,"cover":"https://i0.hdslb.com/bfs/song.jpg","statistic":{"play":3}}}`)
	result, err = NewBilibiliAudio().Extract(context.Background(), Request{URL: audioURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if formats, _ := result.Info.Formats(); len(formats) != 1 {
		t.Fatalf("audio formats=%d", len(formats))
	}
	albumURL := "https://www.bilibili.com/audio/am1"
	albumAPI := "https://www.bilibili.com/audio/music-service-c/web/song/of-menu?sid=1&pn=1&ps=100"
	transport.api[albumAPI] = []byte(`{"code":0,"data":{"data":[{"id":1,"title":"Song"}]}}`)
	result, err = NewBilibiliAudioAlbum().Extract(context.Background(), Request{URL: albumURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	before := len(transport.calls)
	transport.mu.Unlock()
	if _, err := CollectEntries(context.Background(), result.Entries, 2); err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	after := len(transport.calls)
	transport.mu.Unlock()
	if after <= before {
		t.Fatal("album fetched before iterator")
	}
}

func assertBilibiliReusableEntries(t *testing.T, sequence EntrySequence, want []string) {
	t.Helper()
	iterator := sequence.Iterator()
	first, ok, err := iterator.Next(context.Background())
	if err != nil || !ok || first.URL != want[0] {
		t.Fatalf("partial first=%#v ok=%t err=%v want=%s", first, ok, err, want[0])
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := iterator.Next(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("partial cancellation error=%v", err)
	}
	for round := 0; round < 2; round++ {
		entries, err := CollectEntries(context.Background(), sequence, len(want))
		if err != nil || len(entries) != len(want) {
			t.Fatalf("round %d entries=%d err=%v want=%d", round, len(entries), err, len(want))
		}
		for index, entry := range entries {
			if entry.URL != want[index] {
				t.Fatalf("round %d entry %d=%s want=%s", round, index, entry.URL, want[index])
			}
		}
	}
}

func TestBilibiliRetainedPlaylistSequences(t *testing.T) {
	row := func(id string) string {
		return `{"aid":1,"bvid":"` + id + `","title":"Row","pic":"https://i0.hdslb.com/bfs/row.jpg","duration":4,"stat":{"view":2}}`
	}
	section := []byte(`{"code":0,"result":{"main_section":{"episodes":[{"id":1,"aid":1,"cid":2,"bvid":"BV1row0001","title":"One"},{"id":2,"aid":1,"cid":3,"bvid":"BV1row0002","title":"Two"}]}}}`)
	playlistCases := []struct {
		name      string
		rawURL    string
		extractor Extractor
		configure func(*bilibiliFixtureTransport)
		want      []string
	}{
		{
			name: "bangumi media", rawURL: "https://www.bilibili.com/bangumi/media/md9", extractor: NewBilibiliBangumiMedia(),
			configure: func(transport *bilibiliFixtureTransport) {
				transport.pages["https://www.bilibili.com/bangumi/media/md9"] = bilibiliPage(`{"mediaInfo":{"season_id":9,"title":"Season"}}`)
				transport.api["https://api.bilibili.com/pgc/web/season/section?season_id=9"] = section
			},
			want: []string{"https://www.bilibili.com/bangumi/play/ep1", "https://www.bilibili.com/bangumi/play/ep2"},
		},
		{
			name: "bangumi season", rawURL: "https://www.bilibili.com/bangumi/play/ss9", extractor: NewBilibiliBangumiSeason(),
			configure: func(transport *bilibiliFixtureTransport) {
				transport.pages["https://www.bilibili.com/bangumi/play/ss9"] = bilibiliPage(`{"mediaInfo":{"title":"Season"}}`)
				transport.api["https://api.bilibili.com/pgc/web/season/section?season_id=9"] = section
			},
			want: []string{"https://www.bilibili.com/bangumi/play/ep1", "https://www.bilibili.com/bangumi/play/ep2"},
		},
		{
			name: "collection list", rawURL: "https://space.bilibili.com/1/lists/2", extractor: NewBilibiliCollectionList(),
			configure: func(transport *bilibiliFixtureTransport) {
				transport.api["https://api.bilibili.com/x/polymer/web-space/seasons_archives_list?mid=1&season_id=2&page_num=1&page_size=30"] = []byte(`{"code":0,"data":{"meta":{"name":"Collection"},"archives":[` + row("BV1row0001") + `],"page":{"total":2,"page_size":30}}}`)
				transport.api["https://api.bilibili.com/x/polymer/web-space/seasons_archives_list?mid=1&season_id=2&page_num=2&page_size=30"] = []byte(`{"code":0,"data":{"archives":[` + row("BV1row0002") + `],"page":{"total":2,"page_size":30}}}`)
			},
			want: []string{"https://www.bilibili.com/video/BV1row0001", "https://www.bilibili.com/video/BV1row0002"},
		},
		{
			name: "series list", rawURL: "https://space.bilibili.com/1/lists/2?type=series", extractor: NewBilibiliSeriesList(),
			configure: func(transport *bilibiliFixtureTransport) {
				transport.api["https://api.bilibili.com/x/series/series?series_id=2"] = []byte(`{"code":0,"data":{"meta":{"name":"Series"}}}`)
				transport.api["https://api.bilibili.com/x/series/archives?mid=1&series_id=2&pn=1&ps=30"] = []byte(`{"code":0,"data":{"archives":[` + row("BV1row0001") + `],"page":{"total":2,"size":30}}}`)
				transport.api["https://api.bilibili.com/x/series/archives?mid=1&series_id=2&pn=2&ps=30"] = []byte(`{"code":0,"data":{"archives":[` + row("BV1row0002") + `],"page":{"total":2,"size":30}}}`)
			},
			want: []string{"https://www.bilibili.com/video/BV1row0001", "https://www.bilibili.com/video/BV1row0002"},
		},
		{
			name: "category", rawURL: "https://www.bilibili.com/v/kichiku/mad", extractor: NewBilibiliCategory(),
			configure: func(transport *bilibiliFixtureTransport) {
				transport.api["https://api.bilibili.com/x/web-interface/newlist?rid=26&type=1&ps=20&jsonp=jsonp&Search_key=kichiku%3A+mad&pn=1"] = []byte(`{"code":0,"data":{"page":{"count":2,"size":1},"archives":[` + row("BV1row0001") + `]}}`)
				transport.api["https://api.bilibili.com/x/web-interface/newlist?rid=26&type=1&ps=20&jsonp=jsonp&Search_key=kichiku%3A+mad&pn=2"] = []byte(`{"code":0,"data":{"page":{"count":2,"size":1},"archives":[` + row("BV1row0002") + `]}}`)
			},
			want: []string{"https://www.bilibili.com/video/BV1row0001", "https://www.bilibili.com/video/BV1row0002"},
		},
		{
			name: "audio album", rawURL: "https://www.bilibili.com/audio/am1", extractor: NewBilibiliAudioAlbum(),
			configure: func(transport *bilibiliFixtureTransport) {
				transport.api["https://www.bilibili.com/audio/music-service-c/web/song/of-menu?sid=1&pn=1&ps=100"] = []byte(`{"code":0,"data":{"data":[{"id":1,"title":"One"},{"id":2,"title":"Two"}]}}`)
			},
			want: []string{"https://www.bilibili.com/audio/au1", "https://www.bilibili.com/audio/au2"},
		},
		{
			name: "international series", rawURL: "https://www.bilibili.tv/en/play/1", extractor: NewBiliIntlSeries(),
			configure: func(transport *bilibiliFixtureTransport) {
				transport.api["https://api.bilibili.tv/intl/gateway/web/v2/ogv/play/season_info?season_id=1&platform=web"] = []byte(`{"code":0,"data":{"season":{"title":"Series"}}}`)
				transport.api["https://api.bilibili.tv/intl/gateway/web/v2/ogv/play/episodes?season_id=1&platform=web"] = []byte(`{"code":0,"data":{"sections":[{"episodes":[{"episode_id":2,"title":"One"},{"episode_id":3,"title":"Two"}]}]}}`)
			},
			want: []string{"https://www.bilibili.tv/en/play/1/2", "https://www.bilibili.tv/en/play/1/3"},
		},
	}
	for _, test := range playlistCases {
		t.Run(test.name, func(t *testing.T) {
			transport := &bilibiliFixtureTransport{pages: make(map[string][]byte), api: make(map[string][]byte)}
			test.configure(transport)
			result, err := test.extractor.Extract(context.Background(), Request{URL: test.rawURL, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			assertBilibiliReusableEntries(t, result.Entries, test.want)
		})
	}
}

func TestBilibiliPlaylistSequenceRejectsRepeatedPagesAndCancels(t *testing.T) {
	repeated := bilibiliPageSequence{fetch: func(_ context.Context, page int) ([]Entry, int, bool, error) {
		id := "BV1repeat01"
		if page > 1 {
			id = "BV1repeat02"
		}
		return []Entry{{URL: "https://www.bilibili.com/video/BV1repeat01", ID: id}}, 0, true, nil
	}}
	iterator := repeated.Iterator()
	if _, ok, err := iterator.Next(context.Background()); err != nil || !ok {
		t.Fatalf("first repeated-page entry ok=%t err=%v", ok, err)
	}
	if _, _, err := iterator.Next(context.Background()); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("repeated page error=%v", err)
	}

	started := make(chan struct{})
	cancelable := bilibiliPageSequence{fetch: func(ctx context.Context, _ int) ([]Entry, int, bool, error) {
		close(started)
		<-ctx.Done()
		return nil, 0, false, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := cancelable.Iterator().Next(ctx)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("playlist cancellation error=%v", err)
	}
}

func TestBilibiliPlayerDynamicIntlRoutes(t *testing.T) {
	if result, err := NewBilibiliPlayer().Extract(context.Background(), Request{URL: "https://player.bilibili.com/player.html?aid=1&cid=2"}); err != nil || !result.IsURL() || !result.Redirect.Transparent {
		t.Fatalf("player=%#v err=%v", result, err)
	}
	dynamicURL := "https://t.bilibili.com/1"
	dynamicAPI := "https://api.bilibili.com/x/polymer/web-dynamic/v1/detail?id=1"
	transport := &bilibiliFixtureTransport{api: make(map[string][]byte)}
	validChildren := []struct {
		name string
		item string
		want string
	}{
		{name: "major archive", item: `{"modules":{"module_dynamic":{"major":{"archive":{"jump_url":"https://www.bilibili.com/video/BV1abcdef01"}}}}}`, want: "https://www.bilibili.com/video/BV1abcdef01"},
		{name: "major pgc", item: `{"modules":{"module_dynamic":{"major":{"pgc":{"jump_url":"https://www.bilibili.com/video/av1"}}}}}`, want: "https://www.bilibili.com/video/av1"},
		{name: "additional reserve", item: `{"modules":{"module_dynamic":{"additional":{"reserve":{"jump_url":"https://www.bilibili.com/video/BV1abcdef01"}}}}}`, want: "https://www.bilibili.com/video/BV1abcdef01"},
		{name: "additional common", item: `{"modules":{"module_dynamic":{"additional":{"common":{"jump_url":"https://www.bilibili.com/video/BV1abcdef01"}}}}}`, want: "https://www.bilibili.com/video/BV1abcdef01"},
		{name: "original item", item: `{"orig":{"modules":{"module_dynamic":{"major":{"archive":{"jump_url":"https://www.bilibili.com/video/BV1abcdef01"}}}}}}`, want: "https://www.bilibili.com/video/BV1abcdef01"},
		{name: "proto-relative", item: `{"modules":{"module_dynamic":{"major":{"archive":{"jump_url":"//www.bilibili.com/video/BV1abcdef01"}}}}}`, want: "https://www.bilibili.com/video/BV1abcdef01"},
	}
	for _, test := range validChildren {
		t.Run(test.name, func(t *testing.T) {
			transport.api[dynamicAPI] = []byte(`{"code":0,"data":{"item":` + test.item + `}}`)
			result, err := NewBilibiliDynamic().Extract(context.Background(), Request{URL: dynamicURL, Transport: transport})
			if err != nil || !result.IsURL() || !result.Redirect.Transparent || result.Redirect.URL != test.want {
				t.Fatalf("dynamic=%#v err=%v", result, err)
			}
		})
	}
	for _, child := range []string{
		"https://t.bilibili.com/1",
		"https://www.bilibili.com/opus/1",
		"https://www.bilibili.com/bangumi/play/ep1",
	} {
		t.Run("reject "+child, func(t *testing.T) {
			transport.api[dynamicAPI] = []byte(`{"code":0,"data":{"item":{"modules":{"module_dynamic":{"major":{"archive":{"jump_url":"` + child + `"}}}}}}}`)
			if _, err := NewBilibiliDynamic().Extract(context.Background(), Request{URL: dynamicURL, Transport: transport}); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("dynamic child %q error=%v", child, err)
			}
		})
	}

	result, err := NewBilibiliDynamic().Extract(context.Background(), Request{URL: dynamicURL, Transport: transport})
	if err == nil || result.IsURL() {
		t.Fatalf("dynamic rejection=%#v err=%v", result, err)
	}

	intlURL := "https://www.bilibili.tv/en/play/1/2"
	intlAPI := "https://api.bilibili.tv/intl/gateway/web/playurl?platform=web&ep_id=2"
	transport.pages = map[string][]byte{intlURL: []byte(`<script>window.__INITIAL_STATE__={"OgvVideo":{"epDetail":{"title_display":"E2 - Intl","desc":"description","cover":"https://v.bstarstatic.com/cover.jpg","formatted_pub_date":"2022-11-08 17:42:04"}}};</script>`)}
	transport.api[intlAPI] = []byte(`{"code":0,"data":{"playurl":{"video":[{"video_resource":{"url":"https://v.bstarstatic.com/video.mp4?sig=video","width":640,"height":360,"bandwidth":400000,"codecs":"avc1","size":17},"stream_info":{"desc_words":"360P"}}],"audio_resource":[{"url":"https://v.bstarstatic.com/audio.m4a?sig=audio","bandwidth":1000,"codecs":"mp4a","size":4}]}}}`)
	result, err = NewBiliIntl().Extract(context.Background(), Request{URL: intlURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if formats, _ := result.Info.Formats(); len(formats) != 2 {
		t.Fatalf("intl formats=%d", len(formats))
	}
	ugcURL := "https://www.bilibili.tv/en/video/2045730385"
	ugcAPI := "https://api.bilibili.tv/intl/gateway/web/playurl?platform=web&aid=2045730385"
	transport.pages[ugcURL] = []byte(`<script>window.__INITIAL_DATA__={"UgcVideo":{"videoData":{"title":"UGC","desc":"ugc description","cover":"https://v.bstarstatic.com/ugc.jpg"}}};</script>`)
	transport.api[ugcAPI] = []byte(`{"code":0,"data":{"playurl":{"video":[{"video_resource":{"url":"https://v.bstarstatic.com/ugc.mp4?sig=ugc","width":640,"height":360,"bandwidth":400000,"codecs":"avc1","size":3}}]}}}`)
	if result, err = NewBiliIntl().Extract(context.Background(), Request{URL: ugcURL, Transport: transport}); err != nil {
		t.Fatalf("ugc: %v", err)
	} else if id, _ := result.Info.ID(); id != "2045730385" {
		t.Fatalf("ugc id=%q", id)
	}

	seriesURL := "https://www.bilibili.tv/en/play/1"
	seasonAPI := "https://api.bilibili.tv/intl/gateway/web/v2/ogv/play/season_info?season_id=1&platform=web"
	episodesAPI := "https://api.bilibili.tv/intl/gateway/web/v2/ogv/play/episodes?season_id=1&platform=web"
	transport.api[seasonAPI] = []byte(`{"code":0,"data":{"season":{"title":"Series","horizontal_cover":"https://v.bstarstatic.com/cover.jpg"}}}`)
	transport.api[episodesAPI] = []byte(`{"code":0,"data":{"sections":[{"episodes":[{"episode_id":2,"title":"Episode"}]}]}}`)
	result, err = NewBiliIntlSeries().Extract(context.Background(), Request{URL: seriesURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := CollectEntries(context.Background(), result.Entries, 2); err != nil || len(entries) != 1 {
		t.Fatalf("intl series=%d err=%v", len(entries), err)
	}
}

func TestBilibiliCancellationAndFuzzSeed(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, err := NewBiliIntl().Extract(ctx, Request{URL: "https://www.bilibili.tv/en/play/1/2", Transport: &bilibiliFixtureTransport{}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline=%v", err)
	}
}

func FuzzParseBilibiliPage(f *testing.F) {
	f.Add(readPublicFixture(f, "bilibili", "success.html"))
	f.Add([]byte(`<script>window.__INITIAL_STATE__={};</script>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = parseBilibiliPage(data, "BV1fixture01", 0, "https://www.bilibili.com/video/BV1fixture01")
	})
}

func mustParseBilibiliURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
