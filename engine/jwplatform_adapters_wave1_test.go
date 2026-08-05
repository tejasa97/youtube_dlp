package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/network"
)

type fixtureHTTP struct {
	status int
	body   []byte
}

func jwWave1ProductFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "internal", "extractor", "testdata", "jwplatform_adapters_wave1", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestProductRegistryJWPlatformAdaptersWave1Routing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		rawURL string
		name   string
	}{
		{"https://www.bundesliga.com/en/bundesliga/videos?vid=AbCd1234", "bundesliga"},
		{"https://uk.businessinsider.com/article-slug", "businessinsider"},
		{"https://www.dagbladet.no/video/slug/PalfB2Cw", "dbtv"},
		{"https://www.hollywoodreporter.com/video/slug/", "hollywoodreporter"},
		{"https://www.iltalehti.fi/ulkomaat/a/9fbd067f-94e4-46cd-8748-9d958eb4dae2", "iltalehti"},
		{"https://video.lefigaro.fr/embed/figaro/video/slug/", "lefigarovideoembed"},
		{"https://www.mirror.co.uk/tv/tv-news/article-27163139", "mirrorcouk"},
		{"http://www.outsidetv.com/home/play/ZjQYboH6/1/10/Hdg0jukV/4", "outsidetv"},
		{"https://theintercept.com/fieldofvision/slug/", "theintercept"},
	}
	registry := productRegistry()
	for _, test := range tests {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.name {
			t.Fatalf("Select(%q)=%v err=%v want %q", test.rawURL, selected, err, test.name)
		}
	}
}

// jwWave1OperationRoundTripper is an http.RoundTripper that serves a
// configured page set (used by site adapter page reads) and a separate
// configured response set (used by the JW Platform backend extractor). It
// supports the full operation.process path, including transparent recursion.
type jwWave1OperationRoundTripper struct {
	pages     map[string][]byte
	responses map[string]fixtureHTTP
	calls     []string
}

func (rt *jwWave1OperationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	rt.calls = append(rt.calls, request.URL.String())
	if page, ok := rt.pages[request.URL.String()]; ok {
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(bytes.NewReader(page)), Request: request,
		}, nil
	}
	if response, ok := rt.responses[request.URL.String()]; ok {
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(response.body)),
			Request:    request,
		}, nil
	}
	return nil, errors.New("unexpected fixture request: " + request.URL.String())
}

// jwWave1RunProductProcess drives the article URL through the full product
// operation with SkipDownload so the operation recurses adapter → JW Platform
// and returns final metadata via result.InfoJSON.
func jwWave1RunProductProcess(t *testing.T, articleURL string, rt *jwWave1OperationRoundTripper) Result {
	t.Helper()
	transport, err := network.New(network.Config{RoundTripper: rt})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{URL: articleURL, SkipDownload: true}
	op := &operation{
		client:    newBroadTestClient(),
		request:   request,
		transport: transport,
		registry:  productRuntime(),
	}
	result, err := op.process(context.Background(), articleURL, "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatalf("operation.process(%q): %v", articleURL, err)
	}
	return result
}

func TestProductMirrorCoUKReentersJWPlatformMediaID(t *testing.T) {
	t.Parallel()
	const (
		mirrorURL  = "https://www.mirror.co.uk/tv/tv-news/love-island-fans-baffled-after-27163139"
		jwEndpoint = "https://cdn.jwplayer.com/v2/media/voyyS7SV"
	)
	rt := &jwWave1OperationRoundTripper{
		pages: map[string][]byte{mirrorURL: jwWave1ProductFixture(t, "mirror_page.html")},
		responses: map[string]fixtureHTTP{
			jwEndpoint: {body: []byte(`{"title":"Mirror Fixture","playlist":[{"mediaid":"voyyS7SV","title":"Mirror Fixture","sources":[{"file":"https://media.example/jw/master.m3u8","type":"application/x-mpegURL"},{"file":"https://media.example/jw/video.mp4","height":720,"bitrate":1500}]}]}`)},
		},
	}
	result := jwWave1RunProductProcess(t, mirrorURL, rt)
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["id"] != "voyyS7SV" {
		t.Fatalf("final id = %#v", metadata["id"])
	}
	if !slices.Contains(rt.calls, jwEndpoint) {
		t.Fatalf("JW endpoint %q never reached; calls=%v", jwEndpoint, rt.calls)
	}
	if result.Extractor != "jwplatform" {
		t.Fatalf("final extractor = %q", result.Extractor)
	}
}

