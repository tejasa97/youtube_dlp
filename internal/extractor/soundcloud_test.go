package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const soundCloudFixtureClientID = "0123456789abcdef0123456789abcdef"

type soundCloudFixtureTransport struct {
	testingT *testing.T
	fixture  map[string][]byte
	override func(*http.Request) (int, []byte, bool)

	mu            sync.Mutex
	requests      []string
	blockUserPage bool
	pageStarted   chan struct{}
	startOnce     sync.Once
}

func newSoundCloudFixtureTransport(t *testing.T) *soundCloudFixtureTransport {
	t.Helper()
	transport := &soundCloudFixtureTransport{testingT: t, fixture: make(map[string][]byte), pageStarted: make(chan struct{})}
	for _, name := range []string{"home.html", "client.js", "track.json", "progressive.json", "hls.json", "user.json", "page1.json", "page2.json", "playlist.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "soundcloud", name))
		if err != nil {
			t.Fatal(err)
		}
		transport.fixture[name] = data
	}
	return transport
}

func (transport *soundCloudFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.URL.String())
	transport.mu.Unlock()
	if transport.override != nil {
		if status, body, ok := transport.override(request); ok {
			return soundCloudResponse(status, body), nil
		}
	}
	host, path := request.URL.Hostname(), request.URL.Path
	if host == "soundcloud.com" && path == "/" {
		return soundCloudResponse(http.StatusOK, transport.fixture["home.html"]), nil
	}
	if host == "a-v2.sndcdn.com" && path == "/client.js" {
		return soundCloudResponse(http.StatusOK, transport.fixture["client.js"]), nil
	}
	if host != "api-v2.soundcloud.com" {
		return soundCloudResponse(http.StatusNotFound, nil), nil
	}
	if request.URL.Query().Get("client_id") != soundCloudFixtureClientID {
		return soundCloudResponse(http.StatusUnauthorized, nil), nil
	}
	switch path {
	case "/resolve":
		resolved := request.URL.Query().Get("url")
		switch {
		case strings.HasSuffix(resolved, "/tracks"):
			return soundCloudResponse(http.StatusOK, transport.fixture["user.json"]), nil
		case strings.Contains(resolved, "/sets/"):
			return soundCloudResponse(http.StatusOK, transport.fixture["playlist.json"]), nil
		default:
			return soundCloudResponse(http.StatusOK, transport.fixture["track.json"]), nil
		}
	case "/tracks/4242":
		return soundCloudResponse(http.StatusOK, transport.fixture["track.json"]), nil
	case "/playlists/55":
		return soundCloudResponse(http.StatusOK, transport.fixture["playlist.json"]), nil
	case "/media/4242/progressive":
		return soundCloudResponse(http.StatusOK, transport.fixture["progressive.json"]), nil
	case "/media/4242/hls":
		return soundCloudResponse(http.StatusOK, transport.fixture["hls.json"]), nil
	case "/users/7/tracks":
		if transport.blockUserPage {
			transport.startOnce.Do(func() { close(transport.pageStarted) })
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if request.URL.Query().Get("cursor") == "page2" {
			return soundCloudResponse(http.StatusOK, transport.fixture["page2.json"]), nil
		}
		return soundCloudResponse(http.StatusOK, transport.fixture["page1.json"]), nil
	default:
		return soundCloudResponse(http.StatusNotFound, nil), nil
	}
}

func (*soundCloudFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected ReadPage call")
}

func (transport *soundCloudFixtureTransport) requestCount(path string) int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	count := 0
	for _, rawURL := range transport.requests {
		parsed, _ := url.Parse(rawURL)
		if parsed.Path == path {
			count++
		}
	}
	return count
}

func soundCloudResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
}

func TestSoundCloudSuitableGuards(t *testing.T) {
	extractor := NewSoundCloud()
	tests := []struct {
		rawURL string
		want   bool
	}{
		{"https://soundcloud.com/artist/track", true},
		{"https://m.soundcloud.com/artist/track/s-private", true},
		{"https://soundcloud.com/artist/sets/album", true},
		{"https://soundcloud.com/artist/tracks", true},
		{"https://api.soundcloud.com/tracks/4242", true},
		{"https://api.soundcloud.com/tracks/soundcloud%3Atracks%3A4242", true},
		{"https://api-v2.soundcloud.com/playlists/soundcloud:playlists:55", true},
		{"https://soundcloud.com/artist/likes", false},
		{"https://soundcloud.com/artist/track/recommended", false},
		{"https://soundcloud.com/discover/sets/charts", true},
		{"https://soundcloud.com:444/artist/track", false},
		{"https://soundcloud.com/artist%2Ftrack", false},
		{"https://soundcloud.com/artist//track", false},
		{"ftp://soundcloud.com/artist/track", false},
		{"https://example.test/artist/track", false},
	}
	for _, test := range tests {
		parsed, _ := url.Parse(test.rawURL)
		if got := extractor.Suitable(parsed); got != test.want {
			t.Errorf("Suitable(%q) = %v, want %v", test.rawURL, got, test.want)
		}
	}
}

