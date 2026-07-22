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

	mu               sync.Mutex
	requests         []string
	blockUserPage    bool
	blockStationPage bool
	pageStarted      chan struct{}
	startOnce        sync.Once
}

func newSoundCloudFixtureTransport(t *testing.T) *soundCloudFixtureTransport {
	t.Helper()
	transport := &soundCloudFixtureTransport{testingT: t, fixture: make(map[string][]byte), pageStarted: make(chan struct{})}
	for _, name := range []string{
		"home.html", "client.js", "track.json", "progressive.json", "hls.json",
		"user.json", "page1.json", "page2.json", "playlist.json",
		"station_resolve.json", "station_page1.json", "station_page2.json",
		"related_track.json", "recommended_page1.json", "albums_page1.json",
		"sets_page1.json", "mixed_page1.json",
	} {
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
		case strings.Contains(resolved, "/stations/track/"):
			return soundCloudResponse(http.StatusOK, transport.fixture["station_resolve.json"]), nil
		case strings.HasSuffix(resolved, "/tracks"):
			return soundCloudResponse(http.StatusOK, transport.fixture["user.json"]), nil
		case strings.Contains(resolved, "/sets/"):
			return soundCloudResponse(http.StatusOK, transport.fixture["playlist.json"]), nil
		case strings.HasSuffix(resolved, "/related-signal"):
			return soundCloudResponse(http.StatusOK, transport.fixture["related_track.json"]), nil
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
	case "/stations/soundcloud:track-stations:5000/tracks":
		if transport.blockStationPage {
			transport.startOnce.Do(func() { close(transport.pageStarted) })
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if request.URL.Query().Get("cursor") == "page2" {
			return soundCloudResponse(http.StatusOK, transport.fixture["station_page2.json"]), nil
		}
		return soundCloudResponse(http.StatusOK, transport.fixture["station_page1.json"]), nil
	case "/tracks/8000/related":
		return soundCloudResponse(http.StatusOK, transport.fixture["recommended_page1.json"]), nil
	case "/tracks/8000/albums":
		return soundCloudResponse(http.StatusOK, transport.fixture["albums_page1.json"]), nil
	case "/tracks/8000/playlists_without_albums":
		if request.URL.Query().Get("mixed") == "1" {
			return soundCloudResponse(http.StatusOK, transport.fixture["mixed_page1.json"]), nil
		}
		return soundCloudResponse(http.StatusOK, transport.fixture["sets_page1.json"]), nil
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
		{"https://soundcloud.com/stations/track/fixture-artist/synthetic-signal", true},
		{"https://www.soundcloud.com/stations/track/fixture-artist/synthetic-signal", true},
		{"https://m.soundcloud.com/stations/track/fixture-artist/synthetic-signal", true},
		{"https://soundcloud.com/fixture-artist/related-signal/recommended", true},
		{"https://soundcloud.com/fixture-artist/related-signal/albums", true},
		{"https://soundcloud.com/fixture-artist/related-signal/sets", true},
		{"https://api.soundcloud.com/tracks/4242", true},
		{"https://api.soundcloud.com/tracks/0", false},
		{"https://api.soundcloud.com/tracks/soundcloud%3Atracks%3A4242", true},
		{"https://api-v2.soundcloud.com/playlists/soundcloud:playlists:55", true},
		{"https://soundcloud.com/artist/likes", false},
		{"https://soundcloud.com/artist/track/recommended", true},
		{"https://soundcloud.com/discover/sets/charts", true},
		{"https://soundcloud.com:444/artist/track", false},
		{"https://soundcloud.com/artist%2Ftrack", false},
		{"https://soundcloud.com/artist//track", false},
		{"ftp://soundcloud.com/artist/track", false},
		{"https://example.test/artist/track", false},
		{"https://soundcloud.com/stations/track/fixture-artist", false},
		{"https://soundcloud.com/stations/track/fixture-artist/synthetic-signal/extra", false},
		{"https://soundcloud.com/fixture-artist/related-signal/unknown", false},
		{"https://soundcloud.com/fixture-artist/related-signal/recommended/extra", false},
		{"https://soundcloud.com/artist/stations", false},
		{"https://soundcloud.com/artist/recommended", false},
		{"https://soundcloud.com/stations/track/fixture-artist/synthetic-signal?secret_token=invalid", false},
		{"https://user@soundcloud.com/fixture-artist/related-signal/recommended", false},
		{"https://soundcloud.com:8080/stations/track/a/b", false},
		{"https://soundcloud.com/fixture-artist/related-signal%2Frecommended", false},
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
	tooManyTranscodings := soundCloudTrack{ID: json.Number("4242"), Title: "too many"}
	tooManyTranscodings.Media.Transcodings = make([]soundCloudTranscoding, soundCloudMaxTranscodings+1)
	tooManyBody, marshalErr := json.Marshal(tooManyTranscodings)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
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
		{name: "transcoding bound", status: http.StatusOK, body: tooManyBody, want: ErrInvalidMetadata},
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
	userPolicy := soundCloudContinuationPolicy{allowedPath: "/users/7/tracks"}
	for _, rawURL := range []string{
		"https://evil.example/users/7/tracks", "http://api-v2.soundcloud.com/users/7/tracks",
		"https://api-v2.soundcloud.com:444/users/7/tracks", "https://api-v2.soundcloud.com/tracks/7",
		"https://api-v2.soundcloud.com/users/8/tracks",
		"https://api-v2.soundcloud.com/users/7/tracks%2fextra",
		"https://api-v2.soundcloud.com/stations/soundcloud:track-stations:5000/tracks",
		"https://api-v2.soundcloud.com/users/7/tracks/extra",
	} {
		if _, err := userPolicy.validate(rawURL); !errors.Is(err, ErrInvalidPlaylist) {
			t.Errorf("userPolicy.validate(%q) error = %v", rawURL, err)
		}
	}
	stationPolicy := soundCloudContinuationPolicy{allowedPath: "/stations/soundcloud:track-stations:5000/tracks"}
	for _, rawURL := range []string{
		"https://api-v2.soundcloud.com/stations/soundcloud:track-stations:6000/tracks",
		"https://api-v2.soundcloud.com/stations/soundcloud:track-stations:5000/tracks/extra",
		"https://api-v2.soundcloud.com/users/7/tracks",
		"https://api-v2.soundcloud.com/tracks/8000/related",
	} {
		if _, err := stationPolicy.validate(rawURL); !errors.Is(err, ErrInvalidPlaylist) {
			t.Errorf("stationPolicy.validate(%q) error = %v", rawURL, err)
		}
	}
	relatedPolicy := soundCloudContinuationPolicy{allowedPath: "/tracks/8000/related"}
	for _, rawURL := range []string{
		"https://api-v2.soundcloud.com/tracks/8000/albums",
		"https://api-v2.soundcloud.com/tracks/8000/playlists_without_albums",
		"https://api-v2.soundcloud.com/tracks/9000/related",
		"https://api-v2.soundcloud.com/tracks/8000/related/extra",
	} {
		if _, err := relatedPolicy.validate(rawURL); !errors.Is(err, ErrInvalidPlaylist) {
			t.Errorf("relatedPolicy.validate(%q) error = %v", rawURL, err)
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
			_, _ = soundCloudDirectCollectionEntry(item.soundCloudTrack)
			_, _ = soundCloudTrackEntry(item.Track, "")
			_, _ = soundCloudPlaylistEntry(item.Playlist)
			_, _ = soundCloudPlaylistCollectionEntry(item.Playlist)
		}
	})
}

func TestSoundCloudStationResolveAndPlaylistMetadata(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/stations/track/fixture-artist/synthetic-signal", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := result.Info.ID(); got != "5000" {
		t.Fatalf("id = %q", got)
	}
	if got, _ := result.Info.Title(); got != "Track station: Synthetic Station" {
		t.Fatalf("title = %q", got)
	}
	if got, _ := result.Info.Lookup("webpage_url").StringValue(); got != "https://soundcloud.com/stations/track/fixture-artist/synthetic-signal" {
		t.Fatalf("webpage_url = %q", got)
	}
	if transport.requestCount("/stations/soundcloud:track-stations:5000/tracks") != 0 {
		t.Fatal("station page fetched eagerly")
	}
}

