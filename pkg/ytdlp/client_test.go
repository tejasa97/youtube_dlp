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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/cookies/chromium"
	"github.com/ytdlp-go/ytdlp/internal/cookies/chromiumlinux"
	"github.com/ytdlp-go/ytdlp/internal/cookies/chromiumwindows"
	"github.com/ytdlp-go/ytdlp/internal/cookies/firefox"
	"github.com/ytdlp-go/ytdlp/internal/cookies/netscape"
	credentialnetrc "github.com/ytdlp-go/ytdlp/internal/credentials/netrc"
	"github.com/ytdlp-go/ytdlp/internal/downloader"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/media/pipeline"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/dash"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/protocol/ism"
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

func TestLoadNetRCCredentialsFromDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".netrc")
	const username, password = "fixture-user", "netrc-secret-never-export"
	if err := os.WriteFile(path, []byte("machine auth-fixture.invalid login "+username+" password "+password+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := loadNetRCCredentials(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	credential, ok, err := provider.Lookup(context.Background(), "auth-fixture.invalid")
	if err != nil || !ok || credential.Username != username || credential.Password != password {
		t.Fatalf("credential lookup mismatch: found=%t error=%v", ok, err)
	}
	if rendered := fmt.Sprintf("%v", provider); strings.Contains(rendered, username) || strings.Contains(rendered, password) {
		t.Fatalf("provider formatting exposed credentials: %q", rendered)
	}
}

func TestClientRejectsUnsafeNetRCBeforeExtraction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode-bit policy is Unix-specific")
	}
	path := filepath.Join(t.TempDir(), "credentials.netrc")
	if err := os.WriteFile(path, []byte("machine auth-fixture.invalid login user password secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewClient().Run(context.Background(), Request{
		URL: "https://auth-fixture.invalid/watch/auth-001", SkipDownload: true,
		UseNetRC: true, NetRCLocation: path,
	})
	if !IsCategory(err, ErrorSecurity) || !errors.Is(err, credentialnetrc.ErrUnsafeFile) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("Run() error = %v", err)
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
		{extractor.ErrTransportIsolation, ErrorUnsupported},
		{extractor.ErrRegionRestricted, ErrorUnsupported},
		{extractor.ErrPeerTubeNetwork, ErrorNetwork},
		{extractor.ErrInternetArchiveNetwork, ErrorNetwork},
		{extractor.ErrYouTubeChannelRateLimited, ErrorNetwork},
		{extractor.ErrYouTubeChannelNetwork, ErrorNetwork},
	} {
		if err := categorized("extract", test.err); !IsCategory(err, test.category) {
			t.Fatalf("categorized(%v) = %v", test.err, err)
		}
	}
}

