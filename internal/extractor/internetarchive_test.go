package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

type internetArchiveFixtureTransport struct {
	status int
	body   []byte
	err    error
	urls   []string
}

func (transport *internetArchiveFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.urls = append(transport.urls, request.URL.String())
	if transport.err != nil {
		return nil, transport.err
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytesReaderForInternetArchive(transport.body))}, nil
}

func (*internetArchiveFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("Internet Archive extractor must not request an HTML page")
}

func bytesReaderForInternetArchive(body []byte) *strings.Reader {
	return strings.NewReader(string(body))
}

func readInternetArchiveFixture(t testing.TB) []byte {
	t.Helper()
	body, err := os.ReadFile("../../conformance/extractors/public/internetarchive/success.json")
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestInternetArchiveItemPlaylistIsDeterministicAndReusable(t *testing.T) {
	transport := &internetArchiveFixtureTransport{body: readInternetArchiveFixture(t)}
	result, err := NewInternetArchive().Extract(context.Background(), Request{
		URL:       "https://archive.org/details/fixture_concert?view=theater",
		Transport: transport,
	})
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("playlist=%t error=%v", result.IsPlaylist(), err)
	}
	if len(transport.urls) != 1 || transport.urls[0] != "https://archive.org/metadata/fixture_concert" {
		t.Fatalf("requests=%v", transport.urls)
	}
	if id, _ := result.Info.ID(); id != "fixture_concert" {
		t.Fatalf("id=%q", id)
	}
	if title, _ := result.Info.Title(); title != "Fixture Concert" {
		t.Fatalf("title=%q", title)
	}
	for pass := 0; pass < 2; pass++ {
		entries, err := CollectEntries(context.Background(), result.Entries, 3)
		if err != nil || len(entries) != 2 {
			t.Fatalf("pass=%d entries=%v error=%v", pass, entries, err)
		}
		if entries[0].ID != "fixture_concert/track01.flac" || entries[1].ID != "fixture_concert/track02.mp4" ||
			entries[0].URL != "https://archive.org/details/fixture_concert/track01.flac" || !entries[0].Transparent {
			t.Fatalf("pass=%d entries=%+v", pass, entries)
		}
	}
}

