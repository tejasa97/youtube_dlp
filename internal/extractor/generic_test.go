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
	methods            []string
	getReadError       error
}

func (transport *genericHTMLTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.methods = append(transport.methods, request.Method)
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
	responseBody := io.Reader(bytes.NewReader(body))
	if request.Method == http.MethodGet && transport.getReadError != nil {
		responseBody = failingGenericReader{err: transport.getReadError}
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(responseBody),
		Request:    responseRequest,
	}, nil
}

type failingGenericReader struct{ err error }

func (reader failingGenericReader) Read([]byte) (int, error) { return 0, reader.err }

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
	if !result.IsURL() || result.Redirect == nil {
		t.Fatalf("single embed result = %#v", result)
	}
	entry := *result.Redirect
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

func TestGenericDiscoversOpenGraphMediaWithRefererAndManifest(t *testing.T) {
	result, err := NewGeneric().Extract(context.Background(), genericHTMLRequest(readGenericEmbedFixture(t, "open_graph.html")))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsPlaylist() || result.IsURL() {
		t.Fatalf("metadata media result shape: %+v", result)
	}
	if title, _ := result.Info.Title(); title != "Synthetic OpenGraph Feature" {
		t.Fatalf("title = %q", title)
	}
	if description, _ := result.Info.Lookup("description").StringValue(); description != "A deterministic metadata-media fixture." {
		t.Fatalf("description = %q", description)
	}
	if thumbnail, _ := result.Info.Lookup("thumbnail").StringValue(); thumbnail != "https://publisher.invalid/images/poster.jpg" {
		t.Fatalf("thumbnail = %q", thumbnail)
	}
	formats, ok := result.Info.Lookup("formats").ListValue()
	if !ok || len(formats) != 2 {
		t.Fatalf("formats = %#v", formats)
	}
	first, _ := formats[0].Object()
	second, _ := formats[1].Object()
	if rawURL, _ := first.Lookup("url").StringValue(); rawURL != "https://publisher.invalid/media/feature.mp4" {
		t.Fatalf("first URL = %q", rawURL)
	}
	if protocol, _ := second.Lookup("protocol").StringValue(); protocol != "m3u8_native" {
		t.Fatalf("second protocol = %q", protocol)
	}
	headers, _ := second.Lookup("http_headers").Object()
	if referer, _ := headers.Lookup("Referer").StringValue(); referer != "https://publisher.invalid/articles/embed-roundup" {
		t.Fatalf("referer = %q", referer)
	}
}

func TestGenericJSONLDPrecedesOpenGraphAndPreservesMetadata(t *testing.T) {
	result, err := NewGeneric().Extract(context.Background(), genericHTMLRequest(readGenericEmbedFixture(t, "json_ld.html")))
	if err != nil {
		t.Fatal(err)
	}
	if title, _ := result.Info.Title(); title != "Synthetic JSON-LD Feature" {
		t.Fatalf("title = %q", title)
	}
	if description, _ := result.Info.Lookup("description").StringValue(); description != "A bounded VideoObject fixture." {
		t.Fatalf("description = %q", description)
	}
	if duration, ok := result.Info.Lookup("duration").Float(); !ok || duration != 62.5 {
		t.Fatalf("duration = %v, %t", duration, ok)
	}
	for key, expected := range map[string]string{
		"uploader": "Fixture Publisher",
		"artist":   "Fixture Artist",
	} {
		if actual, _ := result.Info.Lookup(key).StringValue(); actual != expected {
			t.Fatalf("%s = %q", key, actual)
		}
	}
	for key, expected := range map[string]int64{
		"timestamp":  time.Date(2026, time.July, 1, 12, 34, 56, 0, time.UTC).Unix(),
		"filesize":   1234567,
		"width":      1920,
		"height":     1080,
		"view_count": 7654,
	} {
		if actual, ok := result.Info.Lookup(key).Int(); !ok || actual != expected {
			t.Fatalf("%s = %d, %t", key, actual, ok)
		}
	}
	if bitrate, ok := result.Info.Lookup("tbr").Float(); !ok || bitrate != 2500.5 {
		t.Fatalf("tbr = %v, %t", bitrate, ok)
	}
	tags, ok := result.Info.Lookup("tags").ListValue()
	if !ok || len(tags) != 2 {
		t.Fatalf("tags = %#v", tags)
	}
	for index, expected := range []string{"native Go", "metadata"} {
		if actual, _ := tags[index].StringValue(); actual != expected {
			t.Fatalf("tag[%d] = %q", index, actual)
		}
	}
	formats, _ := result.Info.Lookup("formats").ListValue()
	if len(formats) != 1 {
		t.Fatalf("formats = %#v", formats)
	}
	format, _ := formats[0].Object()
	if rawURL, _ := format.Lookup("url").StringValue(); rawURL != "https://publisher.invalid/media/jsonld" {
		t.Fatalf("JSON-LD URL = %q", rawURL)
	}
	for key, expected := range map[string]int64{"filesize": 1234567, "width": 1920, "height": 1080} {
		if actual, ok := format.Lookup(key).Int(); !ok || actual != expected {
			t.Fatalf("format %s = %d, %t", key, actual, ok)
		}
	}
	if bitrate, ok := format.Lookup("tbr").Float(); !ok || bitrate != 2500.5 {
		t.Fatalf("format tbr = %v, %t", bitrate, ok)
	}
}

