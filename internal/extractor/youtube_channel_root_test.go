package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
)

type youtubeRootFixtureTransport struct {
	mu            sync.Mutex
	pages         map[string][]byte
	readErrors    map[string]error
	reads         map[string]int
	continuation  []byte
	requests      int
	lastBody      string
	blockReadPage string
	readStarted   chan struct{}
	startOnce     sync.Once
}

func (transport *youtubeRootFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	transport.mu.Lock()
	if transport.reads == nil {
		transport.reads = make(map[string]int)
	}
	transport.reads[rawURL]++
	err := transport.readErrors[rawURL]
	page, ok := transport.pages[rawURL]
	block := rawURL == transport.blockReadPage
	transport.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	if block {
		transport.startOnce.Do(func() {
			if transport.readStarted != nil {
				close(transport.readStarted)
			}
		})
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	if !ok {
		return nil, nil, errors.New("unexpected root page")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return append([]byte(nil), page...), nil, nil
}

func (transport *youtubeRootFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.URL.Path != "/youtubei/v1/browse" {
		return nil, errors.New("unexpected root continuation endpoint")
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.requests++
	transport.lastBody = string(body)
	response := append([]byte(nil), transport.continuation...)
	transport.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(response))),
	}, nil
}

func readYouTubeRootFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/youtube_channel_root/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func youtubeRootTransport(t *testing.T, base string, first []byte) *youtubeRootFixtureTransport {
	t.Helper()
	return &youtubeRootFixtureTransport{
		pages: map[string][]byte{
			base + "/videos":  first,
			base + "/streams": readYouTubeRootFixture(t, "streams.html"),
			base + "/shorts":  readYouTubeRootFixture(t, "shorts.html"),
		},
		readErrors:   make(map[string]error),
		reads:        make(map[string]int),
		continuation: readYouTubeRootFixture(t, "videos-continuation.json"),
	}
}

func collectYouTubeRootEntries(t *testing.T, result Extraction) []string {
	t.Helper()
	var got []string
	iterator := result.Entries.Iterator()
	for {
		entry, ok, err := iterator.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return got
		}
		got = append(got, entry.ID+":"+entry.URL+":"+entry.Title)
	}
}

