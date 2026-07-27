package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
		if err != nil || result.Redirect.URL != "https://www.youtube.com/watch?v=fixture0001" || result.Redirect.ExtractorKey != "youtube" {
			t.Fatalf("%#v %v", result, err)
		}
	})

	t.Run("iltalehti", func(t *testing.T) {
		rawURL := "https://www.iltalehti.fi/ulkomaat/a/9fbd067f-94e4-46cd-8748-9d958eb4dae2"
		canonical := "https://www.iltalehti.fi/ulkomaat/a/9fbd067f-94e4-46cd-8748-9d958eb4dae2"
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: jwWave1Fixture(t, "iltalehti_relaxed_page.html")}}
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
		if !result.Redirect.Transparent {
			t.Fatalf("lefigaro handoff is not transparent: %#v", result.Redirect)
		}
		if result.Redirect.Thumbnail != "https://images.example/lefigaro.jpg" {
			t.Fatalf("lefigaro poster lost: %#v", result.Redirect)
		}
	})
	t.Run("lefigaro_http_poster_omitted", func(t *testing.T) {
		rawURL := "https://video.lefigaro.fr/embed/figaro/video/les-francais-ne-veulent-ils-plus-travailler/"
		canonical := rawURL
		page := []byte(`<html><body>
<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"initialProps":{"pageData":{"playerData":{"videoId":"g9j7Eovo","title":"HTTP Poster","poster":"http://images.example/lefigaro-insecure.jpg"}}}}}}}</script>
</body></html>`)
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: page}}
		result, err := NewLeFigaroVideoEmbed().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if result.Redirect.Thumbnail != "" {
			t.Fatalf("lefigaro accepted http poster: %#v", result.Redirect)
		}
	})

	t.Run("mirrorcouk", func(t *testing.T) {
		rawURL := "https://www.mirror.co.uk/tv/tv-news/love-island-fans-baffled-after-27163139"
		canonical := "https://www.mirror.co.uk/tv/tv-news/love-island-fans-baffled-after-27163139"
		transport := &sharedFixtureTransport{pages: map[string][]byte{canonical: jwWave1Fixture(t, "mirror_page.html")}}
		result, err := NewMirrorCoUK().Extract(context.Background(), Request{URL: rawURL, Transport: transport})
		if err != nil || result.Redirect.URL != "jwplatform:voyyS7SV" || result.Redirect.ID != "voyyS7SV" {
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
		"https://www.iltalehti.fi/ulkomaat/a/uuid": []byte(`<script>window.App = {title: 'broken</script>`),
	}}
	if _, err := NewIltalehti().Extract(context.Background(), Request{
		URL: "https://www.iltalehti.fi/ulkomaat/a/uuid", Transport: malformed,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed=%v", err)
	}

	duplicateIntercept := &sharedFixtureTransport{pages: map[string][]byte{
		"https://theintercept.com/fieldofvision/thisisacoup-episode-four-surrender-or-die/": jwWave1Fixture(t, "theintercept_duplicate_page.html"),
	}}
	if _, err := NewTheIntercept().Extract(context.Background(), Request{
		URL: "https://theintercept.com/fieldofvision/thisisacoup-episode-four-surrender-or-die/", Transport: duplicateIntercept,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("duplicate slug=%v", err)
	}

	lotsOfNonmatchingPosts := &sharedFixtureTransport{pages: map[string][]byte{
		"https://theintercept.com/fieldofvision/match-slug/": []byte(buildManyNonmatchingThenOneMatch(jwWave1MaxEntries+1, "match-slug", "AbCd1234")),
	}}
	bigResult, err := NewTheIntercept().Extract(context.Background(), Request{
		URL: "https://theintercept.com/fieldofvision/match-slug/", Transport: lotsOfNonmatchingPosts,
	})
	if err != nil {
		t.Fatalf("big post set err=%v", err)
	}
	if bigResult.Redirect.URL != "jwplatform:AbCd1234" {
		t.Fatalf("big redirect=%#v", bigResult.Redirect)
	}
	// The generator must emit >128 unique keys. Re-decode the same page and
	// verify the encoded key count actually exceeded the prior 128-entry cap;
	// without this assertion the test would still pass if the generator
	// accidentally collapsed entries onto a single repeated key.
	decoded := jwWave1DecodePostsForBoundary(t, "https://theintercept.com/fieldofvision/match-slug/", lotsOfNonmatchingPosts)
	if decoded <= jwWave1MaxEntries {
		t.Fatalf("encoded post map should exceed %d entries, got %d", jwWave1MaxEntries, decoded)
	}

	malformedIltalehti := &sharedFixtureTransport{pages: map[string][]byte{
		"https://www.iltalehti.fi/ulkomaat/a/uuid": []byte(`<script>window.App = {not-json</script>`),
	}}
	if _, err := NewIltalehti().Extract(context.Background(), Request{
		URL: "https://www.iltalehti.fi/ulkomaat/a/uuid", Transport: malformedIltalehti,
	}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed iltalehti=%v", err)
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

// jwWave1DecodePostsForBoundary decodes the The Intercept post map from a
// generated page fixture and returns the number of unique posts. It proves
// that buildManyNonmatchingThenOneMatch truly emitted the requested number of
// unique keys instead of collapsing them.
func jwWave1DecodePostsForBoundary(t testing.TB, pageURL string, transport *sharedFixtureTransport) int {
	t.Helper()
	page, ok := transport.pages[pageURL]
	if !ok {
		t.Fatalf("missing fixture page %q", pageURL)
	}
	raw, err := extractJSONObjectAfter(page, theInterceptStore)
	if err != nil {
		t.Fatalf("decode intercept store: %v", err)
	}
	var store struct {
		Resources struct {
			Posts map[string]struct {
				ID         json.Number `json:"ID"`
				Slug       string      `json:"slug"`
				Title      string      `json:"title"`
				Date       string      `json:"date"`
				FOVVideoID string      `json:"fov_videoid"`
			} `json:"posts"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(raw, &store); err != nil {
		t.Fatalf("unmarshal intercept store: %v", err)
	}
	return len(store.Resources.Posts)
}

// buildManyNonmatchingThenOneMatch builds an initialStoreTree blob with
// `count` non-matching posts and one matching post whose slug is the
// requested matchSlug and fov_videoid is the requested media id. Each key is
// uniquely generated via strconv so the decoded map genuinely exercises the
// requested count boundary (repeating a fixed JSON key would collapse all
// entries into one in the resulting object).
func buildManyNonmatchingThenOneMatch(count int, matchSlug, mediaID string) string {
	var nonmatching strings.Builder
	nonmatching.Grow(count * 80)
	for index := 0; index < count; index++ {
		if index > 0 {
			nonmatching.WriteByte(',')
		}
		nonmatching.WriteString(`"k`)
		nonmatching.WriteString(strconv.Itoa(index))
		nonmatching.WriteString(`":{"ID":`)
		nonmatching.WriteString(strconv.Itoa(index + 1))
		nonmatching.WriteString(`,"slug":"oops","fov_videoid":"AbCd1234"}`)
	}
	matching := `"match":{"ID":` + strconv.Itoa(count+1) + `,"slug":"` + matchSlug + `","fov_videoid":"` + mediaID + `"}`
	return `<script>initialStoreTree = {"resources":{"posts":{` + nonmatching.String() + `,` + matching + `}}}</script>`
}

func TestJWPlatformAdaptersWave1SlugBounds(t *testing.T) {
	t.Parallel()
	atLimit := strings.Repeat("a", jwWave1MaxSlugBytes)
	overLimit := strings.Repeat("a", jwWave1MaxSlugBytes+1)

	tests := []struct {
		name string
		ok   func(*url.URL) bool
		raw  string
		want bool
	}{
		{
			name: "businessinsider-at-limit",
			ok:   func(u *url.URL) bool { _, ok := parseBusinessInsiderURL(u); return ok },
			raw:  "https://uk.businessinsider.com/" + atLimit,
			want: true,
		},
		{
			name: "businessinsider-over-limit",
			ok:   func(u *url.URL) bool { _, ok := parseBusinessInsiderURL(u); return ok },
			raw:  "https://uk.businessinsider.com/" + overLimit,
			want: false,
		},
		{
			name: "hollywoodreporter-at-limit",
			ok:   func(u *url.URL) bool { _, ok := parseHollywoodReporterURL(u); return ok },
			raw:  "https://www.hollywoodreporter.com/video/" + atLimit + "/",
			want: true,
		},
		{
			name: "hollywoodreporter-over-limit",
			ok:   func(u *url.URL) bool { _, ok := parseHollywoodReporterURL(u); return ok },
			raw:  "https://www.hollywoodreporter.com/video/" + overLimit + "/",
			want: false,
		},
		{
			name: "lefigaro-at-limit",
			ok:   func(u *url.URL) bool { _, ok := parseLeFigaroVideoEmbedURL(u); return ok },
			raw:  "https://video.lefigaro.fr/embed/figaro/video/" + atLimit + "/",
			want: true,
		},
		{
			name: "lefigaro-over-limit",
			ok:   func(u *url.URL) bool { _, ok := parseLeFigaroVideoEmbedURL(u); return ok },
			raw:  "https://video.lefigaro.fr/embed/figaro/video/" + overLimit + "/",
			want: false,
		},
		{
			name: "theintercept-at-limit",
			ok:   func(u *url.URL) bool { _, ok := parseTheInterceptURL(u); return ok },
			raw:  "https://theintercept.com/fieldofvision/" + atLimit + "/",
			want: true,
		},
		{
			name: "theintercept-over-limit",
			ok:   func(u *url.URL) bool { _, ok := parseTheInterceptURL(u); return ok },
			raw:  "https://theintercept.com/fieldofvision/" + overLimit + "/",
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := url.Parse(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := test.ok(parsed); got != test.want {
				t.Fatalf("parse(%q)=%t want %t", test.raw, got, test.want)
			}
		})
	}
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
	f.Add("https://evil-bundesliga.com/en/bundesliga/videos?vid=bhhHkKyN")
	f.Add("https://www.bundesliga.com.evil/en/bundesliga/videos?vid=bhhHkKyN")
	f.Add("https://www.bundesliga.com:443/en/bundesliga/videos?vid=bhhHkKyN")
	f.Add("https://www.bundesliga.com/en/bundesliga/videos?vid=bhhHkKyN#frag")
	f.Add("https://www.bundesliga.com/en/%2ebundesliga/videos?vid=bhhHkKyN")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		unsafe := hostedRejectUnsafeURL(parsed)

		if videoID, ok := parseBundesligaURL(parsed); ok {
			jwWave1FuzzAssertAcceptedURL(t, parsed, unsafe, "bundesliga.com", "www.bundesliga.com")
			if !jwPlatformID.MatchString(videoID) {
				t.Fatalf("bundesliga accepted invalid id %q", videoID)
			}
		}

		if displayID, ok := parseBusinessInsiderURL(parsed); ok {
			jwWave1FuzzAssertAcceptedURL(t, parsed, unsafe, "businessinsider.com", "businessinsider.nl")
			if !jwWave1BoundedSlug(displayID) {
				t.Fatalf("businessinsider accepted invalid slug %q", displayID)
			}
		}

		if videoID, ok := parseDBTVURL(parsed); ok {
			jwWave1FuzzAssertAcceptedURL(t, parsed, unsafe, "dagbladet.no", "www.dagbladet.no")
			if len(videoID) != 8 && len(videoID) != 11 {
				t.Fatalf("dbtv accepted invalid id length %q", videoID)
			}
			if len(videoID) == 8 && !jwPlatformID.MatchString(videoID) {
				t.Fatalf("dbtv accepted invalid jw id %q", videoID)
			}
			if len(videoID) == 11 && !jwWave1YouTubeID.MatchString(videoID) {
				t.Fatalf("dbtv accepted invalid youtube id %q", videoID)
			}
		}

		if slug, ok := parseHollywoodReporterURL(parsed); ok {
			jwWave1FuzzAssertAcceptedURL(t, parsed, unsafe, "hollywoodreporter.com", "www.hollywoodreporter.com")
			if !jwWave1BoundedSlug(slug) {
				t.Fatalf("hollywoodreporter accepted invalid slug %q", slug)
			}
		}

		if articleID, ok := parseIltalehtiURL(parsed); ok {
			jwWave1FuzzAssertAcceptedURL(t, parsed, unsafe, "iltalehti.fi", "www.iltalehti.fi")
			if articleID == "" || len(articleID) > 64 {
				t.Fatalf("iltalehti accepted invalid article id %q", articleID)
			}
		}

		if slug, ok := parseLeFigaroVideoEmbedURL(parsed); ok {
			jwWave1FuzzAssertAcceptedURL(t, parsed, unsafe, "video.lefigaro.fr")
			if strings.ToLower(parsed.Hostname()) != "video.lefigaro.fr" {
				t.Fatalf("lefigaro accepted lookalike host %q", parsed.Hostname())
			}
			if !jwWave1BoundedSlug(slug) {
				t.Fatalf("lefigaro accepted invalid slug %q", slug)
			}
		}

		if displayID, ok := parseMirrorCoUKURL(parsed); ok {
			jwWave1FuzzAssertAcceptedURL(t, parsed, unsafe, "mirror.co.uk", "www.mirror.co.uk")
			if displayID == "" || len(displayID) > 16 {
				t.Fatalf("mirror accepted invalid display id %q", displayID)
			}
		}

		if mediaID, ok := parseOutsideTVURL(parsed); ok {
			jwWave1FuzzAssertAcceptedURL(t, parsed, unsafe, "outsidetv.com", "www.outsidetv.com")
			if !jwPlatformID.MatchString(mediaID) {
				t.Fatalf("outsidetv accepted invalid media id %q", mediaID)
			}
		}

		if slug, ok := parseTheInterceptURL(parsed); ok {
			jwWave1FuzzAssertAcceptedURL(t, parsed, unsafe, "theintercept.com")
			if strings.ToLower(parsed.Hostname()) != "theintercept.com" {
				t.Fatalf("theintercept accepted lookalike host %q", parsed.Hostname())
			}
			if !jwWave1BoundedSlug(slug) {
				t.Fatalf("theintercept accepted invalid slug %q", slug)
			}
		}
	})
}

func jwWave1FuzzAssertAcceptedURL(t *testing.T, parsed *url.URL, unsafe bool, allowedHosts ...string) {
	t.Helper()
	if unsafe {
		t.Fatalf("accepted unsafe URL %q", parsed)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		t.Fatalf("accepted non-http(s) scheme %q", parsed.Scheme)
	}
	if parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawFragment != "" {
		t.Fatalf("accepted unsafe URL parts in %q", parsed)
	}
	host := strings.ToLower(parsed.Hostname())
	for _, allowed := range allowedHosts {
		if host == allowed || host == "www."+allowed {
			return
		}
		if businessInsiderHost(host) && (allowed == "businessinsider.com" || allowed == "businessinsider.nl") {
			return
		}
	}
	t.Fatalf("accepted lookalike host %q not in %v", host, allowedHosts)
}
