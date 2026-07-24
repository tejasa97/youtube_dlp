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
)

type flickrFixtureResponse struct {
	status int
	body   []byte
	err    error
}

type flickrFixtureTransport struct {
	mu        sync.Mutex
	responses map[string]flickrFixtureResponse
	requests  []*http.Request
}

type flickrNonIsolatedTransport struct{ inner *flickrFixtureTransport }

func (transport flickrNonIsolatedTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	return transport.inner.ReadPage(ctx, rawURL)
}

func (transport flickrNonIsolatedTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.inner.Do(ctx, request)
}

func (transport *flickrFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected ReadPage")
}

func (transport *flickrFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return transport.respond(ctx, request)
}

func (transport *flickrFixtureTransport) DoWithoutCookies(ctx context.Context, request *http.Request) (*http.Response, error) {
	if request.Header.Get("Cookie") != "" {
		return nil, errors.New("cookie reached isolated Flickr request")
	}
	return transport.respond(ctx, request)
}

func (transport *flickrFixtureTransport) respond(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Clone(context.Background()))
	response, ok := transport.responses[request.URL.String()]
	transport.mu.Unlock()
	if !ok {
		return nil, errors.New("unexpected Flickr request")
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

func flickrFixture(t testing.TB, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "flickr", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func flickrTestAPIURL(method, id, key, secret string) string {
	query := make(url.Values)
	query.Set("api_key", key)
	query.Set("format", "json")
	query.Set("method", "flickr."+method)
	query.Set("nojsoncallback", "1")
	query.Set("photo_id", id)
	if secret != "" {
		query.Set("secret", secret)
	}
	return flickrAPIURL + "?" + query.Encode()
}

func TestFlickrRouting(t *testing.T) {
	tests := []struct {
		raw string
		id  string
		ok  bool
	}{
		{"http://www.flickr.com/photos/forestwander-nature-pictures/5645318632/in/photostream/", "5645318632", true},
		{"https://secure.flickr.com/photos/10922353@N03/5645318632", "5645318632", true},
		{"https://flickr.com/photos/user_name/5645318632?context=1#photo", "5645318632", true},
		{"https://evil.test/photos/user/5645318632", "", false},
		{"ftp://flickr.com/photos/user/5645318632", "", false},
		{"https://user:pass@flickr.com/photos/user/5645318632", "", false},
		{"https://flickr.com:443/photos/user/5645318632", "", false},
		{"https://flickr.com/photos/user/not-a-number", "", false},
		{"https://flickr.com/photos/user%2Fevil/5645318632", "", false},
		{"https://flickr.com/groups/user/5645318632", "", false},
	}
	for _, test := range tests {
		parsed, err := url.Parse(test.raw)
		if err != nil {
			if test.ok {
				t.Fatal(err)
			}
			continue
		}
		target, ok := classifyFlickrURL(parsed)
		if ok != test.ok || (ok && target.id != test.id) {
			t.Fatalf("classify(%q) = %#v, %v", test.raw, target, ok)
		}
		if NewFlickr().Suitable(parsed) != test.ok {
			t.Fatalf("Suitable(%q) mismatch", test.raw)
		}
	}
}

func TestFlickrExtractsVideoMetadataAndStreams(t *testing.T) {
	key := "fixtureapikey123"
	id := "5645318632"
	transport := &flickrFixtureTransport{responses: map[string]flickrFixtureResponse{
		flickrBeaconURL: {body: flickrFixture(t, "beacon.json")},
		flickrTestAPIURL("photos.getInfo", id, key, ""):                {body: flickrFixture(t, "video_info.json")},
		flickrTestAPIURL("video.getStreamInfo", id, key, "09ff5eef3b"): {body: flickrFixture(t, "streams.json")},
	}}
	result, err := NewFlickr().Extract(context.Background(), Request{
		URL:       "https://www.flickr.com/photos/forestwander-nature-pictures/5645318632/in/photostream/?context=fixture#photo",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsPlaylist() {
		t.Fatal("Flickr video returned playlist")
	}
	if title, _ := result.Info.Title(); title != "Dark Hollow Waterfalls" {
		t.Fatalf("title = %q", title)
	}
	if webpage, _ := result.Info.Lookup("webpage_url").StringValue(); webpage != "https://www.flickr.com/photos/forestwander-nature-pictures/5645318632" {
		t.Fatalf("webpage_url = %q", webpage)
	}
	if timestamp, _ := result.Info.Lookup("timestamp").Int(); timestamp != 1303528740 {
		t.Fatalf("timestamp = %d", timestamp)
	}
	if date, _ := result.Info.Lookup("upload_date").StringValue(); date != "20110423" {
		t.Fatalf("upload_date = %q", date)
	}
	for key, want := range map[string]int64{
		"duration": 19, "width": 640, "height": 363,
		"comment_count": 7, "view_count": 420,
	} {
		if got, _ := result.Info.Lookup(key).Int(); got != want {
			t.Fatalf("%s = %d", key, got)
		}
	}
	if license, _ := result.Info.Lookup("license").StringValue(); license != "Attribution-ShareAlike" {
		t.Fatalf("license = %q", license)
	}
	tags, _ := result.Info.Lookup("tags").ListValue()
	if len(tags) != 2 {
		t.Fatalf("tags = %d", len(tags))
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 3 {
		t.Fatalf("formats = %d, want 3 safe unique streams", len(formats))
	}
	foundHLS := false
	for _, raw := range formats {
		format, _ := raw.Object()
		if protocol, _ := format.Lookup("protocol").StringValue(); protocol == "m3u8_native" {
			foundHLS = true
		}
	}
	if !foundHLS {
		t.Fatal("missing HLS stream")
	}
	if len(transport.requests) != 3 {
		t.Fatalf("requests = %d", len(transport.requests))
	}
	for _, request := range transport.requests {
		if request.Header.Get("Cookie") != "" || request.Header.Get("Accept") != "application/json" {
			t.Fatalf("unsafe request headers: %v", request.Header)
		}
	}
}

func TestFlickrPhotoIsUnavailable(t *testing.T) {
	key := "fixtureapikey123"
	transport := &flickrFixtureTransport{responses: map[string]flickrFixtureResponse{
		flickrBeaconURL: {body: flickrFixture(t, "beacon.json")},
		flickrTestAPIURL("photos.getInfo", "5645318632", key, ""): {body: flickrFixture(t, "photo_info.json")},
	}}
	_, err := NewFlickr().Extract(context.Background(), Request{
		URL: "https://flickr.com/photos/user/5645318632", Transport: transport,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests = %d", len(transport.requests))
	}
}

func TestFlickrAPIFailureIsCategorizedAndSecretSafe(t *testing.T) {
	key := "fixtureapikey123"
	transport := &flickrFixtureTransport{responses: map[string]flickrFixtureResponse{
		flickrBeaconURL: {body: flickrFixture(t, "beacon.json")},
		flickrTestAPIURL("photos.getInfo", "5645318632", key, ""): {body: []byte(`{"stat":"fail","code":1,"message":"secret fixture detail"}`)},
	}}
	_, err := NewFlickr().Extract(context.Background(), Request{
		URL: "https://flickr.com/photos/user/5645318632", Transport: transport,
	})
	if !errors.Is(err, ErrFlickrAPIResponse) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "secret fixture detail") || strings.Contains(err.Error(), key) {
		t.Fatalf("error leaked response or key: %v", err)
	}
}

func TestFlickrHTTPAndJSONFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   []byte
		want   error
	}{
		{"auth", http.StatusForbidden, nil, ErrAuthentication},
		{"missing", http.StatusNotFound, nil, ErrUnavailable},
		{"rate", http.StatusTooManyRequests, nil, ErrFlickrNetwork},
		{"server", http.StatusInternalServerError, nil, ErrFlickrNetwork},
		{"malformed", http.StatusOK, []byte(`{`), ErrInvalidMetadata},
		{"oversized", http.StatusOK, bytes.Repeat([]byte(" "), int(maxExtractorJSONBytes)+1), ErrJSONResponseTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &flickrFixtureTransport{responses: map[string]flickrFixtureResponse{
				flickrBeaconURL: {status: test.status, body: test.body},
			}}
			_, err := NewFlickr().Extract(context.Background(), Request{
				URL: "https://flickr.com/photos/user/5645318632", Transport: transport,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestFlickrRejectsUnsafeOrOversizedStreamInventory(t *testing.T) {
	target := flickrTarget{id: "5645318632", userPath: "user"}
	photo := flickrPhoto{ID: target.id, Title: flickrContent{Content: "fixture"}}
	unsafe := []flickrStream{{Type: "orig", Content: "https://127.0.0.1/private.mp4"}}
	if _, err := normalizeFlickr(target, "https://flickr.com/photos/user/5645318632", photo, unsafe); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unsafe error = %v", err)
	}
	oversized := make([]flickrStream, flickrMaxStreams+1)
	if _, err := normalizeFlickr(target, "https://flickr.com/photos/user/5645318632", photo, oversized); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("oversized error = %v", err)
	}
}

func TestFlickrHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewFlickr().Extract(ctx, Request{
		URL:       "https://flickr.com/photos/user/5645318632",
		Transport: &flickrFixtureTransport{responses: map[string]flickrFixtureResponse{}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestFlickrFailsClosedWithoutCookieIsolation(t *testing.T) {
	transport := flickrNonIsolatedTransport{inner: &flickrFixtureTransport{responses: map[string]flickrFixtureResponse{
		flickrBeaconURL: {body: flickrFixture(t, "beacon.json")},
	}}}
	_, err := NewFlickr().Extract(context.Background(), Request{
		URL: "https://flickr.com/photos/user/5645318632", Transport: transport,
	})
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("error = %v", err)
	}
	if len(transport.inner.requests) != 0 {
		t.Fatal("non-isolated transport was used")
	}
}

func FuzzFlickrRouting(f *testing.F) {
	for _, seed := range []string{
		"https://www.flickr.com/photos/forestwander-nature-pictures/5645318632/in/photostream/",
		"https://secure.flickr.com/photos/10922353@N03/5645318632",
		"https://evil.test/photos/user/5645318632",
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
		target, ok := classifyFlickrURL(parsed)
		if ok && (!flickrIDPattern.MatchString(target.id) || !flickrUserPattern.MatchString(target.userPath)) {
			t.Fatalf("invalid accepted target: %#v", target)
		}
	})
}

func TestNormalizeFlickrStreamURL(t *testing.T) {
	tests := []struct {
		raw  string
		want bool
	}{
		{"https://live.staticflickr.com/video/id/secret/orig.mp4?token=x", true},
		{"http://live.staticflickr.com/video.mp4", false},
		{"https://user:pass@live.staticflickr.com/video.mp4", false},
		{"https://live.staticflickr.com:443/video.mp4", false},
		{"https://live.staticflickr.com/a%2fb.mp4", false},
		{"https://evil.test/video.mp4", false},
	}
	for _, test := range tests {
		_, ok := normalizeFlickrStreamURL(test.raw)
		if ok != test.want {
			t.Fatalf("normalize(%q) = %v", test.raw, ok)
		}
	}
}
