package extractor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestYouTubeMembershipTabVideoOnlyRenderersAndContinuation(t *testing.T) {
	const rawURL = "https://www.youtube.com/@synthetic-handle/membership"
	transport := &youtubeTabBreadthTransport{
		expectedPage: rawURL,
		page:         readYouTubeTabBreadthFixture(t, "membership.html"),
		continuation: readYouTubeTabBreadthFixture(t, "membership-continuation.json"),
	}
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"membVID0001:https://www.youtube.com/watch?v=membVID0001:Membership grid video",
		"membVID0002:https://www.youtube.com/watch?v=membVID0002:Grid video",
		"membSHORT01:https://www.youtube.com/shorts/membSHORT01:Member short",
		"membVID0003:https://www.youtube.com/watch?v=membVID0003:Lockup video",
		"membVID0004:https://www.youtube.com/watch?v=membVID0004:Continued direct video",
		"membVID0005:https://www.youtube.com/watch?v=membVID0005:Continued grid video",
	}
	if got := collectYouTubeTabBreadthEntries(t, result); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("entries=%#v", got)
	}
	if transport.reads != 1 || transport.requests != 1 ||
		!strings.Contains(transport.lastBody, `"continuation":"next-membership-page"`) ||
		!strings.Contains(transport.lastBody, `"visitorData":"breadth-membership-visitor"`) {
		t.Fatalf("reads=%d requests=%d body=%s", transport.reads, transport.requests, transport.lastBody)
	}
}

