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
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewYouTubeChannelTab().Extract(ctx, Request{URL: channelFixtureURL, Transport: &channelFixtureTransport{}})
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