func TestProductRegistryIncludesIntegratedExtractors(t *testing.T) {
	tests := []struct {
		rawURL string
		name   string
	}{
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "youtube"},
		{"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos", "youtube:channel_tab"},
		{"https://vimeo.com/123456789", "vimeo"},
		{"https://www.tiktok.com/@fixture/video/1234567890123456789", "tiktok"},
		{"https://players.brightcove.net/12345/default_default/index.html?videoId=123", "brightcove"},
		{"kaltura:123:1_abcd1234", "kaltura"},
		{"https://cdn.jwplayer.com/players/AbCd1234-ABCDEFGHI.js", "jwplatform"},
		{"wistia:a1b2c3d4e5", "wistia"},
		{"https://videos.sproutvideo.com/embed/4abcdef1234567890a/0abcdef1234567890", "sproutvideo"},
		{"https://www.dailymotion.com/video/x12345", "dailymotion"},
		{"https://www.reddit.com/r/videos/comments/abc123/title/", "reddit"},
		{"https://x.com/fixture/status/1234567890", "twitter"},
		{"https://fixture.bandcamp.com/track/example", "bandcamp"},
		{"https://www.mixcloud.com/fixture/example/", "mixcloud"},
		{"https://rumble.com/embed/v12345/", "rumble"},
		{"https://www.bilibili.com/video/BV1abcdefgh", "bilibili"},
		{"https://www.instagram.com/p/aye83DjauH/", "instagram"},
		{"https://kick.com/fixture-channel", "kick"},
		{"https://www.bbc.co.uk/iplayer/episode/p0000000/title", "bbciplayer"},
		{"https://www.ardmediathek.de/player/Y3JpZDovL2ZpeHR1cmU", "ard"},
		{"nrk:MDDP12000117", "nrk"},
		{"https://www.twitch.tv/fixture_channel", "twitch"},
		{"https://soundcloud.com/fixture-artist/synthetic-signal", "soundcloud"},
		{"https://streamable.com/e/fixture_1", "streamable"},
		{"peertube:peertube.example:00000000-0000-4000-8000-000000000001", "peertube"},
		{"https://archive.org/details/fixture_concert", "internetarchive"},
		{"https://www.svtplay.se/video/fixture-program?modalId=fixture123", "region_svt"},
		{"https://auth-fixture.invalid/watch/fixture123", "synthetic_auth"},
		{"https://example.com/media.mp4", "generic"},
	}
	registry := productRegistry()
	for _, test := range tests {
		selected, err := registry.Select(test.rawURL)
		if err != nil {
			t.Errorf("Select(%q): %v", test.rawURL, err)
			continue
		}
		if selected.Name() != test.name {
			t.Errorf("Select(%q) = %q, want %q", test.rawURL, selected.Name(), test.name)
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
		{downloader.ErrExternalUnavailable, ErrorUnsupported},
		{downloader.ErrExternalFailed, ErrorInternal},
		{hls.ErrUnsupportedEncryption, ErrorUnsupported},
		{hls.ErrInvalidPlaylist, ErrorInternal},
		{dash.ErrUnsupportedAddressing, ErrorUnsupported},
		{dash.ErrInvalidMPD, ErrorInternal},
		{ism.ErrInvalidManifest, ErrorInternal},
		{mediaformat.ErrNoMatch, ErrorInvalidInput},
		{mediaformat.ErrNoFormats, ErrorInternal},
	} {
		if err := categorized("media", test.err); !IsCategory(err, test.category) {
			t.Fatalf("categorized(%v) = %v, want %s", test.err, err, test.category)
		}
	}
}

func TestClientCategorizesMissingCookieFileAsInvalidInput(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-cookies.txt")
	_, err := NewClient().Run(context.Background(), Request{
		URL: "https://example.com/media.mp4", CookieFile: missing, SkipDownload: true,
	})
	if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, netscape.ErrFile) {
		t.Fatalf("error = %v", err)
	}
}

func TestClientCategorizesCookieDirectoryAsInvalidInput(t *testing.T) {
	_, err := NewClient().Run(context.Background(), Request{
		URL: "https://example.com/media.mp4", CookieFile: t.TempDir(), SkipDownload: true,
	})
	if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, netscape.ErrFile) {
		t.Fatalf("error = %v", err)
	}
}

func TestClientRejectsInvalidWaveTwoOptionsBeforeNetwork(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer server.Close()
	tests := []Request{
		{Downloader: DownloaderOptions{FragmentConcurrency: 129}},
		{Downloader: DownloaderOptions{RetryBaseDelay: 2 * time.Second, RetryMaxDelay: time.Second}},
		{Downloader: DownloaderOptions{External: &ExternalDownloader{Executable: "tool", Arguments: []string{"bad\nargument"}}}},
		{Postprocessors: []Postprocessor{{}}},
		{Postprocessors: []Postprocessor{{Move: &MovePostprocessor{Destination: "out.mp4"}, Remux: &RemuxPostprocessor{Destination: "out.mkv"}}}},
		{OutputDir: t.TempDir(), Postprocessors: []Postprocessor{{Move: &MovePostprocessor{Destination: "../escape.mp4"}}}},
	}
	for index, request := range tests {
		request.URL = server.URL + "/media.mp4"
		if _, err := NewClient().Run(context.Background(), request); !IsCategory(err, ErrorInvalidInput) {
			t.Errorf("case %d error = %v", index, err)
		}
	}
	if hits != 0 {
		t.Fatalf("preflight-invalid requests made %d network calls", hits)
	}
}

func TestClientAppliesMetadataBeforeMatchFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", "4")
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte("data"))
		}
	}))
	defer server.Close()
	var events []Event
	result, err := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	})).Run(context.Background(), Request{
		URL: server.URL + "/clip.mp4", SkipDownload: true,
		ReplaceMetadata: []string{"title:clip:renamed"}, MatchFilters: []string{"title=other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || !strings.Contains(result.SkipReason, "renamed") {
		t.Fatalf("result = %#v", result)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["title"] != "renamed" {
		t.Fatalf("metadata title = %#v", metadata["title"])
	}
	if events[len(events)-1].Kind != EventMatchFilterSkipped {
		t.Fatalf("events = %#v", events)
	}
}

func TestClientRendersProgressTemplateIntoEvents(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	var messages []string
	result, err := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventDownloadProgress {
			messages = append(messages, event.Message)
		}
		return nil
	})).Run(context.Background(), Request{
		URL: server.URL + "/one.mp4", OutputDir: t.TempDir(),
		ProgressTemplate: "%(status)s:%(downloaded_bytes)d",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || len(messages) == 0 || !strings.HasPrefix(messages[len(messages)-1], EventDownloadProgress+":") {
		t.Fatalf("result=%#v messages=%#v", result, messages)
	}
}

func TestOperationUsesRequestedFormatSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(strings.TrimPrefix(request.URL.Path, "/")))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	request := Request{OutputDir: t.TempDir(), Format: "low"}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("selection")},
		value.Field{Key: "title", Value: value.String("selection")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("high")}, value.Field{Key: "url", Value: value.String(server.URL + "/high")}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "height", Value: value.Int(1080)}, value.Field{Key: "vcodec", Value: value.String("avc")}, value.Field{Key: "acodec", Value: value.String("aac")})),
			value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("low")}, value.Field{Key: "url", Value: value.String(server.URL + "/low")}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "height", Value: value.Int(360)}, value.Field{Key: "vcodec", Value: value.String("avc")}, value.Field{Key: "acodec", Value: value.String("aac")})),
		)},
	))
	operation := &operation{client: NewClient(), request: request, transport: transport, compatibility: compatibility}
	result, err := operation.processMedia(context.Background(), info, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(result.Filename)
	if err != nil || string(contents) != "low" {
		t.Fatalf("selected contents = %q, error = %v", contents, err)
	}
}