func TestGenericJSONLDInvalidExtendedMetadataIsIgnored(t *testing.T) {
	page := []byte(`<script type="application/ld+json">{
		"@context":"https://schema.org","@type":"VideoObject",
		"contentUrl":"/video.mp4","uploadDate":"not-a-date",
		"contentSize":"-1","bitrate":"NaN","width":"1.5","height":0,
		"interactionCount":"9223372036854775808","keywords":[null,{},42]
	}</script>`)
	result, err := NewGeneric().Extract(context.Background(), genericHTMLRequest(page))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"timestamp", "filesize", "tbr", "width", "height", "view_count", "tags"} {
		if !result.Info.Lookup(key).IsMissing() {
			t.Fatalf("%s unexpectedly present", key)
		}
	}
	formats, _ := result.Info.Formats()
	format, _ := formats[0].Object()
	for _, key := range []string{"filesize", "tbr", "width", "height"} {
		if !format.Lookup(key).IsMissing() {
			t.Fatalf("format %s unexpectedly present", key)
		}
	}
}

func TestGenericProviderEmbedsPrecedeMetadataMedia(t *testing.T) {
	page := []byte(`<html><head><meta property="og:video" content="/fallback.mp4"></head><body><iframe src="https://www.youtube.com/embed/dQw4w9WgXcQ"></iframe></body></html>`)
	result, err := NewGeneric().Extract(context.Background(), genericHTMLRequest(page))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsURL() || result.Redirect.ExtractorKey != "youtube" {
		t.Fatalf("result = %+v", result)
	}
}

func TestGenericMetadataMediaRejectsUnsafeAndNonMediaURLs(t *testing.T) {
	_, err := NewGeneric().Extract(context.Background(), genericHTMLRequest(readGenericEmbedFixture(t, "metadata_unsafe.html")))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("error = %v", err)
	}
}

func TestGenericTwitterStreamPrecedesOpenGraphAndHandlesTypeBeforeURL(t *testing.T) {
	page := []byte(`<html><head>
		<meta property="og:video" content="/fallback.mp4">
		<meta name="twitter:player:stream:content_type" content="video/mp4">
		<meta name="twitter:player:stream" content="/twitter-stream">
	</head></html>`)
	result, err := NewGeneric().Extract(context.Background(), genericHTMLRequest(page))
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Lookup("formats").ListValue()
	format, _ := formats[0].Object()
	if rawURL, _ := format.Lookup("url").StringValue(); rawURL != "https://publisher.invalid/twitter-stream" {
		t.Fatalf("URL = %q", rawURL)
	}
	if formatID, _ := format.Lookup("format_id").StringValue(); formatID != "twitter" {
		t.Fatalf("format ID = %q", formatID)
	}
}

