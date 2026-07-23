package extractor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
)

const handleFixtureURL = "https://www.youtube.com/@synthetic-handle/videos"

type handleFixtureTransport struct {
	page, continuation []byte
	pageURL            string
	mu                 sync.Mutex
	reads, requests    int
	err                error
	status             int
	lastRequest        *http.Request
	lastBody           string
}

func (t *handleFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reads++
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	expectedURL := t.pageURL
	if expectedURL == "" {
		expectedURL = handleFixtureURL
	}
	if rawURL != expectedURL {
		return nil, nil, errors.New("unexpected page")
	}
	return t.page, nil, t.err
}

func (t *handleFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.requests++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.URL.Path != "/youtubei/v1/browse" {
		return nil, errors.New("unexpected continuation endpoint")
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	t.lastRequest = request.Clone(request.Context())
	t.lastBody = string(body)
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(t.continuation)))}, nil
}

func readHandleFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/youtube_handle_tab/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestYouTubeHandleTabLazyReusableContinuation(t *testing.T) {
	transport := &handleFixtureTransport{page: readHandleFixture(t, "handle-videos.html"), continuation: readHandleFixture(t, "handle-continuation.json")}
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: handleFixtureURL, Transport: transport})
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("result=%v err=%v", result.IsPlaylist(), err)
	}
	if transport.reads != 1 || transport.requests != 0 {
		t.Fatalf("initial requests = %d/%d", transport.reads, transport.requests)
	}
	if id, _ := result.Info.ID(); id != "UCabcdefghijklmnopqrstuv" {
		t.Fatalf("playlist id = %q", id)
	}
	if title, _ := result.Info.Title(); title != "Synthetic Handle" {
		t.Fatalf("playlist title = %q", title)
	}
	if webpage, _ := result.Info.WebpageURL(); webpage != handleFixtureURL {
		t.Fatalf("playlist webpage_url = %q", webpage)
	}
	for run := 0; run < 2; run++ {
		iterator := result.Entries.Iterator()
		var got []string
		for {
			entry, ok, err := iterator.Next(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			got = append(got, entry.ID+":"+entry.URL+":"+entry.Title)
		}
		want := []string{"abcdefghijk:https://www.youtube.com/watch?v=abcdefghijk:First handle video", "lmnopqrstuv:https://www.youtube.com/shorts/lmnopqrstuv:Handle short", "wxyzABCDE12:https://www.youtube.com/watch?v=wxyzABCDE12:Third handle video"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("entries = %#v", got)
		}
	}
	if transport.requests != 2 {
		t.Fatalf("continuation requests = %d", transport.requests)
	}
	if transport.lastRequest.URL.Query().Get("key") != "fixture-key" || transport.lastRequest.Header.Get("X-Youtube-Client-Version") != "2.fixture" || !strings.Contains(transport.lastBody, `"continuation":"next-handle-page"`) || !strings.Contains(transport.lastBody, `"visitorData":"fixture-visitor"`) {
		t.Fatalf("continuation request = %s headers=%v body=%s", transport.lastRequest.URL, transport.lastRequest.Header, transport.lastBody)
	}
}

func TestYouTubeHandlePlaylistsTabLegacyModernContinuationAndOccurrences(t *testing.T) {
	const rawURL = "https://www.youtube.com/@synthetic-handle/playlists"
	transport := &handleFixtureTransport{
		pageURL: rawURL, page: readHandleFixture(t, "handle-playlists.html"),
		continuation: readHandleFixture(t, "handle-playlists-continuation.json"),
	}
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("result=%v err=%v", result.IsPlaylist(), err)
	}
	if id, _ := result.Info.ID(); id != "UCabcdefghijklmnopqrstuv" {
		t.Fatalf("id = %q", id)
	}
	if title, _ := result.Info.Title(); title != "Synthetic Handle Playlists" {
		t.Fatalf("title = %q", title)
	}
	want := []string{
		"PLhandleLegacy001:Legacy handle playlist",
		"PLhandleModern002:Modern handle playlist",
		"PLhandleLegacy003:Continued handle playlist",
		"PLhandlePodcast004:Handle podcast",
		"PLhandleLegacy001:Repeated handle playlist",
	}
	for run := 0; run < 2; run++ {
		var got []string
		iterator := result.Entries.Iterator()
		for {
			entry, ok, nextErr := iterator.Next(context.Background())
			if nextErr != nil {
				t.Fatal(nextErr)
			}
			if !ok {
				break
			}
			if entry.Transparent || entry.ExtractorKey != "youtube" ||
				entry.URL != "https://www.youtube.com/playlist?list="+entry.ID {
				t.Fatalf("unsafe playlist entry: %#v", entry)
			}
			got = append(got, entry.ID+":"+entry.Title)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("entries = %#v", got)
		}
	}
	if transport.requests != 2 {
		t.Fatalf("continuation requests = %d", transport.requests)
	}
	if !strings.Contains(transport.lastBody, `"continuation":"next-handle-playlists"`) {
		t.Fatalf("continuation body = %s", transport.lastBody)
	}
}

