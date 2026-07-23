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

	"github.com/ytdlp-go/ytdlp/internal/value"
)

type youtubeTabBreadthTransport struct {
	expectedPage string
	page         []byte
	continuation []byte

	mu          sync.Mutex
	reads       int
	requests    int
	lastBody    string
	lastVisitor string
}

func (transport *youtubeTabBreadthTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	transport.reads++
	if rawURL != transport.expectedPage {
		return nil, nil, errors.New("unexpected tab page")
	}
	return append([]byte(nil), transport.page...), nil, nil
}

func (transport *youtubeTabBreadthTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.URL.Path != "/youtubei/v1/browse" {
		return nil, errors.New("unexpected tab continuation endpoint")
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.requests++
	transport.lastBody = string(body)
	transport.lastVisitor = request.Header.Get("X-Goog-Visitor-Id")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(transport.continuation))),
	}, nil
}

func readYouTubeTabBreadthFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/youtube_tab_breadth/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func collectYouTubeTabBreadthEntries(t *testing.T, result Extraction) []string {
	t.Helper()
	var entries []string
	iterator := result.Entries.Iterator()
	for {
		entry, ok, err := iterator.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return entries
		}
		entries = append(entries, entry.ID+":"+entry.URL+":"+entry.Title)
	}
}

func TestYouTubeHomeTabMixedRenderersAndContinuation(t *testing.T) {
	const rawURL = "https://www.youtube.com/@synthetic-handle/home"
	transport := &youtubeTabBreadthTransport{
		expectedPage: rawURL,
		page:         readYouTubeTabBreadthFixture(t, "home.html"),
		continuation: readYouTubeTabBreadthFixture(t, "home-continuation.json"),
	}
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"homeVID0001:https://www.youtube.com/watch?v=homeVID0001:Home video",
		"PLhomeMixed001:https://www.youtube.com/playlist?list=PLhomeMixed001:Home playlist",
		"homeVID0002:https://www.youtube.com/watch?v=homeVID0002:Home lockup video",
		"PLhomeMixed002:https://www.youtube.com/playlist?list=PLhomeMixed002:Home lockup playlist",
		"homeVID0003:https://www.youtube.com/watch?v=homeVID0003:Continued home video",
		"PLhomeMixed003:https://www.youtube.com/playlist?list=PLhomeMixed003:Continued home playlist",
	}
	if got := collectYouTubeTabBreadthEntries(t, result); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("entries=%#v", got)
	}
	if transport.reads != 1 || transport.requests != 1 ||
		!strings.Contains(transport.lastBody, `"continuation":"next-home-page"`) ||
		!strings.Contains(transport.lastBody, `"visitorData":"breadth-home-visitor"`) {
		t.Fatalf("reads=%d requests=%d visitor=%q body=%s", transport.reads, transport.requests, transport.lastVisitor, transport.lastBody)
	}
}

func TestYouTubeCommunityTabAttachmentsInlineDedupAndContinuation(t *testing.T) {
	const rawURL = "https://www.youtube.com/@synthetic-handle/community"
	transport := &youtubeTabBreadthTransport{
		expectedPage: rawURL,
		page:         readYouTubeTabBreadthFixture(t, "community.html"),
		continuation: readYouTubeTabBreadthFixture(t, "community-continuation.json"),
	}
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"commAttach1:https://www.youtube.com/watch?v=commAttach1:Attached community video",
		"PLcommunity001:https://www.youtube.com/playlist?list=PLcommunity001:Attached community playlist",
		"commLine001:https://www.youtube.com/shorts/commLine001:",
		"commAttach1:https://www.youtube.com/watch?v=commAttach1:Repeated post attachment",
		"commNext001:https://www.youtube.com/watch?v=commNext001:Continued community video",
		"commLine002:https://www.youtube.com/watch?v=commLine002:",
		"commDirect1:https://www.youtube.com/watch?v=commDirect1:Direct continuation video",
	}
	if got := collectYouTubeTabBreadthEntries(t, result); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("entries=%#v", got)
	}
	if transport.requests != 1 || !strings.Contains(transport.lastBody, `"continuation":"next-community-page"`) {
		t.Fatalf("requests=%d body=%s", transport.requests, transport.lastBody)
	}
}

