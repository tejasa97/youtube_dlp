package extractor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readAeonCoFixture(t testing.TB, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "shared", "aeonco", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func aeonCoTransport(pageURL string, page []byte) *publicExtractorTransport {
	return &publicExtractorTransport{pages: map[string][]byte{pageURL: page}}
}

func TestAeonCoHandoffVimeoPreservesReferer(t *testing.T) {
	pageURL := "https://aeon.co/videos/raw-solar-storm-footage-is-the-punk-rock-antidote-to-sleek-james-webb-imagery"
	result, err := NewAeonCo().Extract(context.Background(), Request{
		URL:       pageURL,
		Transport: aeonCoTransport(pageURL, readAeonCoFixture(t, "vimeo_page.html")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsURL() {
		t.Fatalf("result = %+v", result)
	}
	entry := *result.Redirect
	if entry.URL != "https://vimeo.com/123456789" || entry.ExtractorKey != "vimeo" ||
		entry.ID != "123456789" || !entry.Transparent || entry.Referer != aeonCoReferer {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestAeonCoHandoffYouTube(t *testing.T) {
	pageURL := "https://aeon.co/videos/chew-over-the-prisoners-dilemma-and-see-if-you-can-find-the-rational-path-out"
	result, err := NewAeonCo().Extract(context.Background(), Request{
		URL:       pageURL,
		Transport: aeonCoTransport(pageURL, readAeonCoFixture(t, "youtube_page.html")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsURL() {
		t.Fatalf("result = %+v", result)
	}
	entry := *result.Redirect
	if entry.URL != "https://www.youtube.com/watch?v=emyi4z-O0ls" || entry.ExtractorKey != "youtube" ||
		entry.ID != "emyi4z-O0ls" || !entry.Transparent || entry.Referer != "" {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestAeonCoMultipleMalformedJSONLDBlocksPickFirstSupportedVideoEmbed(t *testing.T) {
	pageURL := "https://aeon.co/videos/dazzling-timelapse-shows-how-microbes-spoil-our-food-and-sometimes-enrich-it"
	result, err := NewAeonCo().Extract(context.Background(), Request{
		URL:       pageURL,
		Transport: aeonCoTransport(pageURL, readAeonCoFixture(t, "mixed_jsonld_page.html")),
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := *result.Redirect
	if entry.URL != "https://vimeo.com/759576926" || entry.ExtractorKey != "vimeo" || entry.ID != "759576926" ||
		entry.Referer != aeonCoReferer {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestAeonCoMissingAndHostileEmbedsAreUnavailableAndSecretSafe(t *testing.T) {
	for _, test := range []struct {
		name    string
		slug    string
		fixture string
	}{
		{name: "missing", slug: "no-embed", fixture: "no_embed_page.html"},
		{name: "hostile", slug: "hostile-embed", fixture: "hostile_embed_page.html"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pageURL := "https://aeon.co/videos/" + test.slug
			_, err := NewAeonCo().Extract(context.Background(), Request{
				URL:       pageURL,
				Transport: aeonCoTransport(pageURL, readAeonCoFixture(t, test.fixture)),
			})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error = %v", err)
			}
			if text := strings.ToLower(err.Error()); strings.Contains(text, "secret") || strings.Contains(text, "evil.invalid") {
				t.Fatalf("error leaked hostile embed data: %q", err)
			}
		})
	}
}

func TestAeonCoRouting(t *testing.T) {
	for _, raw := range []string{
		"https://aeon.co/videos/raw-solar-storm-footage",
		"https://www.aeon.co/videos/dazzling-timelapse-2",
	} {
		parsed, err := url.Parse(raw)
		if err != nil || !NewAeonCo().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false, parse error %v", raw, err)
		}
		target, ok := classifyAeonCoURL(parsed)
		if !ok || target.webpageURL != "https://aeon.co/videos/"+strings.TrimPrefix(parsed.Path, "/videos/") {
			t.Fatalf("classifyAeonCoURL(%q) = %#v, %v", raw, target, ok)
		}
	}
	for _, raw := range []string{
		"http://aeon.co/videos/slug",
		"https://notaeon.co/videos/slug",
		"https://aeon.co.evil/videos/slug",
		"https://aeon.co./videos/slug",
		"https://aeon.co:443/videos/slug",
		"https://user@aeon.co/videos/slug",
		"https://aeon.co/videos/slug?x=1",
		"https://aeon.co/videos/slug?",
		"https://aeon.co/videos/slug#fragment",
		"https://aeon.co/videos",
		"https://aeon.co/videos/",
		"https://aeon.co//videos/slug",
		"https://aeon.co/videos/slug/",
		"https://aeon.co/videos/a/b",
		"https://aeon.co/videos/%2f",
		"https://aeon.co/videos/slug%2fmore",
		"https://aeon.co/videos%2fslug",
		"https://aeon.co/videos/%5c",
		"https://aeon.co/videos/slug%5cmore",
		"https://aeon.co/videos/%252f",
		"https://aeon.co/videos/slug%252fmore",
		"https://aeon.co/videos/%255c",
		"https://aeon.co/videos/%00",
		"https://aeon.co/videos/slug%00tail",
		"https://aeon.co/videos/%2500",
		"https://aeon.co/%2e/videos/slug",
		"https://aeon.co/%252e/videos/slug",
		"https://aeon.co/videos/-slug",
		"https://aeon.co/videos/slug-",
		"https://aeon.co/videos/slug--part",
		"https://aeon.co/videos/Slug",
		"https://aeon.co/videos/slug_part",
		"ftp://aeon.co/videos/slug",
	} {
		parsed, err := url.Parse(raw)
		if err == nil && NewAeonCo().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true", raw)
		}
	}
}

func TestAeonCoCancellationAvoidsNetworkAccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &aeonCoCountingTransport{}
	_, err := NewAeonCo().Extract(ctx, Request{URL: "https://aeon.co/videos/cancelled", Transport: transport})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel error = %v", err)
	}
	if transport.reads != 0 {
		t.Fatalf("page reads = %d", transport.reads)
	}

	ctx, cancel = context.WithCancel(context.Background())
	transport.cancel = cancel
	_, err = NewAeonCo().Extract(ctx, Request{URL: "https://aeon.co/videos/cancelled", Transport: transport})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("read cancellation error = %v", err)
	}
}

func TestAeonCoPageBoundsAndMalformedPageErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		page []byte
		want error
	}{
		{name: "oversized response", page: bytes.Repeat([]byte("x"), maxGenericHTMLBytes+1), want: ErrInvalidMetadata},
		{name: "token limit", page: bytes.Repeat([]byte("<meta>"), maxGenericHTMLTokens+1), want: ErrInvalidMetadata},
		{name: "JSON-LD script limit", page: []byte(strings.Repeat(`<script type="application/ld+json">{}</script>`, maxGenericJSONLDScripts+1)), want: ErrPlaylistLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			pageURL := "https://aeon.co/videos/bounded-page"
			_, err := NewAeonCo().Extract(context.Background(), Request{URL: pageURL, Transport: aeonCoTransport(pageURL, test.page)})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestAeonCoVimeoReentryUsesAeonReferer(t *testing.T) {
	pageURL := "https://aeon.co/videos/raw-solar-storm-footage"
	handoff, err := NewAeonCo().Extract(context.Background(), Request{
		URL:       pageURL,
		Transport: aeonCoTransport(pageURL, readAeonCoFixture(t, "vimeo_page.html")),
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := *handoff.Redirect
	transport := &aeonCoVimeoTransport{page: readVimeoFixture(t, "page.html"), config: readVimeoFixture(t, "config.json")}
	result, err := NewVimeo().Extract(context.Background(), Request{URL: entry.URL, Referer: entry.Referer, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsURL() || result.IsPlaylist() {
		t.Fatalf("Vimeo result = %+v", result)
	}
	if transport.configGets != 1 || transport.referer != aeonCoReferer {
		t.Fatalf("config requests=%d referer=%q", transport.configGets, transport.referer)
	}
}

type aeonCoCountingTransport struct {
	reads  int
	cancel context.CancelFunc
}

func (*aeonCoCountingTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected request")
}

func (transport *aeonCoCountingTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	transport.reads++
	if transport.cancel != nil {
		transport.cancel()
	}
	return nil, nil, errors.New("masked transport error")
}

type aeonCoVimeoTransport struct {
	page, config []byte
	referer      string
	configGets   int
}

func (*aeonCoVimeoTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected native page request")
}

func (transport *aeonCoVimeoTransport) ReadPageProfile(_ context.Context, rawURL, _ string) ([]byte, http.Header, error) {
	if rawURL != "https://vimeo.com/123456789" {
		return nil, nil, fmt.Errorf("unexpected webpage URL %q", rawURL)
	}
	return append([]byte(nil), transport.page...), make(http.Header), nil
}

func (transport *aeonCoVimeoTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.configGets++
	transport.referer = request.Header.Get("Referer")
	if request.Method != http.MethodGet || request.URL.Host != "player.vimeo.com" || transport.referer != aeonCoReferer {
		return nil, errors.New("unexpected Vimeo config request")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(transport.config)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func (transport *aeonCoVimeoTransport) DoProfile(ctx context.Context, request *http.Request, _ string) (*http.Response, error) {
	return transport.Do(ctx, request)
}

func FuzzAeonCoRouting(f *testing.F) {
	for _, seed := range []string{
		"https://aeon.co/videos/raw-solar-storm-footage",
		"https://www.aeon.co/videos/dazzling-timelapse-2",
		"https://notaeon.co/videos/slug",
		"https://aeon.co/videos/%2f",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		target, classified := classifyAeonCoURL(parsed)
		if NewAeonCo().Suitable(parsed) != classified {
			t.Fatalf("Suitable(%q) disagrees with classification", raw)
		}
		if !classified {
			return
		}
		if parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			t.Fatalf("unsafe successful route: %#v", parsed)
		}
		if target.webpageURL != "https://aeon.co/videos/"+target.slug || !aeonCoSlugPattern.MatchString(target.slug) {
			t.Fatalf("target = %#v", target)
		}
		canonical, err := url.Parse(target.webpageURL)
		if err != nil || !NewAeonCo().Suitable(canonical) {
			t.Fatalf("canonical route = %q, %v", target.webpageURL, err)
		}
	})
}

func FuzzAeonCoJSONLDHandoff(f *testing.F) {
	f.Add(readAeonCoFixture(f, "vimeo_page.html"))
	f.Add(readAeonCoFixture(f, "youtube_page.html"))
	f.Add(readAeonCoFixture(f, "mixed_jsonld_page.html"))
	f.Add(readAeonCoFixture(f, "hostile_embed_page.html"))
	f.Fuzz(func(t *testing.T, page []byte) {
		if len(page) > 128<<10 {
			t.Skip()
		}
		pageURL := "https://aeon.co/videos/fuzz-page"
		result, err := NewAeonCo().Extract(context.Background(), Request{URL: pageURL, Transport: aeonCoTransport(pageURL, page)})
		if err != nil {
			if !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrInvalidMetadata) && !errors.Is(err, ErrPlaylistLimit) {
				t.Fatalf("uncategorized error: %v", err)
			}
			return
		}
		if !result.IsURL() {
			t.Fatalf("successful result = %+v", result)
		}
		entry := *result.Redirect
		switch entry.ExtractorKey {
		case "vimeo":
			parsed, parseErr := url.Parse(entry.URL)
			if parseErr != nil || !NewVimeo().Suitable(parsed) || entry.Referer != aeonCoReferer {
				t.Fatalf("Vimeo entry = %#v, parse error %v", entry, parseErr)
			}
		case "youtube":
			parsed, parseErr := url.Parse(entry.URL)
			if parseErr != nil || !NewYouTube().Suitable(parsed) || entry.Referer != "" {
				t.Fatalf("YouTube entry = %#v, parse error %v", entry, parseErr)
			}
		default:
			t.Fatalf("unsupported successful handoff = %#v", entry)
		}
		if !entry.Transparent {
			t.Fatalf("non-transparent handoff = %#v", entry)
		}
	})
}