func TestYouTubeHandlePlaylistsTabRejectsHostileRenderersAndCategorizesFailures(t *testing.T) {
	page, err := parseYouTubeHandleTabData([]byte(`{
		"metadata":{"channelMetadataRenderer":{"title":"Playlists"}},
		"playlistRenderer":{"playlistId":"bad%2fid","title":{"simpleText":"bad"}},
		"gridPlaylistRenderer":{"playlistId":"PLsafe_handle","title":{"simpleText":"safe"}},
		"lockupViewModel":{"contentId":"abcdefghijk","contentType":"LOCKUP_CONTENT_TYPE_VIDEO"}
	}`), "playlists")
	if err != nil || len(page.entries) != 1 || page.entries[0].ID != "PLsafe_handle" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if got := categorizeYouTubeHandleTabError(ErrInvalidMetadata); !errors.Is(got, ErrInvalidMetadata) ||
		errors.Is(got, ErrYouTubeHandleTabNetwork) {
		t.Fatalf("metadata category = %v", got)
	}

	const rawURL = "https://www.youtube.com/@synthetic-handle/playlists"
	for _, test := range []struct {
		err  error
		want error
	}{
		{&HTTPStatusError{Code: http.StatusUnauthorized}, ErrAuthentication},
		{&HTTPStatusError{Code: http.StatusNotFound}, ErrUnavailable},
		{&HTTPStatusError{Code: http.StatusTooManyRequests}, ErrYouTubeHandleTabRateLimited},
	} {
		_, extractErr := NewYouTubeHandleTab().Extract(context.Background(), Request{
			URL: rawURL, Transport: &handleFixtureTransport{pageURL: rawURL, err: test.err},
		})
		if !errors.Is(extractErr, test.want) {
			t.Fatalf("error %v: %v", test.err, extractErr)
		}
	}

	transport := &handleFixtureTransport{
		pageURL: rawURL, page: readHandleFixture(t, "handle-playlists.html"),
		status: http.StatusTooManyRequests,
	}
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	iterator := result.Entries.Iterator()
	for index := 0; index < 2; index++ {
		if _, ok, nextErr := iterator.Next(context.Background()); nextErr != nil || !ok {
			t.Fatalf("initial entry %d: ok=%v err=%v", index, ok, nextErr)
		}
	}
	if _, _, nextErr := iterator.Next(context.Background()); !errors.Is(nextErr, ErrYouTubeHandleTabRateLimited) {
		t.Fatalf("continuation rate limit = %v", nextErr)
	}
	malformed := &handleFixtureTransport{
		pageURL: rawURL, page: readHandleFixture(t, "handle-playlists.html"),
		continuation: []byte(`{`),
	}
	malformedResult, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: rawURL, Transport: malformed})
	if err != nil {
		t.Fatal(err)
	}
	malformedIterator := malformedResult.Entries.Iterator()
	for index := 0; index < 2; index++ {
		_, _, _ = malformedIterator.Next(context.Background())
	}
	if _, _, nextErr := malformedIterator.Next(context.Background()); !errors.Is(nextErr, ErrInvalidMetadata) {
		t.Fatalf("malformed continuation = %v", nextErr)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, nextErr := result.Entries.Iterator().Next(cancelled); !errors.Is(nextErr, context.Canceled) {
		t.Fatalf("continuation cancellation = %v", nextErr)
	}

	blocking := &blockingTabContinuationTransport{
		pageURL: rawURL, page: readHandleFixture(t, "handle-playlists.html"),
		started: make(chan struct{}),
	}
	blockingResult, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: rawURL, Transport: blocking})
	if err != nil {
		t.Fatal(err)
	}
	blockingIterator := blockingResult.Entries.Iterator()
	for index := 0; index < 2; index++ {
		_, _, _ = blockingIterator.Next(context.Background())
	}
	inFlight, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, nextErr := blockingIterator.Next(inFlight)
		done <- nextErr
	}()
	<-blocking.started
	stop()
	if nextErr := <-done; !errors.Is(nextErr, context.Canceled) {
		t.Fatalf("in-flight cancellation = %v", nextErr)
	}
}

