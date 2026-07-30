package extractor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type nrkFixtureResponse struct {
	status  int
	body    []byte
	headers http.Header
}

type nrkFamilyFixtureTransport struct {
	riskFixtureTransport
	nrkResponses  map[string]nrkFixtureResponse
	isolatedCalls int
	nrkHandler    func(context.Context, *http.Request) (*http.Response, error)
}

func (transport *nrkFamilyFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if request.Header.Get(header) != "" {
			return nil, ErrTransportIsolation
		}
	}
	transport.mu.Lock()
	transport.isolatedCalls++
	handler := transport.nrkHandler
	response, ok := transport.nrkResponses[request.Method+" "+request.URL.String()]
	wait := transport.wait
	transport.mu.Unlock()
	if wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if handler != nil {
		return handler(ctx, request)
	}
	if !ok {
		return riskHTTPResponse(http.StatusNotFound, nil), nil
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	headers := response.headers
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Header:     headers.Clone(),
		Body:       io.NopCloser(bytes.NewReader(response.body)),
		Request:    request,
	}, nil
}

func TestNRKFamilyRoutingPrecedence(t *testing.T) {
	registry := NewRegistry(
		NewNRKSkole(), NewNRKRadioPodkast(), NewNRKTVEpisode(), NewNRKTVEpisodes(),
		NewNRKTVDirekte(), NewNRKTVSeason(), NewNRKTVSeries(), NewNRKTV(), NewNRKPlaylist(), NewNRK(),
	)
	cases := []struct {
		raw  string
		want string
	}{
		{"https://www.nrk.no/skole/?mediaId=14099", "nrk_skole"},
		{"https://www.nrk.no/skole/?page=search&q=&mediaId=14099", "nrk_skole"},
		{"https://radio.nrk.no/podkast/fixture/l_96f4f1b0-de54-4e6a-b4f1-b0de54fe6af8", "nrk_radio_podkast"},
		{"https://tv.nrk.no/serie/hellums-kro/sesong/1/episode/2", "nrktv_episode"},
		{"https://tv.nrk.no/program/episodes/nytt-paa-nytt/69031", "nrktv_episodes"},
		{"https://tv.nrk.no/direkte/nrk1", "nrktv_direkte"},
		{"https://tv.nrk.no/serie/fixture/sesong/1", "nrktv_season"},
		{"https://tv.nrk.no/serie/fixture", "nrktv_series"},
		{"https://nrksuper.no/serie/fixture", "nrktv_series"},
		{"https://tv.nrk.no/program/MDDP12000117", "nrktv"},
		{"https://www.nrk.no/troms/fixture-article-1.12270763", "nrk_playlist"},
		{"nrk:MDDP12000117", "nrk"},
	}
	for _, tc := range cases {
		selected, err := registry.Select(tc.raw)
		if err != nil || selected.Name() != tc.want {
			t.Fatalf("Select(%q) = %v (%v), want %q", tc.raw, selected, err, tc.want)
		}
	}
	for _, raw := range []string{
		"https://www.nrk.no/?mediaId=14099",
		"https://www.nrk.no/skole/",
		"https://radio.nrk.no/podkast/l_96f4f1b0-de54-4e6a-b4f1-b0de54fe6af8",
		"https://nrksuper.no/program/MDDP12000117",
		"https://example.com/program/MDDP12000117",
		"https://tv.nrk.no/unknown",
		"ftp://tv.nrk.no/program/MDDP12000117",
		"https://www.nrk.no/video/PS*150533",
	} {
		parsed, _ := url.Parse(raw)
		if NewNRKSkole().Suitable(parsed) || NewNRKRadioPodkast().Suitable(parsed) || NewNRKTVEpisode().Suitable(parsed) ||
			NewNRKTVEpisodes().Suitable(parsed) || NewNRKTVDirekte().Suitable(parsed) || NewNRKTVSeason().Suitable(parsed) ||
			NewNRKTVSeries().Suitable(parsed) || NewNRKTV().Suitable(parsed) || NewNRKPlaylist().Suitable(parsed) || NewNRK().Suitable(parsed) {
			t.Fatalf("NRK family accepted %q", raw)
		}
	}
}