func TestYouTubeBareRootsAggregateVideosStreamsAndShortsLazily(t *testing.T) {
	tests := []struct {
		name, rawURL, canonical string
		extract                 func(context.Context, Request) (Extraction, error)
	}{
		{"handle", "https://youtube.com/@synthetic-handle?view=0", "https://www.youtube.com/@synthetic-handle", NewYouTubeHandleTab().Extract},
		{"channel", "http://youtube.com/channel/UCabcdefghijklmnopqrstuv", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv", NewYouTubeChannelTab().Extract},
		{"alias", "https://youtube.com/c/SyntheticAlias?view=0", "https://www.youtube.com/c/SyntheticAlias", NewYouTubeAliasTab().Extract},
	}
	want := []string{
		"rootVideo01:https://www.youtube.com/watch?v=rootVideo01:Root video",
		"rootVideo02:https://www.youtube.com/watch?v=rootVideo02:Continued root video",
		"rootStream1:https://www.youtube.com/watch?v=rootStream1:Root stream",
		"rootShort01:https://www.youtube.com/shorts/rootShort01:Root short",
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := youtubeRootTransport(t, test.canonical, readYouTubeRootFixture(t, "videos.html"))
			result, err := test.extract(context.Background(), Request{URL: test.rawURL, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			if webpage, _ := result.Info.WebpageURL(); webpage != test.canonical {
				t.Fatalf("webpage=%q", webpage)
			}
			if id, _ := result.Info.ID(); id != "UCabcdefghijklmnopqrstuv" {
				t.Fatalf("id=%q", id)
			}
			if transport.reads[test.canonical+"/videos"] != 1 ||
				transport.reads[test.canonical+"/streams"] != 0 ||
				transport.reads[test.canonical+"/shorts"] != 0 || transport.requests != 0 {
				t.Fatalf("eager reads=%#v requests=%d", transport.reads, transport.requests)
			}
			for run := 0; run < 2; run++ {
				if got := collectYouTubeRootEntries(t, result); strings.Join(got, "\n") != strings.Join(want, "\n") {
					t.Fatalf("run %d entries=%#v", run, got)
				}
			}
			if transport.reads[test.canonical+"/videos"] != 1 ||
				transport.reads[test.canonical+"/streams"] != 2 ||
				transport.reads[test.canonical+"/shorts"] != 2 ||
				transport.requests != 2 ||
				!strings.Contains(transport.lastBody, `"continuation":"root-next-videos"`) {
				t.Fatalf("reads=%#v requests=%d body=%s", transport.reads, transport.requests, transport.lastBody)
			}
		})
	}
}

func TestYouTubeBareRootNoVideosExcludesHomeShelvesAndEmptyRoot(t *testing.T) {
	const base = "https://www.youtube.com/@synthetic-handle"
	transport := youtubeRootTransport(t, base, readYouTubeRootFixture(t, "no-videos.html"))
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: base, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"rootStream1:https://www.youtube.com/watch?v=rootStream1:Root stream",
		"rootShort01:https://www.youtube.com/shorts/rootShort01:Root short",
	}
	if got := collectYouTubeRootEntries(t, result); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("entries=%#v", got)
	}
	if transport.requests != 0 || transport.reads[base+"/streams"] != 1 || transport.reads[base+"/shorts"] != 1 {
		t.Fatalf("reads=%#v requests=%d", transport.reads, transport.requests)
	}

	empty := youtubeRootTransport(t, base, readYouTubeRootFixture(t, "empty.html"))
	emptyResult, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: base, Transport: empty})
	if err != nil {
		t.Fatal(err)
	}
	if got := collectYouTubeRootEntries(t, emptyResult); len(got) != 0 {
		t.Fatalf("empty entries=%#v", got)
	}
	const uploadsURL = "https://www.youtube.com/playlist?list=UUabcdefghijklmnopqrstuv"
	if empty.reads[base+"/videos"] != 1 || empty.reads[uploadsURL] != 1 ||
		len(empty.reads) != 2 || empty.requests != 0 {
		t.Fatalf("empty reads=%#v requests=%d", empty.reads, empty.requests)
	}

	withoutUCID := youtubeRootTransport(t, base, []byte(`<script>ytInitialData={
		"metadata":{"channelMetadataRenderer":{"title":"Synthetic Root Without UCID"}},
		"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[
			{"tabRenderer":{"selected":true,"tabIdentifier":"FEhome","content":{"richGridRenderer":{"contents":[]}}}}
		]}}
	};</script>`))
	withoutUCIDResult, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: base, Transport: withoutUCID})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := withoutUCIDResult.Info.ID(); id != "handle:@synthetic-handle" {
		t.Fatalf("fallback id=%q", id)
	}
	if got := collectYouTubeRootEntries(t, withoutUCIDResult); len(got) != 0 || len(withoutUCID.reads) != 1 {
		t.Fatalf("without UCID entries=%#v reads=%#v", got, withoutUCID.reads)
	}
}

