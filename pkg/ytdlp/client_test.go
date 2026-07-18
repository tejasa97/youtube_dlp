package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/cookies/chromium"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/media/pipeline"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestIsCategory(t *testing.T) {
	err := &Error{Category: ErrorNetwork, Op: "fetch", Err: errors.New("offline")}
	if !IsCategory(err, ErrorNetwork) {
		t.Fatal("IsCategory() = false, want true")
	}
	if IsCategory(err, ErrorInvalidInput) {
		t.Fatal("IsCategory() matched the wrong category")
	}
	if !errors.Is(err, err.Err) {
		t.Fatal("Error does not unwrap its cause")
	}
}

func TestExtractorFailuresAreCategorized(t *testing.T) {
	for _, test := range []struct {
		err      error
		category ErrorCategory
	}{
		{extractor.ErrAuthentication, ErrorAuthentication},
		{extractor.ErrUnavailable, ErrorUnsupported},
		{extractor.ErrChallengeSolver, ErrorUnsupported},
	} {
		if err := categorized("extract", test.err); !IsCategory(err, test.category) {
			t.Fatalf("categorized(%v) = %v", test.err, err)
		}
	}
}

func TestMediaFailuresAreCategorized(t *testing.T) {
	for _, test := range []struct {
		err      error
		category ErrorCategory
	}{
		{ffmpeg.ErrFFmpegUnavailable, ErrorUnsupported},
		{ffmpeg.ErrDestinationExists, ErrorInvalidInput},
		{ffmpeg.ErrMediaFailure, ErrorInternal},
		{pipeline.ErrMissingDASHTracks, ErrorInternal},
		{pipeline.ErrMissingToolset, ErrorInternal},
	} {
		if err := categorized("media", test.err); !IsCategory(err, test.category) {
			t.Fatalf("categorized(%v) = %v, want %s", test.err, err, test.category)
		}
	}
}

func TestJavaScriptHelperConfigurationTakesPrecedence(t *testing.T) {
	configured := filepath.Join(t.TempDir(), "custom-helper")
	if got := discoverJavaScriptHelper(configured); got != configured {
		t.Fatalf("discoverJavaScriptHelper() = %q, want %q", got, configured)
	}
}

func TestBrowserCookieSpec(t *testing.T) {
	for _, test := range []struct {
		spec    string
		profile string
	}{
		{"chrome", ""},
		{"chrome:Default", "Default"},
		{"chrome:Profile 1", "Profile 1"},
	} {
		options, err := parseBrowserCookieSpec(test.spec)
		if err != nil || options.Browser != chromium.Chrome || options.Profile != test.profile {
			t.Fatalf("parseBrowserCookieSpec(%q) = %#v, %v", test.spec, options, err)
		}
	}
	for _, spec := range []string{"firefox", "chrome:", "chrome:../Default", "chrome:one:two"} {
		if _, err := parseBrowserCookieSpec(spec); !errors.Is(err, errInvalidBrowserCookieSpec) {
			t.Fatalf("parseBrowserCookieSpec(%q) error = %v", spec, err)
		}
	}
}