func TestClientExtractAudioPostprocessorIntegration(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	root := t.TempDir()
	source := filepath.Join(fixtureRoot, "source.mp4")
	output, err := exec.Command(ffmpegPath, "-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=700:duration=0.2", "-c:a", "aac", source).CombinedOutput()
	if err != nil {
		t.Fatalf("generate fixture: %v: %s", err, output)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/mp4")
		http.ServeFile(writer, request, source)
	}))
	defer server.Close()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/source.mp4", OutputDir: root,
		Postprocessors: []Postprocessor{{ExtractAudio: &ExtractAudioPostprocessor{Codec: "mp3"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(result.Filename) != ".mp3" || len(result.Artifacts) != 1 || result.Artifacts[0].Kind != "media" {
		t.Fatalf("result = %#v", result)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(context.Background(), result.Filename)
	if err != nil || len(probe.Streams) == 0 || probe.Streams[0].CodecType != "audio" {
		t.Fatalf("probe = %#v, error = %v", probe, err)
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
		spec, browser, profile, container string
	}{
		{"chrome", "chrome", "", ""},
		{"chromium:Default", "chromium", "Default", ""},
		{"brave:Profile 1", "brave", "Profile 1", ""},
		{"firefox:work::Work", "firefox", "work", "Work"},
	} {
		options, err := parseBrowserCookieSpec(test.spec)
		if err != nil || options.browser != test.browser || options.profile != test.profile || options.container != test.container {
			t.Fatalf("parseBrowserCookieSpec(%q) = %#v, %v", test.spec, options, err)
		}
	}
	for _, spec := range []string{"safari", "chrome:", "chrome:../Default", "chrome:one:two", "chrome::Work", "firefox:default::", "firefox:default::one:two"} {
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
	client.platform = "darwin"
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

func TestClientLoadsNetscapeCookieFileBeforeExtraction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie("from_file")
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
	cookieFile := filepath.Join(t.TempDir(), "cookies.txt")
	line := target.Hostname() + "\tFALSE\t/\tFALSE\t0\tfrom_file\tpresent\n"
	if err := os.WriteFile(cookieFile, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var events []Event
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	}))
	result, err := client.Run(context.Background(), Request{URL: server.URL + "/media.mp4", CookieFile: cookieFile, SkipDownload: true})
	if err != nil || result.Extractor != "generic" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(events) < 3 || events[0].Kind != EventBrowserCookies || events[0].Message != "imported 1 of 1 cookie-file entries" {
		t.Fatalf("events = %#v", events)
	}
}

func TestPortableBrowserCookieDispatch(t *testing.T) {
	client := NewClient()
	client.firefoxCookieImporter = func(_ context.Context, options firefox.Options) (firefox.Result, error) {
		if options.Profile != "fixture" || options.Container != "Work" {
			t.Fatalf("Firefox options = %#v", options)
		}
		return firefox.Result{Total: 1, Imported: 1}, nil
	}
	result, err := client.importBrowserCookies(context.Background(), browserCookieSpec{browser: "firefox", profile: "fixture", container: "Work"})
	if err != nil || result.Imported != 1 {
		t.Fatalf("Firefox result=%#v err=%v", result, err)
	}
	client.platform = "linux"
	client.linuxCookieImporter = func(_ context.Context, options chromiumlinux.Options) (chromiumlinux.Result, error) {
		if options.Browser != chromiumlinux.Brave || options.Profile != "Profile 1" {
			t.Fatalf("Linux Chromium options = %#v", options)
		}
		return chromiumlinux.Result{Total: 2, Imported: 1, Failed: 1}, chromiumlinux.ErrKeyUnavailable
	}
	result, err = client.importBrowserCookies(context.Background(), browserCookieSpec{browser: "brave", profile: "Profile 1"})
	if !errors.Is(err, chromiumlinux.ErrKeyUnavailable) || result.Imported != 1 || result.Failed != 1 {
		t.Fatalf("Linux result=%#v err=%v", result, err)
	}
	client.platform = "windows"
	client.windowsCookieImporter = func(_ context.Context, options chromiumwindows.Options) (chromiumwindows.Result, error) {
		if options.Browser != chromiumwindows.Edge || options.Profile != "Profile 2" {
			t.Fatalf("Windows Chromium options = %#v", options)
		}
		return chromiumwindows.Result{Total: 3, Imported: 2, Failed: 1}, chromiumwindows.ErrAppBound
	}
	result, err = client.importBrowserCookies(context.Background(), browserCookieSpec{browser: "edge", profile: "Profile 2"})
	if !errors.Is(err, chromiumwindows.ErrAppBound) || result.Imported != 2 || result.Failed != 1 {
		t.Fatalf("Windows result=%#v err=%v", result, err)
	}
}

func TestClientWindowsCookiePartialAppBoundFailurePreservesCookies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie("windows_session")
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
	client := NewClient()
	client.platform = "windows"
	client.windowsCookieImporter = func(context.Context, chromiumwindows.Options) (chromiumwindows.Result, error) {
		return chromiumwindows.Result{
			Cookies: []*http.Cookie{{Name: "windows_session", Value: "present", Domain: target.Hostname(), Path: "/"}},
			Total:   2, Imported: 1, Failed: 1,
		}, chromiumwindows.ErrAppBound
	}
	result, err := client.Run(context.Background(), Request{URL: server.URL + "/protected.mp4", CookiesFromBrowser: "edge:Default", SkipDownload: true})
	if err != nil || result.Extractor != "generic" {
		t.Fatalf("result=%+v error=%v", result, err)
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
	client.platform = "darwin"
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
	client.platform = "darwin"
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

func TestClientDownloadArchiveRecordsAndSkips(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	archivePath := filepath.Join(root, "archive.txt")
	request := Request{URL: server.URL + "/page", OutputDir: root, DownloadArchive: archivePath}
	first, err := NewClient().Run(context.Background(), request)
	if err != nil || !first.Downloaded || first.Archived {
		t.Fatalf("first result=%#v err=%v", first, err)
	}
	data, err := os.ReadFile(archivePath)
	if err != nil || string(data) != "fixture fixture-direct\n" {
		t.Fatalf("archive=%q err=%v", data, err)
	}
	var events []Event
	second, err := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	})).Run(context.Background(), request)
	if err != nil || second.Downloaded || !second.Archived || second.Bytes != 0 {
		t.Fatalf("second result=%#v err=%v", second, err)
	}
	found := false
	for _, event := range events {
		if event.Kind == EventArchiveMatch && event.Message == "fixture fixture-direct" {
			found = true
		}
	}
	if !found {
		t.Fatalf("archive event missing: %#v", events)
	}
}

