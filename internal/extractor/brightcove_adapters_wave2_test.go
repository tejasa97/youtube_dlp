package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const wave2FixtureRoot = "testdata/brightcove_adapters_wave2"

func wave2Fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(wave2FixtureRoot, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func wave2BrightcoveConfig(t testing.TB, account, player, video string) map[string]fixtureHTTP {
	return map[string]fixtureHTTP{
		"https://players.brightcove.net/" + account + "/" + player + "_default/config.json": {
			body: sharedFixture(t, "brightcove.json"),
		},
		"https://edge.api.brightcove.com/playback/v1/accounts/" + account + "/videos/" + video: {
			body: []byte(`{"id":"` + video + `","name":"Brightcove Fixture","duration":12000,"sources":[{"src":"https://media.example/bc/master.m3u8","type":"application/x-mpegURL"},{"src":"https://media.example/bc/video.mp4","height":720,"avg_bitrate":1500000}]}`),
		},
	}
}

func TestBrightcoveAdaptersWave2SuitableAndHandoff(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(
		NewFormula1(), NewEuropeanTour(), NewMaoriTV(), NewTheStar(), NewTheSun(),
		NewWimbledon(), NewUSAToday(), NewSkyNewsAU(), NewBrightcove(),
	)

	t.Run("formula1", func(t *testing.T) {
		rawURL := "https://www.formula1.com/en/latest/video.race-highlights-spain-2016.6060988138001.html"
		transport := &sharedFixtureTransport{}
		result, err := NewFormula1().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || !result.IsURL() || result.Redirect.ExtractorKey != "brightcove" {
			t.Fatalf("formula1=%#v err=%v", result, err)
		}
		if transport.requestCount() != 0 {
			t.Fatalf("formula1 made %d requests, want 0", transport.requestCount())
		}
		if !strings.Contains(result.Redirect.URL, formula1BrightcoveAccount) || !strings.Contains(result.Redirect.URL, formula1BrightcovePlayer) {
			t.Fatalf("redirect=%q", result.Redirect.URL)
		}
		selected, err := registry.SelectFor(result.Redirect.URL, result.Redirect.ExtractorKey)
		if err != nil || selected.Name() != "brightcove" {
			t.Fatal(err)
		}
		bcTransport := &sharedFixtureTransport{responses: wave2BrightcoveConfig(t, formula1BrightcoveAccount, formula1BrightcovePlayer, "6060988138001")}
		if _, err := selected.Extract(context.Background(), Request{URL: result.Redirect.URL, Transport: bcTransport}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("europeantour", func(t *testing.T) {
		rawURL := "https://www.europeantour.com/dpworld-tour/news/video/the-best-shots-of-the-2021-seasons/"
		canonical := "https://www.europeantour.com/dpworld-tour/news/video/the-best-shots-of-the-2021-seasons/"
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: wave2Fixture(t, "europeantour_page.html")}}
		result, err := NewEuropeanTour().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.ExtractorKey != "brightcove" || result.Redirect.ID != "6287788195001" {
			t.Fatalf("%#v %v", result, err)
		}
		selected, _ := registry.SelectFor(result.Redirect.URL, "brightcove")
		bcTransport := &sharedFixtureTransport{responses: wave2BrightcoveConfig(t, europeanTourDefaultAccount, "default", "6287788195001")}
		if _, err := selected.Extract(context.Background(), Request{URL: result.Redirect.URL, Transport: bcTransport}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("maoritv", func(t *testing.T) {
		rawURL := "https://www.maoritelevision.com/shows/korero-mai/S01E054/korero-mai-series-1-episode-54"
		canonical := "https://www.maoritelevision.com/shows/korero-mai/S01E054/korero-mai-series-1-episode-54"
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: wave2Fixture(t, "maoritv_page.html")}}
		result, err := NewMaoriTV().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || !strings.Contains(result.Redirect.URL, maoriTVBrightcovePlayer) {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("thestar", func(t *testing.T) {
		rawURL := "https://www.thestar.com/life/2016/02/01/mankind-why-this-woman-started-a-men-s-skincare-line.html"
		canonical := "https://www.thestar.com/life/2016/02/01/mankind-why-this-woman-started-a-men-s-skincare-line.html"
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: wave2Fixture(t, "thestar_page.html")}}
		result, err := NewTheStar().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.ID != "4732393888001" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("thesun", func(t *testing.T) {
		rawURL := "https://www.thesun.co.uk/tvandshowbiz/2261604/orlando-bloom-and-katy-perry/"
		canonical := "https://www.thesun.co.uk/tvandshowbiz/2261604/orlando-bloom-and-katy-perry/"
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: wave2Fixture(t, "thesun_page_co_uk.html")}}
		result, err := NewTheSun().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		title, ok := result.Info.Title()
		if !ok || title != "Orlando Bloom and Katy Perry" {
			t.Fatalf("title=%q ok=%t", title, ok)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, brightcoveAdapterMaxEntries)
		if err != nil || len(entries) != 2 || entries[0].ID != "1111111111111" || entries[1].ID != "2222222222222" {
			t.Fatalf("entries=%v err=%v", entries, err)
		}
		second := result.Entries.Iterator()
		reused := make([]Entry, 0, 2)
		for {
			entry, ok, err := second.Next(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			reused = append(reused, entry)
		}
		if len(reused) != 2 || reused[0].ID != entries[0].ID {
			t.Fatalf("reuse=%v", reused)
		}
	})

	t.Run("wimbledon", func(t *testing.T) {
		rawURL := "https://www.wimbledon.com/en_GB/video/media/6330247525112.html"
		endpoint := "https://www.wimbledon.com/relatedcontent/rest/v2/wim_v1/en/content/wim_v1_6330247525112_en"
		transport := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
			endpoint: {body: wave2Fixture(t, "wimbledon_metadata.json")},
		}}
		result, err := NewWimbledon().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.Title != "Coco Gauff | My Wimbledon Inspiration" || !result.Redirect.HasDuration {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("usatoday", func(t *testing.T) {
		rawURL := "https://www.usatoday.com/media/cinematic/video/81729424/us-france-warn-syrian-regime-ahead-of-new-peace-talks/"
		ajaxURL := "https://www.usatoday.com/media/cinematic/video/81729424/us-france-warn-syrian-regime-ahead-of-new-peace-talks/?ajax=true"
		transport := &sharedFixtureTransport{pages: map[string][]byte{ajaxURL: wave2Fixture(t, "usatoday_page.html")}}
		result, err := NewUSAToday().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.ID != "4799374959001" || result.Redirect.Title == "" || !result.Redirect.HasDuration {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("skynewsau", func(t *testing.T) {
		rawURL := "https://www.skynews.com.au/world-news/united-states/incredible-vision/video/0f4c6243d6903502c01251f228b91a71"
		canonical := "https://www.skynews.com.au/world-news/united-states/incredible-vision/video/0f4c6243d6903502c01251f228b91a71"
		apiURL := "https://content.api.news/v3/videos/brightcove/5348771529001-6277184925001?api_key=" + skyNewsAUAPIKey
		transport := &sharedFixtureTransport{
			pages:     map[string][]byte{canonical: wave2Fixture(t, "skynewsau_page.html")},
			responses: map[string]fixtureHTTP{apiURL: {body: wave2Fixture(t, "skynewsau_api.json")}},
		}
		result, err := NewSkyNewsAU().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.ID != "0f4c6243d6903502c01251f228b91a71" || result.Redirect.Title == "" || !result.Redirect.HasTimestamp {
			t.Fatalf("%#v %v", result, err)
		}
	})

	for _, test := range []struct {
		rawURL, want string
	}{
		{"https://www.formula1.com/en/latest/video.race-highlights.6060988138001.html", "formula1"},
		{"https://formula1.com/en/latest/video.race-highlights.6060988138001.html", "formula1"},
		{"https://www.europeantour.com/dpworld-tour/news/video/the-best-shots/", "europeantour"},
		{"https://www.maoritelevision.com/shows/korero-mai/S01E054/episode", "maoritv"},
		{"https://www.thestar.com/life/2016/02/01/article.html", "thestar"},
		{"https://www.thesun.co.uk/tvandshowbiz/2261604/slug", "thesun"},
		{"https://www.the-sun.com/entertainment/7611415/slug", "thesun"},
		{"https://www.wimbledon.com/en_GB/video/media/6330247525112.html", "wimbledon"},
		{"https://www.usatoday.com/story/tech/science/2018/08/21/yellowstone/", "usatoday"},
		{"https://www.skynews.com.au/a/b/c/video/abc123def456", "skynewsau"},
		{"https://example.invalid/video/1", ""},
	} {
		selected, err := registry.Select(test.rawURL)
		if test.want == "" {
			if err == nil {
				t.Fatalf("Select(%q) got %q", test.rawURL, selected.Name())
			}
			continue
		}
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q)=%v err=%v want %q", test.rawURL, selected, err, test.want)
		}
	}
}

func TestBrightcoveAdaptersWave2SuitableMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ok   func(*url.URL) bool
		raw  string
		want bool
	}{
		{"formula1-bare", func(u *url.URL) bool { return NewFormula1().Suitable(u) }, "https://formula1.com/en/latest/video.slug.123.html", true},
		{"formula1-www", func(u *url.URL) bool { return NewFormula1().Suitable(u) }, "https://www.formula1.com/en/latest/video.slug.123.html", true},
		{"formula1-lookalike", func(u *url.URL) bool { return NewFormula1().Suitable(u) }, "https://evilformula1.com/en/latest/video.slug.123.html", false},
		{"thesun-bare", func(u *url.URL) bool { return NewTheSun().Suitable(u) }, "https://thesun.co.uk/showbiz/123/slug", true},
		{"thesun-hyphen", func(u *url.URL) bool { return NewTheSun().Suitable(u) }, "https://www.the-sun.co.uk/showbiz/123/slug", true},
		{"thesun-lookalike", func(u *url.URL) bool { return NewTheSun().Suitable(u) }, "https://notthesun.co.uk/showbiz/123", false},
		{"skynews-bare", func(u *url.URL) bool { return NewSkyNewsAU().Suitable(u) }, "https://skynews.com.au/a/b/c/video/abc123", true},
		{"skynews-short", func(u *url.URL) bool { return NewSkyNewsAU().Suitable(u) }, "https://www.skynews.com.au/a/b/video/abc123", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := url.Parse(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := test.ok(parsed); got != test.want {
				t.Fatalf("Suitable(%q)=%t want %t", test.raw, got, test.want)
			}
		})
	}
}

func TestBrightcoveAdaptersWave2Negatives(t *testing.T) {
	t.Parallel()
	unsafe := []string{
		"http://user:pass@www.formula1.com/en/latest/video.slug.123.html",
		"https://www.formula1.com:8443/en/latest/video.slug.123.html",
		"https://127.0.0.1/en/latest/video.slug.123.html",
		"https://www.formula1.com/en/latest/video.slug.123.html#frag",
		"https://www.formula1.com/en/latest/video.%2fslug.123.html",
		"https://www.thesun.co.uk/showbiz/../123/slug",
	}
	for _, raw := range unsafe {
		parsed, _ := url.Parse(raw)
		if NewFormula1().Suitable(parsed) || NewTheSun().Suitable(parsed) {
			t.Fatalf("unsafe accepted: %q", raw)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewEuropeanTour().Extract(canceled, Request{
		URL: "https://www.europeantour.com/dpworld-tour/news/video/x/", Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}

	missing := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.maoritelevision.com/shows/x/y": []byte("<html></html>"),
	}}
	if _, err := NewMaoriTV().Extract(context.Background(), Request{
		URL: "https://www.maoritelevision.com/shows/x/y", Transport: missing,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("missing=%v", err)
	}

	malformed := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.thestar.com/x.html": []byte(`mainartBrightcoveVideoId: not-json`),
	}}
	if _, err := NewTheStar().Extract(context.Background(), Request{
		URL: "https://www.thestar.com/x.html", Transport: malformed,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed=%v", err)
	}

	badVideo := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.thesun.co.uk/showbiz/1/slug": []byte(`<video data-video-id-pending="not-digits"></video>`),
	}}
	if _, err := NewTheSun().Extract(context.Background(), Request{
		URL: "https://www.thesun.co.uk/showbiz/1/slug", Transport: badVideo,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("bad video=%v", err)
	}

	oversized := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.thestar.com/x.html": wave2BytesRepeat('a', int(maxExtractorJSONBytes)+1),
	}}
	if _, err := NewTheStar().Extract(context.Background(), Request{
		URL: "https://www.thestar.com/x.html", Transport: oversized,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("oversized=%v", err)
	}

	secret := &sharedFixtureTransport{responses: map[string]fixtureHTTP{
		"https://www.wimbledon.com/relatedcontent/rest/v2/wim_v1/en/content/wim_v1_6330247525112_en": {
			status: http.StatusUnauthorized, body: []byte("api_key=must-not-leak"),
		},
	}}
	if _, err := NewWimbledon().Extract(context.Background(), Request{
		URL: "https://www.wimbledon.com/en_GB/video/media/6330247525112.html", Transport: secret,
	}); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("secret=%v", err)
	}

	skySecret := &sharedFixtureTransport{
		pages: map[string][]byte{
			"https://www.skynews.com.au/world-news/united-states/incredible-vision/video/0f4c6243d6903502c01251f228b91a71": wave2Fixture(t, "skynewsau_page.html"),
		},
		responses: map[string]fixtureHTTP{
			"https://content.api.news/v3/videos/brightcove/5348771529001-6277184925001?api_key=" + skyNewsAUAPIKey: {
				status: http.StatusUnauthorized, body: []byte(skyNewsAUAPIKey + "=must-not-leak"),
			},
		},
	}
	if _, err := NewSkyNewsAU().Extract(context.Background(), Request{
		URL:       "https://www.skynews.com.au/world-news/united-states/incredible-vision/video/0f4c6243d6903502c01251f228b91a71",
		Transport: skySecret,
	}); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), skyNewsAUAPIKey) || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatalf("sky secret=%v", err)
	}

	invalidRoute := &sharedFixtureTransport{}
	if _, err := NewFormula1().Extract(context.Background(), Request{
		URL: "https://www.formula1.com/en/latest/not-a-video.html", Transport: invalidRoute,
	}); !errors.Is(err, ErrUnsupported) || invalidRoute.requestCount() != 0 {
		t.Fatalf("invalid route=%v requests=%d", err, invalidRoute.requestCount())
	}
}

func wave2BytesRepeat(ch byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = ch
	}
	return out
}
