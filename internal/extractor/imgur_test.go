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
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

type imgurFixtureResponse struct {
	status int
	body   []byte
	err    error
}

type imgurFixtureTransport struct {
	mu        sync.Mutex
	responses map[string]imgurFixtureResponse
	requests  []*http.Request
}

func (transport *imgurFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected ReadPage")
}

func (transport *imgurFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Clone(context.Background()))
	response, ok := transport.responses[request.URL.String()]
	transport.mu.Unlock()
	if !ok {
		return nil, errors.New("unexpected Imgur request")
	}
	if response.err != nil {
		return nil, response.err
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(response.body)),
		Request:    request,
	}, nil
}

func imgurFixture(t testing.TB, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "imgur", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func imgurAPIURL(endpoint, id string) string {
	return imgurAPIBase + endpoint + "/" + id + "?" + imgurIncludeQuery
}

func TestImgurRouting(t *testing.T) {
	tests := []struct {
		raw  string
		kind imgurRouteKind
		id   string
		ok   bool
	}{
		{"https://imgur.com/A61SaA1", imgurMediaRoute, "A61SaA1", true},
		{"https://imgur.com/title-slug-A61SaA1", imgurMediaRoute, "A61SaA1", true},
		{"https://i.imgur.com/A61SaA1.gifv", imgurMediaRoute, "A61SaA1", true},
		{"https://imgur.com/gallery/title-Album12", imgurGalleryRoute, "Album12", true},
		{"https://imgur.com/a/Album12", imgurAlbumRoute, "Album12", true},
		{"http://imgur.com/t/unmuted/Album12", imgurGalleryRoute, "Album12", true},
		{"https://imgur.com/r/aww/Album12", imgurGalleryRoute, "Album12", true},
		{"https://evil.test/A61SaA1", 0, "", false},
		{"ftp://imgur.com/A61SaA1", 0, "", false},
		{"https://user:pass@imgur.com/A61SaA1", 0, "", false},
		{"https://imgur.com:443/A61SaA1", 0, "", false},
		{"https://imgur.com/A61SaA1?download=1", 0, "", false},
		{"https://imgur.com/A61SaA1#x", 0, "", false},
		{"https://imgur.com/a/b/c", 0, "", false},
		{"https://imgur.com/gallery", 0, "", false},
		{"https://imgur.com/a%2fA61SaA1", 0, "", false},
	}
	for _, test := range tests {
		parsed, err := url.Parse(test.raw)
		if err != nil {
			if test.ok {
				t.Fatalf("parse %q: %v", test.raw, err)
			}
			continue
		}
		target, ok := classifyImgurURL(parsed)
		if ok != test.ok || (ok && (target.id != test.id || target.kind != test.kind)) {
			t.Fatalf("classify(%q) = %#v, %v", test.raw, target, ok)
		}
		if NewImgur().Suitable(parsed) != test.ok {
			t.Fatalf("Suitable(%q) mismatch", test.raw)
		}
	}
}

func TestImgurExtractVideoMetadataAndFormat(t *testing.T) {
	transport := &imgurFixtureTransport{responses: map[string]imgurFixtureResponse{
		imgurAPIURL("media", "A61SaA1"): {body: imgurFixture(t, "media_video.json")},
	}}
	result, err := NewImgur().Extract(context.Background(), Request{
		URL: "https://imgur.com/mrw-gifv-A61SaA1", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsPlaylist() {
		t.Fatal("video returned playlist")
	}
	if id, _ := result.Info.ID(); id != "A61SaA1" {
		t.Fatalf("id = %q", id)
	}
	if title, _ := result.Info.Title(); title != "MRW gifv works" {
		t.Fatalf("title = %q", title)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 1 {
		t.Fatalf("formats = %d", len(formats))
	}
	format, _ := formats[0].Object()
	if raw, _ := format.Lookup("url").StringValue(); raw != "https://i.imgur.com/A61SaA1.mp4" {
		t.Fatalf("format url = %q", raw)
	}
	for key, want := range map[string]int64{"width": 640, "height": 360, "filesize": 123456} {
		if got, _ := format.Lookup(key).Int(); got != want {
			t.Fatalf("%s = %d", key, got)
		}
	}
	if timestamp, _ := result.Info.Lookup("timestamp").Int(); timestamp != 1416446068 {
		t.Fatalf("timestamp = %d", timestamp)
	}
	if release, _ := result.Info.Lookup("release_timestamp").Int(); release != 1416446068 {
		t.Fatalf("release timestamp = %d", release)
	}
	if age, _ := result.Info.Lookup("age_limit").Int(); age != 18 {
		t.Fatalf("age limit = %d", age)
	}
	if uploader, _ := result.Info.Lookup("uploader").StringValue(); uploader != "fixture-user" {
		t.Fatalf("uploader = %q", uploader)
	}
	if len(transport.requests) != 1 || transport.requests[0].Header.Get("Accept") != "application/json" {
		t.Fatal("missing bounded API request headers")
	}
}

func TestImgurAnimatedImageIsLowPreferenceSilentFormat(t *testing.T) {
	transport := &imgurFixtureTransport{responses: map[string]imgurFixtureResponse{
		imgurAPIURL("media", "Anim123"): {body: imgurFixture(t, "media_animated.json")},
	}}
	result, err := NewImgur().Extract(context.Background(), Request{URL: "https://i.imgur.com/Anim123.gifv", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	format, _ := formats[0].Object()
	if ext, _ := format.Lookup("ext").StringValue(); ext != "gif" {
		t.Fatalf("ext = %q", ext)
	}
	if codec, _ := format.Lookup("acodec").StringValue(); codec != "none" {
		t.Fatalf("acodec = %q", codec)
	}
	if preference, _ := format.Lookup("preference").Int(); preference != -10 {
		t.Fatalf("preference = %d", preference)
	}
	if raw, _ := format.Lookup("url").StringValue(); !strings.HasPrefix(raw, "https://") {
		t.Fatalf("URL was not upgraded to HTTPS: %q", raw)
	}
}

func TestImgurAlbumFiltersStaticItemsAndStaysLazy(t *testing.T) {
	transport := &imgurFixtureTransport{responses: map[string]imgurFixtureResponse{
		imgurAPIURL("albums", "Album12"): {body: imgurFixture(t, "album_mixed.json")},
	}}
	result, err := NewImgur().Extract(context.Background(), Request{URL: "https://imgur.com/a/Album12", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() {
		t.Fatal("album did not return playlist")
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "Vid0001" || entries[1].ID != "Gif0002" {
		t.Fatalf("entries = %#v", entries)
	}
	for _, entry := range entries {
		if entry.ExtractorKey != "imgur" || !entry.Transparent {
			t.Fatalf("entry is not transparent Imgur route: %#v", entry)
		}
	}
	if len(transport.requests) != 1 {
		t.Fatalf("album eagerly fetched entries: %d requests", len(transport.requests))
	}
}

func TestImgurSingleVideoGalleryAppliesGalleryMetadata(t *testing.T) {
	transport := &imgurFixtureTransport{responses: map[string]imgurFixtureResponse{
		imgurAPIURL("albums", "Gallery1"): {body: imgurFixture(t, "gallery_single.json")},
		imgurAPIURL("media", "OnlyVid"):   {body: imgurFixture(t, "media_gallery_single.json")},
	}}
	result, err := NewImgur().Extract(context.Background(), Request{URL: "https://imgur.com/gallery/Gallery1", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsPlaylist() {
		t.Fatal("single gallery video did not collapse")
	}
	if title, _ := result.Info.Title(); title != "Gallery title" {
		t.Fatalf("title = %q", title)
	}
	if description, _ := result.Info.Lookup("description").StringValue(); description != "Gallery description" {
		t.Fatalf("description = %q", description)
	}
}

func TestImgurCollection404FallsBackToMedia(t *testing.T) {
	transport := &imgurFixtureTransport{responses: map[string]imgurFixtureResponse{
		imgurAPIURL("albums", "A61SaA1"): {status: http.StatusNotFound},
		imgurAPIURL("media", "A61SaA1"):  {body: imgurFixture(t, "media_video.json")},
	}}
	result, err := NewImgur().Extract(context.Background(), Request{
		URL: "https://imgur.com/gallery/A61SaA1", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsPlaylist() {
		t.Fatal("404 collection fallback returned playlist")
	}
	if id, _ := result.Info.ID(); id != "A61SaA1" {
		t.Fatalf("id = %q", id)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests = %d, want collection then media", len(transport.requests))
	}
}

func TestImgurFailuresAreCategorizedAndSecretSafe(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   []byte
		want   error
	}{
		{"authentication", http.StatusForbidden, nil, ErrAuthentication},
		{"missing", http.StatusNotFound, nil, ErrUnavailable},
		{"rate limit", http.StatusTooManyRequests, nil, ErrImgurNetwork},
		{"server", http.StatusInternalServerError, nil, ErrImgurNetwork},
		{"malformed", http.StatusOK, []byte(`{`), ErrInvalidMetadata},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &imgurFixtureTransport{responses: map[string]imgurFixtureResponse{
				imgurAPIURL("media", "A61SaA1"): {status: test.status, body: test.body},
			}}
			_, err := NewImgur().Extract(context.Background(), Request{URL: "https://imgur.com/A61SaA1", Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), imgurClientID) {
				t.Fatal("public API identifier leaked through error")
			}
		})
	}
}

func TestImgurRejectsStaticUnsafeAndOversizedResponses(t *testing.T) {
	static := []byte(`{"media":[{"id":"Static1","type":"image","url":"https://i.imgur.com/Static1.jpg","metadata":{"is_animated":false}}]}`)
	unsafe := []byte(`{"media":[{"id":"Unsafe1","type":"video","url":"https://127.0.0.1/video.mp4","metadata":{}}]}`)
	for _, test := range []struct {
		id   string
		body []byte
		want error
	}{
		{"Static1", static, ErrUnavailable},
		{"Unsafe1", unsafe, ErrInvalidMetadata},
		{"Huge001", bytes.Repeat([]byte(" "), int(maxExtractorJSONBytes)+1), ErrJSONResponseTooLarge},
	} {
		transport := &imgurFixtureTransport{responses: map[string]imgurFixtureResponse{
			imgurAPIURL("media", test.id): {body: test.body},
		}}
		_, err := NewImgur().Extract(context.Background(), Request{URL: "https://imgur.com/" + test.id, Transport: transport})
		if !errors.Is(err, test.want) {
			t.Fatalf("%s error = %v, want %v", test.id, err, test.want)
		}
	}
}

func TestImgurHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewImgur().Extract(ctx, Request{
		URL:       "https://imgur.com/A61SaA1",
		Transport: &imgurFixtureTransport{responses: map[string]imgurFixtureResponse{}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func FuzzImgurRouting(f *testing.F) {
	for _, seed := range []string{
		"https://imgur.com/A61SaA1",
		"https://i.imgur.com/A61SaA1.gifv",
		"https://imgur.com/gallery/title-A61SaA1",
		"https://imgur.com/a/A61SaA1",
		"https://evil.test/A61SaA1",
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 1<<16 {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		target, ok := classifyImgurURL(parsed)
		if ok && (!imgurIDPattern.MatchString(target.id) || target.kind > imgurAlbumRoute) {
			t.Fatalf("invalid accepted target: %#v", target)
		}
	})
}

func TestNormalizeImgurAssetURL(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"https://i.imgur.com/A61SaA1.mp4", true},
		{"http://i.imgur.com/A61SaA1.mp4", true},
		{"https://user:pass@i.imgur.com/A61SaA1.mp4", false},
		{"https://i.imgur.com:443/A61SaA1.mp4", false},
		{"https://i.imgur.com/A%2fB.mp4", false},
		{"https://127.0.0.1/A61SaA1.mp4", false},
	}
	for _, test := range tests {
		_, ok := normalizeImgurAssetURL(test.raw)
		if ok != test.want {
			t.Fatalf("normalize(%q) = %v", test.raw, ok)
		}
	}
}

func TestImgurInfoValueModelRemainsBounded(t *testing.T) {
	info := value.NewObject()
	imgurSetCountsAndAccount(info, imgurAPIResponse{Upvotes: float64(-1)})
	if info.Len() != 0 {
		t.Fatal("negative count was retained")
	}
}
