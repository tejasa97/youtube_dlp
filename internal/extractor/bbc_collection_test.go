package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestBBCCollectionRoutingMatrix(t *testing.T) {
	registry := NewRegistry(
		NewBBCCoUkArticle(), NewBBCCoUkPlaylist(),
		NewBBCCoUkIPlayerEpisodes(), NewBBCCoUkIPlayerGroup(), NewBBCIPlayer(),
	)
	for _, test := range []struct {
		rawURL string
		want   string
	}{
		{"https://www.bbc.co.uk/programmes/articles/FixtureArticleId/not-your-typical-role-model", "bbc_co_uk_article"},
		{"https://www.bbc.co.uk/programmes/p0000000/clips", "bbc_co_uk_playlist"},
		{"https://www.bbc.co.uk/programmes/p0000000/episodes/player", "bbc_co_uk_playlist"},
		{"https://www.bbc.co.uk/iplayer/episodes/p0000000/fixture", "bbc_co_uk_iplayer_episodes"},
		{"https://www.bbc.co.uk/iplayer/group/p0000000", "bbc_co_uk_iplayer_group"},
		{"https://www.bbc.co.uk/iplayer/episode/p0000000/title", "bbciplayer"},
		{"https://bbc.co.uk/programmes/p0000000/player", "bbciplayer"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q) = %v, %v; want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func TestBBCCoUkArticleTransparentReentryAndCanonicalURLs(t *testing.T) {
	pageURL := "https://www.bbc.co.uk/programmes/articles/FixtureArticleId/title"
	transport := &bbcFixtureTransport{
		pages: map[string][]byte{pageURL: readRiskFixture(t, "bbciplayer", "article.html")},
	}
	result, err := NewBBCCoUkArticle().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	for _, entry := range entries {
		if entry.ExtractorKey != "bbciplayer" || !entry.Transparent {
			t.Fatalf("entry=%#v", entry)
		}
		if !strings.HasPrefix(entry.URL, bbcCanonicalOrigin+"/programmes/") {
			t.Fatalf("non-canonical url %q", entry.URL)
		}
		if strings.Contains(entry.URL, "?") || strings.Contains(entry.URL, "#") {
			t.Fatalf("url has query/fragment: %q", entry.URL)
		}
	}
	for _, header := range transport.headers {
		if bbcFixtureHasSensitiveHeader(header) {
			t.Fatalf("isolated article request leaked credentials: %#v", header)
		}
	}
}

func TestBBCCoUkPlaylistLazyPaginationAndReentry(t *testing.T) {
	base := "https://www.bbc.co.uk/programmes/p0000000/clips"
	page2 := "https://www.bbc.co.uk/programmes/p0000000/clips?page=2"
	transport := &bbcFixtureTransport{
		pages: map[string][]byte{
			base:  readRiskFixture(t, "bbciplayer", "programmes_playlist_page1.html"),
			page2: readRiskFixture(t, "bbciplayer", "programmes_playlist_page2.html"),
		},
	}
	result, err := NewBBCCoUkPlaylist().Extract(context.Background(), Request{URL: base, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("eager requests=%d", len(transport.requests))
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 4 {
		t.Fatalf("entries=%#v err=%v requests=%d", entries, err, len(transport.requests))
	}
	if entries[0].URL != bbcCanonicalProgrammeURL("p0000001") {
		t.Fatalf("entry url=%q", entries[0].URL)
	}
}

func TestBBCCoUkIPlayerEpisodesLazyGraphQL(t *testing.T) {
	transport := &bbcFixtureTransport{}
	transport.handler = bbcEpisodesGraphQLHandler(t, false)
	result, err := NewBBCCoUkIPlayerEpisodes().Extract(context.Background(), Request{URL: "https://www.bbc.co.uk/iplayer/episodes/p0000000/fixture", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("eager requests=%d", len(transport.requests))
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%#v error=%v", entries, err)
	}
	if entries[0].URL != bbcCanonicalEpisodeURL("p0000001") {
		t.Fatalf("entry url=%q", entries[0].URL)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests after iteration = %d", len(transport.requests))
	}
}

func TestBBCPlaylistIndependentIterators(t *testing.T) {
	source, err := newBBCReusablePlaylistSource(1, 10, 10, false, "", func(_ context.Context, pageIndex int) (bbcPlaylistPageResult, error) {
		return bbcPlaylistPageResult{
			entries:  []Entry{{ID: fmt.Sprintf("p%07d", pageIndex+1), ExtractorKey: "bbciplayer", Transparent: true}},
			lastPage: pageIndex >= 2,
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	first := source.Iterator()
	second := source.Iterator()
	entry1, ok, err := first.Next(context.Background())
	if err != nil || !ok || entry1.ID != "p0000001" {
		t.Fatalf("first iterator=%#v ok=%t err=%v", entry1, ok, err)
	}
	entry2, ok, err := second.Next(context.Background())
	if err != nil || !ok || entry2.ID != "p0000001" {
		t.Fatalf("second iterator=%#v ok=%t err=%v", entry2, ok, err)
	}
}

func TestBBCPlaylistExplicitPageDrainsBufferedEntries(t *testing.T) {
	transport := &bbcFixtureTransport{}
	transport.handler = bbcEpisodesGraphQLHandler(t, true)
	result, err := NewBBCCoUkIPlayerEpisodes().Extract(context.Background(), Request{
		URL: "https://www.bbc.co.uk/iplayer/episodes/p0000000?page=2", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	iterator := result.Entries.Iterator()
	entries := make([]Entry, 0, 2)
	for range 10 {
		entry, ok, err := iterator.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		entries = append(entries, entry)
	}
	if len(entries) != 2 {
		t.Fatalf("entries=%#v", entries)
	}
	_, ok, err := iterator.Next(context.Background())
	if err != nil || ok {
		t.Fatalf("extra entry ok=%t err=%v", ok, err)
	}
}

func TestBBCPlaylistTerminalPartialPage(t *testing.T) {
	source, err := newBBCReusablePlaylistSource(100, 10, 10, false, "", func(_ context.Context, pageIndex int) (bbcPlaylistPageResult, error) {
		if pageIndex > 0 {
			return bbcPlaylistPageResult{}, nil
		}
		return bbcPlaylistPageResult{
			entries: []Entry{
				{ID: "p0000001", ExtractorKey: "bbciplayer", Transparent: true},
				{ID: "p0000002", ExtractorKey: "bbciplayer", Transparent: true},
			},
			lastPage: true,
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), source, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
}

func TestBBCPlaylistABACycleDetection(t *testing.T) {
	pageA := []Entry{{ID: "p0000001", ExtractorKey: "bbciplayer", Transparent: true}, {ID: "p0000002", ExtractorKey: "bbciplayer", Transparent: true}}
	pageB := []Entry{{ID: "p0000003", ExtractorKey: "bbciplayer", Transparent: true}, {ID: "p0000004", ExtractorKey: "bbciplayer", Transparent: true}}
	calls := 0
	source, err := newBBCReusablePlaylistSource(2, 10, 10, false, bbcPageFingerprint(pageA), func(_ context.Context, pageIndex int) (bbcPlaylistPageResult, error) {
		calls++
		switch pageIndex {
		case 0:
			return bbcPlaylistPageResult{entries: pageA, lastPage: false}, nil
		case 1:
			return bbcPlaylistPageResult{entries: pageB, lastPage: false}, nil
		default:
			return bbcPlaylistPageResult{entries: pageA, lastPage: false}, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), source, 10)
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestBBCPlaylistMaxPagesAndEntries(t *testing.T) {
	source, err := newBBCReusablePlaylistSource(1, 2, 2, false, "", func(_ context.Context, pageIndex int) (bbcPlaylistPageResult, error) {
		return bbcPlaylistPageResult{
			entries:  []Entry{{ID: fmt.Sprintf("p%07d", pageIndex+1), ExtractorKey: "bbciplayer", Transparent: true}},
			lastPage: false,
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), source, 3)
	if !errors.Is(err, ErrPlaylistLimit) || len(entries) != 2 {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
}

func TestBBCPlaylistPageSizeOverflowFailsClosed(t *testing.T) {
	source, err := newBBCReusablePlaylistSource(1, 10, 10, false, "", func(_ context.Context, _ int) (bbcPlaylistPageResult, error) {
		return bbcPlaylistPageResult{
			entries: []Entry{
				{ID: "p0000001", ExtractorKey: "bbciplayer", Transparent: true},
				{ID: "p0000002", ExtractorKey: "bbciplayer", Transparent: true},
			},
			lastPage: true,
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), source, 10)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err=%v", err)
	}
}

func TestBBCEpisodesGraphQLMalformedNodeFailsClosed(t *testing.T) {
	transport := &bbcFixtureTransport{}
	transport.handler = func(_ context.Context, request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		var call struct {
			Variables struct {
				PerPage int `json:"perPage"`
			} `json:"variables"`
		}
		if json.Unmarshal(body, &call) == nil && call.Variables.PerPage == 1 {
			return bbcHTTPResponse(http.StatusOK, []byte(`{"data":{"programme":{"title":{"default":"Fixture"},"synopsis":{},"entities":{"results":[]}}}}`)), nil
		}
		return bbcHTTPResponse(http.StatusOK, []byte(`{"data":{"programme":{"entities":{"results":[{"episode":{"id":"bad!","subtitle":{"default":"Bad"}}}]}}}}`)), nil
	}
	result, err := NewBBCCoUkIPlayerEpisodes().Extract(context.Background(), Request{
		URL: "https://www.bbc.co.uk/iplayer/episodes/p0000000", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), result.Entries, 10)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err=%v", err)
	}
}

func TestBBCCollectionFailureCategoriesCancellationAndSecretSafety(t *testing.T) {
	pageURL := "https://www.bbc.co.uk/programmes/articles/FixtureArticleId/title"
	for _, test := range []struct {
		name      string
		body      string
		status    int
		want      error
		groupJSON bool
	}{
		{"auth-page", `Sign in to watch`, http.StatusUnauthorized, ErrAuthentication, false},
		{"geo-page", `only available in the UK`, http.StatusForbidden, ErrRegionRestricted, false},
		{"unavailable-page", `no longer available`, http.StatusNotFound, ErrUnavailable, false},
		{"malformed-group", `{"secret":"bbc-private-token"} trailing`, 0, ErrInvalidMetadata, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &bbcFixtureTransport{
				responses: map[string]bbcFixtureResponse{
					"GET " + pageURL: {status: test.status, body: []byte(test.body)},
				},
				handler: func(_ context.Context, request *http.Request) (*http.Response, error) {
					if !test.groupJSON {
						return nil, errors.New("unexpected handler call")
					}
					status := test.status
					if status == 0 {
						status = http.StatusOK
					}
					return bbcHTTPResponse(status, []byte(test.body)), nil
				},
			}
			_, err := NewBBCCoUkArticle().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
			if test.groupJSON {
				_, err = NewBBCCoUkIPlayerGroup().Extract(context.Background(), Request{
					URL: "https://www.bbc.co.uk/iplayer/group/p0000000", Transport: transport,
				})
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "bbc-private-token") {
				t.Fatalf("secret leaked: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewBBCCoUkPlaylist().Extract(ctx, Request{
		URL: "https://www.bbc.co.uk/programmes/p0000000/clips", Transport: &bbcFixtureTransport{wait: true},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func bbcEpisodesGraphQLHandler(t *testing.T, explicitTwo bool) func(context.Context, *http.Request) (*http.Response, error) {
	return func(_ context.Context, request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		var call struct {
			Variables struct {
				Page    int `json:"page"`
				PerPage int `json:"perPage"`
			} `json:"variables"`
		}
		if err := json.Unmarshal(body, &call); err != nil {
			t.Fatal(err)
		}
		if call.Variables.PerPage == 1 {
			return bbcHTTPResponse(http.StatusOK, []byte(`{"data":{"programme":{"title":{"default":"Fixture Series"},"synopsis":{"large":"Synthetic series"},"entities":{"results":[]}}}}`)), nil
		}
		if explicitTwo {
			return bbcHTTPResponse(http.StatusOK, []byte(`{"data":{"programme":{"entities":{"results":[{"episode":{"id":"p0000001","subtitle":{"default":"Episode One"}}},{"episode":{"id":"p0000002","subtitle":{"default":"Episode Two"}}}]}}}}`)), nil
		}
		if call.Variables.Page != 1 || call.Variables.PerPage != bbcEpisodesPageSize {
			t.Fatalf("playlist variables = %#v", call.Variables)
		}
		return bbcHTTPResponse(http.StatusOK, []byte(`{"data":{"programme":{"entities":{"results":[{"episode":{"id":"p0000001","subtitle":{"default":"Episode One"}}},{"episode":{"id":"p0000002","subtitle":{"default":"Episode Two"}}}]}}}}`)), nil
	}
}

func FuzzBBCProgrammesPlaylistRoute(f *testing.F) {
	f.Add("https://www.bbc.co.uk/programmes/p0000000/clips")
	f.Add("https://www.bbc.co.uk/iplayer/episodes/p0000000")
	f.Add("https://www.bbc.co.uk/programmes/articles/FixtureArticleId")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		matches := 0
		for _, extractor := range []Extractor{
			NewBBCCoUkArticle(), NewBBCCoUkPlaylist(),
			NewBBCCoUkIPlayerEpisodes(), NewBBCCoUkIPlayerGroup(), NewBBCIPlayer(),
		} {
			if extractor.Suitable(parsed) {
				matches++
			}
		}
		if matches > 1 {
			t.Fatalf("ambiguous route %q", raw)
		}
	})
}

func FuzzParseBBCGroupResponse(f *testing.F) {
	f.Add(readRiskFixture(f, "bbciplayer", "group_page.json"))
	f.Add([]byte(`{"group_episodes":{"elements":[]},"group":{"title":"t"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var response bbcGroupAPIResponse
		if json.Unmarshal(data, &response) == nil {
			_, _ = entriesFromBBCGroupPage(response)
		}
	})
}