func TestInternetArchiveRequestedMediaMetadataFormatsSubtitlesAndThumbnails(t *testing.T) {
	transport := &internetArchiveFixtureTransport{body: readInternetArchiveFixture(t)}
	result, err := NewInternetArchive().Extract(context.Background(), Request{
		URL:       "https://www.archive.org/details/fixture_concert/track01.flac",
		Transport: transport,
	})
	if err != nil || result.IsPlaylist() {
		t.Fatalf("playlist=%t error=%v", result.IsPlaylist(), err)
	}
	if id, _ := result.Info.ID(); id != "fixture_concert/track01.flac" {
		t.Fatalf("id=%q", id)
	}
	if title, _ := result.Info.Title(); title != "First movement" {
		t.Fatalf("title=%q", title)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 2 {
		t.Fatalf("formats=%v", formats)
	}
	first, _ := formats[0].Object()
	second, _ := formats[1].Object()
	if got, _ := first.Lookup("format_id").StringValue(); got != "track01.flac" {
		t.Fatalf("first format=%s", got)
	}
	if got, _ := second.Lookup("url").StringValue(); got != "https://archive.org/download/fixture_concert/track01.mp3" {
		t.Fatalf("second URL=%s", got)
	}
	encoded, err := json.Marshal(result.Info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, expected := range []string{
		`"duration":61.25`, `"track_number":1`, `"album":"Fixture Concert"`,
		`"release_date":"20240203"`, `"uploader":"fixture@example.invalid"`,
		`"subtitles":{"en":[{"url":"https://archive.org/download/fixture_concert/track01.en.vtt","ext":"vtt"}]}`,
		`"thumbnails":[`, `track01.jpg`, `fixture_concert_itemimage.png`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("metadata missing %q: %s", expected, text)
		}
	}
}

func TestInternetArchiveDerivativeRouteSelectsOriginalGroup(t *testing.T) {
	transport := &internetArchiveFixtureTransport{body: readInternetArchiveFixture(t)}
	result, err := NewInternetArchive().Extract(context.Background(), Request{
		URL:       "https://archive.org/details/fixture_concert/track01.mp3",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := result.Info.ID(); id != "fixture_concert/track01.flac" {
		t.Fatalf("id=%q", id)
	}
}

func TestInternetArchiveSingleItemKeepsItemIdentity(t *testing.T) {
	body := []byte(`{"metadata":{"identifier":"single_item","title":"Item title"},"files":[{"name":"video.mp4","format":"MPEG4","source":"original","title":"File title"}]}`)
	result, err := NewInternetArchive().Extract(context.Background(), Request{
		URL:       "https://archive.org/details/single_item",
		Transport: &internetArchiveFixtureTransport{body: body},
	})
	if err != nil || result.IsPlaylist() {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if id, _ := result.Info.ID(); id != "single_item" {
		t.Fatalf("id=%q", id)
	}
	if title, _ := result.Info.Title(); title != "Item title" {
		t.Fatalf("title=%q", title)
	}
	if webpage, _ := result.Info.WebpageURL(); webpage != "https://archive.org/details/single_item" {
		t.Fatalf("webpage=%q", webpage)
	}
}

func TestInternetArchiveRoutesAndUntrustedURLs(t *testing.T) {
	accepted := []string{
		"http://archive.org/details/fixture_concert",
		"https://www.archive.org/embed/fixture_concert",
		"https://archive.org/details/fixture_concert/disc1/Track%2001.mp3",
	}
	for _, raw := range accepted {
		parsed, _ := url.Parse(raw)
		if !NewInternetArchive().Suitable(parsed) {
			t.Fatalf("not suitable: %s", raw)
		}
	}
	rejected := []string{
		"https://evil.example/details/fixture_concert",
		"https://archive.org/download/fixture_concert/file.mp4",
		"https://archive.org:444/details/fixture_concert",
		"https://user@archive.org/details/fixture_concert",
		"https://archive.org/details/fixture_concert/%2e%2e/secret.mp4",
		"https://archive.org/details/fixture_concert/disc%2ftrack.mp3",
	}
	for _, raw := range rejected {
		parsed, _ := url.Parse(raw)
		if NewInternetArchive().Suitable(parsed) {
			t.Fatalf("unexpectedly suitable: %s", raw)
		}
	}
}

func TestInternetArchiveCategorizedFailuresAndCancellation(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrAuthentication},
		{http.StatusForbidden, ErrAuthentication},
		{http.StatusNotFound, ErrUnavailable},
		{http.StatusGone, ErrUnavailable},
		{http.StatusTooManyRequests, ErrInternetArchiveNetwork},
		{http.StatusServiceUnavailable, ErrInternetArchiveNetwork},
	} {
		transport := &internetArchiveFixtureTransport{status: test.status}
		_, err := NewInternetArchive().Extract(context.Background(), Request{URL: "https://archive.org/details/fixture_concert", Transport: transport})
		if !errors.Is(err, test.want) {
			t.Fatalf("status=%d error=%v want=%v", test.status, err, test.want)
		}
	}
	transport := &internetArchiveFixtureTransport{err: errors.New("fixture dial failure")}
	if _, err := NewInternetArchive().Extract(context.Background(), Request{URL: "https://archive.org/details/fixture_concert", Transport: transport}); !errors.Is(err, ErrInternetArchiveNetwork) {
		t.Fatalf("network error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewInternetArchive().Extract(ctx, Request{URL: "https://archive.org/details/fixture_concert", Transport: transport}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestInternetArchiveRejectsMalformedOversizedPrivateAndUntrustedAssets(t *testing.T) {
	tests := []struct {
		name string
		url  string
		body []byte
		want error
	}{
		{"malformed", "https://archive.org/details/fixture_concert", []byte(`{"metadata":`), ErrInvalidMetadata},
		{"mismatched identifier", "https://archive.org/details/fixture_concert", []byte(`{"metadata":{"identifier":"other"},"files":[]}`), ErrInvalidMetadata},
		{"empty", "https://archive.org/details/fixture_concert", []byte(`{"metadata":{"identifier":"fixture_concert"},"files":[]}`), ErrUnavailable},
		{"private", "https://archive.org/details/fixture_concert/private-master.wav", readInternetArchiveFixture(t), ErrAuthentication},
		{"unsafe name", "https://archive.org/details/fixture_concert", []byte(`{"metadata":{"identifier":"fixture_concert"},"files":[{"name":"../escape.mp4","format":"MPEG4"}]}`), ErrInvalidMetadata},
		{"unsafe original", "https://archive.org/details/fixture_concert", []byte(`{"metadata":{"identifier":"fixture_concert"},"files":[{"name":"safe.mp3","original":"../escape.wav","format":"MP3"}]}`), ErrInvalidMetadata},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &internetArchiveFixtureTransport{body: test.body}
			_, err := NewInternetArchive().Extract(context.Background(), Request{URL: test.url, Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
	oversized := &internetArchiveFixtureTransport{body: []byte(strings.Repeat(" ", int(maxExtractorJSONBytes)+1))}
	if _, err := NewInternetArchive().Extract(context.Background(), Request{URL: "https://archive.org/details/fixture_concert", Transport: oversized}); !errors.Is(err, ErrJSONResponseTooLarge) {
		t.Fatalf("oversize error=%v", err)
	}
}

func TestInternetArchiveHelpers(t *testing.T) {
	if got := internetArchiveDuration("01:02:03.5"); got != 3723.5 {
		t.Fatalf("duration=%v", got)
	}
	if got := internetArchiveDuration("99:99"); got != 0 {
		t.Fatalf("invalid duration=%v", got)
	}
	if got := internetArchiveDownloadURL("fixture_concert", `disc1/Track one & two.mp3`); got != "https://archive.org/download/fixture_concert/disc1/Track%20one%20&%20two.mp3" {
		t.Fatalf("download URL=%q", got)
	}
	if got := internetArchiveDetailsURL("fixture_concert", `disc1/A+B.mp3`); got != "https://archive.org/details/fixture_concert/disc1/A%2BB.mp3" {
		t.Fatalf("details URL=%q", got)
	}
	if !errors.Is(categorizeInternetArchiveError(fmt.Errorf("wrapped: %w", &HTTPStatusError{Code: 503})), ErrInternetArchiveNetwork) {
		t.Fatal("wrapped status was not categorized")
	}
}

func TestInternetArchiveInventoryBoundsAndDarkItem(t *testing.T) {
	tooMany := internetArchiveMetadata{Metadata: map[string]any{"identifier": "fixture_concert"}, Files: make([]internetArchiveFile, internetArchiveMaxFiles+1)}
	if _, err := normalizeInternetArchive(tooMany, "fixture_concert", ""); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("inventory bound error=%v", err)
	}
	dark := internetArchiveMetadata{Metadata: map[string]any{"identifier": "fixture_concert"}, Files: []internetArchiveFile{}, IsDark: true}
	if _, err := normalizeInternetArchive(dark, "fixture_concert", ""); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("dark item error=%v", err)
	}
}

func FuzzNormalizeInternetArchive(f *testing.F) {
	f.Add(readInternetArchiveFixture(f), "")
	f.Add([]byte(`{"metadata":{},"files":[]}`), "../unsafe")
	f.Fuzz(func(t *testing.T, payload []byte, requested string) {
		if len(payload) > 1<<20 || len(requested) > 2048 {
			t.Skip()
		}
		var metadata internetArchiveMetadata
		decoder := json.NewDecoder(strings.NewReader(string(payload)))
		decoder.UseNumber()
		if decoder.Decode(&metadata) != nil {
			return
		}
		_, _ = normalizeInternetArchive(metadata, "fixture_concert", requested)
	})
}

func FuzzClassifyInternetArchiveURL(f *testing.F) {
	f.Add("https://archive.org/details/fixture_concert/track01.flac")
	f.Add("https://archive.org/details/x/%2e%2e/escape.mp4")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 16<<10 {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err == nil {
			_, _, _ = classifyInternetArchiveURL(parsed)
		}
	})
}
