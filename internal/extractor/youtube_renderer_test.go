package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestYouTubeRendererWalksSupportedFamilies(t *testing.T) {
	data := []byte(`{
		"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"content":{"sectionListRenderer":{"contents":[
			{"itemSectionRenderer":{"contents":[
				{"videoRenderer":{"videoId":"aaaaaaaaaaa","title":{"simpleText":"vid"}}},
				{"reelItemRenderer":{"videoId":"bbbbbbbbbbb","videoType":"SHORT","title":{"simpleText":"short"}}},
				{"playlistRenderer":{"playlistId":"PLfixture0001","title":{"simpleText":"pl"}}},
				{"channelRenderer":{"channelId":"UCabcdefghijklmnopqrstuv","title":{"simpleText":"ch"}}},
				{"hashtagTileRenderer":{"hashtag":{"simpleText":"#cats"},"onTapCommand":{"commandMetadata":{"webCommandMetadata":{"url":"/hashtag/cats"}}}}},
				{"shelfRenderer":{"title":{"simpleText":"Shelf"},"endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/feed/trending"}}}}},
				{"lockupViewModel":{"contentType":"LOCKUP_CONTENT_TYPE_VIDEO","contentId":"ccccccccccc","metadata":{"lockupMetadataViewModel":{"title":{"content":"lockup"}}}}},
				{"lockupViewModel":{"contentType":"LOCKUP_CONTENT_TYPE_PODCAST","contentId":"PLpodcast0001","metadata":{"lockupMetadataViewModel":{"title":{"content":"pod"}}}}},
				{"shortsLockupViewModel":{"onTap":{"innertubeCommand":{"reelWatchEndpoint":{"videoId":"ddddddddddd"}}},"overlayMetadata":{"primaryText":{"content":"shorts lockup"}}}}
			]}}
		]}}}}]}},
		"metadata":{"channelMetadataRenderer":{"title":"Fixture Channel","externalId":"UCabcdefghijklmnopqrstuv"}},
		"responseContext":{"visitorData":"visitor-1"}
	}`)
	page, err := parseYouTubeRendererData(data, youtubeRendererPolicy{kinds: youtubeRendererSearchAll})
	if err != nil {
		t.Fatal(err)
	}
	if page.title != "Fixture Channel" || page.channelID != "UCabcdefghijklmnopqrstuv" || page.visitorData != "visitor-1" {
		t.Fatalf("metadata=%#v", page)
	}
	if len(page.entries) != 9 {
		t.Fatalf("entries=%d %#v", len(page.entries), page.entries)
	}
	checks := []struct{ id, key, url string }{
		{"aaaaaaaaaaa", "youtube", "https://www.youtube.com/watch?v=aaaaaaaaaaa"},
		{"bbbbbbbbbbb", "youtube", "https://www.youtube.com/shorts/bbbbbbbbbbb"},
		{"PLfixture0001", "youtube", "https://www.youtube.com/playlist?list=PLfixture0001"},
		{"UCabcdefghijklmnopqrstuv", "youtube_channel_tab", "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv"},
		{"cats", "", "https://www.youtube.com/hashtag/cats"},
		{"", "", "https://www.youtube.com/feed/trending"},
		{"ccccccccccc", "youtube", "https://www.youtube.com/watch?v=ccccccccccc"},
		{"PLpodcast0001", "youtube", "https://www.youtube.com/playlist?list=PLpodcast0001"},
		{"ddddddddddd", "youtube", "https://www.youtube.com/shorts/ddddddddddd"},
	}
	for i, want := range checks {
		got := page.entries[i]
		if got.ID != want.id || got.ExtractorKey != want.key || got.URL != want.url {
			t.Fatalf("entry[%d]=%#v want %#v", i, got, want)
		}
	}
}