func TestClientInitializesConfiguredCacheSafely(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	result, err := NewClient().Run(context.Background(), Request{URL: server.URL + "/page", CacheDir: cacheRoot, SkipDownload: true})
	if err != nil || result.Extractor != "fixture" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	info, err := os.Lstat(cacheRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("cache root info=%v err=%v", info, err)
	}
	unsafe := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(cacheRoot, unsafe); err == nil {
		_, err = NewClient().Run(context.Background(), Request{URL: server.URL + "/page", CacheDir: unsafe, SkipDownload: true})
		if !IsCategory(err, ErrorInvalidInput) {
			t.Fatalf("unsafe cache error = %v", err)
		}
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

func TestClientISMDispatch(t *testing.T) {
	const manifest = `<SmoothStreamingMedia TimeScale="10" Duration="20"><StreamIndex Type="video" Url="video/QualityLevels({bitrate})/Fragments(video={start time})"><QualityLevel Bitrate="200" FourCC="H264"/><c t="0" d="10" r="1"/></StreamIndex></SmoothStreamingMedia>`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/Manifest" {
			writer.Header().Set("Content-Type", "application/vnd.ms-sstr+xml")
			writer.Header().Set("Content-Length", fmt.Sprint(len(manifest)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write([]byte(manifest))
			}
			return
		}
		_, _ = writer.Write([]byte(filepath.Base(request.URL.Path)))
	}))
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{URL: server.URL + "/Manifest", OutputDir: root})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(result.Filename)
	if err != nil || string(contents) != "Fragments(video=0)Fragments(video=10)" {
		t.Fatalf("ISM output = %q, error = %v", contents, err)
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

func FuzzConfinedPostprocessPath(f *testing.F) {
	f.Add("media.mp4")
	f.Add("nested/output.mkv")
	f.Add("../escape")
	f.Fuzz(func(t *testing.T, requested string) {
		root := t.TempDir()
		path, err := confinedPostprocessPath(root, requested)
		if err != nil {
			return
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("accepted escaping path %q as %q", requested, path)
		}
	})
}
