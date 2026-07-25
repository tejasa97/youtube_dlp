package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAnvatoFixedAuthVectorAndSuccess(t *testing.T) {
	t.Parallel()
	accessKey := fox9AnvatoAccessKey
	videoID := "8032455"
	rawURL := "anvato:" + accessKey + ":" + videoID
	const (
		serverTime   = int64(1700000000)
		wantAdstAuth = "APVytK5DkP4=" // pinned-reference short-key XOR vector
		videoDataURL = "https://tkx.mp.lura.live/rest/v2/mcp/video/8032455?anvack=anvato_epfox_app_web_prod_b3373168e12f423f41504f207000188daf88251b"
	)
	if got := anvatoAdstAuth(videoDataURL, serverTime); got != wantAdstAuth {
		t.Fatalf("anvatoAdstAuth=%q want %q (pinned Python short-key XOR vector)", got, wantAdstAuth)
	}

	serverEndpoint := anvatoAPIBase + "/server_time?anvack=" + url.QueryEscape(accessKey)
	query := url.Values{}
	query.Set("anvack", accessKey)
	query.Set("X-Anvato-Adst-Auth", wantAdstAuth)
	query.Set("rtyp", "fp")
	videoEndpoint := anvatoAPIBase + "/mcp/video/" + videoID + "?" + query.Encode()
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		serverEndpoint: {body: []byte(`{"server_time":1700000000}`)},
		videoEndpoint:  {body: familyFixture(t, "anvato", "video.json")},
	}}
	result, err := NewAnvato().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	id, ok := result.Info.ID()
	if !ok || id != videoID {
		t.Fatalf("id=%q ok=%t", id, ok)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) == 0 {
		t.Fatal("missing formats")
	}
	// Wrong algorithm would miss the fixture endpoint and fail Extract.
	for _, req := range transport.requests {
		if strings.Contains(req, "X-Anvato-Adst-Auth=") && !strings.Contains(req, url.QueryEscape(wantAdstAuth)) && !strings.Contains(req, wantAdstAuth) {
			t.Fatalf("request missing fixed auth vector: %q", req)
		}
	}
}

func TestAnvatoNegativeMatrix(t *testing.T) {
	t.Parallel()
	rawURL := "anvato:" + fox9AnvatoAccessKey + ":8032455"
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewAnvato().Extract(canceled, Request{URL: rawURL, Transport: &sharedFixtureTransport{}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	serverEndpoint := anvatoAPIBase + "/server_time?anvack=" + url.QueryEscape(fox9AnvatoAccessKey)
	const wantAdstAuth = "APVytK5DkP4="
	query := url.Values{}
	query.Set("anvack", fox9AnvatoAccessKey)
	query.Set("X-Anvato-Adst-Auth", wantAdstAuth)
	query.Set("rtyp", "fp")
	videoEndpoint := anvatoAPIBase + "/mcp/video/8032455?" + query.Encode()

	authTransport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		serverEndpoint: {body: []byte(`{"server_time":1700000000}`)},
		videoEndpoint:  {status: http.StatusUnauthorized, body: []byte("token=must-not-leak")},
	}}
	if _, err := NewAnvato().Extract(context.Background(), Request{URL: rawURL, Transport: authTransport}); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("auth=%v", err)
	}
	missing := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		serverEndpoint: {body: []byte(`{"server_time":1700000000}`)},
		videoEndpoint:  {status: http.StatusNotFound, body: []byte("gone")},
	}}
	if _, err := NewAnvato().Extract(context.Background(), Request{URL: rawURL, Transport: missing}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable=%v", err)
	}
	truncated := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		serverEndpoint: {body: []byte(`{"server_time":1700000000}`)},
		videoEndpoint:  {body: []byte(`{"def_title":`)},
	}}
	if _, err := NewAnvato().Extract(context.Background(), Request{URL: rawURL, Transport: truncated}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("truncated=%v", err)
	}
	oversized := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		serverEndpoint: {body: []byte(`{"server_time":1700000000}`)},
		videoEndpoint:  {body: []byte(strings.Repeat(" ", int(maxExtractorJSONBytes)+1))},
	}}
	if _, err := NewAnvato().Extract(context.Background(), Request{URL: rawURL, Transport: oversized}); !errors.Is(err, ErrJSONResponseTooLarge) {
		t.Fatalf("oversized=%v", err)
	}
}