func TestYouTubeRendererMixedOrderAndRepeatedOccurrence(t *testing.T) {
	data := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"content":{"richGridRenderer":{"contents":[
		{"richItemRenderer":{"content":{"videoRenderer":{"videoId":"aaaaaaaaaaa"}}}},
		{"richItemRenderer":{"content":{"playlistRenderer":{"playlistId":"PLfixture0001"}}}},
		{"richItemRenderer":{"content":{"videoRenderer":{"videoId":"aaaaaaaaaaa"}}}}
	]}}}}]}}}`)
	page, err := parseYouTubeRendererData(data, youtubeRendererPolicy{kinds: youtubeRendererVideo | youtubeRendererPlaylist})
	if err != nil || len(page.entries) != 3 || page.entries[0].ID != "aaaaaaaaaaa" || page.entries[2].ID != "aaaaaaaaaaa" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
}

func TestYouTubeRendererTabPolicyGatesKinds(t *testing.T) {
	data := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"content":{"sectionListRenderer":{"contents":[{"itemSectionRenderer":{"contents":[
		{"videoRenderer":{"videoId":"aaaaaaaaaaa"}},
		{"playlistRenderer":{"playlistId":"PLfixture0001"}},
		{"channelRenderer":{"channelId":"UCabcdefghijklmnopqrstuv"}}
	]}}]}}}}]}}}`)
	videos, err := parseYouTubeRendererData(data, youtubeRendererPolicyForTab("videos"))
	if err != nil || len(videos.entries) != 1 || videos.entries[0].ID != "aaaaaaaaaaa" {
		t.Fatalf("videos=%#v err=%v", videos, err)
	}
	playlists, err := parseYouTubeRendererData(data, youtubeRendererPolicyForTab("playlists"))
	if err != nil || len(playlists.entries) != 1 || playlists.entries[0].ID != "PLfixture0001" {
		t.Fatalf("playlists=%#v err=%v", playlists, err)
	}
}

func TestYouTubeRendererRejectsHostileHashtagAndShelf(t *testing.T) {
	items := []any{
		map[string]any{"hashtagTileRenderer": map[string]any{"onTapCommand": map[string]any{"commandMetadata": map[string]any{"webCommandMetadata": map[string]any{"url": "https://evil.com/hashtag/cats"}}}}},
		map[string]any{"hashtagTileRenderer": map[string]any{"onTapCommand": map[string]any{"commandMetadata": map[string]any{"webCommandMetadata": map[string]any{"url": "/hashtag/cats/extra"}}}}},
		map[string]any{"shelfRenderer": map[string]any{"endpoint": map[string]any{"commandMetadata": map[string]any{"webCommandMetadata": map[string]any{"url": "/channels?app=desktop"}}}}},
		map[string]any{"shelfRenderer": map[string]any{"endpoint": map[string]any{"commandMetadata": map[string]any{"webCommandMetadata": map[string]any{"url": "https://evil.com/feed"}}}}},
	}
	payload, err := json.Marshal(map[string]any{
		"contents": map[string]any{
			"sectionListRenderer": map[string]any{
				"contents": []any{map[string]any{"itemSectionRenderer": map[string]any{"contents": items}}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := parseYouTubeRendererData(payload, youtubeRendererPolicy{kinds: youtubeRendererHashtag | youtubeRendererShelf})
	if err != nil || len(page.entries) != 0 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
}

func TestYouTubeCustomTabIdentityAttacks(t *testing.T) {
	identity := youtubeChannelIdentity{ChannelID: "UCabcdefghijklmnopqrstuv"}
	if err := youtubeValidateCustomTabEndpoint("/channel/UCabcdefghijklmnopqrstuv/letsplay", identity); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"https://evil.com/channel/UCabcdefghijklmnopqrstuv/videos",
		"https://www.youtube.com/channel/UCzzzzzzzzzzzzzzzzzzzzzz/videos",
		"https://www.youtube.com/@other/videos",
		"https://user@www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos",
	} {
		if err := youtubeValidateCustomTabEndpoint(raw, identity); err == nil {
			t.Fatalf("accepted hostile endpoint %q", raw)
		}
	}
	if err := youtubeValidateCustomTabBrowseID("UCzzzzzzzzzzzzzzzzzzzzzz", identity); err == nil {
		t.Fatal("accepted browse id swap")
	}
	selected := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[
		{"tabRenderer":{"selected":true,"title":"Let's Play","endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/channel/UCabcdefghijklmnopqrstuv/letsplay"}},"browseEndpoint":{"browseId":"UCabcdefghijklmnopqrstuv","canonicalBaseUrl":"/channel/UCabcdefghijklmnopqrstuv/letsplay"}}}},
		{"tabRenderer":{"selected":true,"title":"Videos","endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/channel/UCabcdefghijklmnopqrstuv/videos"}}}}}
	]}}}`)
	if err := youtubeCustomTabSelectedAndBound(selected, "letsplay", identity); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("conflicting selected tabs: %v", err)
	}
	crossHost := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[
		{"tabRenderer":{"selected":true,"tabIdentifier":"letsplay","endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"https://evil.com/channel/UCabcdefghijklmnopqrstuv/letsplay"}},"browseEndpoint":{"browseId":"UCabcdefghijklmnopqrstuv"}}}}
	]}}}`)
	if err := youtubeCustomTabSelectedAndBound(crossHost, "letsplay", identity); err == nil {
		t.Fatal("accepted cross-host selected tab")
	}
}