func TestSoundCloudStationLazyMultiPageOrdering(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/stations/track/fixture-artist/synthetic-signal", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport.requestCount("/stations/soundcloud:track-stations:5000/tracks") != 0 {
		t.Fatal("station page fetched eagerly")
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
	}
	if strings.Join(ids, ",") != "200,201,202" {
		t.Fatalf("ids = %v", ids)
	}
	if transport.requestCount("/stations/soundcloud:track-stations:5000/tracks") != 2 {
		t.Fatalf("page requests = %d", transport.requestCount("/stations/soundcloud:track-stations:5000/tracks"))
	}
	again, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || len(again) != 3 || again[0].ID != "200" || again[2].ID != "202" {
		t.Fatalf("second iteration = %#v, %v", again, err)
	}
}

func TestSoundCloudRecommendedTrackEntries(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/related-signal/recommended", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := result.Info.ID(); got != "8000" {
		t.Fatalf("id = %q", got)
	}
	if got, _ := result.Info.Title(); got != "Related Signal (Recommended)" {
		t.Fatalf("title = %q", got)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "300" || entries[1].ID != "301" || !entries[0].Transparent {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestSoundCloudAlbumsPlaylistEntries(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/related-signal/albums", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := result.Info.Title(); got != "Related Signal (Albums)" {
		t.Fatalf("title = %q", got)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "60" || !entries[0].Transparent {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestSoundCloudSetsPlaylistEntries(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/related-signal/sets", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := result.Info.Title(); got != "Related Signal (Sets)" {
		t.Fatalf("title = %q", got)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != "61" || !entries[0].Transparent {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestSoundCloudMixedCollectionDecoding(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	transport.override = func(request *http.Request) (int, []byte, bool) {
		if request.URL.Path == "/tracks/8000/playlists_without_albums" {
			return http.StatusOK, transport.fixture["mixed_page1.json"], true
		}
		return 0, nil, false
	}
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/related-signal/sets", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil {
		t.Fatal(err)
	}
	// Expect: direct track (400), direct playlist (63), nested playlist (62)
	if len(entries) != 3 || entries[0].ID != "400" || entries[1].ID != "63" || entries[2].ID != "62" {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0].URL != "https://soundcloud.com/fixture-artist/mixed-track" {
		t.Fatalf("track entry URL = %q", entries[0].URL)
	}
	// Direct playlist should use its set permalink, not a track API URL
	if entries[1].URL != "https://soundcloud.com/fixture-artist/sets/direct-playlist" {
		t.Fatalf("direct playlist entry URL = %q", entries[1].URL)
	}
}

func TestSoundCloudRepeatedCursorHandling(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	loopBody := []byte(`{"collection":[{"id":200,"title":"Loop","permalink_url":"https://soundcloud.com/fixture-artist/loop"}],"next_href":"https://api-v2.soundcloud.com/stations/soundcloud:track-stations:5000/tracks?cursor=loop&client_id=stale"}`)
	transport.override = func(request *http.Request) (int, []byte, bool) {
		if request.URL.Path == "/stations/soundcloud:track-stations:5000/tracks" {
			return http.StatusOK, loopBody, true
		}
		return 0, nil, false
	}
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/stations/track/fixture-artist/synthetic-signal", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) > 2 {
		t.Fatalf("repeated cursor produced %d entries, expected at most 2", len(entries))
	}
}

func TestSoundCloudOversizedPageRejection(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	collection := make([]map[string]any, soundCloudMaxPageEntries+1)
	for i := range collection {
		collection[i] = map[string]any{"id": i, "title": "x"}
	}
	body, marshalErr := json.Marshal(map[string]any{"collection": collection})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	transport.override = func(request *http.Request) (int, []byte, bool) {
		if request.URL.Path == "/stations/soundcloud:track-stations:5000/tracks" {
			return http.StatusOK, body, true
		}
		return 0, nil, false
	}
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/stations/track/fixture-artist/synthetic-signal", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = result.Entries.Iterator().Next(context.Background())
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("Next() error = %v, want ErrInvalidPlaylist", err)
	}
}

func TestSoundCloudMalformedStationIdentifier(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	transport.override = func(request *http.Request) (int, []byte, bool) {
		if request.URL.Path == "/resolve" {
			return http.StatusOK, []byte(`{"id":"soundcloud:track-stations:0","title":"Bad Station"}`), true
		}
		return 0, nil, false
	}
	_, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/stations/track/fixture-artist/synthetic-signal", Transport: transport,
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("Extract() error = %v, want ErrInvalidMetadata", err)
	}
}

func TestSoundCloudMalformedResolvedTrack(t *testing.T) {
	// Missing/zero/malformed ID remains rejected
	transport := newSoundCloudFixtureTransport(t)
	transport.override = func(request *http.Request) (int, []byte, bool) {
		if request.URL.Path == "/resolve" {
			return http.StatusOK, []byte(`{"id":0,"title":"Valid Title"}`), true
		}
		return 0, nil, false
	}
	_, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/related-signal/recommended", Transport: transport,
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("Extract() error = %v, want ErrInvalidMetadata", err)
	}
}

func TestSoundCloudCancellationDuringStationPage(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	transport.blockStationPage = true
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/stations/track/fixture-artist/synthetic-signal", Transport: transport,
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
		t.Fatal("station page request did not start")
	}
	select {
	case nextErr := <-done:
		if !errors.Is(nextErr, context.Canceled) {
			t.Fatalf("Next() error = %v", nextErr)
		}
	case <-time.After(time.Second):
		t.Fatal("station page request did not cancel")
	}
}

func TestSoundCloudCategorizedStationFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   []byte
		want   error
	}{
		{name: "unavailable", status: http.StatusNotFound, want: ErrUnavailable},
		{name: "authentication", status: http.StatusForbidden, want: ErrAuthentication},
		{name: "malformed JSON", status: http.StatusOK, body: []byte(`{"id":`), want: ErrInvalidMetadata},
		{name: "missing title", status: http.StatusOK, body: []byte(`{"id":"soundcloud:track-stations:5000"}`), want: ErrInvalidMetadata},
		{name: "related errors field", status: http.StatusOK, body: []byte(`{"id":8000,"title":"x","errors":[{"error_message":"not available"}]}`), want: ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := newSoundCloudFixtureTransport(t)
			transport.override = func(request *http.Request) (int, []byte, bool) {
				if request.URL.Path == "/resolve" {
					if test.name == "related errors field" {
						return test.status, test.body, true
					}
					return test.status, test.body, true
				}
				return 0, nil, false
			}
			url := "https://soundcloud.com/stations/track/fixture-artist/synthetic-signal"
			if test.name == "related errors field" {
				url = "https://soundcloud.com/fixture-artist/related-signal/recommended"
			}
			_, err := NewSoundCloud().Extract(context.Background(), Request{URL: url, Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("Extract() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSoundCloudSecretRedactionInStationErrors(t *testing.T) {
	transport := newSoundCloudFixtureTransport(t)
	transport.override = func(request *http.Request) (int, []byte, bool) {
		if request.URL.Path == "/resolve" {
			return http.StatusForbidden, nil, true
		}
		return 0, nil, false
	}
	_, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/stations/track/fixture-artist/synthetic-signal", Transport: transport,
	})
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("Extract() error = %v", err)
	}
	for _, requestURL := range transport.requests {
		if strings.Contains(requestURL, "client_id=") {
			if strings.Contains(requestURL, soundCloudFixtureClientID) {
				continue
			}
		}
	}
	if strings.Contains(err.Error(), soundCloudFixtureClientID) {
		t.Fatalf("error leaked client ID: %v", err)
	}
}

func TestSoundCloudContinuationPolicyAcceptsValidCursors(t *testing.T) {
	policy := soundCloudContinuationPolicy{allowedPath: "/users/7/tracks"}
	valid, err := policy.validate("https://api-v2.soundcloud.com/users/7/tracks?cursor=abc&client_id=stale")
	if err != nil {
		t.Fatalf("validate error = %v", err)
	}
	if strings.Contains(valid, "client_id") {
		t.Fatalf("client_id not stripped: %s", valid)
	}
	if !strings.Contains(valid, "cursor=abc") {
		t.Fatalf("cursor lost: %s", valid)
	}
}

func TestSoundCloudContinuationQueryBounds(t *testing.T) {
	policy := soundCloudContinuationPolicy{allowedPath: "/users/7/tracks"}
	longValue := strings.Repeat("x", soundCloudMaxQueryValue+1)
	if _, err := policy.validate("https://api-v2.soundcloud.com/users/7/tracks?cursor=" + longValue); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("oversized query value not rejected: %v", err)
	}
	queries := make([]string, soundCloudMaxQueryParams+1)
	for i := range queries {
		queries[i] = fmt.Sprintf("k%d=v%d", i, i)
	}
	if _, err := policy.validate("https://api-v2.soundcloud.com/users/7/tracks?" + strings.Join(queries, "&")); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("too many query params not rejected: %v", err)
	}
}