func TestClientImportsBrowserCookiesBeforeExtraction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie("authenticated")
		if err != nil || cookie.Value != "present" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", "4")
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte("data"))
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	var events []Event
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	}))
	var optionsSeen chromium.Options
	client.browserCookieImporter = func(_ context.Context, options chromium.Options) (chromium.Result, error) {
		optionsSeen = options
		return chromium.Result{
			Cookies: []*http.Cookie{{Name: "authenticated", Value: "present", Domain: target.Hostname(), Path: "/"}},
			Total:   2, Imported: 1, Failed: 1,
		}, chromium.ErrDecrypt
	}
	result, err := client.Run(context.Background(), Request{
		URL: server.URL + "/protected.mp4", CookiesFromBrowser: "chrome:Profile 1", SkipDownload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Extractor != "generic" || optionsSeen.Profile != "Profile 1" {
		t.Fatalf("result=%#v options=%#v", result, optionsSeen)
	}
	if len(events) < 2 || events[0].Kind != "browser_cookies" || events[0].Message != "imported 1 of 2 browser cookies; skipped 1" {
		t.Fatalf("events = %#v", events)
	}
}

func TestExtractionEventsRedactInputURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", "4")
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte("data"))
		}
	}))
	defer server.Close()
	var captured []Event
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		captured = append(captured, event)
		return nil
	}))
	_, err := client.Run(context.Background(), Request{
		URL: server.URL + "/media.mp4?token=input-secret&visible=yes", SkipDownload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 2 {
		t.Fatalf("events = %#v", captured)
	}
	for _, event := range captured {
		if strings.Contains(event.URL, "secret") || !strings.Contains(event.URL, "visible=yes") {
			t.Fatalf("event URL was not safely redacted: %#v", event)
		}
	}
}

func TestClientBrowserCookieFailureIsAuthenticationError(t *testing.T) {
	client := NewClient()
	client.browserCookieImporter = func(context.Context, chromium.Options) (chromium.Result, error) {
		return chromium.Result{}, chromium.ErrKeyUnavailable
	}
	_, err := client.Run(context.Background(), Request{URL: "https://example.invalid/media.mp4", CookiesFromBrowser: "chrome", SkipDownload: true})
	if !IsCategory(err, ErrorAuthentication) || !errors.Is(err, chromium.ErrKeyUnavailable) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestClientDoesNotRecoverFromCancelledCookieImport(t *testing.T) {
	client := NewClient()
	client.browserCookieImporter = func(context.Context, chromium.Options) (chromium.Result, error) {
		return chromium.Result{
			Cookies:  []*http.Cookie{{Name: "partial", Value: "secret", Domain: "example.invalid", Path: "/"}},
			Total:    2,
			Imported: 1,
		}, context.Canceled
	}
	_, err := client.Run(context.Background(), Request{URL: "https://example.invalid/media.mp4", CookiesFromBrowser: "chrome", SkipDownload: true})
	if !IsCategory(err, ErrorCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestClientCancellationReachesTransport(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := NewClient().Run(ctx, Request{URL: server.URL + "/slow?delay=1s", SkipDownload: true})
	if !IsCategory(err, ErrorCancelled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestClientWalkingSkeleton(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	var events []Event
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	}))
	result, err := client.Run(context.Background(), Request{URL: server.URL + "/page", OutputDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || result.Extractor != "fixture" {
		t.Fatalf("result = %#v", result)
	}
	if !json.Valid(result.InfoJSON) {
		t.Fatalf("invalid metadata JSON: %s", result.InfoJSON)
	}
	downloaded, err := os.ReadFile(result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(downloaded) != string(server.Media()) {
		t.Fatal("downloaded media mismatch")
	}
	if len(events) < 4 || events[0].Kind != "extracting" || events[len(events)-1].Kind != "download_completed" {
		t.Fatalf("events = %#v", events)
	}
}

func TestClientHLSAndDASHDispatch(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	for _, test := range []struct {
		name     string
		path     string
		expected []byte
	}{
		{"HLS", "/hls/master.m3u8", server.HLSMedia()},
		{"DASH", "/dash/manifest.mpd", server.DASHMedia()},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := NewClient().Run(context.Background(), Request{URL: server.URL + test.path, OutputDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(result.Filename)
			if err != nil {
				t.Fatal(err)
			}
			if string(contents) != string(test.expected) {
				t.Fatalf("contents = %q, want %q", contents, test.expected)
			}
		})
	}
}

func TestClientDASHMergeDispatch(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	video := filepath.Join(root, "source-video.mp4")
	audio := filepath.Join(root, "source-audio.m4a")
	generate := func(arguments ...string) {
		output, err := exec.Command(ffmpegPath, arguments...).CombinedOutput()
		if err != nil {
			t.Fatalf("generate fixture: %v: %s", err, output)
		}
	}
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "color=c=green:s=16x16:d=0.2", "-an", "-c:v", "mpeg4", video)
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=700:duration=0.2", "-vn", "-c:a", "aac", audio)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.mpd":
			writer.Header().Set("Content-Type", "application/dash+xml")
			_, _ = fmt.Fprint(writer, `<MPD type="static"><Period>
<AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="video" bandwidth="1000"><BaseURL>video.mp4</BaseURL></Representation></AdaptationSet>
<AdaptationSet contentType="audio" mimeType="audio/mp4"><Representation id="audio" bandwidth="128"><BaseURL>audio.m4a</BaseURL></Representation></AdaptationSet>
</Period></MPD>`)
		case "/video.mp4":
			http.ServeFile(writer, request, video)
		case "/audio.m4a":
			http.ServeFile(writer, request, audio)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := NewClient().Run(ctx, Request{URL: server.URL + "/manifest.mpd", OutputDir: root, Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	types := make(map[string]bool)
	for _, stream := range probe.Streams {
		types[stream.CodecType] = true
	}
	if !types["video"] || !types["audio"] {
		t.Fatalf("merged streams = %#v", probe.Streams)
	}
}

func TestClientConcurrentOperationsDoNotShareState(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	client := NewClient()
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := client.Run(context.Background(), Request{
				URL: server.URL + "/page", OutputDir: filepath.Join(t.TempDir(), "operation"),
			})
			errorsSeen <- err
		}(index)
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
}

type playlistFixtureExtractor struct{}

func (playlistFixtureExtractor) Name() string { return "playlist-fixture" }

func (playlistFixtureExtractor) Suitable(parsed *url.URL) bool {
	return parsed.Path == "/list" || parsed.Path == "/nested" || parsed.Path == "/cycle"
}

func (playlistFixtureExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, _ := url.Parse(request.URL)
	base := parsed.Scheme + "://" + parsed.Host
	var id, title string
	var entries []extractor.Entry
	switch parsed.Path {
	case "/list":
		id, title = "root", "Root Playlist"
		entries = []extractor.Entry{
			{URL: base + "/one.mp4", ExtractorKey: "generic", ID: "one"},
			{URL: base + "/nested", ExtractorKey: "playlist-fixture", ID: "nested"},
		}
	case "/nested":
		id, title = "nested", "Nested Playlist"
		entries = []extractor.Entry{{URL: base + "/two.mp4", ExtractorKey: "generic", ID: "two"}}
	case "/cycle":
		id, title = "cycle", "Cycle"
		entries = []extractor.Entry{{URL: request.URL, ExtractorKey: "playlist-fixture"}}
	default:
		return extractor.Extraction{}, extractor.ErrUnsupported
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(request.URL)},
	))
	return extractor.Playlist(info, extractor.StaticEntries(entries...))
}

func TestOperationResolvesNestedPlaylistInOrder(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(playlistFixtureExtractor{}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), server.URL+"/list", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Extractor != "playlist-fixture" || len(result.Entries) != 2 || len(result.Entries[1].Entries) != 1 {
		t.Fatalf("result = %#v", result)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	entries, ok := metadata["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("metadata entries = %#v", metadata["entries"])
	}
	first := entries[0].(map[string]any)
	second := entries[1].(map[string]any)
	if first["id"] != "one" || first["playlist_index"] != float64(1) || second["_type"] != "playlist" {
		t.Fatalf("entries = %#v", entries)
	}
	nested := second["entries"].([]any)[0].(map[string]any)
	if nested["id"] != "two" || nested["playlist_id"] != "nested" || nested["playlist_index"] != float64(1) {
		t.Fatalf("nested entry = %#v", nested)
	}
}

func TestOperationDownloadsPlaylistAndAggregatesBytes(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	operation := &operation{
		client: NewClient(), request: Request{OutputDir: root}, transport: transport,
		registry: extractor.NewRegistry(playlistFixtureExtractor{}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), server.URL+"/list", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || result.Bytes != int64(len("one")+len("second")) {
		t.Fatalf("aggregate result = %#v", result)
	}
	for name, want := range map[string]string{"one.mp4": "one", "two.mp4": "second"} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v", name, got, err)
		}
	}
}

func TestOperationRejectsNestedPlaylistCycle(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(playlistFixtureExtractor{}, extractor.NewGeneric()),
	}
	_, err := operation.process(context.Background(), server.URL+"/cycle", "", nil, make(map[string]bool), 0)
	if !IsCategory(err, ErrorInternal) || !errors.Is(err, extractor.ErrPlaylistLimit) {
		t.Fatalf("cycle error = %v", err)
	}
}

func TestOperationMergesTransparentEntryMetadata(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(extractor.NewGeneric()),
	}
	overlay := &extractor.Entry{ID: "producer-id", Title: "Producer Title", Transparent: true}
	result, err := operation.process(context.Background(), server.URL+"/one.mp4", "generic", overlay, make(map[string]bool), 1)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["id"] != "producer-id" || metadata["title"] != "Producer Title" {
		t.Fatalf("transparent metadata = %#v", metadata)
	}
}

func playlistMediaServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body string
		switch request.URL.Path {
		case "/one.mp4":
			body = "one"
		case "/two.mp4":
			body = "second"
		default:
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", fmt.Sprint(len(body)))
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte(body))
		}
	}))
}