func TestFOX9AdaptersHandoffEvidence(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(NewFOX9News(), NewFOX9(), NewAnvato())
	fox9, err := NewFOX9().Extract(context.Background(), Request{URL: "https://www.fox9.com/video/314473"})
	if err != nil || !fox9.IsURL() || fox9.Redirect.ExtractorKey != "anvato" {
		t.Fatalf("fox9=%#v err=%v", fox9, err)
	}
	const wantAdstAuth = "APVytK5DkP4="
	serverEndpoint := anvatoAPIBase + "/server_time?anvack=" + url.QueryEscape(fox9AnvatoAccessKey)
	query := url.Values{}
	query.Set("anvack", fox9AnvatoAccessKey)
	query.Set("X-Anvato-Adst-Auth", wantAdstAuth)
	query.Set("rtyp", "fp")
	// FOX9 video 314473 uses a different id than the fixed auth vector video; build auth for that id.
	videoDataURL := anvatoAPIBase + "/mcp/video/314473?anvack=" + url.QueryEscape(fox9AnvatoAccessKey)
	auth314 := anvatoAdstAuth(videoDataURL, 1700000000)
	q314 := url.Values{}
	q314.Set("anvack", fox9AnvatoAccessKey)
	q314.Set("X-Anvato-Adst-Auth", auth314)
	q314.Set("rtyp", "fp")
	videoEndpoint := anvatoAPIBase + "/mcp/video/314473?" + q314.Encode()
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		serverEndpoint: {body: []byte(`{"server_time":1700000000}`)},
		videoEndpoint:  {body: familyFixture(t, "anvato", "video.json")},
	}}
	selected, err := registry.SelectFor(fox9.Redirect.URL, fox9.Redirect.ExtractorKey)
	if err != nil || selected.Name() != "anvato" {
		t.Fatalf("fox9 SelectFor=%v", err)
	}
	media, err := selected.Extract(context.Background(), Request{URL: fox9.Redirect.URL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
		t.Fatal("fox9 re-entry missing formats")
	}

	newsTransport := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.fox9.com/news/bear-climbs-tree": familyFixture(t, "fox9", "news.html"),
	}}
	news, err := NewFOX9News().Extract(context.Background(), Request{
		URL: "https://www.fox9.com/news/bear-climbs-tree", Transport: newsTransport,
	})
	if err != nil || !news.IsURL() || news.Redirect.ExtractorKey != "fox9" || news.Redirect.ID != "314473" {
		t.Fatalf("fox9_news=%#v err=%v", news, err)
	}
	fox9Selected, err := registry.SelectFor(news.Redirect.URL, news.Redirect.ExtractorKey)
	if err != nil || fox9Selected.Name() != "fox9" {
		t.Fatalf("news->fox9=%v", err)
	}
	fox9Result, err := fox9Selected.Extract(context.Background(), Request{URL: news.Redirect.URL})
	if err != nil || !fox9Result.IsURL() || fox9Result.Redirect.ExtractorKey != "anvato" {
		t.Fatalf("news fox9 extract=%#v err=%v", fox9Result, err)
	}
}

func TestFOX9NegativeMatrix(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewFOX9News().Extract(canceled, Request{
		URL: "https://www.fox9.com/news/bear-climbs-tree", Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	auth := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.fox9.com/news/bear-climbs-tree": []byte(`<html>please log in</html>`),
	}}
	if _, err := NewFOX9News().Extract(context.Background(), Request{
		URL: "https://www.fox9.com/news/bear-climbs-tree", Transport: auth,
	}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("auth=%v", err)
	}
	missing := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.fox9.com/news/bear-climbs-tree": []byte(`<html>page not found</html>`),
	}}
	if _, err := NewFOX9News().Extract(context.Background(), Request{
		URL: "https://www.fox9.com/news/bear-climbs-tree", Transport: missing,
	}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable=%v", err)
	}
	hostile := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.fox9.com/news/bear-climbs-tree": []byte(`<html><script>anvatoId: 'not-digits'</script></html>`),
	}}
	if _, err := NewFOX9News().Extract(context.Background(), Request{
		URL: "https://www.fox9.com/news/bear-climbs-tree", Transport: hostile,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("hostile=%v", err)
	}
}

func FuzzParseAnvatoURL(f *testing.F) {
	f.Add("anvato:" + fox9AnvatoAccessKey + ":8032455")
	f.Add("anvato:lin:123")
	f.Add("nope")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _ = parseAnvatoURL(parsed)
		_, _ = parseFOX9URL(parsed)
		_, _ = parseFOX9NewsURL(parsed)
	})
}
