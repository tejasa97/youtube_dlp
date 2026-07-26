package extractor

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const jwWave1FixtureRoot = "testdata/jwplatform_adapters_wave1"

func jwWave1Fixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(jwWave1FixtureRoot, name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func jwWave1JWConfig(t testing.TB, mediaIDs ...string) map[string]fixtureHTTP {
	if len(mediaIDs) == 0 {
		mediaIDs = []string{"AbCd1234", "EfGh5678", "zH4jZaR5", "g9j7Eovo", "voyyS7SV", "bhhHkKyN"}
	}
	responses := make(map[string]fixtureHTTP, len(mediaIDs))
	body := sharedFixture(t, "jwplatform.json")
	for _, mediaID := range mediaIDs {
		responses["https://cdn.jwplayer.com/v2/media/"+mediaID] = fixtureHTTP{body: body}
	}
	return responses
}

func TestJWPlatformAdaptersWave1SuitableAndHandoff(t *testing.T) {
	t.Parallel()
	registry := NewRegistry(
		NewBundesliga(), NewBusinessInsider(), NewDBTV(), NewHollywoodReporter(),
		NewIltalehti(), NewLeFigaroVideoEmbed(), NewMirrorCoUK(), NewOutsideTV(),
		NewTheIntercept(), NewJWPlatform(),
	)

	t.Run("bundesliga", func(t *testing.T) {
		rawURL := "https://www.bundesliga.com/en/bundesliga/videos?vid=AbCd1234"
		transport := &sharedFixtureTransport{}
		result, err := NewBundesliga().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "jwplatform:AbCd1234" || result.Redirect.ID != "AbCd1234" {
			t.Fatalf("%#v %v", result, err)
		}
		if transport.requestCount() != 0 {
			t.Fatalf("bundesliga made %d requests, want 0", transport.requestCount())
		}
		selected, err := registry.SelectFor(result.Redirect.URL, "jwplatform")
		if err != nil || selected.Name() != "jwplatform" {
			t.Fatal(err)
		}
		if _, err := selected.Extract(context.Background(), Request{
			URL: result.Redirect.URL, Transport: &sharedFixtureTransport{responses: jwWave1JWConfig(t)},
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("businessinsider", func(t *testing.T) {
		rawURL := "http://uk.businessinsider.com/how-much-radiation-youre-exposed-to-in-everyday-life-2016-6"
		canonical := "https://uk.businessinsider.com/how-much-radiation-youre-exposed-to-in-everyday-life-2016-6"
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: jwWave1Fixture(t, "businessinsider_page.html")}}
		result, err := NewBusinessInsider().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "jwplatform:AbCd1234" || result.Redirect.ID != "how-much-radiation-youre-exposed-to-in-everyday-life-2016-6" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("dbtv_jw", func(t *testing.T) {
		rawURL := "https://www.dagbladet.no/video/truer-iran-bor-passe-dere/PalfB2Cw"
		transport := &sharedFixtureTransport{}
		result, err := NewDBTV().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "jwplatform:PalfB2Cw" || !result.Redirect.Transparent {
			t.Fatalf("%#v %v", result, err)
		}
		if transport.requestCount() != 0 {
			t.Fatal("dbtv JW should not fetch")
		}
	})

	t.Run("dbtv_youtube", func(t *testing.T) {
		rawURL := "https://www.dagbladet.no/video/PynxJnNWChE/"
		result, err := NewDBTV().Extract(context.Background(), Request{URL: rawURL})
		if err != nil || result.Redirect.URL != "https://www.youtube.com/watch?v=PynxJnNWChE" || result.Redirect.ExtractorKey != "youtube" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("hollywoodreporter_jw", func(t *testing.T) {
		rawURL := "https://www.hollywoodreporter.com/video/chris-pine-michelle-rodriguez-dungeons-dragons/"
		canonical := "https://www.hollywoodreporter.com/video/chris-pine-michelle-rodriguez-dungeons-dragons/"
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: jwWave1Fixture(t, "hollywoodreporter_jw_page.html")}}
		result, err := NewHollywoodReporter().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "jwplatform:zH4jZaR5" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("hollywoodreporter_youtube", func(t *testing.T) {
		rawURL := "https://www.hollywoodreporter.com/video/youtube-fixture/"
		canonical := "https://www.hollywoodreporter.com/video/youtube-fixture/"
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: jwWave1Fixture(t, "hollywoodreporter_yt_page.html")}}
		result, err := NewHollywoodReporter().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" || result.Redirect.ExtractorKey != "youtube" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("iltalehti", func(t *testing.T) {
		rawURL := "https://www.iltalehti.fi/ulkomaat/a/9fbd067f-94e4-46cd-8748-9d958eb4dae2"
		canonical := "https://www.iltalehti.fi/ulkomaat/a/9fbd067f-94e4-46cd-8748-9d958eb4dae2"
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: jwWave1Fixture(t, "iltalehti_page.html")}}
		result, err := NewIltalehti().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || !result.IsPlaylist() {
			t.Fatalf("%#v %v", result, err)
		}
		title, ok := result.Info.Title()
		if !ok || title != "Fixture Article" {
			t.Fatalf("title=%q ok=%t", title, ok)
		}
		entries, err := CollectEntries(context.Background(), result.Entries, jwWave1MaxEntries)
		if err != nil || len(entries) != 2 || entries[0].URL != "jwplatform:AbCd1234" || entries[1].URL != "jwplatform:EfGh5678" {
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

	t.Run("lefigaro", func(t *testing.T) {
		rawURL := "https://video.lefigaro.fr/embed/figaro/video/les-francais-ne-veulent-ils-plus-travailler/"
		canonical := "https://video.lefigaro.fr/embed/figaro/video/les-francais-ne-veulent-ils-plus-travailler/"
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: jwWave1Fixture(t, "lefigaro_page.html")}}
		result, err := NewLeFigaroVideoEmbed().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "jwplatform:g9j7Eovo" || result.Redirect.Title != "Le Figaro Fixture" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("mirrorcouk", func(t *testing.T) {
		rawURL := "https://www.mirror.co.uk/tv/tv-news/love-island-fans-baffled-after-27163139"
		canonical := "https://www.mirror.co.uk/tv/tv-news/love-island-fans-baffled-after-27163139"
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: jwWave1Fixture(t, "mirror_page.html")}}
		result, err := NewMirrorCoUK().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "jwplatform:voyyS7SV" || result.Redirect.ID != "27163139" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("outsidetv", func(t *testing.T) {
		rawURL := "http://www.outsidetv.com/category/snow/play/ZjQYboH6/1/10/Hdg0jukV/4"
		transport := &sharedFixtureTransport{}
		result, err := NewOutsideTV().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "jwplatform:Hdg0jukV" || result.Redirect.ID != "Hdg0jukV" {
			t.Fatalf("%#v %v", result, err)
		}
		if transport.requestCount() != 0 {
			t.Fatal("outsidetv should not fetch")
		}
	})

	t.Run("theintercept", func(t *testing.T) {
		rawURL := "https://theintercept.com/fieldofvision/thisisacoup-episode-four-surrender-or-die/"
		canonical := "https://theintercept.com/fieldofvision/thisisacoup-episode-four-surrender-or-die/"
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: jwWave1Fixture(t, "theintercept_page.html")}}
		result, err := NewTheIntercept().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "jwplatform:AbCd1234" || result.Redirect.ID != "46214" || !result.Redirect.Transparent {
			t.Fatalf("%#v %v", result, err)
		}
		if !result.Redirect.HasTimestamp {
			t.Fatal("expected timestamp")
		}
	})

	for _, test := range []struct {
		rawURL, want string
	}{
		{"https://www.bundesliga.com/en/bundesliga/videos?vid=AbCd1234", "bundesliga"},
		{"https://uk.businessinsider.com/article-slug", "businessinsider"},
		{"https://www.businessinsider.nl/article-slug/", "businessinsider"},
		{"https://www.dagbladet.no/video/slug/PalfB2Cw", "dbtv"},
		{"https://www.hollywoodreporter.com/video/slug/", "hollywoodreporter"},
		{"https://www.iltalehti.fi/ulkomaat/a/9fbd067f-94e4-46cd-8748-9d958eb4dae2", "iltalehti"},
		{"https://video.lefigaro.fr/embed/figaro/video/slug/", "lefigarovideoembed"},
		{"https://www.mirror.co.uk/tv/tv-news/article-27163139", "mirrorcouk"},
		{"http://www.outsidetv.com/home/play/ZjQYboH6/1/10/Hdg0jukV/4", "outsidetv"},
		{"https://theintercept.com/fieldofvision/slug/", "theintercept"},
		{"jwplatform:AbCd1234", "jwplatform"},
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

func TestJWPlatformAdaptersWave1SuitableMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ok   func(*url.URL) bool
		raw  string
		want bool
	}{
		{"bundesliga-vid", func(u *url.URL) bool { return NewBundesliga().Suitable(u) }, "https://www.bundesliga.com/en/bundesliga/videos?vid=bhhHkKyN", true},
		{"bundesliga-no-vid", func(u *url.URL) bool { return NewBundesliga().Suitable(u) }, "https://www.bundesliga.com/en/bundesliga/videos", false},
		{"bundesliga-lookalike", func(u *url.URL) bool { return NewBundesliga().Suitable(u) }, "https://evilbundesliga.com/en/bundesliga/videos?vid=bhhHkKyN", false},
		{"businessinsider-subdomain", func(u *url.URL) bool { return NewBusinessInsider().Suitable(u) }, "https://uk.businessinsider.com/article", true},
		{"businessinsider-bad-subdomain", func(u *url.URL) bool { return NewBusinessInsider().Suitable(u) }, "https://not!valid.businessinsider.com/article", false},
		{"dbtv-jw", func(u *url.URL) bool { return NewDBTV().Suitable(u) }, "https://www.dagbladet.no/video/slug/PalfB2Cw", true},
		{"dbtv-youtube", func(u *url.URL) bool { return NewDBTV().Suitable(u) }, "https://www.dagbladet.no/video/PynxJnNWChE/", true},
		{"theintercept-bare-host", func(u *url.URL) bool { return NewTheIntercept().Suitable(u) }, "https://theintercept.com/fieldofvision/slug/", true},
		{"theintercept-www", func(u *url.URL) bool { return NewTheIntercept().Suitable(u) }, "https://www.theintercept.com/fieldofvision/slug/", false},
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

func TestJWPlatformAdaptersWave1Negatives(t *testing.T) {
	t.Parallel()
	unsafe := []string{
		"http://user:pass@www.bundesliga.com/en/bundesliga/videos?vid=bhhHkKyN",
		"https://www.bundesliga.com:8443/en/bundesliga/videos?vid=bhhHkKyN",
		"https://www.bundesliga.com/en/bundesliga/videos?vid=bhhHkKyN#frag",
		"https://www.mirror.co.uk/tv/../news/article-27163139",
		"https://www.iltalehti.fi/ulkomaat/a/%2fuuid",
	}
	for _, raw := range unsafe {
		parsed, _ := url.Parse(raw)
		if NewBundesliga().Suitable(parsed) || NewMirrorCoUK().Suitable(parsed) || NewIltalehti().Suitable(parsed) {
			t.Fatalf("unsafe accepted: %q", raw)
		}
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewBusinessInsider().Extract(canceled, Request{
		URL: "https://uk.businessinsider.com/article", Transport: &sharedFixtureTransport{},
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}

	missing := &sharedFixtureTransport{pages: map[string][]byte{
		"https://uk.businessinsider.com/article": []byte("<html></html>"),
	}}
	if _, err := NewBusinessInsider().Extract(context.Background(), Request{
		URL: "https://uk.businessinsider.com/article", Transport: missing,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("missing=%v", err)
	}

	hostile := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.hollywoodreporter.com/video/x/": []byte(`<html>please sign in token=secret</html>`),
	}}
	if _, err := NewHollywoodReporter().Extract(context.Background(), Request{
		URL: "https://www.hollywoodreporter.com/video/x/", Transport: hostile,
	}); !errors.Is(err, ErrAuthentication) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("auth=%v", err)
	}

	unsupportedShowcase := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.hollywoodreporter.com/video/x/": []byte(`<a class="vlanding-video-card__link" data-video-showcase-type="vimeo" data-video-showcase-trigger="12345678"></a>`),
	}}
	if _, err := NewHollywoodReporter().Extract(context.Background(), Request{
		URL: "https://www.hollywoodreporter.com/video/x/", Transport: unsupportedShowcase,
	}); !errors.Is(err, ErrInvalidMetadata) || strings.Contains(err.Error(), "vimeo") {
		t.Fatalf("showcase=%v", err)
	}

	malformed := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.iltalehti.fi/ulkomaat/a/uuid": []byte(`<script>window.App = {not-json</script>`),
	}}
	if _, err := NewIltalehti().Extract(context.Background(), Request{
		URL: "https://www.iltalehti.fi/ulkomaat/a/uuid", Transport: malformed,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed=%v", err)
	}

	oversized := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.mirror.co.uk/tv/article-27163139": wave1BytesRepeat('a', int(maxExtractorJSONBytes)+1),
	}}
	if _, err := NewMirrorCoUK().Extract(context.Background(), Request{
		URL: "https://www.mirror.co.uk/tv/article-27163139", Transport: oversized,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("oversized=%v", err)
	}

	invalidRoute := &sharedFixtureTransport{}
	if _, err := NewBundesliga().Extract(context.Background(), Request{
		URL: "https://www.bundesliga.com/en/bundesliga/videos", Transport: invalidRoute,
	}); !errors.Is(err, ErrUnsupported) || invalidRoute.requestCount() != 0 {
		t.Fatalf("invalid route=%v requests=%d", err, invalidRoute.requestCount())
	}

	if _, err := NewTheIntercept().Extract(context.Background(), Request{
		URL: "https://theintercept.com/fieldofvision/missing-slug/", Transport: &sharedFixtureTransport{
			pages: map[string][]byte{
				"https://theintercept.com/fieldofvision/missing-slug/": jwWave1Fixture(t, "theintercept_page.html"),
			},
		},
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("missing post=%v", err)
	}
}

func wave1BytesRepeat(ch byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = ch
	}
	return out
}

func FuzzParseJWPlatformAdaptersWave1URL(f *testing.F) {
	f.Add("https://www.bundesliga.com/en/bundesliga/videos?vid=bhhHkKyN")
	f.Add("https://uk.businessinsider.com/article-slug")
	f.Add("https://www.dagbladet.no/video/slug/PalfB2Cw")
	f.Add("https://www.hollywoodreporter.com/video/slug/")
	f.Add("https://www.iltalehti.fi/ulkomaat/a/9fbd067f-94e4-46cd-8748-9d958eb4dae2")
	f.Add("https://video.lefigaro.fr/embed/figaro/video/slug/")
	f.Add("https://www.mirror.co.uk/tv/tv-news/article-27163139")
	f.Add("http://www.outsidetv.com/home/play/ZjQYboH6/1/10/Hdg0jukV/4")
	f.Add("https://theintercept.com/fieldofvision/slug/")
	f.Add("http://user:pass@www.bundesliga.com/en/bundesliga/videos?vid=bhhHkKyN")
	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		_, _ = parseBundesligaURL(parsed)
		_, _ = parseBusinessInsiderURL(parsed)
		_, _ = parseDBTVURL(parsed)
		_, _ = parseHollywoodReporterURL(parsed)
		_, _ = parseIltalehtiURL(parsed)
		_, _ = parseLeFigaroVideoEmbedURL(parsed)
		_, _ = parseMirrorCoUKURL(parsed)
		_, _ = parseOutsideTVURL(parsed)
		_, _ = parseTheInterceptURL(parsed)
	})
}