func TestYouTubeAdvertisedTabsExposeStableMetadata(t *testing.T) {
	rootJSON := []byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[
		{"tabRenderer":{"title":"Videos","endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/channel/UCabcdefghijklmnopqrstuv/videos"}},"browseEndpoint":{"canonicalBaseUrl":"/channel/UCabcdefghijklmnopqrstuv/videos"}},"accessibility":{"accessibilityData":{"label":"123 videos"}}}},
		{"expandableTabRenderer":{"title":"Search","endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/channel/UCabcdefghijklmnopqrstuv/search"}}}}},
		{"tabRenderer":{"title":"Let's Play","endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/channel/UCabcdefghijklmnopqrstuv/letsplay"}}}}}
	]}}}`)
	var root value.Value
	if err := json.Unmarshal(rootJSON, &root); err != nil {
		t.Fatal(err)
	}
	object, _ := root.Object()
	tabs := youtubeDiscoverAdvertisedTabs(object)
	if len(tabs) != 3 || tabs[0].ID != "videos" || tabs[0].Count != 123 || tabs[1].ID != "search" || tabs[2].ID != "letsplay" {
		t.Fatalf("tabs=%#v", tabs)
	}
}

type scriptedBrowseTransport struct {
	responses [][]byte
	calls     int
	visitors  []string
	mu        sync.Mutex
}

func (t *scriptedBrowseTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected page read")
}

func (t *scriptedBrowseTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	body, _ := io.ReadAll(request.Body)
	if strings.Contains(string(body), `"visitorData":"`) {
		start := strings.Index(string(body), `"visitorData":"`) + len(`"visitorData":"`)
		end := strings.Index(string(body)[start:], `"`)
		t.visitors = append(t.visitors, string(body)[start:start+end])
	}
	if t.calls >= len(t.responses) {
		return nil, errors.New("unexpected continuation")
	}
	response := t.responses[t.calls]
	t.calls++
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(response))}, nil
}

