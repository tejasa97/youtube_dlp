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

const channelFixtureURL = "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos"

type channelFixtureTransport struct {
	page, continuation []byte
	mu                 sync.Mutex
	reads, requests    int
	err                error
	lastRequest        *http.Request
	lastBody           string
}

func (t *channelFixtureTransport) ReadPage(_ context.Context, rawURL string) ([]byte, http.Header, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reads++
	if rawURL != channelFixtureURL {
		return nil, nil, errors.New("unexpected page")
	}
	return t.page, nil, t.err
}
func (t *channelFixtureTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
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

func readChannelFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/youtube_channel/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestYouTubeChannelTabLazyReusableContinuation(t *testing.T) {
	transport := &channelFixtureTransport{page: readChannelFixture(t, "channel-videos.html"), continuation: readChannelFixture(t, "channel-continuation.json")}
	result, err := NewYouTubeChannelTab().Extract(context.Background(), Request{URL: channelFixtureURL, Transport: transport})
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("result=%v err=%v", result.IsPlaylist(), err)
	}
	if transport.reads != 1 || transport.requests != 0 {
		t.Fatalf("initial requests = %d/%d", transport.reads, transport.requests)
	}
	if id, _ := result.Info.ID(); id != "UCabcdefghijklmnopqrstuv" {
		t.Fatalf("playlist id = %q", id)
	}
	if title, _ := result.Info.Title(); title != "Synthetic Channel" {
		t.Fatalf("playlist title = %q", title)
	}
	if webpage, _ := result.Info.WebpageURL(); webpage != channelFixtureURL {
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
			got = append(got, entry.ID+":"+entry.Title)
		}
		want := []string{"abcdefghijk:First channel video", "lmnopqrstuv:Second channel video", "wxyzABCDE12:Third channel video"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("entries = %#v", got)
		}
	}
	if transport.requests != 2 {
		t.Fatalf("continuation requests = %d", transport.requests)
	}
	if transport.lastRequest.URL.Query().Get("key") != "fixture-key" ||
		transport.lastRequest.Header.Get("X-Youtube-Client-Version") != "2.fixture" ||
		!strings.Contains(transport.lastBody, `"continuation":"next-channel-page"`) ||
		!strings.Contains(transport.lastBody, `"visitorData":"fixture-visitor"`) {
		t.Fatalf("continuation request = %s headers=%v body=%s", transport.lastRequest.URL, transport.lastRequest.Header, transport.lastBody)
	}
}

func TestYouTubeChannelTabShortsEntry(t *testing.T) {
	page, err := parseYouTubeChannelTabData([]byte(`{
		"metadata":{"channelMetadataRenderer":{"title":"Shorts"}},
		"videoRenderer":{
			"videoId":"abcdefghijk",
			"title":{"simpleText":"A short"},
			"navigationEndpoint":{"reelWatchEndpoint":{"videoId":"abcdefghijk"}}
		}
	}`))
	if err != nil || len(page.entries) != 1 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if got := page.entries[0].URL; got != "https://www.youtube.com/shorts/abcdefghijk" {
		t.Fatalf("short URL = %q", got)
	}
}

func TestYouTubeChannelTabRoutingPolicy(t *testing.T) {
	valid := []string{channelFixtureURL, "https://youtube.com/channel/UCabcdefghijklmnopqrstuv/shorts", "http://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/streams"}
	for _, raw := range valid {
		parsed, _ := http.NewRequest(http.MethodGet, raw, nil)
		if !NewYouTubeChannelTab().Suitable(parsed.URL) {
			t.Errorf("not suitable: %s", raw)
		}
	}
	invalid := []string{
		"https://evil-youtube.com/channel/UCabcdefghijklmnopqrstuv/videos", "https://m.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos", "https://www.youtube.com:443/channel/UCabcdefghijklmnopqrstuv/videos", "https://user@www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos", "ftp://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv", "https://www.youtube.com/channel/UCshort/videos", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/playlists", "https://www.youtube.com/channel%2fUCabcdefghijklmnopqrstuv/videos", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos?x=%00",
	}
	for _, raw := range invalid {
		request, _ := http.NewRequest(http.MethodGet, raw, nil)
		if NewYouTubeChannelTab().Suitable(request.URL) {
			t.Errorf("accepted hostile/unsupported URL: %s", raw)
		}
	}
}

func TestYouTubeChannelTabFailuresAndCancellation(t *testing.T) {
	for _, test := range []struct {
		code int
		want error
	}{{http.StatusUnauthorized, ErrAuthentication}, {http.StatusNotFound, ErrUnavailable}, {http.StatusTooManyRequests, ErrYouTubeChannelRateLimited}} {
		got := categorizeYouTubeChannelError(&HTTPStatusError{Code: test.code})
		if !errors.Is(got, test.want) {
			t.Errorf("%d: %v", test.code, got)
		}
	}
	if !errors.Is(categorizeYouTubeChannelError(errors.New("dial failed")), ErrYouTubeChannelNetwork) {
		t.Fatal("network failure was not categorized")
	}
	private, err := parseYouTubeChannelTabData([]byte(`{
		"alerts":[{"alertRenderer":{"text":{"simpleText":"This channel is private"}}}]
	}`))
	if err != nil || !errors.Is(youtubePlaylistAlertError(private.alert), ErrAuthentication) {
		t.Fatalf("private alert=%q err=%v", private.alert, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewYouTubeChannelTab().Extract(ctx, Request{URL: channelFixtureURL, Transport: &channelFixtureTransport{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestYouTubeChannelTabContinuationRejectsBadToken(t *testing.T) {
	_, _, err := fetchYouTubeChannelContinuation(context.Background(), &channelFixtureTransport{}, "bad\nvalue", youtubePlaylistConfig{})
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("err = %v", err)
	}
}

func FuzzParseYouTubeChannelTabData(f *testing.F) {
	f.Add([]byte(`{"metadata":{"channelMetadataRenderer":{"title":"x"}}}`))
	f.Add([]byte(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[]}}]}`))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = parseYouTubeChannelTabData(data) })
}

func FuzzYouTubeChannelTabTarget(f *testing.F) {
	f.Add(channelFixtureURL)
	f.Add("https://youtube.com/channel/UCabcdefghijklmnopqrstuv/shorts")
	f.Add("https://www.youtube.com/channel%2fUCabcdefghijklmnopqrstuv/videos")
	f.Add("https://evil-youtube.com/channel/UCabcdefghijklmnopqrstuv/streams")
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 4096 {
			t.Skip()
		}
		parsed, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			return
		}
		channelID, tab, ok := youtubeChannelTabTarget(parsed.URL)
		if !ok {
			return
		}
		if !youtubeChannelIDPattern.MatchString(channelID) ||
			(tab != "videos" && tab != "shorts" && tab != "streams") {
			t.Fatalf("accepted invalid target %q: %q/%q", rawURL, channelID, tab)
		}
	})
}