func FuzzSoundCloudContinuationPolicy(f *testing.F) {
	f.Add("https://api-v2.soundcloud.com/users/7/tracks?cursor=abc")
	f.Add("https://api-v2.soundcloud.com/stations/soundcloud:track-stations:5000/tracks?cursor=page2")
	f.Add("https://api-v2.soundcloud.com/tracks/8000/related?offset=200")
	f.Add("https://evil.example/users/7/tracks")
	f.Add("http://api-v2.soundcloud.com/users/7/tracks")
	f.Add("https://api-v2.soundcloud.com/users/8/../7/tracks")
	f.Add("https://api-v2.soundcloud.com/users/7/tracks/")
	f.Add("https://api-v2.soundcloud.com/users/7/tracks#frag")
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 16<<10 {
			t.Skip()
		}
		policy := soundCloudContinuationPolicy{allowedPath: "/users/7/tracks"}
		result, err := policy.validate(rawURL)
		if err != nil {
			return // Rejected: safe
		}
		// Assert canonical invariant on any accepted result
		parsed, parseErr := url.Parse(result)
		if parseErr != nil {
			t.Fatalf("accepted result does not parse: %v", parseErr)
		}
		if parsed.Scheme != "https" {
			t.Fatalf("accepted non-https scheme: %s", result)
		}
		if strings.ToLower(parsed.Hostname()) != "api-v2.soundcloud.com" {
			t.Fatalf("accepted wrong host: %s", result)
		}
		if parsed.Port() != "" {
			t.Fatalf("accepted explicit port: %s", result)
		}
		if parsed.User != nil {
			t.Fatalf("accepted userinfo: %s", result)
		}
		if parsed.Fragment != "" || parsed.RawFragment != "" {
			t.Fatalf("accepted fragment: %s", result)
		}
		if parsed.Path != "/users/7/tracks" {
			t.Fatalf("accepted non-exact path %q: %s", parsed.Path, result)
		}
		if strings.Contains(result, "client_id") {
			t.Fatalf("client_id not stripped: %s", result)
		}
		// No encoded separators
		escaped := strings.ToLower(parsed.EscapedPath())
		if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
			t.Fatalf("accepted encoded separator: %s", result)
		}
		// No dot segments
		for _, segment := range strings.Split(parsed.EscapedPath(), "/") {
			if segment == "." || segment == ".." {
				t.Fatalf("accepted dot segment: %s", result)
			}
		}
	})
}