func TestYouTubeMembershipTabIntegratesAllRouteFamilies(t *testing.T) {
	page := readYouTubeTabBreadthFixture(t, "membership.html")
	continuation := readYouTubeTabBreadthFixture(t, "membership-continuation.json")
	cases := []struct {
		name      string
		url       string
		extractor Extractor
	}{
		{"channel", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/membership", NewYouTubeChannelTab()},
		{"handle", "https://www.youtube.com/@synthetic-handle/membership", NewYouTubeHandleTab()},
		{"user", "https://www.youtube.com/user/Synthetic/membership", NewYouTubeAliasTab()},
		{"c", "https://www.youtube.com/c/Synthetic/membership", NewYouTubeAliasTab()},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			transport := &youtubeTabBreadthTransport{
				expectedPage: test.url,
				page:         page,
				continuation: continuation,
			}
			result, err := test.extractor.Extract(context.Background(), Request{URL: test.url, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			if id, _ := result.Info.ID(); id != "UCabcdefghijklmnopqrstuv" && !strings.HasPrefix(id, "user:") && !strings.HasPrefix(id, "c:") && !strings.HasPrefix(id, "handle:") {
				t.Fatalf("id=%q", id)
			}
			entries := collectYouTubeTabBreadthEntries(t, result)
			if len(entries) != 6 {
				t.Fatalf("entries=%#v", entries)
			}
			for _, entry := range entries {
				if strings.Contains(entry, "playlist?list=") {
					t.Fatalf("playlist leaked into membership tab: %s", entry)
				}
			}
		})
	}
}

func TestYouTubeMembershipTabSelectedIdentityVariants(t *testing.T) {
	for _, test := range []struct {
		requested string
		renderer  string
	}{
		{"membership", `"tabIdentifier":"TAB_ID_SPONSORSHIPS"`},
		{"membership", `"tabIdentifier":"FEmembership"`},
		{"membership", `"endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/@synthetic-handle/membership"}}}`},
		{"membership", `"title":"Membership"`},
	} {
		data := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,` + test.renderer + `}}]}}}`)
		if err := validateYouTubeSelectedTab(data, test.requested); err != nil {
			t.Errorf("%s: %v", test.renderer, err)
		}
	}
	mismatch := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"tabIdentifier":"FEvideos"}}]}}}`)
	if err := validateYouTubeSelectedTab(mismatch, "membership"); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("mismatch=%v", err)
	}
	conflict := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"tabIdentifier":"TAB_ID_SPONSORSHIPS","endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/@synthetic-handle/videos"}}}}}]}}}`)
	if err := validateYouTubeSelectedTab(conflict, "membership"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("conflict=%v", err)
	}
}

func TestYouTubeMembershipTabAccessFailuresAndEmptyAccessiblePlaylist(t *testing.T) {
	const rawURL = "https://www.youtube.com/@synthetic-handle/membership"
	for _, test := range []struct {
		name string
		page string
		code int
		want error
	}{
		{"sign in alert", `{"metadata":{"channelMetadataRenderer":{"title":"Membership"}},"alerts":[{"alertRenderer":{"text":{"simpleText":"Sign in to view this tab"}}}]}`, 0, ErrAuthentication},
		{"members only alert", `{"metadata":{"channelMetadataRenderer":{"title":"Membership"}},"alerts":[{"alertRenderer":{"text":{"simpleText":"Members-only content"}}}]}`, 0, ErrAuthentication},
		{"private alert", `{"metadata":{"channelMetadataRenderer":{"title":"Membership"}},"alerts":[{"alertRenderer":{"text":{"simpleText":"This channel is private"}}}]}`, 0, ErrAuthentication},
		{"unavailable alert", `{"metadata":{"channelMetadataRenderer":{"title":"Membership"}},"alerts":[{"alertRenderer":{"text":{"simpleText":"This tab is unavailable"}}}]}`, 0, ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := []byte(`<script>ytInitialData=` + test.page + `;</script>`)
			_, err := NewYouTubeHandleTab().Extract(context.Background(), Request{
				URL: rawURL, Transport: &youtubeTabBreadthTransport{expectedPage: rawURL, page: page},
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
		})
	}
	for _, test := range []struct {
		code int
		want error
	}{
		{http.StatusUnauthorized, ErrAuthentication},
		{http.StatusForbidden, ErrAuthentication},
	} {
		transport := &handleFixtureTransport{
			pageURL: rawURL,
			page:    readYouTubeTabBreadthFixture(t, "membership.html"),
			status:  test.code,
		}
		result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil {
			t.Fatalf("status %d extract: %v", test.code, err)
		}
		iterator := result.Entries.Iterator()
		for index := 0; index < 4; index++ {
			if _, ok, nextErr := iterator.Next(context.Background()); nextErr != nil || !ok {
				t.Fatalf("status %d entry %d ok=%v err=%v", test.code, index, ok, nextErr)
			}
		}
		if _, _, nextErr := iterator.Next(context.Background()); !errors.Is(nextErr, test.want) {
			t.Fatalf("status %d continuation: %v", test.code, nextErr)
		}
	}

	emptyPage := []byte(`<script>ytInitialData={
		"metadata":{"channelMetadataRenderer":{"title":"Synthetic Membership","externalId":"UCabcdefghijklmnopqrstuv"}},
		"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[
			{"tabRenderer":{"selected":true,"tabIdentifier":"FEmembership","title":"Membership","content":{"richGridRenderer":{"contents":[]}}}}
		]}}
	};</script>`)
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{
		URL: rawURL, Transport: &youtubeTabBreadthTransport{expectedPage: rawURL, page: emptyPage},
	})
	if err != nil {
		t.Fatal(err)
	}
	iterator := result.Entries.Iterator()
	if _, ok, err := iterator.Next(context.Background()); err != nil || ok {
		t.Fatalf("empty accessible membership ok=%v err=%v", ok, err)
	}
}

func TestYouTubeMembershipTabRoutingNegativesCancellationAndRace(t *testing.T) {
	invalid := []string{
		"https://evil-youtube.com/@synthetic-handle/membership",
		"https://m.youtube.com/channel/UCabcdefghijklmnopqrstuv/membership",
		"https://www.youtube.com:443/@synthetic-handle/membership",
		"https://www.youtube.com/@synthetic-handle/membership/",
		"https://www.youtube.com/@synthetic-handle/membership/extra",
		"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/membership#tab",
		"https://www.youtube.com/user/name/membership?x=%00",
	}
	for _, raw := range invalid {
		request, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			continue
		}
		if NewYouTubeHandleTab().Suitable(request.URL) || NewYouTubeChannelTab().Suitable(request.URL) || NewYouTubeAliasTab().Suitable(request.URL) {
			t.Fatalf("accepted hostile membership URL: %s", raw)
		}
	}

	const rawURL = "https://www.youtube.com/@synthetic-handle/membership"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewYouTubeHandleTab().Extract(ctx, Request{
		URL: rawURL, Transport: &youtubeTabBreadthTransport{expectedPage: rawURL, page: readYouTubeTabBreadthFixture(t, "membership.html")},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}

	transport := &youtubeTabBreadthTransport{
		expectedPage: rawURL,
		page:         readYouTubeTabBreadthFixture(t, "membership.html"),
		continuation: readYouTubeTabBreadthFixture(t, "membership-continuation.json"),
	}
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			iterator := result.Entries.Iterator()
			count := 0
			for {
				_, ok, nextErr := iterator.Next(context.Background())
				if nextErr != nil {
					errs <- nextErr
					return
				}
				if !ok {
					if count != 6 {
						errs <- errors.New("unexpected entry count")
					}
					return
				}
				count++
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if transport.requests != 8 {
		t.Fatalf("continuation requests=%d", transport.requests)
	}
}

func TestYouTubeMembershipTabContinuationHTTPFailures(t *testing.T) {
	const rawURL = "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/membership"
	transport := &channelFixtureTransport{
		pageURL:      rawURL,
		page:         readYouTubeTabBreadthFixture(t, "membership.html"),
		continuation: []byte(`{"metadata":{"channelMetadataRenderer":{"title":"Membership"}},"alerts":[{"alertRenderer":{"text":{"simpleText":"Sign in to continue"}}}]}`),
	}
	result, err := NewYouTubeChannelTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	iterator := result.Entries.Iterator()
	for index := 0; index < 4; index++ {
		if _, ok, nextErr := iterator.Next(context.Background()); nextErr != nil || !ok {
			t.Fatalf("initial entry %d ok=%v err=%v", index, ok, nextErr)
		}
	}
	if _, _, nextErr := iterator.Next(context.Background()); !errors.Is(nextErr, ErrAuthentication) {
		t.Fatalf("continuation alert=%v", nextErr)
	}

	transport = &channelFixtureTransport{
		pageURL: rawURL,
		page:    readYouTubeTabBreadthFixture(t, "membership.html"),
		status:  http.StatusForbidden,
	}
	result, err = NewYouTubeChannelTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	iterator = result.Entries.Iterator()
	for index := 0; index < 4; index++ {
		if _, ok, nextErr := iterator.Next(context.Background()); nextErr != nil || !ok {
			t.Fatalf("page entry %d ok=%v err=%v", index, ok, nextErr)
		}
	}
	if _, _, nextErr := iterator.Next(context.Background()); !errors.Is(nextErr, ErrAuthentication) {
		t.Fatalf("continuation status=%v", nextErr)
	}
}

func TestYouTubeMembershipTabParserRejectsPlaylists(t *testing.T) {
	page, err := parseYouTubeHandleTabData([]byte(`{
		"metadata":{"channelMetadataRenderer":{"title":"Membership"}},
		"videoRenderer":{"videoId":"membVID0001","title":{"simpleText":"direct"}},
		"gridPlaylistRenderer":{"playlistId":"PLmember001","title":{"simpleText":"ignored"}},
		"lockupViewModel":{"contentId":"PLmember002","contentType":"LOCKUP_CONTENT_TYPE_PLAYLIST"}
	}`), "membership")
	if err != nil || len(page.entries) != 1 || page.entries[0].ID != "membVID0001" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
}

type blockingMembershipContinuationTransport struct {
	pageURL string
	page    []byte
	started chan struct{}
	once    sync.Once
}

func (transport *blockingMembershipContinuationTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if rawURL != transport.pageURL {
		return nil, nil, errors.New("unexpected page")
	}
	return transport.page, nil, nil
}

func (transport *blockingMembershipContinuationTransport) Do(ctx context.Context, _ *http.Request) (*http.Response, error) {
	transport.once.Do(func() { close(transport.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestYouTubeMembershipTabContinuationCancellation(t *testing.T) {
	const rawURL = "https://www.youtube.com/@synthetic-handle/membership"
	transport := &blockingMembershipContinuationTransport{
		pageURL: rawURL,
		page:    readYouTubeTabBreadthFixture(t, "membership.html"),
		started: make(chan struct{}),
	}
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	iterator := result.Entries.Iterator()
	for index := 0; index < 4; index++ {
		if _, ok, nextErr := iterator.Next(context.Background()); nextErr != nil || !ok {
			t.Fatalf("initial entry %d ok=%v err=%v", index, ok, nextErr)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-transport.started
		cancel()
	}()
	if _, _, nextErr := iterator.Next(ctx); !errors.Is(nextErr, context.Canceled) {
		t.Fatalf("continuation cancellation=%v", nextErr)
	}
}

func FuzzYouTubeMembershipSelectedTab(f *testing.F) {
	f.Add([]byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"tabIdentifier":"TAB_ID_SPONSORSHIPS"}}]}}}`), "membership")
	f.Add([]byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"title":"Membership"}}]}}}`), "membership")
	f.Fuzz(func(t *testing.T, data []byte, requested string) {
		if requested != "membership" {
			return
		}
		if err := validateYouTubeSelectedTab(data, requested); err != nil {
			return
		}
		page, err := parseYouTubeHandleTabData(data, requested)
		if err != nil {
			return
		}
		assertYouTubeTabEntriesSafe(t, page.entries, requested)
	})
}