func TestSoundCloudRefreshesRejectedClientIDAndSkipsMissingTranscoding(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	metadataAttempts := 0
	transport.override = func(request *http.Request) (int, []byte, bool) {
		if request.URL.Path == "/resolve" {
			metadataAttempts++
			if metadataAttempts == 1 {
				return http.StatusUnauthorized, nil, true
			}
		}
		if request.URL.Path == "/media/4242/progressive" {
			return http.StatusNotFound, nil, true
		}
		return 0, nil, false
	}
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/synthetic-signal", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if metadataAttempts != 2 || transport.requestCount("/") != 2 || len(formats) != 1 {
		t.Fatalf("metadata attempts=%d discovery=%d formats=%d", metadataAttempts, transport.requestCount("/"), len(formats))
	}
}

func TestSoundCloudTrackMetadataAndTranscodingResolution(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/synthetic-signal", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		ID         string   `json:"id"`
		Title      string   `json:"title"`
		Duration   float64  `json:"duration"`
		Timestamp  int64    `json:"timestamp"`
		Uploader   string   `json:"uploader"`
		FormatIDs  []string `json:"format_ids"`
		Extensions []string `json:"extensions"`
		Protocols  []string `json:"protocols"`
	}
	expectedBytes, readErr := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "soundcloud", "track.expected.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err := json.Unmarshal(expectedBytes, &expected); err != nil {
		t.Fatal(err)
	}
	if got, _ := result.Info.ID(); got != expected.ID {
		t.Fatalf("id = %q", got)
	}
	if got, _ := result.Info.Title(); got != expected.Title {
		t.Fatalf("title = %q", got)
	}
	if got, _ := result.Info.Lookup("duration").Float(); got != expected.Duration {
		t.Fatalf("duration = %v", got)
	}
	if got, _ := result.Info.Lookup("timestamp").Int(); got != expected.Timestamp {
		t.Fatalf("timestamp = %d", got)
	}
	if got, _ := result.Info.Lookup("uploader").StringValue(); got != expected.Uploader {
		t.Fatalf("uploader = %q", got)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != len(expected.FormatIDs) {
		t.Fatalf("formats = %#v", formats)
	}
	for index, formatValue := range formats {
		format, _ := formatValue.Object()
		if got, _ := format.Lookup("format_id").StringValue(); got != expected.FormatIDs[index] {
			t.Errorf("format %d id = %q", index, got)
		}
		if got, _ := format.Lookup("ext").StringValue(); got != expected.Extensions[index] {
			t.Errorf("format %d ext = %q", index, got)
		}
		if got, _ := format.Lookup("protocol").StringValue(); got != expected.Protocols[index] {
			t.Errorf("format %d protocol = %q", index, got)
		}
	}
	if transport.requestCount("/") != 1 || transport.requestCount("/client.js") != 1 {
		t.Fatalf("client discovery requests = root:%d script:%d", transport.requestCount("/"), transport.requestCount("/client.js"))
	}
}

func TestSoundCloudUserTrackPagesAreLazyOrderedAndReusable(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/tracks", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport.requestCount("/users/7/tracks") != 0 {
		t.Fatal("playlist page fetched eagerly")
	}
	iterator := result.Entries.Iterator()
	var ids []string
	for {
		entry, ok, nextErr := iterator.Next(context.Background())
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if !ok {
			break
		}
		ids = append(ids, entry.ID)
		if len(ids) == 2 && transport.requestCount("/users/7/tracks") != 1 {
			t.Fatal("second item should be served from the first page")
		}
	}
	if strings.Join(ids, ",") != "100,101,102" {
		t.Fatalf("ids = %v", ids)
	}
	if transport.requestCount("/users/7/tracks") != 2 {
		t.Fatalf("page requests = %d", transport.requestCount("/users/7/tracks"))
	}
	again, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(again) != 3 || again[0].ID != "100" || again[2].ID != "102" {
		t.Fatalf("second iteration = %#v, %v", again, err)
	}
}

func TestSoundCloudSetEntriesRemainOrderedTransparentURLs(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://api.soundcloud.com/playlists/55", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "100" || entries[1].ID != "101" || !entries[0].Transparent || entries[1].ExtractorKey != "soundcloud" {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[1].URL != "https://api-v2.soundcloud.com/tracks/101" {
		t.Fatalf("fallback entry URL = %q", entries[1].URL)
	}
}

