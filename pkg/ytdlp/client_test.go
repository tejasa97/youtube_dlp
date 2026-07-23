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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/archive"
	"github.com/ytdlp-go/ytdlp/internal/compat/matchfilter"
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
	"github.com/ytdlp-go/ytdlp/internal/protocol/youtubelive"
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
		{extractor.ErrYouTubeSearchRateLimited, ErrorNetwork},
		{extractor.ErrYouTubeSearchNetwork, ErrorNetwork},
		{extractor.ErrYouTubeHandleTabRateLimited, ErrorNetwork},
		{extractor.ErrYouTubeHandleTabNetwork, ErrorNetwork},
		{extractor.ErrYouTubeMusicSearchRateLimited, ErrorNetwork},
		{extractor.ErrYouTubeMusicSearchNetwork, ErrorNetwork},
		{extractor.ErrYouTubeCommentsRateLimited, ErrorNetwork},
		{extractor.ErrYouTubeCommentsNetwork, ErrorNetwork},
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
		{"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos", "youtube_channel_tab"},
		{"https://www.youtube.com/@synthetic-handle/videos", "youtube_handle_tab"},
		{"https://www.youtube.com/user/SyntheticAlias/videos", "youtube_alias_tab"},
		{"https://www.youtube.com/c/СинтетическийКанал/playlists", "youtube_alias_tab"},
		{"ytsearch5:fixture query", "youtube_search"},
		{"https://music.youtube.com/search?q=fixture#songs", "youtube_music_search"},
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
		{"scsearch3:fixture query", "soundcloud_search"},
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
		{YouTubeComments: YouTubeCommentOptions{Sort: "popular"}},
		{YouTubeComments: YouTubeCommentOptions{MaxComments: 10_001}},
		{YouTubeComments: YouTubeCommentOptions{MaxDepth: 9}},
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

type numericMetadataExtractor struct{}

func (numericMetadataExtractor) Name() string           { return "numeric-metadata" }
func (numericMetadataExtractor) Suitable(*url.URL) bool { return true }
func (numericMetadataExtractor) Extract(context.Context, extractor.Request) (extractor.Extraction, error) {
	return extractor.Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture")},
		value.Field{Key: "title", Value: value.String("Fixture")},
		value.Field{Key: "duration", Value: value.Int(4)},
	))), nil
}

type deferredMetadataExtractor struct {
	calls *int
}

func (deferredMetadataExtractor) Name() string           { return "deferred-metadata" }
func (deferredMetadataExtractor) Suitable(*url.URL) bool { return true }
func (fixture deferredMetadataExtractor) Extract(context.Context, extractor.Request) (extractor.Extraction, error) {
	extraction := extractor.Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("deferred")},
		value.Field{Key: "title", Value: value.String("Fixture")},
		value.Field{Key: "url", Value: value.String("https://fixture.invalid/media.mp4")},
	)))
	extraction.Enrich = func(_ context.Context, info *value.Info) error {
		*fixture.calls++
		info.Set("comments", value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "id", Value: value.String("comment")},
		))))
		info.Set("comment_count", value.Int(1))
		return nil
	}
	return extraction, nil
}

func TestClientDefersExpensiveMetadataUntilAfterMatchFilter(t *testing.T) {
	for _, test := range []struct {
		filter    string
		wantCalls int
		wantSkip  bool
	}{
		{filter: "title=discarded", wantCalls: 0, wantSkip: true},
		{filter: "title=Fixture", wantCalls: 1},
	} {
		calls := 0
		request := Request{URL: "https://fixture.invalid/video", SkipDownload: true, MatchFilters: []string{test.filter}}
		compatibility, err := prepareCompatibility(request)
		if err != nil {
			t.Fatal(err)
		}
		operation := &operation{
			client: NewClient(), request: request,
			registry:      extractor.NewRegistry(deferredMetadataExtractor{calls: &calls}),
			compatibility: compatibility,
		}
		result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
		if err != nil {
			t.Fatal(err)
		}
		if calls != test.wantCalls || result.Skipped != test.wantSkip {
			t.Fatalf("filter=%q calls=%d skipped=%v", test.filter, calls, result.Skipped)
		}
		var metadata map[string]any
		if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
			t.Fatal(err)
		}
		_, hasComments := metadata["comments"]
		if hasComments != (test.wantCalls == 1) {
			t.Fatalf("filter=%q metadata=%#v", test.filter, metadata)
		}
	}
}