func TestYouTubeHandleTabScopesFirstContinuationActionAndRefreshesVisitorData(t *testing.T) {
	page, err := parseYouTubeHandleTabData([]byte(`{
		"responseContext":{"visitorData":"rotated-handle"},
		"onResponseReceivedEndpoints":[
			{"appendContinuationItemsAction":{"continuationItems":[{"playlistRenderer":{"playlistId":"PLfirst","title":{"simpleText":"first"}}}]}},
			{"appendContinuationItemsAction":{"continuationItems":[{"playlistRenderer":{"playlistId":"PLdecoy","title":{"simpleText":"decoy"}}}]}}
		],
		"continuationContents":{"sectionListContinuation":{"continuations":[{"nextContinuationData":{"continuation":"handle-root-token"}}]}},
		"unrelated":{"playlistRenderer":{"playlistId":"PLother","title":{"simpleText":"other"}}}
	}`), "playlists")
	if err != nil || len(page.entries) != 1 || page.entries[0].ID != "PLfirst" ||
		page.visitorData != "rotated-handle" || page.continuation != "handle-root-token" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
}

func TestYouTubeHandleTabFallbackIDAndRoutingPolicy(t *testing.T) {
	page, err := parseYouTubeHandleTabData([]byte(`{"metadata":{"channelMetadataRenderer":{"title":"No ID"}}}`), "videos")
	if err != nil || page.channelID != "" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	transport := &handleFixtureTransport{page: []byte(`<script>ytInitialData={"metadata":{"channelMetadataRenderer":{"title":"No ID"}}};</script>`)}
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: handleFixtureURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := result.Info.ID(); id != "handle:@synthetic-handle" {
		t.Fatalf("fallback playlist id = %q", id)
	}
	valid := []string{
		handleFixtureURL,
		"https://youtube.com/@Foo.Bar_1/shorts?view=0",
		"http://www.youtube.com/@abc/streams",
		"https://www.youtube.com/@synthetic-handle/playlists",
		"https://www.youtube.com/@日本語/videos",
		"https://www.youtube.com/@één/videos",
		"https://www.youtube.com/@１２３/videos",
		"https://www.youtube.com/@___/videos",
		"https://www.youtube.com/%40%E6%97%A5%E6%9C%AC%E8%AA%9E/videos",
	}
	for _, raw := range valid {
		request, _ := http.NewRequest(http.MethodGet, raw, nil)
		if !NewYouTubeHandleTab().Suitable(request.URL) {
			t.Errorf("not suitable: %s", raw)
		}
	}
	invalid := []string{
		"https://evil-youtube.com/@synthetic-handle/videos", "https://m.youtube.com/@synthetic-handle/videos", "https://www.youtube.com:443/@synthetic-handle/videos", "https://user@www.youtube.com/@synthetic-handle/videos", "ftp://www.youtube.com/@synthetic-handle/videos", "https://www.youtube.com/@ab/videos", "https://www.youtube.com/@synthetic-handle", "https://www.youtube.com/@synthetic-handle/community", "https://www.youtube.com/@synthetic-handle/playlists/", "https://www.youtube.com/@ab💥/videos", "https://www.youtube.com/%40ab%F0%9F%92%A5/videos", "https://www.youtube.com/@हिन्दी/videos", "https://www.youtube.com/@a\u0301b/videos", "https://www.youtube.com/%40ab%FF/videos", "https://www.youtube.com/@synthetic%2fhandle/videos", "https://www.youtube.com/@synthetic%5chandle/videos", "https://www.youtube.com/@synthetic-handle/videos#tab", "https://www.youtube.com/@synthetic-handle/videos?x=%00",
	}
	for _, raw := range invalid {
		request, _ := http.NewRequest(http.MethodGet, raw, nil)
		if NewYouTubeHandleTab().Suitable(request.URL) {
			t.Errorf("accepted hostile/unsupported URL: %s", raw)
		}
	}
}

func TestYouTubeHandleTabCanonicalizesDecodedUnicodeWithoutCaseFolding(t *testing.T) {
	const rawURL = "https://www.youtube.com/%40%E6%97%A5%E6%9C%ACAbC/videos"
	const canonical = "https://www.youtube.com/@日本AbC/videos"
	transport := &handleFixtureTransport{
		pageURL: canonical,
		page:    []byte(`<script>ytInitialData={"metadata":{"channelMetadataRenderer":{"title":"Unicode Handle"}}};</script>`),
	}
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if webpage, _ := result.Info.WebpageURL(); webpage != canonical {
		t.Fatalf("webpage URL = %q", webpage)
	}
	if id, _ := result.Info.ID(); id != "handle:@日本AbC" {
		t.Fatalf("fallback ID = %q", id)
	}
}