// Fix 1: Direct playlist collection entries classification tests

func TestSoundCloudDirectCollectionEntryClassification(t *testing.T) {
	tests := []struct {
		name    string
		item    soundCloudTrack
		wantOK  bool
		wantURL string
		wantID  string
	}{
		{
			name:    "direct top-level track",
			item:    soundCloudTrack{ID: json.Number("100"), Title: "Track", PermalinkURL: "https://soundcloud.com/artist/track"},
			wantOK:  true,
			wantURL: "https://soundcloud.com/artist/track",
			wantID:  "100",
		},
		{
			name:    "direct top-level playlist",
			item:    soundCloudTrack{ID: json.Number("60"), Title: "Playlist", PermalinkURL: "https://soundcloud.com/artist/sets/album"},
			wantOK:  true,
			wantURL: "https://soundcloud.com/artist/sets/album",
			wantID:  "60",
		},
		{
			name:    "missing permalink with valid ID falls back to track API",
			item:    soundCloudTrack{ID: json.Number("100"), Title: "Track"},
			wantOK:  true,
			wantURL: "https://api-v2.soundcloud.com/tracks/100",
			wantID:  "100",
		},
		{
			name:    "malformed permalink with valid ID falls back to track API",
			item:    soundCloudTrack{ID: json.Number("100"), Title: "Track", PermalinkURL: "://invalid"},
			wantOK:  true,
			wantURL: "https://api-v2.soundcloud.com/tracks/100",
			wantID:  "100",
		},
		{
			name:    "unclassifiable permalink with valid ID falls back to track API",
			item:    soundCloudTrack{ID: json.Number("100"), Title: "Track", PermalinkURL: "https://example.com/not-soundcloud"},
			wantOK:  true,
			wantURL: "https://api-v2.soundcloud.com/tracks/100",
			wantID:  "100",
		},
		{
			name:   "missing permalink and missing ID skipped",
			item:   soundCloudTrack{Title: "No ID"},
			wantOK: false,
		},
		{
			name:   "zero ID skipped",
			item:   soundCloudTrack{ID: json.Number("0"), Title: "Zero"},
			wantOK: false,
		},
		{
			name:    "API playlist URL classified as playlist",
			item:    soundCloudTrack{ID: json.Number("55"), Title: "API Playlist", PermalinkURL: "https://api-v2.soundcloud.com/playlists/55"},
			wantOK:  true,
			wantURL: "https://api-v2.soundcloud.com/playlists/55",
			wantID:  "55",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry, ok := soundCloudDirectCollectionEntry(test.item)
			if ok != test.wantOK {
				t.Fatalf("ok = %v, want %v", ok, test.wantOK)
			}
			if !ok {
				return
			}
			if entry.URL != test.wantURL {
				t.Errorf("URL = %q, want %q", entry.URL, test.wantURL)
			}
			if entry.ID != test.wantID {
				t.Errorf("ID = %q, want %q", entry.ID, test.wantID)
			}
		})
	}
}