// TestProductBusinessInsiderArticleIDSurvivesJWReentry runs BusinessInsider
// through the full product operation and asserts the article slug survives
// recursion as the final media id, while backend-only fields (formats) also
// flow through to prove recursion reached the JW backend.
func TestProductBusinessInsiderArticleIDSurvivesJWReentry(t *testing.T) {
	t.Parallel()
	const (
		articleURL = "https://uk.businessinsider.com/how-much-radiation-youre-exposed-to-in-everyday-life-2016-6"
		jwMediaID  = "AbCd1234"
		jwEndpoint = "https://cdn.jwplayer.com/v2/media/" + jwMediaID
	)
	rt := &jwWave1OperationRoundTripper{
		pages: map[string][]byte{articleURL: jwWave1ProductFixture(t, "businessinsider_page.html")},
		responses: map[string]fixtureHTTP{
			jwEndpoint: {body: []byte(`{"title":"BI Fixture","playlist":[{"mediaid":"` + jwMediaID + `","title":"BI Fixture","sources":[{"file":"https://media.example/jw/master.m3u8","type":"application/x-mpegURL"}]}]}`)},
		},
	}
	result := jwWave1RunProductProcess(t, articleURL, rt)
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	articleSlug := "how-much-radiation-youre-exposed-to-in-everyday-life-2016-6"
	if metadata["id"] != articleSlug {
		t.Fatalf("final id = %#v want %q", metadata["id"], articleSlug)
	}
	if !slices.Contains(rt.calls, jwEndpoint) {
		t.Fatalf("JW endpoint %q never reached; calls=%v", jwEndpoint, rt.calls)
	}
	if result.Extractor != "jwplatform" {
		t.Fatalf("final extractor = %q", result.Extractor)
	}
	// Backend-only media metadata (formats) must flow through, proving the
	// recursion reached the JW backend. The fixture supplies one HLS source.
	formats, ok := metadata["formats"].([]any)
	if !ok || len(formats) == 0 {
		t.Fatalf("backend formats missing: %#v", metadata["formats"])
	}
}

// TestProductLeFigaroPosterAndTitleSurviveJWReentry asserts the producer
// title and HTTPS poster win through the full product recursion even when
// the JW backend reports a different title and image. The test fails if the
// transparent overlay is removed because the backend-only title would win.
func TestProductLeFigaroPosterAndTitleSurviveJWReentry(t *testing.T) {
	t.Parallel()
	const (
		articleURL = "https://video.lefigaro.fr/embed/figaro/video/les-francais-ne-veulent-ils-plus-travailler/"
		jwMediaID  = "g9j7Eovo"
		jwEndpoint = "https://cdn.jwplayer.com/v2/media/" + jwMediaID
	)
	rt := &jwWave1OperationRoundTripper{
		pages: map[string][]byte{articleURL: jwWave1ProductFixture(t, "lefigaro_page.html")},
		responses: map[string]fixtureHTTP{
			jwEndpoint: {body: []byte(`{"title":"Backend Title","playlist":[{"mediaid":"` + jwMediaID + `","title":"Backend Title","image":"https://media.example/jw/poster.jpg","sources":[{"file":"https://media.example/jw/master.m3u8","type":"application/x-mpegURL"}]}]}`)},
		},
	}
	result := jwWave1RunProductProcess(t, articleURL, rt)
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["title"] != "Le Figaro Fixture" {
		t.Fatalf("producer title lost: got %#v", metadata["title"])
	}
	if metadata["thumbnail"] != "https://images.example/lefigaro.jpg" {
		t.Fatalf("producer poster lost: got %#v", metadata["thumbnail"])
	}
	if !slices.Contains(rt.calls, jwEndpoint) {
		t.Fatalf("JW endpoint %q never reached; calls=%v", jwEndpoint, rt.calls)
	}
	if result.Extractor != "jwplatform" {
		t.Fatalf("final extractor = %q", result.Extractor)
	}
	// Sanity: backend-only media metadata (sources/formats) remains. The JW
	// backend supplied one HLS source; we expect at least one format to be
	// emitted, proving the recursion reached JW.
	formats, ok := metadata["formats"].([]any)
	if !ok || len(formats) == 0 {
		t.Fatalf("backend formats missing: %#v", metadata["formats"])
	}
}

// TestProductTheInterceptProducerMetadataSurvivesJWReentry asserts the
// producer id, title, and timestamp survive The Intercept's nested URL
// handoff into the JW Platform backend via the full product operation. The
// test fails if the transparent overlay is removed because the backend-only
// title would replace "Episode Four".
func TestProductTheInterceptProducerMetadataSurvivesJWReentry(t *testing.T) {
	t.Parallel()
	const (
		articleURL = "https://theintercept.com/fieldofvision/thisisacoup-episode-four-surrender-or-die/"
		jwMediaID  = "AbCd1234"
		jwEndpoint = "https://cdn.jwplayer.com/v2/media/" + jwMediaID
	)
	rt := &jwWave1OperationRoundTripper{
		pages: map[string][]byte{articleURL: jwWave1ProductFixture(t, "theintercept_page.html")},
		responses: map[string]fixtureHTTP{
			jwEndpoint: {body: []byte(`{"title":"Backend Title","playlist":[{"mediaid":"` + jwMediaID + `","title":"Backend Title","sources":[{"file":"https://media.example/jw/master.m3u8","type":"application/x-mpegURL"}]}]}`)},
		},
	}
	result := jwWave1RunProductProcess(t, articleURL, rt)
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["id"] != "46214" {
		t.Fatalf("producer id lost: got %#v", metadata["id"])
	}
	if metadata["title"] != "Episode Four" {
		t.Fatalf("producer title lost: got %#v", metadata["title"])
	}
	ts, ok := metadata["timestamp"].(float64)
	if !ok || ts != 1450440039 {
		t.Fatalf("producer timestamp lost: got %#v want 1450440039", metadata["timestamp"])
	}
	if !slices.Contains(rt.calls, jwEndpoint) {
		t.Fatalf("JW endpoint %q never reached; calls=%v", jwEndpoint, rt.calls)
	}
	if result.Extractor != "jwplatform" {
		t.Fatalf("final extractor = %q", result.Extractor)
	}
	formats, ok := metadata["formats"].([]any)
	if !ok || len(formats) == 0 {
		t.Fatalf("backend formats missing: %#v", metadata["formats"])
	}
}