func TestSoundCloudRejectsMalformedPlaylistAndPage(t *testing.T) {
	t.Run("playlist", func(t *testing.T) {
		transport := newSoundCloudFixtureTransport(t)
		transport.override = func(request *http.Request) (int, []byte, bool) {
			if request.URL.Path == "/playlists/55" {
				return http.StatusOK, []byte(`{"id":55,"title":"missing tracks"}`), true
			}
			return 0, nil, false
		}
		_, err := NewSoundCloud().Extract(context.Background(), Request{URL: "https://api.soundcloud.com/playlists/55", Transport: transport})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("Extract() error = %v", err)
		}
	})
	t.Run("page", func(t *testing.T) {
		transport := newSoundCloudFixtureTransport(t)
		transport.override = func(request *http.Request) (int, []byte, bool) {
			if request.URL.Path == "/users/7/tracks" {
				return http.StatusOK, []byte(`{"next_href":null}`), true
			}
			return 0, nil, false
		}
		result, err := NewSoundCloud().Extract(context.Background(), Request{URL: "https://soundcloud.com/fixture-artist/tracks", Transport: transport})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = result.Entries.Iterator().Next(context.Background())
		if !errors.Is(err, ErrInvalidPlaylist) {
			t.Fatalf("Next() error = %v", err)
		}
	})
}

func TestSoundCloudCancellationInterruptsLazyPage(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	transport.blockUserPage = true
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/tracks", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, nextErr := result.Entries.Iterator().Next(ctx)
		done <- nextErr
	}()
	select {
	case <-transport.pageStarted:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("page request did not start")
	}
	select {
	case nextErr := <-done:
		if !errors.Is(nextErr, context.Canceled) {
			t.Fatalf("Next() error = %v", nextErr)
		}
	case <-time.After(time.Second):
		t.Fatal("page request did not cancel")
	}
}

func TestSoundCloudCategorizedFailuresAndSecretRedaction(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   []byte
		want   error
	}{
		{name: "unavailable", status: http.StatusNotFound, want: ErrUnavailable},
		{name: "authentication", status: http.StatusForbidden, want: ErrAuthentication},
		{name: "malformed JSON", status: http.StatusOK, body: []byte(`{"id":`), want: ErrInvalidMetadata},
		{name: "malformed metadata", status: http.StatusOK, body: []byte(`{"id":4242,"media":{"transcodings":[]}}`), want: ErrInvalidMetadata},
		{name: "blocked", status: http.StatusOK, body: []byte(`{"id":4242,"title":"blocked","policy":"BLOCK","media":{"transcodings":[]}}`), want: ErrUnavailable},
		{name: "bounded", status: http.StatusOK, body: []byte(strings.Repeat(" ", int(maxExtractorJSONBytes+1))), want: ErrJSONResponseTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := newSoundCloudFixtureTransport(t)
			transport.override = func(request *http.Request) (int, []byte, bool) {
				if request.URL.Path == "/tracks/4242" {
					return test.status, test.body, true
				}
				return 0, nil, false
			}
			_, err := NewSoundCloud().Extract(context.Background(), Request{
				URL: "https://api.soundcloud.com/tracks/4242?secret_token=s-super-secret", Transport: transport,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("Extract() error = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("error leaked secret: %v", err)
			}
		})
	}
}

func TestSoundCloudRejectsUntrustedContinuationAndAsset(t *testing.T) {
	for _, rawURL := range []string{
		"https://evil.example/users/7/tracks", "http://api-v2.soundcloud.com/users/7/tracks",
		"https://api-v2.soundcloud.com:444/users/7/tracks", "https://api-v2.soundcloud.com/tracks/7",
	} {
		if _, err := validateSoundCloudCursor(rawURL); !errors.Is(err, ErrInvalidPlaylist) {
			t.Errorf("validateSoundCloudCursor(%q) error = %v", rawURL, err)
		}
	}
	for _, rawURL := range []string{"https://evil.example/client.js", "http://a-v2.sndcdn.com/client.js", "https://a-v2.sndcdn.com:444/client.js"} {
		if _, ok := soundCloudAssetURL(rawURL); ok {
			t.Errorf("soundCloudAssetURL(%q) accepted", rawURL)
		}
	}
}

func FuzzSoundCloudURLClassification(f *testing.F) {
	f.Add("https://soundcloud.com/artist/track")
	f.Add("https://api.soundcloud.com/tracks/4242")
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 16<<10 {
			t.Skip()
		}
		parsed, _ := url.Parse(rawURL)
		_, _ = classifySoundCloudURL(parsed)
	})
}

func FuzzSoundCloudPageEntries(f *testing.F) {
	f.Add([]byte(`{"collection":[{"id":1,"title":"x","permalink_url":"https://soundcloud.com/a/b"}]}`))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 1<<20 {
			t.Skip()
		}
		var page soundCloudPage
		if json.Unmarshal(input, &page) != nil {
			return
		}
		for _, item := range page.Collection {
			_, _ = soundCloudTrackEntry(item.soundCloudTrack, "")
			_, _ = soundCloudTrackEntry(item.Track, "")
			_, _ = soundCloudPlaylistEntry(item.Playlist)
		}
	})
}