func TestYouTubeBareRootFallsBackToTopicUploadsPlaylist(t *testing.T) {
	const (
		channelID  = "UCabcdefghijklmnopqrstuv"
		uploadsID  = "UUabcdefghijklmnopqrstuv"
		uploadsURL = "https://www.youtube.com/playlist?list=" + uploadsID
	)
	tests := []struct {
		name, rawURL, base string
		extract            func(context.Context, Request) (Extraction, error)
	}{
		{"channel", "https://youtube.com/channel/" + channelID, "https://www.youtube.com/channel/" + channelID, NewYouTubeChannelTab().Extract},
		{"handle", "https://youtube.com/@synthetic-handle", "https://www.youtube.com/@synthetic-handle", NewYouTubeHandleTab().Extract},
		{"alias", "https://youtube.com/user/SyntheticAlias", "https://www.youtube.com/user/SyntheticAlias", NewYouTubeAliasTab().Extract},
	}
	want := []string{
		"topicVid001:https://www.youtube.com/watch?v=topicVid001:First topic upload",
		"topicVid002:https://www.youtube.com/watch?v=topicVid002:Continued topic upload",
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := youtubeRootTransport(t, test.base, readYouTubeRootFixture(t, "empty.html"))
			transport.pages[uploadsURL] = readYouTubeRootFixture(t, "topic-playlist.html")
			transport.continuation = readYouTubeRootFixture(t, "topic-playlist-continuation.json")
			result, err := test.extract(context.Background(), Request{URL: test.rawURL, Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			if id, _ := result.Info.ID(); id != uploadsID {
				t.Fatalf("id=%q", id)
			}
			if title, _ := result.Info.Title(); title != "Uploads from Synthetic Topic" {
				t.Fatalf("title=%q", title)
			}
			if webpage, _ := result.Info.WebpageURL(); webpage != uploadsURL {
				t.Fatalf("webpage=%q", webpage)
			}
			for run := 0; run < 2; run++ {
				if got := collectYouTubeRootEntries(t, result); strings.Join(got, "\n") != strings.Join(want, "\n") {
					t.Fatalf("run %d entries=%#v", run, got)
				}
			}
			if transport.reads[test.base+"/videos"] != 1 || transport.reads[uploadsURL] != 1 ||
				transport.requests != 2 || !strings.Contains(transport.lastBody, `"continuation":"topic-next"`) {
				t.Fatalf("reads=%#v requests=%d body=%s", transport.reads, transport.requests, transport.lastBody)
			}
		})
	}
}

func TestYouTubeBareTopicFallbackPreservesCancellation(t *testing.T) {
	const (
		base       = "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv"
		uploadsURL = "https://www.youtube.com/playlist?list=UUabcdefghijklmnopqrstuv"
	)
	transport := youtubeRootTransport(t, base, readYouTubeRootFixture(t, "empty.html"))
	transport.blockReadPage = uploadsURL
	transport.readStarted = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewYouTubeChannelTab().Extract(ctx, Request{URL: base, Transport: transport})
		done <- err
	}()
	<-transport.readStarted
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestYouTubeBareRootFallsBackAfterMissingVideosPage(t *testing.T) {
	const base = "https://www.youtube.com/@synthetic-handle"
	transport := youtubeRootTransport(t, base, readYouTubeRootFixture(t, "no-videos.html"))
	delete(transport.pages, base+"/videos")
	transport.readErrors[base+"/videos"] = &HTTPStatusError{Code: http.StatusNotFound}
	transport.pages[base] = readYouTubeRootFixture(t, "no-videos.html")
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: base, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if got := collectYouTubeRootEntries(t, result); len(got) != 2 {
		t.Fatalf("entries=%#v", got)
	}
	if transport.reads[base+"/videos"] != 1 || transport.reads[base] != 1 {
		t.Fatalf("reads=%#v", transport.reads)
	}
}