func TestYouTubeBrowseContinuationVisitorRotationAndLoop(t *testing.T) {
	transport := &scriptedBrowseTransport{responses: [][]byte{
		[]byte(`{"responseContext":{"visitorData":"visitor-2"},"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[{"videoRenderer":{"videoId":"bbbbbbbbbbb"}},{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"token-2"}}}}]}}]}`),
		[]byte(`{"responseContext":{"visitorData":"visitor-3"},"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[{"videoRenderer":{"videoId":"ccccccccccc"}},{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"token-2"}}}}]}}]}`),
	}}
	policy := youtubeRendererPolicy{kinds: youtubeRendererVideo}
	entries, err := StatefulContinuationEntries(
		[]Entry{{URL: "https://www.youtube.com/watch?v=aaaaaaaaaaa", ExtractorKey: "youtube", ID: "aaaaaaaaaaa"}},
		"token-1", "visitor-1",
		func(ctx context.Context, token, visitor string) ([]Entry, string, string, error) {
			return fetchYouTubeBrowseContinuation(ctx, transport, token, visitor, youtubePlaylistConfig{ClientVersion: "fixture"}, policy, "channel", categorizeYouTubeChannelError, nil)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := CollectEntries(context.Background(), entries, 10)
	if err != nil || len(got) != 3 {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	if transport.calls != 2 {
		t.Fatalf("loop should stop after repeated token: calls=%d", transport.calls)
	}
	if len(transport.visitors) != 2 || transport.visitors[0] != "visitor-1" || transport.visitors[1] != "visitor-2" {
		t.Fatalf("visitor rotation=%v", transport.visitors)
	}
}

type authBrowseTransport struct {
	cookies     []*http.Cookie
	response    []byte
	sawAuth     bool
	redirected  bool
	anonymousDo bool
}

func (t *authBrowseTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected page")
}
func (t *authBrowseTransport) Cookies(string) ([]*http.Cookie, error) { return t.cookies, nil }
func (t *authBrowseTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	t.anonymousDo = true
	return nil, errors.New("authenticated browse must not use anonymous Do")
}
func (t *authBrowseTransport) DoNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	if request.Header.Get("Authorization") == "" || request.Header.Get("Origin") != "https://www.youtube.com" {
		return nil, errors.New("missing auth headers")
	}
	t.sawAuth = true
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(t.response))}, nil
}