func TestNRKFamilyTransparentReentryUsesIsolatedPlayback(t *testing.T) {
	manifestURL := nrkAPIBase + "playback/manifest/program/MDDP12000117?preferredCdn=akamai"
	metadataURL := nrkAPIBase + "playback/metadata/program/MDDP12000117"
	transport := &nrkFamilyFixtureTransport{
		nrkResponses: map[string]nrkFixtureResponse{
			"GET " + manifestURL: {body: readRiskFixture(t, "nrk", "manifest.json")},
			"GET " + metadataURL: {body: readRiskFixture(t, "nrk", "metadata.json")},
		},
	}
	redirect, err := NewNRKTV().Extract(context.Background(), Request{
		URL: "https://tv.nrk.no/program/MDDP12000117", Transport: transport,
	})
	if err != nil || !redirect.IsURL() || redirect.Redirect.URL != "nrk:MDDP12000117" {
		t.Fatalf("redirect=%#v err=%v", redirect.Redirect, err)
	}
	registry := NewRegistry(NewNRKTV(), NewNRK())
	media, err := registry.SelectFor(redirect.Redirect.URL, redirect.Redirect.ExtractorKey)
	if err != nil || media.Name() != "nrk" {
		t.Fatalf("media=%v err=%v", media, err)
	}
	result, err := media.Extract(context.Background(), Request{URL: redirect.Redirect.URL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	assertRiskString(t, result, "title", "Fixture NRK Programme")
	if transport.isolatedCalls < 2 {
		t.Fatalf("isolated calls=%d", transport.isolatedCalls)
	}
	formats, _ := result.Info.Formats()
	if len(formats) == 0 {
		t.Fatal("missing formats")
	}
	format, _ := formats[0].Object()
	if isolated, _ := format.Lookup("_credential_isolated").Bool(); !isolated {
		t.Fatalf("format missing credential isolation: %#v", format)
	}
}

func TestNRKSkoleMediaIDQueryParsing(t *testing.T) {
	pinned := "https://www.nrk.no/skole/?page=search&q=&mediaId=14099"
	parsed, _ := url.Parse(pinned)
	if mediaID, ok := nrkSkoleMediaID(parsed); !ok || mediaID != "14099" {
		t.Fatalf("pinned mediaId=%q ok=%v", mediaID, ok)
	}
	if !nrkSkoleSuitable(parsed) {
		t.Fatalf("pinned URL rejected: %q", pinned)
	}
	for _, raw := range []string{
		"https://www.nrk.no/?mediaId=14099",
		"https://www.nrk.no/skole/",
		"https://www.nrk.no/skole/?mediaId=",
		"https://www.nrk.no/skole/?mediaId=bad",
		"https://www.nrk.no/skole/?mediaId=14099&mediaId=14098",
		"https://www.nrk.no/skole/?page=%ZZ&mediaId=14099",
	} {
		parsed, _ := url.Parse(raw)
		if nrkSkoleSuitable(parsed) {
			t.Fatalf("Suitable(%q) = true", raw)
		}
	}
	transport := &nrkFamilyFixtureTransport{
		nrkResponses: map[string]nrkFixtureResponse{
			"GET " + nrkSkoleAPIBase + "14099": {body: readRiskFixture(t, "nrk", "skole_media.json")},
		},
	}
	for _, raw := range []string{
		"https://www.nrk.no/skole/?mediaId=14099",
		pinned,
	} {
		redirect, err := NewNRKSkole().Extract(context.Background(), Request{URL: raw, Transport: transport})
		if err != nil || !redirect.IsURL() || redirect.Redirect.URL != "nrk:MDDP12000117" {
			t.Fatalf("Extract(%q) redirect=%#v err=%v", raw, redirect.Redirect, err)
		}
	}
}

func TestNRKRadioPodkastRequiresSeriesSegment(t *testing.T) {
	id := "l_96f4f1b0-de54-4e6a-b4f1-b0de54fe6af8"
	parsed, _ := url.Parse("https://radio.nrk.no/podkast/" + id)
	if nrkRadioPodkastSuitable(parsed) {
		t.Fatal("two-segment podkast URL accepted")
	}
	redirect, err := NewNRKRadioPodkast().Extract(context.Background(), Request{
		URL: "https://radio.nrk.no/podkast/fixture/" + id,
	})
	if err != nil || !redirect.IsURL() || redirect.Redirect.URL != "nrk:"+id {
		t.Fatalf("redirect=%#v err=%v", redirect.Redirect, err)
	}
}

func TestNRKTVEpisodeRedirectAndPageData(t *testing.T) {
	start := "https://tv.nrk.no/serie/hellums-kro/sesong/1/episode/2"
	redirected := "https://tv.nrk.no/serie/hellums-kro/sesong/1/episode/2/avspiller"
	transport := &nrkFamilyFixtureTransport{
		nrkHandler: func(_ context.Context, request *http.Request) (*http.Response, error) {
			switch request.URL.String() {
			case start:
				return &http.Response{
					StatusCode: http.StatusFound,
					Header:     http.Header{"Location": {"/serie/hellums-kro/sesong/1/episode/2/avspiller"}},
					Body:       io.NopCloser(strings.NewReader("")),
					Request:    request,
				}, nil
			case redirected:
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewReader(readRiskFixture(t, "nrk", "episode_page.html"))),
					Request:    request,
				}, nil
			default:
				return riskHTTPResponse(http.StatusNotFound, nil), nil
			}
		},
	}
	redirect, err := NewNRKTVEpisode().Extract(context.Background(), Request{URL: start, Transport: transport})
	if err != nil || !redirect.IsURL() || redirect.Redirect.URL != "nrk:MUHH36005220" {
		t.Fatalf("redirect=%#v err=%v", redirect.Redirect, err)
	}
}