func TestGenericMalformedJSONLDFallsBackAndAudioObjectIsAudioOnly(t *testing.T) {
	t.Run("fallback", func(t *testing.T) {
		page := []byte(`<html><head>
			<script type="application/ld+json">{"@type":"VideoObject",broken}</script>
			<meta property="og:video" content="/fallback.mp4">
		</head></html>`)
		result, err := NewGeneric().Extract(context.Background(), genericHTMLRequest(page))
		if err != nil {
			t.Fatal(err)
		}
		formats, _ := result.Info.Lookup("formats").ListValue()
		format, _ := formats[0].Object()
		if formatID, _ := format.Lookup("format_id").StringValue(); formatID != "open-graph" {
			t.Fatalf("format ID = %q", formatID)
		}
	})
	t.Run("audio", func(t *testing.T) {
		page := []byte(`<script type="application/ld+json">{
			"@context":"https://schema.org","@type":"AudioObject",
			"name":"Synthetic audio","encodingFormat":"audio/mpeg","contentUrl":"/audio"
		}</script>`)
		result, err := NewGeneric().Extract(context.Background(), genericHTMLRequest(page))
		if err != nil {
			t.Fatal(err)
		}
		formats, _ := result.Info.Lookup("formats").ListValue()
		format, _ := formats[0].Object()
		if extension, _ := format.Lookup("ext").StringValue(); extension != "mp3" {
			t.Fatalf("extension = %q", extension)
		}
		if vcodec, _ := format.Lookup("vcodec").StringValue(); vcodec != "none" {
			t.Fatalf("vcodec = %q", vcodec)
		}
	})
}

