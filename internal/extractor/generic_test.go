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
	"testing"
	"time"
)

type genericHTMLTransport struct {
	body               []byte
	headType, getType  string
	headStatus, status int
	finalURL           string
	blockGET           bool
}

func (transport *genericHTMLTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	status := transport.status
	contentType := transport.getType
	body := transport.body
	if request.Method == http.MethodHead {
		status = transport.headStatus
		contentType = transport.headType
		body = nil
	} else if transport.blockGET {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if status == 0 {
		status = http.StatusOK
	}
	responseRequest := request.Clone(ctx)
	if transport.finalURL != "" {
		final, err := url.Parse(transport.finalURL)
		if err != nil {
			return nil, err
		}
		responseRequest.URL = final
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    responseRequest,
	}, nil
}

func (*genericHTMLTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected ReadPage call")
}

func readGenericEmbedFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "generic_embed", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func genericHTMLRequest(body []byte) Request {
	return Request{
		URL: "https://publisher.invalid/articles/embed-roundup",
		Transport: &genericHTMLTransport{
			body: body, headType: "text/html; charset=utf-8", getType: "text/html",
		},
	}
}

func TestGenericDiscoversSingleCanonicalEmbed(t *testing.T) {
	result, err := NewGeneric().Extract(context.Background(), genericHTMLRequest(readGenericEmbedFixture(t, "single.html")))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() {
		t.Fatal("single embed is not the temporary recursive entry container")
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	entry := entries[0]
	if entry.URL != "https://www.youtube.com/watch?start=10&v=ABCDEFGHIJK" ||
		entry.ExtractorKey != "youtube" || entry.ID != "ABCDEFGHIJK" || !entry.Transparent {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestGenericDiscoversMultipleEmbedsInDocumentOrder(t *testing.T) {
	result, err := NewGeneric().Extract(context.Background(), genericHTMLRequest(readGenericEmbedFixture(t, "multiple.html")))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		url, key string
	}{
		{"https://streamable.com/Ab_C12", "streamable"},
		{"https://vimeo.com/123456789", "vimeo"},
		{"https://www.dailymotion.com/video/x7abcde", "dailymotion"},
		{"https://rumble.com/embed/vabc123", "rumble"},
	}
	if len(entries) != len(want) {
		t.Fatalf("entries = %#v", entries)
	}
	for index, expected := range want {
		if entries[index].URL != expected.url || entries[index].ExtractorKey != expected.key || !entries[index].Transparent {
			t.Errorf("entry[%d] = %#v, want URL %q key %q", index, entries[index], expected.url, expected.key)
		}
	}
}

func TestCanonicalGenericEmbedSupportedProviderRoutes(t *testing.T) {
	base, _ := url.Parse("https://publisher.invalid/article")
	tests := []struct {
		raw, wantURL, wantKey string
	}{
		{"https://www.youtube.com/embed/ABCDEFGHIJK", "https://www.youtube.com/watch?v=ABCDEFGHIJK", "youtube"},
		{"https://player.vimeo.com/video/123456789", "https://vimeo.com/123456789", "vimeo"},
		{"https://players.brightcove.net/123/default_default/index.html?videoId=456", "https://players.brightcove.net/123/default_default/index.html?videoId=456", "brightcove"},
		{"https://cdnapisec.kaltura.com/p/123/sp/12300/embedIframeJs?entry_id=1_abcd1234&partner_id=123", "kaltura:123:1_abcd1234:html5", "kaltura"},
		{"https://cdn.jwplayer.com/players/Ab12Cd34-player.js", "https://cdn.jwplayer.com/players/Ab12Cd34", "jwplatform"},
		{"https://fast.wistia.net/embed/iframe/a1b2c3d4e5", "wistia:a1b2c3d4e5", "wistia"},
		{"https://videos.sproutvideo.com/embed/abcdef/1234567890abcdef", "https://videos.sproutvideo.com/embed/abcdef/1234567890abcdef", "sproutvideo"},
		{"https://www.dailymotion.com/embed/video/x7abcde", "https://www.dailymotion.com/video/x7abcde", "dailymotion"},
		{"https://rumble.com/embed/vabc123.html", "https://rumble.com/embed/vabc123", "rumble"},
		{"https://streamable.com/e/Ab_C12", "https://streamable.com/Ab_C12", "streamable"},
		{"https://peertube.example/videos/embed/12345678-1234-1234-1234-123456789abc", "https://peertube.example/videos/watch/12345678-1234-1234-1234-123456789abc", "peertube"},
	}
	for _, test := range tests {
		t.Run(test.wantKey, func(t *testing.T) {
			entry, ok := canonicalGenericEmbed(base, test.raw)
			if !ok {
				t.Fatalf("canonicalGenericEmbed(%q) rejected", test.raw)
			}
			if entry.URL != test.wantURL || entry.ExtractorKey != test.wantKey || !entry.Transparent {
				t.Fatalf("entry = %#v", entry)
			}
		})
	}
}

func TestCanonicalGenericEmbedRelativeResolutionIsProviderBounded(t *testing.T) {
	vimeoBase, _ := url.Parse("https://player.vimeo.com/articles/one")
	entry, ok := canonicalGenericEmbed(vimeoBase, "/video/987654321")
	if !ok || entry.URL != "https://vimeo.com/987654321" {
		t.Fatalf("relative Vimeo entry = %#v, %t", entry, ok)
	}
	publisher, _ := url.Parse("https://publisher.invalid/articles/one")
	if entry, ok := canonicalGenericEmbed(publisher, "/video/987654321"); ok {
		t.Fatalf("publisher-relative path accepted: %#v", entry)
	}
}

func TestGenericUsesFinalResponseURLForRelativeEmbed(t *testing.T) {
	transport := &genericHTMLTransport{
		headType: "text/html", getType: "text/html",
		finalURL: "https://player.vimeo.com/articles/one",
		body:     []byte(`<iframe src="/video/987654321"></iframe>`),
	}
	result, err := NewGeneric().Extract(context.Background(), Request{
		URL: "https://publisher.invalid/redirect", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].URL != "https://vimeo.com/987654321" {
		t.Fatalf("entries = %#v", entries)
	}
	if got, _ := result.Info.Lookup("webpage_url").StringValue(); got != transport.finalURL {
		t.Fatalf("webpage_url = %q", got)
	}
}

func TestCanonicalGenericEmbedRejectsUnsafeAndNonEmbedURLs(t *testing.T) {
	base, _ := url.Parse("https://publisher.invalid/article")
	for _, raw := range []string{
		"javascript:alert(1)",
		"data:text/html,<iframe>",
		"https://user@player.vimeo.com/video/123",
		"https://player.vimeo.com:443/video/123",
		"https://player.vimeo.com/video/123#fragment",
		"https://player.vimeo.com/video%2f123",
		"https://player.vimeo.com.evil.invalid/video/123",
		"https://vimeo.com/123",
		"https://www.youtube.com/watch?v=ABCDEFGHIJK",
		"https://streamable.com/Ab_C12",
		"https://rumble.com/vabc123-title.html",
		"//evil.invalid/embed/ABCDEFGHIJK",
	} {
		if entry, ok := canonicalGenericEmbed(base, raw); ok {
			t.Errorf("canonicalGenericEmbed(%q) = %#v", raw, entry)
		}
	}
}

func TestGenericHTMLUnsupportedAndResponseFailures(t *testing.T) {
	tests := []struct {
		name      string
		transport *genericHTMLTransport
		want      error
	}{
		{
			name: "no supported embed",
			transport: &genericHTMLTransport{
				headType: "text/html", getType: "text/html",
				body: []byte(`<iframe src="https://unsupported.invalid/player"></iframe>`),
			},
			want: ErrUnsupported,
		},
		{
			name: "head status",
			transport: &genericHTMLTransport{
				headType: "text/html", headStatus: http.StatusNotFound,
			},
			want: ErrUnsupported,
		},
		{
			name: "get status",
			transport: &genericHTMLTransport{
				headType: "text/html", getType: "text/html", status: http.StatusForbidden,
			},
			want: ErrUnsupported,
		},
		{
			name: "content changes",
			transport: &genericHTMLTransport{
				headType: "text/html", getType: "application/json", body: []byte(`{}`),
			},
			want: ErrUnsupported,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewGeneric().Extract(context.Background(), Request{
				URL: "https://publisher.invalid/article", Transport: test.transport,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("Extract() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestGenericHTMLBounds(t *testing.T) {
	t.Run("page bytes", func(t *testing.T) {
		request := genericHTMLRequest(bytes.Repeat([]byte("x"), maxGenericHTMLBytes+1))
		if _, err := NewGeneric().Extract(context.Background(), request); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("Extract() error = %v", err)
		}
	})
	t.Run("depth", func(t *testing.T) {
		page := []byte(strings.Repeat("<div>", maxGenericHTMLDepth+1))
		if _, err := discoverGenericEmbedEntries(context.Background(), mustGenericURL(t, "https://publisher.invalid"), page); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("discover error = %v", err)
		}
	})
	t.Run("tokens", func(t *testing.T) {
		page := []byte(strings.Repeat("<br>", maxGenericHTMLTokens+1))
		if _, err := discoverGenericEmbedEntries(context.Background(), mustGenericURL(t, "https://publisher.invalid"), page); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("discover error = %v", err)
		}
	})
	t.Run("candidates", func(t *testing.T) {
		page := []byte(strings.Repeat(`<iframe src="https://unsupported.invalid/embed"></iframe>`, maxGenericEmbedCandidates+1))
		if _, err := discoverGenericEmbedEntries(context.Background(), mustGenericURL(t, "https://publisher.invalid"), page); !errors.Is(err, ErrPlaylistLimit) {
			t.Fatalf("discover error = %v", err)
		}
	})
	t.Run("unique embeds", func(t *testing.T) {
		var page strings.Builder
		for index := 0; index <= maxGenericEmbeds; index++ {
			page.WriteString(`<iframe src="https://www.dailymotion.com/embed/video/x`)
			page.WriteString(strings.Repeat("a", index+1))
			page.WriteString(`"></iframe>`)
		}
		if _, err := discoverGenericEmbedEntries(context.Background(), mustGenericURL(t, "https://publisher.invalid"), []byte(page.String())); !errors.Is(err, ErrPlaylistLimit) {
			t.Fatalf("discover error = %v", err)
		}
	})
	t.Run("candidate URL", func(t *testing.T) {
		page := []byte(`<iframe src="https://player.vimeo.com/video/` + strings.Repeat("1", maxGenericEmbedURLBytes) + `"></iframe>`)
		if _, err := discoverGenericEmbedEntries(context.Background(), mustGenericURL(t, "https://publisher.invalid"), page); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("discover error = %v", err)
		}
	})
}

func TestGenericHTMLCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	transport := &genericHTMLTransport{headType: "text/html", getType: "text/html", blockGET: true}
	_, err := NewGeneric().Extract(ctx, Request{URL: "https://publisher.invalid/article", Transport: transport})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Extract() error = %v", err)
	}

	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := discoverGenericEmbedEntries(cancelled, mustGenericURL(t, "https://publisher.invalid"), []byte("<html>")); !errors.Is(err, context.Canceled) {
		t.Fatalf("discover error = %v", err)
	}
}

func mustGenericURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func FuzzDiscoverGenericEmbedEntries(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`<iframe src="https://www.youtube.com/embed/ABCDEFGHIJK"></iframe>`),
		[]byte(`<meta property="twitter:player" content="//player.vimeo.com/video/123">`),
		[]byte(`<object data="javascript:alert(1)"></object>`),
		[]byte(strings.Repeat("<div>", 300)),
		{0xff, 0xfe, '<', 'b', 'r', '>'},
	} {
		f.Add(seed)
	}
	base, _ := url.Parse("https://publisher.invalid/article")
	f.Fuzz(func(t *testing.T, page []byte) {
		if len(page) > maxGenericHTMLBytes+1 {
			return
		}
		entries, err := discoverGenericEmbedEntries(context.Background(), base, page)
		if err != nil {
			return
		}
		if len(entries) > maxGenericEmbeds {
			t.Fatalf("entries = %d", len(entries))
		}
		seen := make(map[string]bool)
		for _, entry := range entries {
			if entry.URL == "" || entry.ExtractorKey == "" || !entry.Transparent {
				t.Fatalf("invalid entry: %#v", entry)
			}
			key := entry.ExtractorKey + "\x00" + entry.URL
			if seen[key] {
				t.Fatalf("duplicate entry: %#v", entry)
			}
			seen[key] = true
		}
	})
}
