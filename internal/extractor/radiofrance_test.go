package extractor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type radioFranceFixtureTransport struct {
	mu          sync.Mutex
	pages       map[string][]byte
	responses   map[string]riskFixtureResponse
	handler     func(context.Context, *http.Request) (*http.Response, error)
	requests    []string
	noRedirects int
	wait        bool
}

func readRadioFranceFixture(t testing.TB, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(riskFixtureRoot, "radiofrance", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (transport *radioFranceFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.DoWithoutCredentialsNoRedirect(ctx, request)
}

func (transport *radioFranceFixtureTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	body, ok := transport.pages[rawURL]
	if !ok {
		return nil, nil, errors.New("unexpected Radio France page request")
	}
	return append([]byte(nil), body...), make(http.Header), nil
}

func (transport *radioFranceFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		if request.Header.Get(header) != "" {
			return nil, errors.New("credential leak")
		}
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Method+" "+request.URL.String())
	transport.noRedirects++
	handler := transport.handler
	response, ok := transport.responses[request.Method+" "+request.URL.String()]
	transport.mu.Unlock()
	if transport.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if handler != nil {
		return handler(ctx, request)
	}
	if !ok {
		if body, pageOK := transport.pages[request.URL.String()]; pageOK {
			return riskHTTPResponse(http.StatusOK, body), nil
		}
		return riskHTTPResponse(http.StatusNotFound, nil), nil
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	return riskHTTPResponse(status, response.body), nil
}

func TestRadioFranceRoutingMatrixAndNonOverlap(t *testing.T) {
	episodeURL := "https://www.radiofrance.fr/franceculture/podcasts/science-en-questions/la-physique-d-einstein-8440487"
	podcastURL := "https://www.radiofrance.fr/franceinfo/podcasts/le-billet-vert"
	profileURL := "https://www.radiofrance.fr/personnes/thomas-pesquet?p=3"
	scheduleURL := "https://www.radiofrance.fr/franceinter/grille-programmes?date=17-02-2023"
	liveURL := "https://www.radiofrance.fr/franceinter/"
	legacyURL := "http://maison.radiofrance.fr/radiovisions/one-one"

	cases := []struct {
		rawURL string
		want   string
	}{
		{episodeURL, "franceculture"},
		{podcastURL, "radiofrance_podcast"},
		{profileURL, "radiofrance_profile"},
		{scheduleURL, "radiofrance_program_schedule"},
		{liveURL, "radiofrance_live"},
		{legacyURL, "radiofrance"},
	}
	registry := NewRegistry(
		NewRadioFranceProgramSchedule(),
		NewFranceCulture(),
		NewRadioFrancePodcast(),
		NewRadioFranceProfile(),
		NewRadioFranceLive(),
		NewRadioFrance(),
	)
	for _, test := range cases {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q) = %v err=%v want %q", test.rawURL, selected, err, test.want)
		}
	}
	for _, rawURL := range []string{
		"https://www.radiofrance.fr/franceinter/podcasts/le-7-9-30/le-7-9-30-du-vendredi-10-mars-2023-2107675",
		"https://www.radiofrance.fr/mouv/radio-musique-kids-family",
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewFranceCulture().Suitable(parsed) && !NewRadioFranceLive().Suitable(parsed) {
			t.Fatalf("expected episode or live routing for %q", rawURL)
		}
	}
	rejected := []string{
		"https://example.com/franceinter/",
		"https://www.radiofrance.fr/franceinter/podcasts",
		"https://www.radiofrance.fr/personnes/",
		"https://www.radiofrance.fr/franceinter/grille-programmes/extra",
		"https://www.radiofrance.fr/franceinter/live",
		"https://user@www.radiofrance.fr/franceinter/",
		"https://www.radiofrance.fr:443/franceinter/",
		"ftp://www.radiofrance.fr/franceinter/",
	}
	for _, rawURL := range rejected {
		parsed, _ := url.Parse(rawURL)
		if NewRadioFranceLive().Suitable(parsed) || NewFranceCulture().Suitable(parsed) ||
			NewRadioFrancePodcast().Suitable(parsed) || NewRadioFranceProfile().Suitable(parsed) ||
			NewRadioFranceProgramSchedule().Suitable(parsed) || NewRadioFrance().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true", rawURL)
		}
	}
}