func TestSoundCloudNestedTrackAndPlaylistEntries(t *testing.T) {
	// Nested track
	nestedTrack := soundCloudTrack{ID: json.Number("200"), Title: "Nested Track", PermalinkURL: "https://soundcloud.com/artist/nested"}
	if entry, ok := soundCloudTrackEntry(nestedTrack, ""); !ok || entry.URL != "https://soundcloud.com/artist/nested" || entry.ID != "200" {
		t.Fatalf("nested track entry = %#v, %v", entry, ok)
	}
	// Nested playlist
	nestedPlaylist := soundCloudPlaylist{ID: json.Number("62"), Title: "Nested Playlist", PermalinkURL: "https://soundcloud.com/artist/sets/nested"}
	if entry, ok := soundCloudPlaylistCollectionEntry(nestedPlaylist); !ok || entry.URL != "https://soundcloud.com/artist/sets/nested" || entry.ID != "62" {
		t.Fatalf("nested playlist entry = %#v, %v", entry, ok)
	}
	// Nested playlist with missing permalink falls back to API URL
	noPermalink := soundCloudPlaylist{ID: json.Number("63"), Title: "No Permalink"}
	if entry, ok := soundCloudPlaylistCollectionEntry(noPermalink); !ok || entry.URL != "https://api-v2.soundcloud.com/playlists/63" {
		t.Fatalf("no-permalink playlist entry = %#v, %v", entry, ok)
	}
}

