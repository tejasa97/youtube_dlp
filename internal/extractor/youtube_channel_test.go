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
	pageURL            string
	mu                 sync.Mutex
	reads, requests    int
	err                error
	status             int
	lastRequest        *http.Request
	lastBody           string
}

type blockingTabContinuationTransport struct {
	pageURL string
	page    []byte
	started chan struct{}
	once    sync.Once
}

func (t *blockingTabContinuationTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if rawURL != t.pageURL {
		return nil, nil, errors.New("unexpected page")
	}
	return t.page, nil, nil
}

func (t *blockingTabContinuationTransport) Do(ctx context.Context, _ *http.Request) (*http.Response, error) {
	t.once.Do(func() { close(t.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (t *channelFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reads++
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	expectedURL := t.pageURL
	if expectedURL == "" {
		expectedURL = channelFixtureURL
	}
	if rawURL != expectedURL {
		return nil, nil, errors.New("unexpected page")
	}
	return t.page, nil, t.err
}
func (t *channelFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
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

func TestYouTubeChannelPlaylistsTabLegacyModernContinuationAndOccurrences(t *testing.T) {
	const rawURL = "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/playlists"
	transport := &channelFixtureTransport{
		pageURL: rawURL, page: readChannelFixture(t, "channel-playlists.html"),
		continuation: readChannelFixture(t, "channel-playlists-continuation.json"),
	}
	result, err := NewYouTubeChannelTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("result=%v err=%v", result.IsPlaylist(), err)
	}
	if title, _ := result.Info.Title(); title != "Synthetic Channel Playlists" {
		t.Fatalf("title = %q", title)
	}
	want := []string{
		"PLchannelLegacy001:Legacy channel playlist",
		"PLchannelModern002:Modern channel playlist",
		"PLchannelLegacy003:Continued channel playlist",
		"PLchannelPodcast004:Channel podcast",
		"PLchannelLegacy001:Repeated channel playlist",
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
	if !strings.Contains(transport.lastBody, `"continuation":"next-channel-playlists"`) {
		t.Fatalf("continuation body = %s", transport.lastBody)
	}
}

func TestYouTubeChannelPlaylistsTabRejectsHostileRenderersAndCategorizesFailures(t *testing.T) {
	page, err := parseYouTubeChannelTabData([]byte(`{
		"metadata":{"channelMetadataRenderer":{"title":"Playlists"}},
		"playlistRenderer":{"playlistId":"bad/id","title":{"simpleText":"bad"}},
		"gridPlaylistRenderer":{"playlistId":"PLsafe_123","title":{"simpleText":"safe"}},
		"lockupViewModel":{"contentId":"abcdefghijk","contentType":"LOCKUP_CONTENT_TYPE_VIDEO"}
	}`), "playlists")
	if err != nil || len(page.entries) != 1 || page.entries[0].ID != "PLsafe_123" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	oversized := strings.Repeat("x", youtubeMaxTabEntryTitleBytes+1)
	entry := youtubeTabPlaylistResult("PLsafe_123", oversized)
	if entry.Title != "" {
		t.Fatalf("oversized title retained: %d", len(entry.Title))
	}
	if got := categorizeYouTubeChannelError(ErrInvalidMetadata); !errors.Is(got, ErrInvalidMetadata) ||
		errors.Is(got, ErrYouTubeChannelNetwork) {
		t.Fatalf("metadata category = %v", got)
	}

	const rawURL = "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/playlists"
	for _, test := range []struct {
		err  error
		want error
	}{
		{&HTTPStatusError{Code: http.StatusForbidden}, ErrAuthentication},
		{&HTTPStatusError{Code: http.StatusGone}, ErrUnavailable},
		{&HTTPStatusError{Code: http.StatusTooManyRequests}, ErrYouTubeChannelRateLimited},
	} {
		_, extractErr := NewYouTubeChannelTab().Extract(context.Background(), Request{
			URL: rawURL, Transport: &channelFixtureTransport{pageURL: rawURL, err: test.err},
		})
		if !errors.Is(extractErr, test.want) {
			t.Fatalf("error %v: %v", test.err, extractErr)
		}
	}

	transport := &channelFixtureTransport{
		pageURL: rawURL, page: readChannelFixture(t, "channel-playlists.html"),
		status: http.StatusTooManyRequests,
	}
	result, err := NewYouTubeChannelTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	iterator := result.Entries.Iterator()
	for index := 0; index < 2; index++ {
		if _, ok, nextErr := iterator.Next(context.Background()); nextErr != nil || !ok {
			t.Fatalf("initial entry %d: ok=%v err=%v", index, ok, nextErr)
		}
	}
	if _, _, nextErr := iterator.Next(context.Background()); !errors.Is(nextErr, ErrYouTubeChannelRateLimited) {
		t.Fatalf("continuation rate limit = %v", nextErr)
	}
	malformed := &channelFixtureTransport{
		pageURL: rawURL, page: readChannelFixture(t, "channel-playlists.html"),
		continuation: []byte(`{`),
	}
	malformedResult, err := NewYouTubeChannelTab().Extract(context.Background(), Request{URL: rawURL, Transport: malformed})
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
		pageURL: rawURL, page: readChannelFixture(t, "channel-playlists.html"),
		started: make(chan struct{}),
	}
	blockingResult, err := NewYouTubeChannelTab().Extract(context.Background(), Request{URL: rawURL, Transport: blocking})
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

func TestYouTubeChannelTabScopesSelectedContentAndRefreshesVisitorData(t *testing.T) {
	page, err := parseYouTubeChannelTabData([]byte(`{
		"metadata":{"channelMetadataRenderer":{"title":"Selected"}},
		"responseContext":{"visitorData":"rotated-channel"},
		"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[
			{"tabRenderer":{"selected":false,"content":{"gridPlaylistRenderer":{"playlistId":"PLdecoy","title":{"simpleText":"decoy"}}}}},
			{"tabRenderer":{"selected":true,"content":{"gridPlaylistRenderer":{"playlistId":"PLchosen","title":{"simpleText":"chosen"}}}}}
		]}},
		"continuationContents":{"gridContinuation":{"continuations":[{"nextContinuationData":{"continuation":"channel-root-token"}}]}},
		"unrelated":{"gridPlaylistRenderer":{"playlistId":"PLother","title":{"simpleText":"other"}}}
	}`), "playlists")
	if err != nil || len(page.entries) != 1 || page.entries[0].ID != "PLchosen" ||
		page.visitorData != "rotated-channel" || page.continuation != "channel-root-token" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
}

func assertYouTubeTabEntriesSafe(t *testing.T, entries []Entry, tab string) {
	t.Helper()
	for _, entry := range entries {
		if entry.ExtractorKey != "youtube" {
			t.Fatalf("unsafe extractor key: %#v", entry)
		}
		isPlaylist := entry.URL == "https://www.youtube.com/playlist?list="+entry.ID
		if isPlaylist {
			if !youtubeTabAllowsPlaylists(tab) {
				t.Fatalf("playlist entry in video-only tab %q: %#v", tab, entry)
			}
			if !youtubePlaylistIDPattern.MatchString(entry.ID) || entry.Transparent ||
				!isPlaylist {
				t.Fatalf("unsafe playlist entry: %#v", entry)
			}
			continue
		}
		if !youtubeTabAllowsVideos(tab) {
			t.Fatalf("video entry in playlist-only tab %q: %#v", tab, entry)
		}
		if !youtubeIDPattern.MatchString(entry.ID) || entry.Transparent {
			t.Fatalf("unsafe video entry: %#v", entry)
		}
		watchURL := "https://www.youtube.com/watch?v=" + entry.ID
		shortURL := "https://www.youtube.com/shorts/" + entry.ID
		if entry.URL != watchURL && entry.URL != shortURL {
			t.Fatalf("unsafe video URL: %#v", entry)
		}
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
	}`), "shorts")
	if err != nil || len(page.entries) != 1 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if got := page.entries[0].URL; got != "https://www.youtube.com/shorts/abcdefghijk" {
		t.Fatalf("short URL = %q", got)
	}
}

func TestYouTubeChannelTabRoutingPolicy(t *testing.T) {
	valid := []string{
		channelFixtureURL,
		"https://youtube.com/channel/UCabcdefghijklmnopqrstuv/shorts",
		"http://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/streams",
		"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/playlists",
		"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/home",
		"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/featured",
		"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/community",
		"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/releases",
		"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/podcasts",
	}
	for _, raw := range valid {
		parsed, _ := http.NewRequest(http.MethodGet, raw, nil)
		if !NewYouTubeChannelTab().Suitable(parsed.URL) {
			t.Errorf("not suitable: %s", raw)
		}
	}
	invalid := []string{
		"https://evil-youtube.com/channel/UCabcdefghijklmnopqrstuv/videos", "https://m.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos", "https://www.youtube.com:443/channel/UCabcdefghijklmnopqrstuv/videos", "https://user@www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos", "ftp://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv", "https://www.youtube.com/channel/UCshort/videos", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/membership", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/playlists/", "https://www.youtube.com/channel%2fUCabcdefghijklmnopqrstuv/videos", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos#fragment", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos?x=%00",
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
	}`), "playlists")
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
	_, _, _, err := fetchYouTubeChannelContinuation(context.Background(), &channelFixtureTransport{}, "bad\nvalue", "visitor", youtubePlaylistConfig{}, "playlists")
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("err = %v", err)
	}
}

func FuzzParseYouTubeChannelTabData(f *testing.F) {
	f.Add([]byte(`{"metadata":{"channelMetadataRenderer":{"title":"x"}}}`))
	f.Add([]byte(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[]}}]}`))
	f.Add([]byte(`{"gridPlaylistRenderer":{"playlistId":"PLfuzz","title":{"simpleText":"fuzz"}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, tab := range []string{"videos", "playlists", "home", "community", "releases", "podcasts"} {
			page, err := parseYouTubeChannelTabData(data, tab)
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
			youtubePublicTabType(tab) == youtubeTabUnsupported {
			t.Fatalf("accepted invalid target %q: %q/%q", rawURL, channelID, tab)
		}
	})
}