func TestRadioFranceLegacyFormatsAndMetadata(t *testing.T) {
	pageURL := "http://maison.radiofrance.fr/radiovisions/one-one"
	transport := &radioFranceFixtureTransport{
		pages: map[string][]byte{pageURL: readRadioFranceFixture(t, "radiovisions.html")},
	}
	result, err := NewRadioFrance().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	assertRiskString(t, result, "id", "one-one")
	assertRiskString(t, result, "title", "One to one")
	assertRiskString(t, result, "uploader", "Thomas Hercouët")
	formats, _ := result.Info.Formats()
	if len(formats) != 2 {
		t.Fatalf("formats = %#v", formats)
	}
	for _, formatValue := range formats {
		format, _ := formatValue.Object()
		isolated, ok := format.Lookup("_credential_isolated").Bool()
		if !ok || !isolated {
			t.Fatalf("missing credential isolation: %#v", format)
		}
	}
}

func TestFranceCultureEpisodeMetadata(t *testing.T) {
	pageURL := "https://www.radiofrance.fr/franceculture/podcasts/science-en-questions/la-physique-d-einstein-8440487"
	transport := &radioFranceFixtureTransport{
		pages: map[string][]byte{pageURL: readRadioFranceFixture(t, "episode.html")},
	}
	result, err := NewFranceCulture().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	assertRiskString(t, result, "id", "8440487")
	assertRiskString(t, result, "display_id", "la-physique-d-einstein")
	assertRiskString(t, result, "title", "La physique d'Einstein aiderait-elle à comprendre le cerveau ?")
	formats, _ := result.Info.Formats()
	if len(formats) != 1 {
		t.Fatalf("formats = %#v", formats)
	}
	format, _ := formats[0].Object()
	urlValue, _ := format.Lookup("url").StringValue()
	if urlValue != "https://audio-mp3.radiofrance.fr/fixture/episode-8440487.mp3" {
		t.Fatalf("url = %q", urlValue)
	}
}

