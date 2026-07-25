package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestArcPublishingSuitableAndSuccess(t *testing.T) {
	t.Parallel()
	rawURL := "arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab"
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if !NewArcPublishing().Suitable(parsed) {
		t.Fatal("expected Suitable")
	}
	endpoint := arcAPIEndpoint("adn") + "?uuid=8c99cb6e-b29c-4bc9-9173-7bf9979225ab"
	transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		endpoint: {body: familyFixture(t, "arcpublishing", "video.json")},
	}}
	result, err := NewArcPublishing().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	id, ok := result.Info.ID()
	if !ok || id != "8c99cb6e-b29c-4bc9-9173-7bf9979225ab" {
		t.Fatalf("id=%q ok=%t", id, ok)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) == 0 || !sharedHasProtocol(formats, "m3u8_native") {
		t.Fatalf("formats=%v", formats)
	}
}

func TestArcPublishingNegativeMatrix(t *testing.T) {
	t.Parallel()
	rawURL := "arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab"
	endpoint := arcAPIEndpoint("adn") + "?uuid=8c99cb6e-b29c-4bc9-9173-7bf9979225ab"
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewArcPublishing().Extract(canceled, Request{URL: rawURL, Transport: &sharedFixtureTransport{}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	for _, test := range []struct {
		name   string
		status int
		body   []byte
		want   error
	}{
		{"unavailable", http.StatusNotFound, []byte(`{"token":"must-not-leak"}`), ErrUnavailable},
		{"auth", http.StatusUnauthorized, []byte("cookie=secret"), ErrAuthentication},
		{"forbidden", http.StatusForbidden, []byte("sig=must-not-leak"), ErrAuthentication},
	} {
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			endpoint: {status: test.status, body: test.body},
		}}
		_, err := NewArcPublishing().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if !errors.Is(err, test.want) || strings.Contains(err.Error(), "must-not-leak") || strings.Contains(err.Error(), "secret") {
			t.Fatalf("%s: err=%v", test.name, err)
		}
	}
	empty := &sharedFixtureTransport{responses: map[string]fixtureHTTP{endpoint: {body: []byte(`[]`)}}}
	if _, err := NewArcPublishing().Extract(context.Background(), Request{URL: rawURL, Transport: empty}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("empty=%v", err)
	}
	truncated := &sharedFixtureTransport{responses: map[string]fixtureHTTP{endpoint: {body: []byte(`[{"headlines":`)}}}
	if _, err := NewArcPublishing().Extract(context.Background(), Request{URL: rawURL, Transport: truncated}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("truncated=%v", err)
	}
	oversized := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		endpoint: {body: []byte(strings.Repeat(" ", int(maxExtractorJSONBytes)+1))},
	}}
	if _, err := NewArcPublishing().Extract(context.Background(), Request{URL: rawURL, Transport: oversized}); !errors.Is(err, ErrJSONResponseTooLarge) {
		t.Fatalf("oversized=%v", err)
	}
}