func TestYouTubeBareRootSelectedTabValidationBoundsAndCancellation(t *testing.T) {
	valid := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[
		{"tabRenderer":{"selected":true,"tabIdentifier":"FEvideos"}},
		{"tabRenderer":{"selected":false,"tabIdentifier":"FElive"}},
		{"tabRenderer":{"selected":false,"tabIdentifier":"FEshorts"}},
		{"tabRenderer":{"selected":false,"tabIdentifier":"FEhome"}}
	]}}}`)
	available, selected, decisive, err := youtubeBareUploadTabs(valid)
	if err != nil || !decisive || selected != "videos" ||
		!available["videos"] || !available["streams"] || !available["shorts"] || available["home"] {
		t.Fatalf("available=%#v selected=%q decisive=%v err=%v", available, selected, decisive, err)
	}
	for _, malformed := range []string{
		`{`,
		`[]`,
		`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[
			{"tabRenderer":{"selected":true,"tabIdentifier":"FEvideos"}},
			{"tabRenderer":{"selected":true,"tabIdentifier":"FEshorts"}}
		]}}}`,
		`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[
			{"tabRenderer":{"selected":true,"tabIdentifier":"FEvideos","endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/@x/shorts"}}}}}
		]}}}`,
		`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[` +
			strings.Repeat(`{"tabRenderer":{}},`, youtubeMaxBareChannelTabs) +
			`{"tabRenderer":{}}]}}}`,
	} {
		if _, _, _, err := youtubeBareUploadTabs([]byte(malformed)); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("malformed=%s err=%v", malformed, err)
		}
	}

	sequence := youtubeBareChannelEntries{
		tabs: []string{"videos"},
		load: func(ctx context.Context, _ string) (EntrySequence, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := sequence.Iterator().Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	bounded := &youtubeBareChannelIterator{
		current: StaticEntries(Entry{ID: "abcdefghijk"}).Iterator(),
		entries: defaultMaxPlaylistEntries,
	}
	if _, _, err := bounded.Next(context.Background()); !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("entry bound=%v", err)
	}

	const base = "https://www.youtube.com/@synthetic-handle"
	transport := youtubeRootTransport(t, base, readYouTubeRootFixture(t, "no-videos.html"))
	transport.blockReadPage = base + "/streams"
	transport.readStarted = make(chan struct{})
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: base, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	inFlight, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, nextErr := result.Entries.Iterator().Next(inFlight)
		done <- nextErr
	}()
	<-transport.readStarted
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight cancellation=%v", err)
	}
}

func TestYouTubeBareRootNetworkAndAlertCategories(t *testing.T) {
	const base = "https://www.youtube.com/@synthetic-handle"
	for _, test := range []struct {
		err  error
		want error
	}{
		{&HTTPStatusError{Code: http.StatusForbidden}, ErrAuthentication},
		{&HTTPStatusError{Code: http.StatusTooManyRequests}, ErrYouTubeHandleTabRateLimited},
		{errors.New("dial failed"), ErrYouTubeHandleTabNetwork},
	} {
		transport := youtubeRootTransport(t, base, nil)
		transport.readErrors[base+"/videos"] = test.err
		_, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: base, Transport: transport})
		if !errors.Is(err, test.want) {
			t.Fatalf("input=%v err=%v", test.err, err)
		}
	}
	for _, test := range []struct {
		alert string
		want  error
	}{
		{"This channel is private", ErrAuthentication},
		{"This channel is unavailable", ErrUnavailable},
	} {
		page := []byte(`<script>ytInitialData={"metadata":{"channelMetadataRenderer":{"title":"Alert"}},"alerts":[{"alertRenderer":{"text":{"simpleText":"` + test.alert + `"}}}]};</script>`)
		transport := youtubeRootTransport(t, base, page)
		_, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: base, Transport: transport})
		if !errors.Is(err, test.want) {
			t.Fatalf("alert=%q err=%v", test.alert, err)
		}
	}
}

func FuzzYouTubeBareUploadTabs(f *testing.F) {
	f.Add([]byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"tabIdentifier":"FEvideos"}}]}}}`))
	f.Add([]byte(`{"contents":[]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		available, selected, _, err := youtubeBareUploadTabs(data)
		if err != nil {
			return
		}
		for tab := range available {
			if tab != "videos" && tab != "streams" && tab != "shorts" {
				t.Fatalf("unsafe tab=%q", tab)
			}
		}
		if selected != "" && selected != "videos" && selected != "streams" && selected != "shorts" &&
			selected != "home" && selected != "featured" && selected != "community" &&
			selected != "playlists" && selected != "releases" && selected != "podcasts" &&
			selected != "membership" {
			t.Fatalf("unsafe selected=%q", selected)
		}
		encoded, _ := json.Marshal(available)
		if len(encoded) > 128 {
			t.Fatalf("unbounded available=%s", encoded)
		}
	})
}