func TestRadioFranceLiveStationAndSubstation(t *testing.T) {
	stationTransport := &radioFranceFixtureTransport{
		handler: func(_ context.Context, request *http.Request) (*http.Response, error) {
			if request.URL.Host == "www.radiofrance.fr" && strings.HasSuffix(request.URL.Path, "/api/live") {
				return riskHTTPResponse(http.StatusOK, readRadioFranceFixture(t, "live.json")), nil
			}
			return riskHTTPResponse(http.StatusNotFound, nil), nil
		},
	}
	stationResult, err := NewRadioFranceLive().Extract(context.Background(), Request{
		URL: "https://www.radiofrance.fr/franceinter/", Transport: stationTransport,
	})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := stationResult.Info.Formats()
	if len(formats) != 2 {
		t.Fatalf("station formats = %#v", formats)
	}
	substationURL := "https://www.radiofrance.fr/mouv/radio-musique-kids-family"
	substationTransport := &radioFranceFixtureTransport{
		handler: func(_ context.Context, request *http.Request) (*http.Response, error) {
			if request.URL.Host == "www.radiofrance.fr" && strings.Contains(request.URL.Path, "radio-musique-kids-family") {
				return riskHTTPResponse(http.StatusOK, readRadioFranceFixture(t, "substation.html")), nil
			}
			return riskHTTPResponse(http.StatusNotFound, nil), nil
		},
	}
	substationResult, err := NewRadioFranceLive().Extract(context.Background(), Request{
		URL: substationURL, Transport: substationTransport,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := substationResult.Info.Lookup("id").StringValue()
	if id != "mouv-radio-musique-kids-family" {
		t.Fatalf("id = %q", id)
	}
}

func TestRadioFrancePodcastLazyCursorAndDedupe(t *testing.T) {
	pageURL := "https://www.radiofrance.fr/franceinfo/podcasts/le-billet-vert"
	pathURL := radioFrancePathAPI + "?value=%2Ffranceinfo%2Fpodcasts%2Fle-billet-vert"
	page2URL := radioFranceAPIBase + "/api/v2.1/concepts/eaf6ef81-a980-4f1c-a7d1-8a75ecd54b17/expressions?pageCursor=cursor-page-2-secret"
	transport := &radioFranceFixtureTransport{
		responses: map[string]riskFixtureResponse{
			"GET " + pathURL:  {body: readRadioFranceFixture(t, "path_podcast.json")},
			"GET " + page2URL: {body: readRadioFranceFixture(t, "expressions_page2.json")},
		},
	}
	result, err := NewRadioFrancePodcast().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if transport.noRedirects != 1 {
		t.Fatalf("eager fetch redirects=%d", transport.noRedirects)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	if !entries[0].Transparent || entries[0].Title != "Episode Alpha" {
		t.Fatalf("first entry=%#v", entries[0])
	}
	iterator := result.Entries.Iterator()
	firstBatch, err := CollectEntries(context.Background(), staticEntriesFromIterator(iterator, 2), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstBatch) != 2 {
		t.Fatalf("partial=%#v", firstBatch)
	}
}

func staticEntriesFromIterator(iterator EntryIterator, limit int) EntrySequence {
	entries := make([]Entry, 0, limit)
	for len(entries) < limit {
		entry, ok, err := iterator.Next(context.Background())
		if err != nil || !ok {
			break
		}
		entries = append(entries, entry)
	}
	return StaticEntries(entries...)
}

func TestRadioFranceProfileCursorProgression(t *testing.T) {
	pageURL := "https://www.radiofrance.fr/personnes/thomas-pesquet"
	pathURL := radioFrancePathAPI + "?value=%2Fpersonnes%2Fthomas-pesquet"
	page2URL := radioFranceAPIBase + "/api/v2.1/taxonomy/86c62790-e481-11e2-9f7b-782bcb6744eb/documents?cursor=profile-cursor-secret&relation=personality"
	transport := &radioFranceFixtureTransport{
		responses: map[string]riskFixtureResponse{
			"GET " + pathURL:  {body: readRadioFranceFixture(t, "path_profile.json")},
			"GET " + page2URL: {body: readRadioFranceFixture(t, "documents_page2.json")},
		},
	}
	result, err := NewRadioFranceProfile().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
}

func TestRadioFranceProgramScheduleTransparentReentry(t *testing.T) {
	pageURL := "https://www.radiofrance.fr/franceinter/grille-programmes?date=17-02-2023"
	transport := &radioFranceFixtureTransport{
		pages: map[string][]byte{pageURL: readRadioFranceFixture(t, "schedule.html")},
	}
	result, err := NewRadioFranceProgramSchedule().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	if entries[0].ExtractorKey != "franceculture" || !entries[0].Transparent {
		t.Fatalf("entry=%#v", entries[0])
	}
	if entries[0].SeriesID != "concept-1" || entries[0].Series != "Fixture Concept" {
		t.Fatalf("series metadata=%#v", entries[0])
	}
	obj := entries[0].Object()
	seriesID, _ := obj.Lookup("series_id").StringValue()
	series, _ := obj.Lookup("series").StringValue()
	if seriesID != "concept-1" || series != "Fixture Concept" {
		t.Fatalf("object series_id=%q series=%q", seriesID, series)
	}
	registry := NewRegistry(NewRadioFranceProgramSchedule(), NewFranceCulture())
	selected, err := registry.SelectFor(entries[0].URL, entries[0].ExtractorKey)
	if err != nil || selected.Name() != "franceculture" {
		t.Fatalf("reentry=%v err=%v", selected, err)
	}
}

func TestRadioFranceScheduleQueryValidation(t *testing.T) {
	schedule := NewRadioFranceProgramSchedule()
	rejected := []string{
		"https://www.radiofrance.fr/franceinter/grille-programmes?date=17-02-2023&date=18-02-2023",
		"https://www.radiofrance.fr/franceinter/grille-programmes?date=",
		"https://www.radiofrance.fr/franceinter/grille-programmes?date=32-01-2023",
		"https://www.radiofrance.fr/franceinter/grille-programmes?date=29-02-2023",
		"https://www.radiofrance.fr/franceinter/grille-programmes?date=not-a-date",
		"https://www.radiofrance.fr/franceinter/grille-programmes?foo=bar",
		"https://www.radiofrance.fr/franceinter/grille-programmes?date=17-02-2023&foo=bar",
	}
	for _, rawURL := range rejected {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse %q: %v", rawURL, err)
		}
		if schedule.Suitable(parsed) {
			t.Fatalf("accepted hostile schedule URL %q", rawURL)
		}
	}
	accepted := []string{
		"https://www.radiofrance.fr/franceinter/grille-programmes",
		"https://www.radiofrance.fr/franceculture/grille-programmes?date=01-02-2023",
		"https://www.radiofrance.fr/franceinter/grille-programmes?date=17-02-2023",
	}
	for _, rawURL := range accepted {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse %q: %v", rawURL, err)
		}
		if !schedule.Suitable(parsed) {
			t.Fatalf("rejected valid schedule URL %q", rawURL)
		}
	}
}

func TestRadioFranceRejectsNonAttributableMediaHosts(t *testing.T) {
	for _, rawURL := range []string{
		"https://cdn.example.invalid/radiofrance/episode.mp3",
		"https://media.example.invalid/radiofrance/episode.mp3",
		"https://example.com/audio.mp3",
	} {
		if radioFranceValidMediaURL(rawURL) {
			t.Fatalf("accepted non-attributable media URL %q", rawURL)
		}
	}
	for _, rawURL := range []string{
		"https://audio-mp3.radiofrance.fr/fixture/episode.mp3",
		"https://icecast.radiofrance.fr/franceinter/live.m3u8",
		"https://maison.radiofrance.fr/media/one-one.ogg",
	} {
		if !radioFranceValidMediaURL(rawURL) {
			t.Fatalf("rejected attributable media URL %q", rawURL)
		}
	}
}

func TestRadioFranceFailureCategoriesCancellationAndSecretSafety(t *testing.T) {
	pageURL := "https://www.radiofrance.fr/franceinfo/podcasts/le-billet-vert"
	secret := "cursor-page-2-secret"
	for _, test := range []struct {
		name   string
		status int
		want   error
	}{
		{"auth", http.StatusUnauthorized, ErrAuthentication},
		{"forbidden", http.StatusForbidden, ErrRegionRestricted},
		{"notfound", http.StatusNotFound, ErrUnavailable},
		{"gone", http.StatusGone, ErrUnavailable},
		{"legal", http.StatusUnavailableForLegalReasons, ErrRegionRestricted},
	} {
		t.Run(test.name, func(t *testing.T) {
			pathURL := radioFrancePathAPI + "?value=%2Ffranceinfo%2Fpodcasts%2Fle-billet-vert"
			transport := &radioFranceFixtureTransport{
				responses: map[string]riskFixtureResponse{
					"GET " + pathURL: {status: test.status},
				},
			}
			_, err := NewRadioFrancePodcast().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v want %v", err, test.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &radioFranceFixtureTransport{wait: true}
	if _, err := NewRadioFrancePodcast().Extract(ctx, Request{URL: pageURL, Transport: transport}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err=%v", err)
	}
	if strings.Contains(secret, "secret") {
		_, err := NewRadioFrancePodcast().Extract(context.Background(), Request{
			URL: pageURL, Transport: &radioFranceFixtureTransport{
				responses: map[string]riskFixtureResponse{
					"GET " + radioFrancePathAPI + "?value=%2Ffranceinfo%2Fpodcasts%2Fle-billet-vert": {status: http.StatusBadRequest, body: []byte(`{"error":"bad"}`)},
				},
			},
		})
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Fatalf("secret leaked into error: %v", err)
		}
	}
}

func TestRadioFranceRequiresCredentialIsolation(t *testing.T) {
	_, err := NewFranceCulture().Extract(context.Background(), Request{
		URL: "https://www.radiofrance.fr/franceculture/podcasts/show/episode-100001",
		Transport: &struct {
			Transport
		}{},
	})
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("err=%v", err)
	}
}

func TestRadioFranceNilTransportFailsClosed(t *testing.T) {
	_, err := NewRadioFranceLive().Extract(context.Background(), Request{
		URL: "https://www.radiofrance.fr/franceinter/",
	})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err=%v", err)
	}
}

func TestRadioFranceRedirectRejection(t *testing.T) {
	transport := &radioFranceFixtureTransport{
		handler: func(_ context.Context, request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://www.radiofrance.fr/franceinter/"}},
				Body:       io.NopCloser(bytes.NewReader(nil)),
				Request:    request,
			}, nil
		},
	}
	_, err := NewRadioFranceLive().Extract(context.Background(), Request{
		URL: "https://www.radiofrance.fr/franceinter/", Transport: transport,
	})
	if err == nil {
		t.Fatal("expected redirect failure")
	}
}
