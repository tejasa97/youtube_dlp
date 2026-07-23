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
	mu                 sync.Mutex
	reads, requests    int
	err                error
	lastRequest        *http.Request
	lastBody           string
}

func (t *handleFixtureTransport) ReadPage(_ context.Context, rawURL string) ([]byte, http.Header, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reads++
	if rawURL != handleFixtureURL {
		return nil, nil, errors.New("unexpected page")
	}
	return t.page, nil, t.err
}

func (t *handleFixtureTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.requests++
	if request.URL.Path != "/youtubei/v1/browse" {
		return nil, errors.New("unexpected continuation endpoint")
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	t.lastRequest = request.Clone(request.Context())
	t.lastBody = string(body)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(t.continuation)))}, nil
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

func TestYouTubeHandleTabFallbackIDAndRoutingPolicy(t *testing.T) {
	page, err := parseYouTubeHandleTabData([]byte(`{"metadata":{"channelMetadataRenderer":{"title":"No ID"}}}`))
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
	valid := []string{handleFixtureURL, "https://youtube.com/@Foo.Bar_1/shorts?view=0", "http://www.youtube.com/@abc/streams"}
	for _, raw := range valid {
		request, _ := http.NewRequest(http.MethodGet, raw, nil)
		if !NewYouTubeHandleTab().Suitable(request.URL) {
			t.Errorf("not suitable: %s", raw)
		}
	}
	invalid := []string{
		"https://evil-youtube.com/@synthetic-handle/videos", "https://m.youtube.com/@synthetic-handle/videos", "https://www.youtube.com:443/@synthetic-handle/videos", "https://user@www.youtube.com/@synthetic-handle/videos", "ftp://www.youtube.com/@synthetic-handle/videos", "https://www.youtube.com/@ab/videos", "https://www.youtube.com/@___/videos", "https://www.youtube.com/@synthetic-handle", "https://www.youtube.com/@synthetic-handle/playlists", "https://www.youtube.com/@synthetic-handle/videos/", "https://www.youtube.com/%40synthetic-handle/videos", "https://www.youtube.com/@synthetic-handle/videos#tab", "https://www.youtube.com/@synthetic-handle/videos?x=%00",
	}
	for _, raw := range invalid {
		request, _ := http.NewRequest(http.MethodGet, raw, nil)
		if NewYouTubeHandleTab().Suitable(request.URL) {
			t.Errorf("accepted hostile/unsupported URL: %s", raw)
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
	if _, err := parseYouTubeHandleTabData([]byte(`[]`)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed root = %v", err)
	}
	private, err := parseYouTubeHandleTabData([]byte(`{
		"alerts":[{"alertRenderer":{"text":{"simpleText":"This channel is private"}}}]
	}`))
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
	if _, _, err := fetchYouTubeHandleTabContinuation(context.Background(), &handleFixtureTransport{}, "bad\nvalue", youtubePlaylistConfig{}); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("bad token = %v", err)
	}
}

func FuzzParseYouTubeHandleTabData(f *testing.F) {
	f.Add([]byte(`{"metadata":{"channelMetadataRenderer":{"title":"x"}}}`))
	f.Add([]byte(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[]}}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		page, err := parseYouTubeHandleTabData(data)
		if err != nil {
			return
		}
		for _, entry := range page.entries {
			if !youtubeIDPattern.MatchString(entry.ID) || entry.ExtractorKey != "youtube" {
				t.Fatalf("unsafe entry: %#v", entry)
			}
			watchURL := "https://www.youtube.com/watch?v=" + entry.ID
			shortURL := "https://www.youtube.com/shorts/" + entry.ID
			if entry.URL != watchURL && entry.URL != shortURL {
				t.Fatalf("unsafe entry URL: %#v", entry)
			}
		}
		if page.continuation != "" && validYouTubeContinuationToken(page.continuation) != page.continuation {
			t.Fatalf("unsafe continuation: %q", page.continuation)
		}
	})
}

func FuzzYouTubeHandleTabTarget(f *testing.F) {
	f.Add(handleFixtureURL)
	f.Add("https://youtube.com/@Foo.Bar_1/shorts")
	f.Add("https://www.youtube.com/%40synthetic-handle/videos")
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
		if !youtubeHandlePattern.MatchString(handle) || !youtubeHandleHasAlnumPattern.MatchString(handle) || (tab != "videos" && tab != "shorts" && tab != "streams") {
			t.Fatalf("accepted invalid target %q: %q/%q", rawURL, handle, tab)
		}
	})
}