func TestGenericMetadataMediaBoundsAndCancellation(t *testing.T) {
	base := mustGenericURL(t, "https://publisher.invalid/article")
	t.Run("scripts", func(t *testing.T) {
		page := []byte(strings.Repeat(`<script type="application/ld+json">{}</script>`, maxGenericJSONLDScripts+1))
		if _, _, err := discoverGenericMetadataMedia(context.Background(), base, page); !errors.Is(err, ErrPlaylistLimit) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("script bytes", func(t *testing.T) {
		page := []byte(`<script type="application/ld+json">"` + strings.Repeat("x", maxGenericJSONLDBytes+1) + `"</script>`)
		if _, _, err := discoverGenericMetadataMedia(context.Background(), base, page); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("candidates", func(t *testing.T) {
		page := []byte(strings.Repeat(`<meta property="og:video" content="/video.mp4">`, maxGenericMetadataCandidates+1))
		if _, _, err := discoverGenericMetadataMedia(context.Background(), base, page); !errors.Is(err, ErrPlaylistLimit) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("JSON-LD candidates", func(t *testing.T) {
		object := `{"@context":"https://schema.org","@type":"VideoObject","contentUrl":"/video.mp4"}`
		page := []byte(`<script type="application/ld+json">[` +
			strings.TrimSuffix(strings.Repeat(object+",", maxGenericMetadataCandidates+1), ",") +
			`]</script>`)
		if _, _, err := discoverGenericMetadataMedia(context.Background(), base, page); !errors.Is(err, ErrPlaylistLimit) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("JSON-LD nodes", func(t *testing.T) {
		page := []byte(`<script type="application/ld+json">[` +
			strings.TrimSuffix(strings.Repeat(`{},`, maxGenericJSONLDNodes+1), ",") +
			`]</script>`)
		if _, _, err := discoverGenericMetadataMedia(context.Background(), base, page); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, _, err := discoverGenericMetadataMedia(ctx, base, []byte(`<meta property="og:video" content="/video.mp4">`)); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	})
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

func TestGenericReturnsRedirectBeforeScanningFinalDocument(t *testing.T) {
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
	if !result.IsURL() || result.Redirect == nil || result.Redirect.URL != transport.finalURL {
		t.Fatalf("redirect result = %#v", result)
	}
	if len(transport.methods) != 1 || transport.methods[0] != http.MethodHead {
		t.Fatalf("methods = %v", transport.methods)
	}
}

func TestGenericFallsBackToGETWhenHEADIsUnusable(t *testing.T) {
	for _, test := range []struct {
		name       string
		headStatus int
		headType   string
	}{
		{name: "method not allowed", headStatus: http.StatusMethodNotAllowed},
		{name: "not implemented", headStatus: http.StatusNotImplemented},
		{name: "empty type"},
		{name: "misleading type", headType: "application/json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &genericHTMLTransport{
				headStatus: test.headStatus, headType: test.headType, getType: "text/html",
				body: readGenericEmbedFixture(t, "single.html"),
			}
			result, err := NewGeneric().Extract(context.Background(), Request{
				URL: "https://publisher.invalid/article", Transport: transport,
			})
			if err != nil || !result.IsURL() {
				t.Fatalf("Extract() = %#v, %v", result, err)
			}
			if strings.Join(transport.methods, ",") != "HEAD,GET" {
				t.Fatalf("methods = %v", transport.methods)
			}
		})
	}
}

func TestGenericSniffsLargeDirectGETWithoutBufferingWholeBody(t *testing.T) {
	transport := &genericHTMLTransport{
		body: bytes.Repeat([]byte{0}, maxGenericHTMLBytes+1),
	}
	result, err := NewGeneric().Extract(context.Background(), Request{
		URL: "https://publisher.invalid/media.bin", Transport: transport,
	})
	if err != nil || result.IsPlaylist() || result.IsURL() {
		t.Fatalf("Extract() = %#v, %v", result, err)
	}
	if strings.Join(transport.methods, ",") != "HEAD,GET" {
		t.Fatalf("methods = %v", transport.methods)
	}
}

func TestCanonicalGenericEmbedRejectsUnsafeAndNonEmbedURLs(t *testing.T) {
	base, _ := url.Parse("https://publisher.invalid/article")
	for _, raw := range []string{
		"javascript:alert(1)",
		"data:text/html,<iframe>",
		"https://user@player.vimeo.com/video/123",
		"https://player.vimeo.com:443/video/123",
		"https://player.vimeo.com:/video/123",
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

func TestGenericPreservesResponseBodyReadFailure(t *testing.T) {
	want := errors.New("transport body failed")
	transport := &genericHTMLTransport{
		headType: "text/html", getType: "text/html", getReadError: want,
	}
	_, err := NewGeneric().Extract(context.Background(), Request{
		URL: "https://publisher.invalid/article", Transport: transport,
	})
	if !errors.Is(err, want) || errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("Extract() error = %v", err)
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
	t.Run("mismatched end tag", func(t *testing.T) {
		page := []byte(`<div></span>`)
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

func FuzzDiscoverGenericMetadataMedia(f *testing.F) {
	f.Add([]byte(`<meta property="og:video" content="/video.mp4">`))
	f.Add([]byte(`<script type="application/ld+json">{"@context":"https://schema.org","@type":"VideoObject","contentUrl":"/video.mp4"}</script>`))
	base, _ := url.Parse("https://publisher.invalid/article")
	f.Fuzz(func(t *testing.T, page []byte) {
		if len(page) > maxGenericHTMLBytes+1 {
			return
		}
		result, found, err := discoverGenericMetadataMedia(context.Background(), base, page)
		if err != nil || !found {
			return
		}
		if result.IsPlaylist() || result.IsURL() {
			t.Fatalf("metadata media returned non-media result")
		}
		formats, ok := result.Info.Lookup("formats").ListValue()
		if !ok || len(formats) == 0 || len(formats) > maxGenericMetadataCandidates {
			t.Fatalf("invalid formats: %#v", formats)
		}
		for _, encoded := range formats {
			format, ok := encoded.Object()
			if !ok {
				t.Fatal("non-object format")
			}
			rawURL, ok := format.Lookup("url").StringValue()
			parsed, parseErr := url.Parse(rawURL)
			if !ok || parseErr != nil || parsed.User != nil || parsed.Scheme != "https" && parsed.Scheme != "http" {
				t.Fatalf("unsafe format URL %q", rawURL)
			}
		}
	})
}
