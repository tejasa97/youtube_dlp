package extractor

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestKalturaJWAdaptersHandoff(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(
		NewUnitedNationsWebTV(), NewAZMedien(), NewInc(), NewHeise(),
		NewSpiegel(), NewOneFootball(), NewKaltura(), NewJWPlatform(),
	)

	t.Run("unitednationswebtv", func(t *testing.T) {
		pageURL := "https://webtv.un.org/en/asset/k1o/k1o7stmi6p"
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://webtv.un.org/en/asset/k1o/k1o7stmi6p": familyFixture(t, "unitednationswebtv", "page.html"),
		}}
		result, err := NewUnitedNationsWebTV().Extract(context.Background(), Request{URL: pageURL, Transport: transport})
		if err != nil || result.Redirect.URL != "kaltura:123:1_abcd1234" {
			t.Fatalf("%#v %v", result, err)
		}
		selected, err := registry.SelectFor(result.Redirect.URL, "kaltura")
		if err != nil || selected.Name() != "kaltura" {
			t.Fatal(err)
		}
		media, err := selected.Extract(context.Background(), Request{
			URL: result.Redirect.URL,
			Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
				"https://cdnapi.kaltura.com/api_v3/service/multirequest": {body: sharedFixture(t, "kaltura.json")},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing formats")
		}
	})

	t.Run("azmedien_fragment", func(t *testing.T) {
		rawURL := "https://www.telebaern.tv/telebaern-news/montag-1-oktober-2018-ganze-sendung-133531189#video=0_7xjo9lf1"
		result, err := NewAZMedien().Extract(context.Background(), Request{URL: rawURL})
		if err != nil || result.Redirect.URL != "kaltura:1719221:0_7xjo9lf1" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("azmedien_page", func(t *testing.T) {
		rawURL := "https://tv.telezueri.ch/sonntalk/bundesrats-vakanzen-eu-rahmenabkommen-133214569"
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://tv.telezueri.ch/sonntalk/bundesrats-vakanzen-eu-rahmenabkommen-133214569": familyFixture(t, "azmedien", "page.html"),
		}}
		result, err := NewAZMedien().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "kaltura:1719221:1_abcd1234" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("inc", func(t *testing.T) {
		rawURL := "https://www.inc.com/tip-sheet/bill-gates-says-these-5-books-will-make-you-smarter.html"
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.inc.com/tip-sheet/bill-gates-says-these-5-books-will-make-you-smarter.html": familyFixture(t, "inc", "page.html"),
		}}
		result, err := NewInc().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "kaltura:1034971:1_abcd1234" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("heise", func(t *testing.T) {
		rawURL := "https://www.heise.de/video/artikel/Podcast-c-t-uplink-3-3-Owncloud-2404147.html"
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.heise.de/video/artikel/Podcast-c-t-uplink-3-3-Owncloud-2404147.html": familyFixture(t, "heise", "page.html"),
		}}
		result, err := NewHeise().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "kaltura:2238431:1_abcd1234" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("spiegel", func(t *testing.T) {
		rawURL := "https://www.spiegel.de/video/vulkan-tungurahua-in-ecuador-ist-wieder-aktiv-video-1259285.html"
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://www.spiegel.de/video/vulkan-tungurahua-in-ecuador-ist-wieder-aktiv-video-1259285.html": familyFixture(t, "spiegel", "page.html"),
		}}
		result, err := NewSpiegel().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "jwplatform:AbCd1234" {
			t.Fatalf("%#v %v", result, err)
		}
		selected, _ := registry.SelectFor(result.Redirect.URL, "jwplatform")
		media, err := selected.Extract(context.Background(), Request{
			URL: result.Redirect.URL,
			Transport: &sharedFixtureTransport{responses: map[string]fixtureHTTP{
				"https://cdn.jwplayer.com/v2/media/AbCd1234": {body: sharedFixture(t, "jwplatform.json")},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if formats, ok := media.Info.Formats(); !ok || len(formats) == 0 {
			t.Fatal("missing JW formats")
		}
	})

	t.Run("onefootball", func(t *testing.T) {
		rawURL := "https://onefootball.com/en/video/highlights-fc-zuerich-3-3-fc-basel-34012334"
		transport := &sharedFixtureTransport{pages: map[string][]byte{
			"https://onefootball.com/en/video/highlights-fc-zuerich-3-3-fc-basel-34012334": familyFixture(t, "onefootball", "page.html"),
		}}
		result, err := NewOneFootball().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "jwplatform:AbCd1234" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	for _, test := range []struct {
		rawURL, want string
	}{
		{"https://webtv.un.org/en/asset/k1o/k1o7stmi6p", "unitednationswebtv"},
		{"https://tv.telezueri.ch/sonntalk/bundesrats-vakanzen-133214569", "azmedien"},
		{"https://www.inc.com/tip-sheet/bill-gates.html", "inc"},
		{"https://www.heise.de/video/artikel/Podcast-2404147.html", "heise"},
		{"https://www.spiegel.de/video/vulkan-video-1259285.html", "spiegel"},
		{"https://www.manager-magazin.de/unternehmen/video-aae8df48-43c1-4c61-867d-23f0a2d254b7", "spiegel"},
		{"https://onefootball.com/en/video/highlights-34012334", "onefootball"},
		{"kaltura:123:1_abcd1234", "kaltura"},
		{"jwplatform:AbCd1234", "jwplatform"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q)=%v err=%v want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func TestKalturaJWAdapterNegatives(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewInc().Extract(canceled, Request{
		URL: "https://www.inc.com/tip-sheet/x.html", Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	parsed, _ := url.Parse("https://webtv.un.org/en/asset/k1o/k1o7stmi6p#frag")
	if NewUnitedNationsWebTV().Suitable(parsed) {
		t.Fatal("fragment should be rejected for UN WebTV")
	}
	hostile := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.heise.de/video/artikel/Podcast-2404147.html": []byte(`<html>please sign in token=secret</html>`),
	}}
	if _, err := NewHeise().Extract(context.Background(), Request{
		URL: "https://www.heise.de/video/artikel/Podcast-2404147.html", Transport: hostile,
	}); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("auth=%v", err)
	}
}

func FuzzParseSpiegelURL(f *testing.F) {
	f.Add("https://www.spiegel.de/video/vulkan-video-1259285.html")
	f.Add("https://www.manager-magazin.de/unternehmen/a-aae8df48-43c1-4c61-867d-23f0a2d254b7")
	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _ = parseSpiegelURL(parsed)
	})
}

func TestJWPlatformEntryConstructor(t *testing.T) {
	t.Parallel()
	t.Run("valid id with fallback", func(t *testing.T) {
		entry, err := jwPlatformEntry("AbCd1234", Entry{})
		if err != nil {
			t.Fatal(err)
		}
		if entry.URL != "jwplatform:AbCd1234" {
			t.Fatalf("URL=%q", entry.URL)
		}
		if entry.ExtractorKey != "jwplatform" {
			t.Fatalf("ExtractorKey=%q", entry.ExtractorKey)
		}
		if entry.ID != "AbCd1234" {
			t.Fatalf("ID=%q", entry.ID)
		}
		if !entry.Transparent {
			t.Fatal("not transparent")
		}
	})
	t.Run("valid id with producer metadata preserved", func(t *testing.T) {
		entry, err := jwPlatformEntry("AbCd1234", Entry{
			ID:      "producer-id",
			Title:   "Producer Title",
			Referer: "https://example.invalid/article",
		})
		if err != nil {
			t.Fatal(err)
		}
		if entry.ID != "producer-id" {
			t.Fatalf("producer ID overwritten: %q", entry.ID)
		}
		if entry.Title != "Producer Title" {
			t.Fatalf("producer title overwritten: %q", entry.Title)
		}
		if entry.Referer != "https://example.invalid/article" {
			t.Fatalf("referer overwritten: %q", entry.Referer)
		}
	})
	t.Run("invalid media id rejected", func(t *testing.T) {
		if _, err := jwPlatformEntry("short", Entry{}); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("err=%v", err)
		}
	})
}