// Fix 2: Secret-safe related error tests

func TestSoundCloudRelatedErrorSecretSafety(t *testing.T) {
	// error_message contains unmistakable synthetic secrets and URLs
	secretBody := []byte(`{"id":8000,"title":"x","errors":[{"error_message":"client_id=SUPERSECRET123 token=s-topsecret https://signed.example/url?sig=ABC123"}]}`)
	transport := newSoundCloudFixtureTransport(t)
	transport.override = func(request *http.Request) (int, []byte, bool) {
		if request.URL.Path == "/resolve" {
			return http.StatusOK, secretBody, true
		}
		return 0, nil, false
	}
	_, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/related-signal/recommended", Transport: transport,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Extract() error = %v, want ErrUnavailable", err)
	}
	errMsg := err.Error()
	for _, secret := range []string{"SUPERSECRET123", "s-topsecret", "signed.example", "sig=ABC123", "client_id="} {
		if strings.Contains(errMsg, secret) {
			t.Fatalf("error leaked secret %q: %v", secret, err)
		}
	}
	// Verify generic diagnostic is present
	if !strings.Contains(errMsg, "SoundCloud related resource unavailable") {
		t.Fatalf("error missing generic diagnostic: %v", err)
	}
}

// Fix 3: Exact continuation path comparison tests

func TestSoundCloudContinuationExactPathComparison(t *testing.T) {
	policy := soundCloudContinuationPolicy{allowedPath: "/users/7/tracks"}
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		// Valid
		{"exact valid path", "https://api-v2.soundcloud.com/users/7/tracks", false},
		{"valid with cursor", "https://api-v2.soundcloud.com/users/7/tracks?cursor=abc", false},
		{"valid with linked_partitioning", "https://api-v2.soundcloud.com/users/7/tracks?linked_partitioning=1&limit=200", false},
		{"valid strips client_id", "https://api-v2.soundcloud.com/users/7/tracks?cursor=x&client_id=stale", false},
		// Cross-identity paths
		{"cross-user path", "https://api-v2.soundcloud.com/users/8/tracks", true},
		{"cross-track path", "https://api-v2.soundcloud.com/tracks/7", true},
		{"cross-station path", "https://api-v2.soundcloud.com/stations/soundcloud:track-stations:5000/tracks", true},
		{"cross-relation path", "https://api-v2.soundcloud.com/tracks/8000/related", true},
		// Trailing slash
		{"trailing slash", "https://api-v2.soundcloud.com/users/7/tracks/", true},
		// Dot and dot-dot segments
		{"dot-dot segment", "https://api-v2.soundcloud.com/users/8/../7/tracks", true},
		{"dot segment", "https://api-v2.soundcloud.com/users/./7/tracks", true},
		{"dot-dot in middle", "https://api-v2.soundcloud.com/users/7/../7/tracks", true},
		// Encoded separators
		{"encoded slash", "https://api-v2.soundcloud.com/users/7/tracks%2fextra", true},
		{"encoded backslash", "https://api-v2.soundcloud.com/users/7/tracks%5cextra", true},
		{"encoded NUL", "https://api-v2.soundcloud.com/users/7/tracks%00extra", true},
		// Authority violations
		{"explicit port", "https://api-v2.soundcloud.com:443/users/7/tracks", true},
		{"userinfo", "https://user@api-v2.soundcloud.com/users/7/tracks", true},
		{"wrong host", "https://evil.example/users/7/tracks", true},
		{"http scheme", "http://api-v2.soundcloud.com/users/7/tracks", true},
		// Fragment
		{"fragment", "https://api-v2.soundcloud.com/users/7/tracks#frag", true},
		// Extra path segments
		{"extra segment", "https://api-v2.soundcloud.com/users/7/tracks/extra", true},
		// Malformed
		{"NUL in URL", "https://api-v2.soundcloud.com/users/7/tracks\x00", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := policy.validate(test.rawURL)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidPlaylist) {
					t.Fatalf("validate(%q) error = %v, want ErrInvalidPlaylist", test.rawURL, err)
				}
			} else {
				if err != nil {
					t.Fatalf("validate(%q) unexpected error = %v", test.rawURL, err)
				}
				// Verify client_id is stripped
				if strings.Contains(result, "client_id") {
					t.Fatalf("client_id not stripped: %s", result)
				}
			}
		})
	}
}