func TestYouTubeReleasesTabPlaylistOnlyContinuation(t *testing.T) {
	const rawURL = "https://www.youtube.com/@synthetic-handle/releases"
	transport := &youtubeTabBreadthTransport{
		expectedPage: rawURL,
		page:         readYouTubeTabBreadthFixture(t, "releases.html"),
		continuation: readYouTubeTabBreadthFixture(t, "releases-continuation.json"),
	}
	result, err := NewYouTubeHandleTab().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"PLrelease001:https://www.youtube.com/playlist?list=PLrelease001:Release playlist",
		"PLrelease002:https://www.youtube.com/playlist?list=PLrelease002:Release lockup",
		"PLpodcast003:https://www.youtube.com/playlist?list=PLpodcast003:Release podcast",
		"PLrelease004:https://www.youtube.com/playlist?list=PLrelease004:Continued release",
	}
	if got := collectYouTubeTabBreadthEntries(t, result); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("entries=%#v", got)
	}
}

func TestYouTubeExpandedTabsIntegrateChannelAndAliasRoutes(t *testing.T) {
	page := readYouTubeTabBreadthFixture(t, "home.html")
	const channelURL = "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/home"
	channel, err := NewYouTubeChannelTab().Extract(context.Background(), Request{
		URL: channelURL, Transport: &youtubeTabBreadthTransport{expectedPage: channelURL, page: page},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := channel.Info.ID(); id != "UCabcdefghijklmnopqrstuv" {
		t.Fatalf("channel id=%q", id)
	}

	const aliasURL = "https://www.youtube.com/c/Synthetic/home"
	alias, err := NewYouTubeAliasTab().Extract(context.Background(), Request{
		URL: aliasURL, Transport: &youtubeTabBreadthTransport{expectedPage: aliasURL, page: page},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := alias.Info.ID(); id != "UCabcdefghijklmnopqrstuv" {
		t.Fatalf("alias id=%q", id)
	}
}

func TestYouTubeExpandedTabIdentityAndCommunityLinkPolicy(t *testing.T) {
	for _, test := range []struct {
		requested string
		renderer  string
	}{
		{"home", `"tabIdentifier":"FEhome"`},
		{"featured", `"endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/channel/UCabcdefghijklmnopqrstuv/featured"}}}`},
		{"community", `"title":"Community"`},
		{"releases", `"tabIdentifier":"FEreleases"`},
		{"podcasts", `"tabIdentifier":"FEpodcasts"`},
		{"featured", `"title":"Home","endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/channel/UCabcdefghijklmnopqrstuv/featured"}}}`},
	} {
		data := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,` + test.renderer + `}}]}}}`)
		if err := validateYouTubeSelectedTab(data, test.requested); err != nil {
			t.Errorf("%s: %v", test.requested, err)
		}
	}
	mismatch := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"tabIdentifier":"FEreleases"}}]}}}`)
	if err := validateYouTubeSelectedTab(mismatch, "community"); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("mismatch=%v", err)
	}

	var root value.Value
	if err := json.Unmarshal([]byte(`{
		"contentText":{"runs":[
			{"navigationEndpoint":{"urlEndpoint":{"url":"https://evil.example/watch?v=abcdefghijk"}}},
			{"navigationEndpoint":{"urlEndpoint":{"url":"/watch?v=bad"}}},
			{"navigationEndpoint":{"urlEndpoint":{"url":"/watch?v=abcdefghijk"}}},
			{"navigationEndpoint":{"urlEndpoint":{"url":"/watch?v=abcdefghijk"}}}
		]}
	}`), &root); err != nil {
		t.Fatal(err)
	}
	post, _ := root.Object()
	entries := youtubeCommunityPostEntries(post)
	if len(entries) != 1 || entries[0].ID != "abcdefghijk" {
		t.Fatalf("entries=%#v", entries)
	}
}

func FuzzYouTubeCommunityPostEntries(f *testing.F) {
	f.Add([]byte(`{"contentText":{"runs":[{"navigationEndpoint":{"urlEndpoint":{"url":"/watch?v=abcdefghijk"}}}]}}`))
	f.Add([]byte(`{"backstageAttachment":{"playlistRenderer":{"playlistId":"PLfuzz"}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var root value.Value
		if json.Unmarshal(data, &root) != nil {
			return
		}
		post, ok := root.Object()
		if !ok {
			return
		}
		assertYouTubeTabEntriesSafe(t, youtubeCommunityPostEntries(post), "community")
	})
}