func TestArcAdaptersExactRoutingHandoffAndEvidence(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(
		NewWashingtonPost(), NewADN(), NewBostonGlobe(), NewGray(), NewClickOnDetroit(),
		NewActionNewsJax(), NewElComercio(), NewLateja(), NewFifthDomain(), NewVLNO(),
		NewFourteenNews(), NewGlobeAndMail(), NewPilotOnline(), NewUpperMichiganSource(),
		NewArcPublishing(),
	)
	arcTransport := func(org, uuid string) *sharedFixtureTransport {
		return &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			arcAPIEndpoint(org) + "?uuid=" + uuid: {body: familyFixture(t, "arcpublishing", "video.json")},
		}}
	}

	wapoURL := "https://www.washingtonpost.com/video/c/video/480ba4ee-1ec7-11e6-82c2-a7dcb313287d"
	wapo, err := NewWashingtonPost().Extract(context.Background(), Request{URL: wapoURL})
	if err != nil || !wapo.IsURL() || wapo.Redirect.URL != "arcpublishing:wapo:480ba4ee-1ec7-11e6-82c2-a7dcb313287d" {
		t.Fatalf("wapo=%#v err=%v", wapo, err)
	}
	selected, err := registry.SelectFor(wapo.Redirect.URL, wapo.Redirect.ExtractorKey)
	if err != nil || selected.Name() != "arcpublishing" {
		t.Fatalf("wapo handoff=%v", err)
	}
	media, err := selected.Extract(context.Background(), Request{URL: wapo.Redirect.URL, Transport: arcTransport("wapo", "480ba4ee-1ec7-11e6-82c2-a7dcb313287d")})
	if err != nil {
		t.Fatal(err)
	}
	if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
		t.Fatal("wapo re-entry missing formats")
	}

	for _, test := range []struct {
		name, pageURL, hostPath, org, uuid, fixtureDir string
		ctor                                           Extractor
	}{
		{"adn", "https://www.adn.com/politics/2020/11/02/video-senate-candidates/", "https://adn.com/politics/2020/11/02/video-senate-candidates/", "adn", "8c99cb6e-b29c-4bc9-9173-7bf9979225ab", "adn", NewADN()},
		{"bostonglobe", "https://www.bostonglobe.com/video/2020/12/30/metro/example/", "https://bostonglobe.com/video/2020/12/30/metro/example/", "bostonglobe", "232b7ae6-7d73-432d-bc0a-85dbf0119ab1", "bostonglobe", NewBostonGlobe()},
		{"gray", "https://www.wabi.tv/video/2020/12/30/example/", "https://wabi.tv/video/2020/12/30/example/", "gray", "0b0ba30e-032a-4598-8810-901d70e6033e", "gray", NewGray()},
		{"clickondetroit", "https://www.clickondetroit.com/video/community/2020/05/15/example/", "https://clickondetroit.com/video/community/2020/05/15/example/", "gmg", "c8793fb2-8d44-4242-881e-2db31da2d9fe", "clickondetroit", NewClickOnDetroit()},
		{"actionnewsjax", "https://www.actionnewsjax.com/video/live-stream/", "https://actionnewsjax.com/video/live-stream/", "cmg", "cfb1cf1b-3ab5-4d1b-86c5-a5515d311f2a", "actionnewsjax", NewActionNewsJax()},
		{"elcomercio", "https://www.elcomercio.pe/videos/deportes/example/", "https://elcomercio.pe/videos/deportes/example/", "elcomercio", "27a7e1f8-2ec7-4177-874f-a4feed2885b3", "elcomercio", NewElComercio()},
		{"lateja", "https://www.lateja.cr/el-mundo/video-china/dfcbfa57-527f-45ff-a69b-35fe71054143/video/", "https://lateja.cr/el-mundo/video-china/dfcbfa57-527f-45ff-a69b-35fe71054143/video/", "gruponacion", "dfcbfa57-527f-45ff-a69b-35fe71054143", "lateja", NewLateja()},
		{"fifthdomain", "https://www.fifthdomain.com/video/2018/03/09/example/", "https://fifthdomain.com/video/2018/03/09/example/", "mco", "aa0ca6fe-1127-46d4-b32c-be0d6fdb8055", "fifthdomain", NewFifthDomain()},
		{"vlno", "https://www.vl.no/kultur/2020/12/09/example-article/", "https://vl.no/kultur/2020/12/09/example-article/", "mentormedier", "47a12084-650b-4011-bfd0-3699b6947b2d", "vlno", NewVLNO()},
		{"fourteennews", "https://www.14news.com/2020/12/30/whiskey-theft/", "https://14news.com/2020/12/30/whiskey-theft/", "raycom", "b89f61f8-79fa-4c09-8255-e64237119bf7", "fourteennews", NewFourteenNews()},
		{"globeandmail", "https://www.theglobeandmail.com/world/video-ethiopian-woman/", "https://theglobeandmail.com/world/video-ethiopian-woman/", "tgam", "411b34c1-8701-4036-9831-26964711664b", "globeandmail", NewGlobeAndMail()},
		{"pilotonline", "https://www.pilotonline.com/news/460f2931-8130-4719-8ea1-ffcb2d7cb685-132.html", "https://pilotonline.com/news/460f2931-8130-4719-8ea1-ffcb2d7cb685-132.html", "tronc", "460f2931-8130-4719-8ea1-ffcb2d7cb685", "pilotonline", NewPilotOnline()},
		{"uppermichigansource", "https://www.uppermichigansource.com/2025/07/18/scattered-showers/", "https://uppermichigansource.com/2025/07/18/scattered-showers/", "gray", "508116f7-e999-48db-b7c2-60a04842679b", "uppermichigansource", NewUpperMichiganSource()},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := familyFixture(t, test.fixtureDir, "powa.html")
			transport := &sharedFixtureTransport{pages: map[string][]byte{test.hostPath: page}}
			result, err := test.ctor.Extract(context.Background(), Request{URL: test.pageURL, Transport: transport})
			if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "arcpublishing" {
				t.Fatalf("adapter=%#v err=%v", result, err)
			}
			wantURL := "arcpublishing:" + test.org + ":" + test.uuid
			if result.Redirect.URL != wantURL {
				t.Fatalf("redirect=%q want %q", result.Redirect.URL, wantURL)
			}
			selected, err := registry.SelectFor(result.Redirect.URL, result.Redirect.ExtractorKey)
			if err != nil || selected.Name() != "arcpublishing" {
				t.Fatalf("SelectFor=%v err=%v", selected, err)
			}
			media, err := selected.Extract(context.Background(), Request{
				URL: result.Redirect.URL, Transport: arcTransport(test.org, test.uuid),
			})
			if err != nil {
				t.Fatal(err)
			}
			if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
				t.Fatal("missing formats after re-entry")
			}
		})
	}

	for _, test := range []struct {
		rawURL, want string
	}{
		{wapoURL, "washingtonpost"},
		{"https://www.adn.com/politics/2020/11/02/video-senate-candidates/", "adn"},
		{"https://www.bostonglobe.com/video/2020/12/30/metro/example/", "bostonglobe"},
		{"https://www.wabi.tv/video/2020/12/30/example/", "gray"},
		{"https://www.clickondetroit.com/video/community/2020/05/15/example/", "clickondetroit"},
		{"arcpublishing:gmg:c8793fb2-8d44-4242-881e-2db31da2d9fe", "arcpublishing"},
		{"https://players.brightcove.net/12345/default_default/index.html?videoId=123", ""},
	} {
		selected, err := registry.Select(test.rawURL)
		if test.want == "" {
			if err == nil {
				t.Fatalf("Select(%q) unexpectedly got %q", test.rawURL, selected.Name())
			}
			continue
		}
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q)=%v err=%v want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func TestArcAdapterNegatives(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewADN().Extract(canceled, Request{
		URL: "https://www.adn.com/politics/2020/11/02/video-senate-candidates/", Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("adn cancel=%v", err)
	}
	auth := &sharedFixtureTransport{pages: map[string][]byte{
		"https://adn.com/politics/2020/11/02/video-senate-candidates/": []byte(`<html>please sign in</html>`),
	}}
	if _, err := NewADN().Extract(context.Background(), Request{
		URL: "https://www.adn.com/politics/2020/11/02/video-senate-candidates/", Transport: auth,
	}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("adn auth=%v", err)
	}
	hostile := &sharedFixtureTransport{pages: map[string][]byte{
		"https://adn.com/politics/2020/11/02/video-senate-candidates/": []byte(`<html><div class="powa" data-org="evil" data-uuid="8c99cb6e-b29c-4bc9-9173-7bf9979225ab"></div></html>`),
	}}
	if _, err := NewADN().Extract(context.Background(), Request{
		URL: "https://www.adn.com/politics/2020/11/02/video-senate-candidates/", Transport: hostile,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("hostile org=%v", err)
	}
}

func FuzzParseArcPublishingURL(f *testing.F) {
	f.Add("arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab")
	f.Add("arcpublishing:wapo:480ba4ee-1ec7-11e6-82c2-a7dcb313287d")
	f.Add("not-arc")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _ = parseArcPublishingURL(parsed)
		_, _ = parseWashingtonPostURL(parsed)
		_, _ = extractArcPowaEntries([]byte(raw), "adn")
	})
}