// Fix 4: Slug fallback tests

func TestSoundCloudRelatedSlugFallback(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantTitle string
		wantErr   error
	}{
		{
			name:      "valid ID with missing title uses slug",
			body:      `{"id":8000}`,
			wantTitle: "fixture-artist/related-signal (Recommended)",
		},
		{
			name:      "whitespace-only title uses slug",
			body:      `{"id":8000,"title":"   "}`,
			wantTitle: "fixture-artist/related-signal (Recommended)",
		},
		{
			name:      "empty title uses slug",
			body:      `{"id":8000,"title":""}`,
			wantTitle: "fixture-artist/related-signal (Recommended)",
		},
		{
			name:      "normal title behavior unchanged",
			body:      `{"id":8000,"title":"Related Signal"}`,
			wantTitle: "Related Signal (Recommended)",
		},
		{
			name:    "missing ID rejected",
			body:    `{"title":"No ID"}`,
			wantErr: ErrInvalidMetadata,
		},
		{
			name:    "zero ID rejected",
			body:    `{"id":0,"title":"Zero"}`,
			wantErr: ErrInvalidMetadata,
		},
		{
			name:    "malformed ID rejected",
			body:    `{"id":"abc","title":"Bad"}`,
			wantErr: ErrInvalidMetadata,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := newSoundCloudFixtureTransport(t)
			transport.override = func(request *http.Request) (int, []byte, bool) {
				if request.URL.Path == "/resolve" {
					return http.StatusOK, []byte(test.body), true
				}
				return 0, nil, false
			}
			result, err := NewSoundCloud().Extract(context.Background(), Request{
				URL: "https://soundcloud.com/fixture-artist/related-signal/recommended", Transport: transport,
			})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Extract() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := result.Info.Title(); got != test.wantTitle {
				t.Fatalf("title = %q, want %q", got, test.wantTitle)
			}
		})
	}
}

func TestSoundCloudRelatedSlugFallbackAlbumsAndSets(t *testing.T) {
	// Verify slug fallback works for albums and sets relations too
	for _, relation := range []struct {
		urlSuffix string
		apiPath   string
		suffix    string
	}{
		{"albums", "/tracks/8000/albums", "Albums"},
		{"sets", "/tracks/8000/playlists_without_albums", "Sets"},
	} {
		transport := newSoundCloudFixtureTransport(t)
		transport.override = func(request *http.Request) (int, []byte, bool) {
			if request.URL.Path == "/resolve" {
				return http.StatusOK, []byte(`{"id":8000}`), true
			}
			return 0, nil, false
		}
		result, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://soundcloud.com/fixture-artist/related-signal/" + relation.urlSuffix, Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		wantTitle := "fixture-artist/related-signal (" + relation.suffix + ")"
		if got, _ := result.Info.Title(); got != wantTitle {
			t.Fatalf("%s title = %q, want %q", relation.suffix, got, wantTitle)
		}
	}
}