func TestYouTubeBrowseAuthenticatedContinuationNoAnonymousFallback(t *testing.T) {
	transport := &authBrowseTransport{
		cookies: youtubeAuthCookies(),
		response: []byte(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[
			{"videoRenderer":{"videoId":"bbbbbbbbbbb"}}
		]}}]}`),
	}
	auth := &youtubeBrowseAuth{
		config: &youtubeWEBAuthConfig{
			ClientName: "WEB", ClientID: "1", ClientVersion: "2.fixture", VisitorData: "auth-visitor",
			UserAgent: "ua", UserSessionID: "user-session", LoggedIn: true,
		},
		apiKey: "fixture-key",
		now:    func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}
	entries, token, visitor, err := fetchYouTubeBrowseContinuation(
		context.Background(), transport, "token-1", "anon-visitor",
		youtubePlaylistConfig{ClientVersion: "ignored"},
		youtubeRendererPolicy{kinds: youtubeRendererVideo},
		"channel", categorizeYouTubeChannelError, auth,
	)
	if err != nil || token != "" || visitor == "" || len(entries) != 1 || entries[0].ID != "bbbbbbbbbbb" {
		t.Fatalf("entries=%#v token=%q visitor=%q err=%v", entries, token, visitor, err)
	}
	if !transport.sawAuth || transport.anonymousDo {
		t.Fatalf("auth=%v anonymous=%v", transport.sawAuth, transport.anonymousDo)
	}
	// Missing cookies after authenticated state must fail closed.
	transport.cookies = nil
	_, _, _, err = fetchYouTubeBrowseContinuation(
		context.Background(), transport, "token-1", "anon-visitor",
		youtubePlaylistConfig{}, youtubeRendererPolicy{kinds: youtubeRendererVideo},
		"channel", categorizeYouTubeChannelError, auth,
	)
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected auth failure, got %v", err)
	}
}

func TestYouTubeRendererLazyPlaylistRace(t *testing.T) {
	response := []byte(`{"responseContext":{"visitorData":"v2"},"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[{"videoRenderer":{"videoId":"bbbbbbbbbbb"}}]}}]}`)
	responses := make([][]byte, 32)
	for i := range responses {
		responses[i] = response
	}
	transport := &scriptedBrowseTransport{responses: responses}
	entries, err := StatefulContinuationEntries(
		[]Entry{{URL: "https://www.youtube.com/watch?v=aaaaaaaaaaa", ExtractorKey: "youtube", ID: "aaaaaaaaaaa"}},
		"token-1", "v1",
		func(ctx context.Context, token, visitor string) ([]Entry, string, string, error) {
			return fetchYouTubeBrowseContinuation(ctx, transport, token, visitor, youtubePlaylistConfig{ClientVersion: "fixture"}, youtubeRendererPolicy{kinds: youtubeRendererVideo}, "channel", categorizeYouTubeChannelError, nil)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			got, err := CollectEntries(context.Background(), entries, 2)
			if err != nil {
				errs <- err
				return
			}
			if len(got) != 2 || got[0].ID != "aaaaaaaaaaa" || got[1].ID != "bbbbbbbbbbb" {
				errs <- errors.New("unexpected entries")
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestYouTubeChannelSearchRouteAndCancellation(t *testing.T) {
	page := []byte(`ytcfg.set({"INNERTUBE_API_KEY":"k","INNERTUBE_CLIENT_VERSION":"v","VISITOR_DATA":"vis"});ytInitialData={"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"title":"Search","endpoint":{"commandMetadata":{"webCommandMetadata":{"url":"/channel/UCabcdefghijklmnopqrstuv/search"}}},"content":{"sectionListRenderer":{"contents":[{"itemSectionRenderer":{"contents":[
		{"videoRenderer":{"videoId":"aaaaaaaaaaa","title":{"simpleText":"hit"}}},
		{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"next"}}}}
	]}}]}}}}]}},"metadata":{"channelMetadataRenderer":{"title":"Fixture","externalId":"UCabcdefghijklmnopqrstuv"}}};`)
	transport := &channelSearchTransport{page: page, continuation: []byte(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[{"videoRenderer":{"videoId":"bbbbbbbbbbb"}}]}}]}`)}
	result, err := NewYouTubeChannelTab().Extract(context.Background(), Request{
		URL: "https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/search?query=linear", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := CollectEntries(context.Background(), result.Entries, 5)
	if err != nil || len(got) != 2 || got[0].ID != "aaaaaaaaaaa" || got[1].ID != "bbbbbbbbbbb" {
		t.Fatalf("got=%#v err=%v", got, err)
	}
	title, _ := result.Info.Fields().Lookup("title").StringValue()
	if !strings.Contains(title, "Search - linear") {
		t.Fatalf("title=%q", title)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok, err := result.Entries.Iterator().Next(ctx)
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v %v", ok, err)
	}
}

type channelSearchTransport struct {
	page, continuation []byte
	calls              int
}

func (t *channelSearchTransport) ReadPage(_ context.Context, raw string) ([]byte, http.Header, error) {
	if !strings.Contains(raw, "/search?") || !strings.Contains(raw, "query=linear") {
		return nil, nil, errors.New("bad search url: " + raw)
	}
	return t.page, make(http.Header), nil
}
func (t *channelSearchTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	t.calls++
	if request.URL.Path != "/youtubei/v1/browse" {
		return nil, errors.New("bad path")
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(t.continuation))}, nil
}

func FuzzParseYouTubeRendererData(f *testing.F) {
	f.Add([]byte(`{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"selected":true,"content":{"videoRenderer":{"videoId":"aaaaaaaaaaa"}}}}]}}}`))
	f.Add([]byte(`{"lockupViewModel":{"contentType":"LOCKUP_CONTENT_TYPE_PLAYLIST","contentId":"PLfuzz"}}`))
	f.Add([]byte(`{"hashtagTileRenderer":{"onTapCommand":{"commandMetadata":{"webCommandMetadata":{"url":"/hashtag/x"}}}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		page, err := parseYouTubeRendererData(data, youtubeRendererPolicy{kinds: youtubeRendererSearchAll | youtubeRendererMusicBrowse})
		if err != nil {
			return
		}
		if page.continuation != "" && validYouTubeContinuationToken(page.continuation) != page.continuation {
			t.Fatalf("unsafe continuation %q", page.continuation)
		}
		for _, entry := range page.entries {
			if entry.URL == "" || strings.ContainsAny(entry.URL, "\x00\r\n") || strings.ContainsAny(entry.Title, "\x00") {
				t.Fatalf("unsafe entry %#v", entry)
			}
		}
	})
}