func TestNRKTVEpisodeRejectsHostileAndNilBodyRedirects(t *testing.T) {
	start := "https://tv.nrk.no/serie/hellums-kro/sesong/1/episode/2"
	for name, handler := range map[string]func(context.Context, *http.Request) (*http.Response, error){
		"hostile": func(_ context.Context, request *http.Request) (*http.Response, error) {
			if request.URL.String() != start {
				return riskHTTPResponse(http.StatusNotFound, nil), nil
			}
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": {"https://evil.example.test/steal"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    request,
			}, nil
		},
		"nil-body": func(_ context.Context, _ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			transport := &nrkFamilyFixtureTransport{nrkHandler: handler}
			_, err := NewNRKTVEpisode().Extract(context.Background(), Request{URL: start, Transport: transport})
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestNRKHTMLPageStatusCategories(t *testing.T) {
	articleURL := "https://www.nrk.no/troms/fixture-article-1.12270763"
	secretBody := []byte(`{"secret":"nrk-private-token"}`)
	for _, test := range []struct {
		name string
		code int
		want error
	}{
		{"auth", http.StatusUnauthorized, ErrAuthentication},
		{"geo-forbidden", http.StatusForbidden, ErrRegionRestricted},
		{"geo-legal", http.StatusUnavailableForLegalReasons, ErrRegionRestricted},
		{"unavailable", http.StatusNotFound, ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &nrkFamilyFixtureTransport{
				nrkResponses: map[string]nrkFixtureResponse{
					"GET " + articleURL: {status: test.code, body: secretBody},
				},
			}
			_, err := NewNRKPlaylist().Extract(context.Background(), Request{URL: articleURL, Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
			var status *HTTPStatusError
			if errors.As(err, &status) {
				t.Fatalf("leaked HTTPStatusError: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "nrk-private-token") {
				t.Fatalf("secret leaked: %v", err)
			}
		})
	}
	t.Run("read-failure", func(t *testing.T) {
		transport := &nrkFamilyFixtureTransport{
			nrkHandler: func(_ context.Context, request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(nrkErrReader{}),
					Request:    request,
				}, nil
			},
		}
		_, err := NewNRKPlaylist().Extract(context.Background(), Request{URL: articleURL, Transport: transport})
		if !errors.Is(err, ErrNRKHTMLNetwork) {
			t.Fatalf("error=%v want=%v", err, ErrNRKHTMLNetwork)
		}
	})
	t.Run("transport-failure", func(t *testing.T) {
		transport := &nrkFamilyFixtureTransport{
			nrkHandler: func(context.Context, *http.Request) (*http.Response, error) {
				return nil, errors.New("nrk-private-network-detail")
			},
		}
		_, err := NewNRKPlaylist().Extract(context.Background(), Request{URL: articleURL, Transport: transport})
		if !errors.Is(err, ErrNRKHTMLNetwork) {
			t.Fatalf("error=%v want=%v", err, ErrNRKHTMLNetwork)
		}
		if strings.Contains(err.Error(), "nrk-private-network-detail") {
			t.Fatalf("transport detail leaked: %v", err)
		}
	})
}

type nrkErrReader struct{}

func (nrkErrReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestNRKHTMLPlaylistsUseURLIdentifiersAndIsolatedPages(t *testing.T) {
	episodesURL := "https://tv.nrk.no/program/episodes/nytt-paa-nytt/69031"
	articleURL := "https://www.nrk.no/troms/fixture-article-1.12270763"
	transport := &nrkFamilyFixtureTransport{
		nrkResponses: map[string]nrkFixtureResponse{
			"GET " + episodesURL: {body: readRiskFixture(t, "nrk", "episodes_page.html")},
			"GET " + articleURL:  {body: readRiskFixture(t, "nrk", "playlist_article.html")},
		},
	}
	episodes, err := NewNRKTVEpisodes().Extract(context.Background(), Request{URL: episodesURL, Transport: transport})
	if err != nil || !episodes.IsPlaylist() {
		t.Fatalf("episodes=%#v err=%v", episodes, err)
	}
	if id, _ := episodes.Info.Lookup("id").StringValue(); id != "69031" {
		t.Fatalf("episodes id=%q", id)
	}
	article, err := NewNRKPlaylist().Extract(context.Background(), Request{URL: articleURL, Transport: transport})
	if err != nil || !article.IsPlaylist() {
		t.Fatalf("article=%#v err=%v", article, err)
	}
	if id, _ := article.Info.Lookup("id").StringValue(); id != "fixture-article-1.12270763" {
		t.Fatalf("article id=%q", id)
	}
	if transport.isolatedCalls != 2 {
		t.Fatalf("isolated calls=%d", transport.isolatedCalls)
	}
}

func TestNRKRichWidgetOverflowFailsAtBoundary(t *testing.T) {
	page := strings.Repeat(`<div class="rich" data-video-id="MDDP12000117"></div>`, 65)
	transport := &nrkFamilyFixtureTransport{
		nrkResponses: map[string]nrkFixtureResponse{
			"GET https://www.nrk.no/troms/fixture-article-1.12270763": {body: []byte(page)},
		},
	}
	_, err := NewNRKPlaylist().Extract(context.Background(), Request{
		URL: "https://www.nrk.no/troms/fixture-article-1.12270763", Transport: transport,
	})
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("overflow error=%v", err)
	}
	page64 := strings.Repeat(`<div class="rich" data-video-id="MDDP12000117"></div>`, 64)
	transport.nrkResponses["GET https://www.nrk.no/troms/fixture-article-1.12270763"] = nrkFixtureResponse{body: []byte(page64)}
	result, err := NewNRKPlaylist().Extract(context.Background(), Request{
		URL: "https://www.nrk.no/troms/fixture-article-1.12270763", Transport: transport,
	})
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("64-widget playlist=%#v err=%v", result, err)
	}
}

func TestNRKSeriesCatalogDedupesAndRejectsHostileSeasonLinks(t *testing.T) {
	root := map[string]any{
		"_embedded": map[string]any{
			"instalments": []any{map[string]any{"prfId": "MDDP12000117", "title": "Episode One"}},
			"extraMaterial": []any{
				map[string]any{"prfId": "MDDP12000117", "title": "Duplicate"},
				map[string]any{"prfId": "MDDP12000217", "title": "Extra"},
			},
		},
		"_links": map[string]any{
			"seasons": []any{
				map[string]any{"name": "1", "title": "Sesong 1"},
				map[string]any{"href": "https://evil.example.test/serie/fixture/sesong/2", "title": "Hostile"},
				map[string]any{"href": "/serie/fixture/sesong/3", "title": "Relative"},
			},
		},
	}
	entries, _, _, _, err := parseNRKSeriesCatalog(root, nrkTarget{series: "fixture", domain: "tv", serieKind: "serie"})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int{}
	for _, entry := range entries {
		if entry.ID != "" {
			ids[entry.ID]++
		}
	}
	if ids["MDDP12000117"] != 1 || ids["MDDP12000217"] != 1 {
		t.Fatalf("duplicate ids=%v entries=%#v", ids, entries)
	}
	foundRelative := false
	for _, entry := range entries {
		if strings.HasSuffix(entry.URL, "/sesong/3") {
			foundRelative = true
		}
		if strings.Contains(entry.URL, "evil.example.test") {
			t.Fatalf("hostile season accepted: %q", entry.URL)
		}
	}
	if !foundRelative {
		t.Fatalf("missing relative season entry: %#v", entries)
	}
}

func TestNRKCatalogCursorHardeningAndIteratorReuse(t *testing.T) {
	if _, err := validateNRKCatalogCursor("http://psapi.nrk.no/tv/catalog/series/fixture"); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("http cursor accepted: %v", err)
	}
	if _, err := validateNRKCatalogCursor("https://psapi.nrk.no/tv/catalog/series/fixture#frag"); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("fragment cursor accepted: %v", err)
	}
	firstURL := nrkAPIBase + "tv/catalog/series/fixture/seasons/1?pageSize=50"
	loopURL := nrkAPIBase + "tv/catalog/series/fixture/seasons/1?page=2"
	transport := &nrkFamilyFixtureTransport{
		nrkResponses: map[string]nrkFixtureResponse{
			"GET " + firstURL: {body: []byte(`{"titles":{"title":"Fixture Season"},"_embedded":{"instalments":[{"prfId":"MDDP12000117"}]},"_links":{"next":{"href":"/tv/catalog/series/fixture/seasons/1?page=2"}}}`)},
			"GET " + loopURL:  {body: []byte(`{"_links":{"next":{"href":"/tv/catalog/series/fixture/seasons/1?page=2"}}}`)},
		},
	}
	result, err := NewNRKTVSeason().Extract(context.Background(), Request{
		URL: "https://tv.nrk.no/serie/fixture/sesong/1", Transport: transport,
	})
	if err != nil || !result.IsPlaylist() {
		t.Fatal(err)
	}
	first := result.Entries.Iterator()
	second := result.Entries.Iterator()
	entryA, okA, errA := first.Next(context.Background())
	entryB, okB, errB := second.Next(context.Background())
	if errA != nil || errB != nil || !okA || !okB || entryA.ID != entryB.ID {
		t.Fatalf("iterators=%#v %#v errors=%v %v", entryA, entryB, errA, errB)
	}
	if _, ok, err := first.Next(context.Background()); ok || err != nil {
		t.Fatalf("loop next=%v err=%v", ok, err)
	}
}

func TestNRKPlaylistCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &nrkFamilyFixtureTransport{riskFixtureTransport: riskFixtureTransport{wait: true}}
	_, err := NewNRKPlaylist().Extract(ctx, Request{
		URL: "https://www.nrk.no/troms/fixture-article-1.12270763", Transport: transport,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func FuzzNRKFamilyRouting(f *testing.F) {
	f.Add("https://tv.nrk.no/program/MDDP12000117")
	f.Add("https://tv.nrk.no/serie/fixture/sesong/1")
	f.Add("nrk:MDDP12000117")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 4096 {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_ = NewNRKTV().Suitable(parsed)
		_ = NewNRKTVSeason().Suitable(parsed)
		_ = NewNRKTVSeries().Suitable(parsed)
		_ = NewNRK().Suitable(parsed)
	})
}