func TestValidYouTubeHandleUsesUnicodeCodePointBounds(t *testing.T) {
	for _, handle := range []string{"@日本語", "@１２３", "@___", "@---", "@...", "@" + strings.Repeat("界", 30)} {
		if !validYouTubeHandle(handle) {
			t.Errorf("valid handle rejected: %q", handle)
		}
	}
	for _, handle := range []string{
		"日本語", "@ab", "@" + strings.Repeat("界", 31), "@ab💥", "@a\u0301b",
		string([]byte{'@', 'a', 'b', 0xff}),
	} {
		if validYouTubeHandle(handle) {
			t.Errorf("invalid handle accepted: %q", handle)
		}
	}
}

func TestYouTubeHandleTabFailuresMalformedCancellationAndLoop(t *testing.T) {
	for _, test := range []struct {
		code int
		want error
	}{{http.StatusUnauthorized, ErrAuthentication}, {http.StatusNotFound, ErrUnavailable}, {http.StatusTooManyRequests, ErrYouTubeHandleTabRateLimited}} {
		if got := categorizeYouTubeHandleTabError(&HTTPStatusError{Code: test.code}); !errors.Is(got, test.want) {
			t.Errorf("%d: %v", test.code, got)
		}
	}
	if !errors.Is(categorizeYouTubeHandleTabError(errors.New("dial failed")), ErrYouTubeHandleTabNetwork) {
		t.Fatal("network failure was not categorized")
	}
	if _, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: handleFixtureURL, Transport: &handleFixtureTransport{err: errors.New("dial failed")}}); !errors.Is(err, ErrYouTubeHandleTabNetwork) {
		t.Fatalf("initial network classification = %v", err)
	}
	if _, err := parseYouTubeHandleTabData([]byte(`[]`), "videos"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed root = %v", err)
	}
	private, err := parseYouTubeHandleTabData([]byte(`{
		"alerts":[{"alertRenderer":{"text":{"simpleText":"This channel is private"}}}]
	}`), "playlists")
	if err != nil || !errors.Is(youtubeHandleTabAlertError(private.alert), ErrAuthentication) {
		t.Fatalf("private alert=%q err=%v", private.alert, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewYouTubeHandleTab().Extract(ctx, Request{URL: handleFixtureURL, Transport: &handleFixtureTransport{}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	sequence, err := ContinuationEntries(nil, "same", func(_ context.Context, _ string) ([]Entry, string, error) { return []Entry{{ID: "one"}}, "same", nil })
	if err != nil {
		t.Fatal(err)
	}
	iterator := sequence.Iterator()
	if _, ok, err := iterator.Next(context.Background()); err != nil || !ok {
		t.Fatalf("loop first = ok:%v err:%v", ok, err)
	}
	if _, ok, err := iterator.Next(context.Background()); err != nil || ok {
		t.Fatalf("loop stop = ok:%v err:%v", ok, err)
	}
	if _, _, _, err := fetchYouTubeHandleTabContinuation(context.Background(), &handleFixtureTransport{}, "bad\nvalue", "visitor", youtubePlaylistConfig{}, "playlists"); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("bad token = %v", err)
	}
}

func FuzzParseYouTubeHandleTabData(f *testing.F) {
	f.Add([]byte(`{"metadata":{"channelMetadataRenderer":{"title":"x"}}}`))
	f.Add([]byte(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[]}}]}`))
	f.Add([]byte(`{"lockupViewModel":{"contentId":"PLfuzz","contentType":"LOCKUP_CONTENT_TYPE_PLAYLIST"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, tab := range []string{"videos", "playlists"} {
			page, err := parseYouTubeHandleTabData(data, tab)
			if err != nil {
				continue
			}
			assertYouTubeTabEntriesSafe(t, page.entries, tab)
			if page.continuation != "" && validYouTubeContinuationToken(page.continuation) != page.continuation {
				t.Fatalf("unsafe continuation: %q", page.continuation)
			}
		}
	})
}

func FuzzYouTubeHandleTabTarget(f *testing.F) {
	f.Add(handleFixtureURL)
	f.Add("https://youtube.com/@Foo.Bar_1/shorts")
	f.Add("https://www.youtube.com/%40%E6%97%A5%E6%9C%AC%E8%AA%9E/videos")
	f.Add("https://evil-youtube.com/@synthetic-handle/streams")
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 4096 {
			t.Skip()
		}
		request, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			return
		}
		handle, tab, ok := youtubeHandleTabTarget(request.URL)
		if !ok {
			return
		}
		if !validYouTubeHandle(handle) || (tab != "videos" && tab != "shorts" && tab != "streams" && tab != "playlists") {
			t.Fatalf("accepted invalid target %q: %q/%q", rawURL, handle, tab)
		}
	})
}