func TestClientDoesNotEnrichArchivedMedia(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.txt")
	if err := os.WriteFile(path, []byte("deferred-metadata deferred\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := archive.Open(context.Background(), path, archive.Options{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	request := Request{URL: "https://fixture.invalid/video", SkipDownload: true}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request,
		registry:      extractor.NewRegistry(deferredMetadataExtractor{calls: &calls}),
		compatibility: compatibility, archive: store,
	}
	result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || !result.Archived {
		t.Fatalf("calls=%d result=%#v", calls, result)
	}
}

func TestClientCategorizesMatchFilterEvaluationFailure(t *testing.T) {
	request := Request{URL: "https://fixture.invalid/video", SkipDownload: true, MatchFilters: []string{"duration *= 4"}}
	plan, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request, registry: extractor.NewRegistry(numericMetadataExtractor{}),
		compatibility: plan,
	}
	_, err = operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err == nil {
		t.Fatal("process() error = nil")
	}
	if !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("process() category = %v, want %v", err, ErrorInvalidInput)
	}
	if !errors.Is(err, matchfilter.ErrEvaluation) {
		t.Fatalf("process() error = %v, want matchfilter.ErrEvaluation", err)
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
	result, err := operation.processMedia(context.Background(), extractor.Media(info), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(result.Filename)
	if err != nil || string(contents) != "low" {
		t.Fatalf("selected contents = %q, error = %v", contents, err)
	}
}

func TestOperationPostLivePreferenceKeepsExplicitDirectFormatAuthoritative(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/incomplete" || request.URL.Query().Get("pot") != "fixture" {
			http.Error(writer, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = writer.Write([]byte("explicit-direct"))
	}))
	defer server.Close()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("post-live-selection")},
		value.Field{Key: "title", Value: value.String("post-live-selection")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("137")},
				value.Field{Key: "url", Value: value.String(server.URL + "/video")},
				value.Field{Key: "ext", Value: value.String("mp4")},
				value.Field{Key: "height", Value: value.Int(720)},
				value.Field{Key: "vcodec", Value: value.String("avc")},
				value.Field{Key: "acodec", Value: value.String("none")},
				value.Field{Key: "_youtube_post_live", Value: value.Bool(true)},
				value.Field{Key: "target_duration", Value: value.Float(5)},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("140")},
				value.Field{Key: "url", Value: value.String(server.URL + "/audio")},
				value.Field{Key: "ext", Value: value.String("m4a")},
				value.Field{Key: "vcodec", Value: value.String("none")},
				value.Field{Key: "acodec", Value: value.String("aac")},
				value.Field{Key: "_youtube_post_live", Value: value.Bool(true)},
				value.Field{Key: "target_duration", Value: value.Float(5)},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("18")},
				value.Field{Key: "url", Value: value.String(server.URL + "/incomplete?pot=fixture")},
				value.Field{Key: "ext", Value: value.String("mp4")},
				value.Field{Key: "height", Value: value.Int(2160)},
				value.Field{Key: "vcodec", Value: value.String("avc")},
				value.Field{Key: "acodec", Value: value.String("aac")},
				value.Field{Key: "preference", Value: value.Int(-10)},
			)),
		)},
	))
	defaultOperation := &operation{compatibility: compatibilityPlan{}}
	selected, err := defaultOperation.selectFormats(info)
	if err != nil || len(selected) != 2 || selected[0].ID != "137" || selected[1].ID != "140" {
		t.Fatalf("default selected = %#v, %v", selected, err)
	}
	sortedCompatibility, err := prepareCompatibility(Request{FormatSort: []string{"height"}})
	if err != nil {
		t.Fatal(err)
	}
	sortedOperation := &operation{compatibility: sortedCompatibility}
	sorted, err := sortedOperation.selectFormats(info)
	if err != nil || len(sorted) != 2 || sorted[0].ID != "137" || sorted[1].ID != "140" {
		t.Fatalf("height-sorted selected = %#v, %v", sorted, err)
	}

	request := Request{OutputDir: t.TempDir(), Format: "18"}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	explicitOperation := &operation{
		client: NewClient(), request: request, transport: transport, compatibility: compatibility,
	}
	explicit, err := explicitOperation.selectFormats(info)
	if err != nil || len(explicit) != 1 || explicit[0].ID != "18" || explicit[0].YouTubePostLive {
		t.Fatalf("explicit selected = %#v, %v", explicit, err)
	}
	result, err := explicitOperation.processMedia(context.Background(), extractor.Media(info), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(result.Filename)
	if err != nil || string(body) != "explicit-direct" {
		t.Fatalf("explicit body = %q, %v", body, err)
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

func TestYouTubePostLiveAdaptiveTracksDownloadAndMerge(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	videoPath := filepath.Join(fixtureRoot, "video.mp4")
	audioPath := filepath.Join(fixtureRoot, "audio.m4a")
	generate := func(arguments ...string) {
		output, err := exec.Command(ffmpegPath, arguments...).CombinedOutput()
		if err != nil {
			t.Fatalf("generate fixture: %v: %s", err, output)
		}
	}
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "color=c=blue:s=16x16:d=0.3", "-an", "-c:v", "mpeg4", videoPath)
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=880:duration=0.3", "-vn", "-c:a", "aac", audioPath)
	readChunks := func(path string) [][]byte {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		chunks := make([][]byte, 4)
		for index := range chunks {
			start := len(body) * index / len(chunks)
			end := len(body) * (index + 1) / len(chunks)
			chunks[index] = body[start:end]
		}
		return chunks
	}
	chunks := map[string][][]byte{
		"/video": readChunks(videoPath),
		"/audio": readChunks(audioPath),
	}
	var requestMu sync.Mutex
	requested := map[string][]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Live-Fixture") != "post-live" ||
			(request.URL.Query().Get("token") != "video" && request.URL.Query().Get("token") != "audio") {
			http.Error(writer, "missing signed request context", http.StatusForbidden)
			return
		}
		sequenceText := request.URL.Query().Get("sq")
		if sequenceText == "" {
			writer.Header().Set("X-Head-Seqnum", "5")
			return
		}
		sequence, err := strconv.Atoi(sequenceText)
		if err != nil || sequence < 0 || sequence >= len(chunks[request.URL.Path]) {
			http.Error(writer, "bad sequence", http.StatusBadRequest)
			return
		}
		requestMu.Lock()
		requested[request.URL.Path] = append(requested[request.URL.Path], sequence)
		requestMu.Unlock()
		_, _ = writer.Write(chunks[request.URL.Path][sequence])
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	operation := &operation{
		client: NewClient(), transport: transport,
		request: Request{OutputDir: root, Downloader: DownloaderOptions{
			MaxSegments: 16, MaxSegmentBytes: 8 << 20, FragmentConcurrency: 2,
		}},
	}
	headers := http.Header{"X-Live-Fixture": []string{"post-live"}}
	selections := []mediaformat.Selection{
		{
			ID: "137", URL: server.URL + "/video?token=video", Ext: "mp4",
			Protocol: "http_dash_segments", VCodec: "mpeg4", ACodec: "none",
			Headers: headers, YouTubePostLive: true, TargetDuration: 5,
			LiveStartTimestamp: time.Now().Unix(),
		},
		{
			ID: "140", URL: server.URL + "/audio?token=audio", Ext: "m4a",
			Protocol: "http_dash_segments", VCodec: "none", ACodec: "aac",
			Headers: headers, YouTubePostLive: true, TargetDuration: 5,
			LiveStartTimestamp: time.Now().Unix(),
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	destination := filepath.Join(root, "post-live.mp4")
	path, bytes, err := operation.downloadSelections(ctx, selections, root, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != destination || bytes <= 0 {
		t.Fatalf("path=%q bytes=%d", path, bytes)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, stream := range probe.Streams {
		types[stream.CodecType] = true
	}
	if !types["video"] || !types["audio"] {
		t.Fatalf("streams=%#v", probe.Streams)
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	for _, path := range []string{"/video", "/audio"} {
		sort.Ints(requested[path])
		if got := fmt.Sprint(requested[path]); got != "[0 1 2 3]" {
			t.Fatalf("%s sequences=%s", path, got)
		}
	}
}

func TestYouTubeLiveFromStartDownloadsTracksConcurrentlyAndMerges(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	videoPath := filepath.Join(fixtureRoot, "video.mp4")
	audioPath := filepath.Join(fixtureRoot, "audio.m4a")
	generate := func(arguments ...string) {
		output, err := exec.Command(ffmpegPath, arguments...).CombinedOutput()
		if err != nil {
			t.Fatalf("generate fixture: %v: %s", err, output)
		}
	}
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "color=c=green:s=16x16:d=0.3", "-an", "-c:v", "mpeg4", videoPath)
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", "-vn", "-c:a", "aac", audioPath)
	chunk := func(path string) [][]byte {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result := make([][]byte, 4)
		for index := range result {
			result[index] = body[len(body)*index/4 : len(body)*(index+1)/4]
		}
		return result
	}
	chunks := map[string][][]byte{"/video": chunk(videoPath), "/audio": chunk(audioPath)}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sequenceText := request.URL.Query().Get("sq")
		if sequenceText == "" {
			writer.Header().Set("X-Head-Seqnum", "3")
			return
		}
		sequence, parseErr := strconv.Atoi(sequenceText)
		if parseErr != nil || sequence < 0 || sequence >= 4 {
			http.Error(writer, "bad sequence", http.StatusBadRequest)
			return
		}
		_, _ = writer.Write(chunks[request.URL.Path][sequence])
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	bothRefreshing := make(chan struct{})
	operation := &operation{
		client: NewClient(), transport: transport,
		request: Request{OutputDir: t.TempDir(), Downloader: DownloaderOptions{
			MaxSegments: 16, MaxSegmentBytes: 8 << 20, Attempts: 1,
			LivePollInterval: time.Millisecond, LiveRefreshInterval: time.Nanosecond,
			LiveMaxPolls: 4, LiveMaxNoProgressPolls: 2,
		}},
	}
	operation.youtubeLiveRefresh = func(selection mediaformat.Selection) youtubelive.LiveRefreshFunc {
		return func(ctx context.Context, request youtubelive.LiveRefreshRequest) (youtubelive.LiveRefreshResult, error) {
			if refreshes.Add(1) == 2 {
				close(bothRefreshing)
			}
			select {
			case <-bothRefreshing:
			case <-ctx.Done():
				return youtubelive.LiveRefreshResult{}, ctx.Err()
			}
			return youtubelive.LiveRefreshResult{
				URL: request.URL, Headers: request.Headers, StillLive: false,
			}, nil
		}
	}
	selections := []mediaformat.Selection{
		{
			ID: "137", URL: server.URL + "/video?pot=video", Ext: "mp4",
			VCodec: "mpeg4", ACodec: "none", YouTubeLiveFromStart: true, TargetDuration: 5,
		},
		{
			ID: "140", URL: server.URL + "/audio?pot=audio", Ext: "m4a",
			VCodec: "none", ACodec: "aac", YouTubeLiveFromStart: true, TargetDuration: 5,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := operation.request.OutputDir
	destination := filepath.Join(root, "live-from-start.mp4")
	path, bytes, err := operation.downloadSelections(ctx, selections, root, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != destination || bytes <= 0 || refreshes.Load() != 2 {
		t.Fatalf("path=%q bytes=%d refreshes=%d", path, bytes, refreshes.Load())
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, stream := range probe.Streams {
		types[stream.CodecType] = true
	}
	if !types["video"] || !types["audio"] {
		t.Fatalf("streams=%#v", probe.Streams)
	}
}

func TestYouTubePostLiveRejectsExternalDownloaderAndCategorizesFailures(t *testing.T) {
	op := &operation{request: Request{Downloader: DownloaderOptions{External: &ExternalDownloader{
		Executable: "unused",
	}}}}
	_, _, err := op.downloadSelection(context.Background(), mediaformat.Selection{
		URL: "https://media.example/video", YouTubePostLive: true, TargetDuration: 5,
	}, t.TempDir(), filepath.Join(t.TempDir(), "out"), nil)
	if !errors.Is(err, extractor.ErrUnsupported) {
		t.Fatalf("external error=%v", err)
	}
	_, _, err = op.downloadSelection(context.Background(), mediaformat.Selection{
		URL: "https://media.example/video", YouTubeLiveFromStart: true, TargetDuration: 5,
	}, t.TempDir(), filepath.Join(t.TempDir(), "out"), nil)
	if !errors.Is(err, extractor.ErrUnsupported) {
		t.Fatalf("live external error=%v", err)
	}
	for _, test := range []struct {
		err      error
		category ErrorCategory
	}{
		{youtubelive.ErrInvalidConfig, ErrorInvalidInput},
		{youtubelive.ErrOutputExists, ErrorInvalidInput},
		{youtubelive.ErrHeadSequence, ErrorInternal},
		{youtubelive.ErrDownloadFailed, ErrorInternal},
		{youtubelive.ErrEventSink, ErrorInternal},
		{youtubelive.ErrLiveInvalidConfig, ErrorInvalidInput},
		{youtubelive.ErrLiveHeadSequence, ErrorInternal},
		{youtubelive.ErrLiveNoProgress, ErrorInternal},
		{youtubelive.ErrLivePollLimit, ErrorInternal},
		{youtubelive.ErrLiveProbeFailed, ErrorNetwork},
		{youtubelive.ErrLiveRefreshFailed, ErrorNetwork},
		{youtubelive.ErrProbeFailed, ErrorNetwork},
	} {
		if got := categorized("post-live", test.err); !IsCategory(got, test.category) {
			t.Fatalf("categorized(%v)=%v", test.err, got)
		}
	}

	var networkCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		networkCalls.Add(1)
	}))
	defer server.Close()
	transport, transportErr := network.New(network.Config{})
	if transportErr != nil {
		t.Fatal(transportErr)
	}
	observerFailure := errors.New("observer failed")
	sinkOp := &operation{
		client: NewClient(WithEventHandler(func(_ context.Context, event Event) error {
			if event.Kind == EventDownloadStarting {
				return observerFailure
			}
			return nil
		})),
		transport: transport,
		request:   Request{},
	}
	root := t.TempDir()
	_, _, err = sinkOp.downloadSelection(context.Background(), mediaformat.Selection{
		URL: server.URL + "/video?pot=secret", YouTubePostLive: true, TargetDuration: 5,
	}, root, filepath.Join(root, "out"), sinkOp.eventSink())
	if !errors.Is(err, youtubelive.ErrEventSink) || !errors.Is(err, observerFailure) {
		t.Fatalf("operation sink error = %v", err)
	}
	if got := categorized("post-live", err); !IsCategory(got, ErrorInternal) {
		t.Fatalf("categorized operation sink error = %v", got)
	}
	if networkCalls.Load() != 0 {
		t.Fatalf("network calls = %d", networkCalls.Load())
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

type URLResultFixtureExtractor struct{}

func (URLResultFixtureExtractor) Name() string { return "url-result-fixture" }
func (URLResultFixtureExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Path == "/handoff"
}
func (URLResultFixtureExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return extractor.Extraction{}, err
	}
	parsed.Path = "/one.mp4"
	return extractor.URLResult(extractor.Entry{URL: parsed.String(), ExtractorKey: "generic", Transparent: true})
}

func TestOperationURLResultBypassesPlaylistControls(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	operation := &operation{
		client: NewClient(),
		request: Request{
			SkipDownload: true,
			Playlist:     PlaylistOptions{Flat: true, Items: "2", Start: 4, End: 4, Reverse: true},
		},
		transport: transport,
		registry:  extractor.NewRegistry(URLResultFixtureExtractor{}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), server.URL+"/handoff", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Extractor != "generic" || len(result.Entries) != 0 || result.Skipped {
		t.Fatalf("result = %#v", result)
	}
}

type cyclicURLResultExtractor struct{}

func (cyclicURLResultExtractor) Name() string { return "url-cycle" }
func (cyclicURLResultExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Host == "cycle.invalid"
}
func (cyclicURLResultExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	return extractor.URLResult(extractor.Entry{URL: request.URL, ExtractorKey: "url-cycle"})
}

func TestOperationRejectsURLResultCycle(t *testing.T) {
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true},
		registry: extractor.NewRegistry(cyclicURLResultExtractor{}),
	}
	_, err := operation.process(context.Background(), "https://cycle.invalid/video", "", nil, make(map[string]bool), 0)
	if !errors.Is(err, extractor.ErrPlaylistLimit) {
		t.Fatalf("cycle error = %v", err)
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

// TestClientConcurrentRunAndClose is a basic concurrency smoke test verifying
// that concurrent Run and Close calls do not panic. It uses the generic
// fixture extractor (no JavaScript helper). The helper-backed active-solve
// drain test is TestSupervisorConcurrentExecuteAndCloseDrainsActiveSolves in
// the supervisor package, which exercises real JavaScript execution, asserts
// operation results, and verifies helper process cleanup.
func TestClientConcurrentRunAndClose(t *testing.T) {
	server := testserver.New()
	defer server.Close()

	for iteration := 0; iteration < 5; iteration++ {
		client := NewClient()
		var wg sync.WaitGroup
		// Launch concurrent Run calls.
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = client.Run(context.Background(), Request{URL: server.URL + "/page", SkipDownload: true})
			}()
		}
		// Concurrently close while runs may be in flight.
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Millisecond)
			client.Close()
		}()
		wg.Wait()
		// After Close, subsequent Run calls should still work (lazy re-creation)
		// or fail gracefully—no panics.
		_, _ = client.Run(context.Background(), Request{URL: server.URL + "/page", SkipDownload: true})
		client.Close()
	}
}
