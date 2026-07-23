package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if got := stdout.String(); got != "ytdlp-go "+Version+"\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestRunTelemetryJSONSuccessFailureAndConflict(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--telemetry-json", "--skip-download", server.URL + "/page"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("success code=%d stderr=%q", code, stderr.String())
	}
	snapshot, err := ytdlp.DecodeTelemetrySnapshot(context.Background(), bytes.NewReader(stdout.Bytes()), 0)
	if err != nil || len(snapshot.Counts) != 1 || snapshot.Counts[0].Extractor != "fixture" || snapshot.Counts[0].Outcome != ytdlp.TelemetryOutcomeSuccess {
		t.Fatalf("success snapshot=%+v error=%v", snapshot, err)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"--telemetry-json", "not-a-url"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("invalid URL succeeded: stdout=%q", stdout.String())
	}
	snapshot, err = ytdlp.DecodeTelemetrySnapshot(context.Background(), bytes.NewReader(stdout.Bytes()), 0)
	if err != nil || len(snapshot.Counts) != 1 || snapshot.Counts[0].Extractor != ytdlp.TelemetryUnknownExtractor || snapshot.Counts[0].Outcome != ytdlp.TelemetryOutcomeUnsupported {
		t.Fatalf("failure snapshot=%+v error=%v stderr=%q", snapshot, err, stderr.String())
	}
	if strings.Contains(stdout.String(), "not-a-url") {
		t.Fatalf("telemetry exposed input: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--telemetry-json", "--print-json", server.URL + "/page"}, &stdout, &stderr); code != 2 || stdout.Len() != 0 {
		t.Fatalf("conflict code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunExplicitImpersonationProfileAndFailClosedUnknown(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--impersonate", "firefox-120", "--skip-download", server.URL + "/page"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Firefox code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"--impersonate", "firefox-latest", "--skip-download", server.URL + "/page"}, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "impersonation profile unavailable") {
		t.Fatalf("unknown code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunNetRCFlagsFailClosedBeforeNetwork(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.netrc")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--netrc", "--netrc-location", missing, "https://auth-fixture.invalid/watch/auth-001"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "load netrc credentials") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "password") || strings.Contains(stderr.String(), "login") {
		t.Fatalf("credential diagnostic exposed fields: %q", stderr.String())
	}
}

func TestRunHelpIsCurrentAndSuccessful(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage: ytdlp-go") ||
		!strings.Contains(stderr.String(), "Experimental Python-free Go implementation") ||
		!strings.Contains(stderr.String(), "live-from-start") ||
		!strings.Contains(stderr.String(), "no-simulate") ||
		strings.Contains(stderr.String(), "Phase 2 alpha development") {
		t.Fatalf("help is stale: %q", stderr.String())
	}
}

func TestRunAcceptsLiveFromStartAndLastNegativeFlagWins(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	for _, arguments := range [][]string{
		{"--skip-download", "--live-from-start", server.URL + "/page"},
		{"--skip-download", "--live-from-start", "--no-live-from-start", server.URL + "/page"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(arguments, &stdout, &stderr); code != 0 {
			t.Fatalf("Run(%v) code=%d stderr=%q", arguments, code, stderr.String())
		}
	}
}

func TestRunResumesPartialDownload(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	destination := filepath.Join(root, "Deterministic Fixture.bin")
	media := server.Media()
	if err := os.WriteFile(destination+".part", media[:len(media)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	state := `{"url":"` + server.URL + `/media","etag":` + strconv.Quote(server.MediaETag()) + `,"total":` + strconv.Itoa(len(media)) + `}`
	if err := os.WriteFile(destination+".part.json", []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--output-dir", root, "--progress-json", server.URL + "/page"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d; stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), `"resuming":true`) {
		t.Fatalf("progress does not show resume: %s", stderr.String())
	}
}

func TestRunCancellationExitCode(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	code := RunContext(ctx, []string{"--skip-download", server.URL + "/slow?delay=1s"}, &stdout, &stderr)
	if code != 130 {
		t.Fatalf("code = %d, want 130; stderr = %s", code, stderr.String())
	}
}

func TestRunRequiresURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "Usage: ytdlp-go") {
		t.Fatalf("stderr does not contain usage: %q", stderr.String())
	}
}

func TestRunRejectsInvalidPlaylistRange(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--playlist-start", "4", "--playlist-end", "3", "https://example.invalid/video.mp4"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "invalid request options: playlist range") {
		t.Fatalf("code = %d; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestRunAcceptsLegacyUnboundedPlaylistEnd(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--playlist-end", "-1", "--skip-download", server.URL + "/page"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code = %d; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestRunAcceptsNoPlaylistReverseAfterInheritedReverse(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "yt-dlp.conf")
	if err := os.WriteFile(configPath, []byte("--playlist-reverse\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--config-location", configPath,
		"--no-playlist-reverse",
		"--skip-download",
		server.URL + "/page",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestRunPlaylistItemsAliasAndInvalidSpec(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"-I", "2,4", "--skip-download", server.URL + "/page"}, &stdout, &stderr); code != 0 {
		t.Fatalf("alias code = %d; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := Run([]string{"--playlist-items", "1::0", "--skip-download", server.URL + "/page"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "invalid playlist items") {
		t.Fatalf("invalid code = %d; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestRunAcceptsFlatPlaylistAndCommandLineDisable(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "yt-dlp.conf")
	if err := os.WriteFile(configPath, []byte("--flat-playlist\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--config-location", configPath,
		"--no-flat-playlist",
		"--skip-download",
		server.URL + "/page",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d; stdout = %q; stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestRunRejectsMalformedURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"not-a-url"}, &stdout, &stderr); code != 3 {
		t.Fatalf("Run() code = %d, want 3", code)
	}
	if !strings.Contains(stderr.String(), "unsupported") {
		t.Fatalf("stderr does not explain status: %q", stderr.String())
	}
}

func TestRunRejectsUnsupportedBrowserCookieSource(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--cookies-from-browser", "safari", "https://example.invalid/video.mp4"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "unsupported browser") {
		t.Fatalf("code = %d; stderr = %q", code, stderr.String())
	}
}

func TestRunLoadsExplicitConfigurationAndCommandLineWins(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	configDir := t.TempDir()
	configuredOutput := filepath.Join(configDir, "configured")
	commandOutput := filepath.Join(configDir, "command")
	configPath := filepath.Join(configDir, "yt-dlp.conf")
	configText := "--output configured.%(ext)s\n--output-dir '" + configuredOutput + "'\n"
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := RunContext(context.Background(), []string{
		"--config-location", configPath,
		"--output", "command.%(ext)s",
		"--output-dir", commandOutput,
		server.URL + "/page",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(commandOutput, "command.bin")); err != nil {
		t.Fatalf("command-line output did not win precedence: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configuredOutput, "configured.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lower-precedence configured output exists: %v", err)
	}
}

func TestRunReportsSourceLocatedConfigurationFailure(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "broken.conf")
	if err := os.WriteFile(configPath, []byte("--output 'unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := RunContext(context.Background(), []string{"--config-location", configPath, "https://example.invalid/video"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), filepath.Base(configPath)+":1:") || !strings.Contains(stderr.String(), "unterminated quote") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestHomePathFromArgs(t *testing.T) {
	if got := homePathFromArgs([]string{"-P", "home:first", "--paths=home:second"}); got != "second" {
		t.Fatalf("homePathFromArgs() = %q", got)
	}
}

func TestRunWalkingSkeletonAndJSONSeparation(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := RunContext(context.Background(), []string{
		"--output-dir", root, "--output", "%(title)s.%(ext)s", "--print-json", server.URL + "/page",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d; stderr = %s", code, stderr.String())
	}
	if !json.Valid(bytes.TrimSpace(stdout.Bytes())) {
		t.Fatalf("stdout is not isolated JSON: %q", stdout.String())
	}
	destination := filepath.Join(root, "Deterministic Fixture.bin")
	downloaded, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(downloaded) != string(server.Media()) {
		t.Fatal("downloaded media mismatch")
	}
	if !strings.Contains(stderr.String(), "Completed") {
		t.Fatalf("stderr lacks completion: %q", stderr.String())
	}
}

func TestRunWritesSelectedSubtitlesWhileSkippingMedia(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := RunContext(context.Background(), []string{
		"--output-dir", root,
		"--skip-download",
		"--write-subs",
		"--sub-langs", "es,fr",
		"--sub-format", "srt/vtt/best",
		server.URL + "/page",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, language := range []string{"es", "fr"} {
		path := filepath.Join(root, "Deterministic Fixture."+language+".vtt")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("subtitle %s: %v", language, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "Deterministic Fixture.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("media was downloaded: %v", err)
	}
}

func TestRunEmbedSubtitlesImplicitSelectionAndClear(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Run([]string{
		"--output-dir", root, "--skip-download", "--embed-subs", server.URL + "/page",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("embed code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "Deterministic Fixture.en.vtt")); err != nil {
		t.Fatalf("implicit manual subtitle: %v", err)
	}

	clearRoot := t.TempDir()
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{
		"--output-dir", clearRoot, "--skip-download", "--embed-subs", "--no-embed-subs", server.URL + "/page",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("clear code=%d stderr=%q", code, stderr.String())
	}
	entries, err := os.ReadDir(clearRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("--no-embed-subs left implicit downloads: %#v", entries)
	}
}

func TestRunEmbedsAutomaticSubtitleAndUsesPinnedRetentionRule(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	mediaPath := filepath.Join(fixtureRoot, "media.mp4")
	output, err := exec.Command(ffmpegPath,
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=32x32:d=0.3",
		"-c:v", "mpeg4", mediaPath,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("generate media: %v: %s", err, output)
	}
	media, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			_, _ = fmt.Fprintf(writer, `{
				"id":"cli-embed","title":"CLI Embed","ext":"mp4",
				"formats":[{"format_id":"media","url":%q,"ext":"mp4","vcodec":"mpeg4","acodec":"none"}],
				"subtitles":{"en":[{"url":%q,"ext":"vtt"}]},
				"automatic_captions":{"pt":[{"url":%q,"ext":"srt"}]}
			}`, server.URL+"/media.mp4", server.URL+"/en.vtt", server.URL+"/pt.srt")
		case "/media.mp4":
			writer.Header().Set("Content-Length", fmt.Sprint(len(media)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write(media)
			}
		case "/pt.srt":
			_, _ = writer.Write([]byte("1\n00:00:00,000 --> 00:00:00,200\nAutomatic\n"))
		case "/en.vtt":
			_, _ = writer.Write([]byte("WEBVTT\n\n00:00.000 --> 00:00.200\nManual\n"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Run([]string{
		"--quiet", "--output-dir", root, "--embed-subs", "--write-auto-subs",
		"--sub-langs", "pt", server.URL + "/page",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	outputPath := filepath.Join(root, "CLI Embed.mp4")
	tools, _ := ffmpeg.Discover(ffmpeg.Config{})
	probe, err := tools.Probe(context.Background(), outputPath)
	if err != nil {
		t.Fatal(err)
	}
	subtitles := 0
	for _, stream := range probe.Streams {
		if stream.CodecType == "subtitle" {
			subtitles++
		}
	}
	if subtitles != 1 {
		t.Fatalf("streams=%#v", probe.Streams)
	}
	if _, err := os.Stat(filepath.Join(root, "CLI Embed.pt.srt")); !os.IsNotExist(err) {
		t.Fatalf("automatic sidecar retained without --write-subs: %v", err)
	}

	manualRoot := t.TempDir()
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{
		"--quiet", "--output-dir", manualRoot, "--embed-subs", "--write-subs",
		"--sub-langs", "en", server.URL + "/page",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("manual code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(manualRoot, "CLI Embed.en.vtt")); err != nil {
		t.Fatalf("manual sidecar not retained with --write-subs: %v", err)
	}

	conversionRoot := t.TempDir()
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{
		"--quiet", "--skip-download", "--output-dir", conversionRoot,
		"--write-auto-subs", "--convert-subs", "vtt",
		"--sub-langs", "pt", server.URL + "/page",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("conversion code=%d stderr=%q", code, stderr.String())
	}
	if body, err := os.ReadFile(filepath.Join(conversionRoot, "CLI Embed.pt.vtt")); err != nil ||
		!strings.Contains(string(body), "WEBVTT") {
		t.Fatalf("converted sidecar=%q err=%v", body, err)
	}
	if _, err := os.Stat(filepath.Join(conversionRoot, "CLI Embed.pt.srt")); !os.IsNotExist(err) {
		t.Fatalf("source sidecar retained after conversion: %v", err)
	}
}

func TestRunListSubsSimulatesAndPreservesOutputChannels(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := RunContext(context.Background(), []string{
		"--list-subs", "--write-subs", "--write-auto-subs", "--output-dir", root, server.URL + "/page",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Language") || !strings.Contains(stdout.String(), "es") || !strings.Contains(stdout.String(), "en") {
		t.Fatalf("missing subtitle tables: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Available automatic captions") || !strings.Contains(stderr.String(), "Available subtitles") {
		t.Fatalf("status channel=%q", stderr.String())
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("--list-subs wrote files: %#v", entries)
	}

	var quietOut, quietErr bytes.Buffer
	if code := Run([]string{"--quiet", "--list-subs", "--skip-download", server.URL + "/page"}, &quietOut, &quietErr); code != 0 {
		t.Fatalf("quiet code=%d stderr=%q", code, quietErr.String())
	}
	if !strings.Contains(quietOut.String(), "Language") || !strings.Contains(quietErr.String(), "Available subtitles") || strings.Contains(quietErr.String(), "Extracting") {
		t.Fatalf("quiet stdout=%q stderr=%q", quietOut.String(), quietErr.String())
	}
}

func TestRunSimulationTriStateAndSkipDownloadRemainDistinct(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	mediaName := "Deterministic Fixture.bin"
	tests := []struct {
		name       string
		arguments  []string
		wantMedia  bool
		wantManual bool
	}{
		{
			name:      "explicit simulate",
			arguments: []string{"--simulate", "--write-subs"},
		},
		{
			name:      "short simulate alias wins last",
			arguments: []string{"--no-simulate", "-s"},
		},
		{
			name:      "explicit no-simulate wins last",
			arguments: []string{"--simulate", "--no-simulate"},
			wantMedia: true,
		},
		{
			name:      "false positive form disables simulation",
			arguments: []string{"--simulate=false"},
			wantMedia: true,
		},
		{
			name:      "negative false form enables simulation",
			arguments: []string{"--no-simulate=false"},
		},
		{
			name:       "skip download still writes related files",
			arguments:  []string{"--no-download", "--write-subs", "--sub-langs", "es"},
			wantManual: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			arguments := append([]string{"--output-dir", root}, test.arguments...)
			arguments = append(arguments, server.URL+"/page")
			var stdout, stderr bytes.Buffer
			if code := Run(arguments, &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			assertPathExists(t, filepath.Join(root, mediaName), test.wantMedia)
			assertPathExists(t, filepath.Join(root, "Deterministic Fixture.es.vtt"), test.wantManual)
		})
	}
}

func TestRunListSubsNoSimulateDownloadsAndHonorsSubtitleWrites(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--list-subs", "--no-simulate", "--write-subs", "--sub-langs", "es",
		"--output-dir", root, server.URL + "/page",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Language") {
		t.Fatalf("missing subtitle listing: %q", stdout.String())
	}
	assertPathExists(t, filepath.Join(root, "Deterministic Fixture.bin"), true)
	assertPathExists(t, filepath.Join(root, "Deterministic Fixture.es.vtt"), true)
}

func TestRunCommandLineNoSimulateOverridesInheritedSimulation(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "yt-dlp.conf")
	if err := os.WriteFile(configPath, []byte("--simulate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--config-location", configPath, "--list-subs", "--no-simulate",
		"--output-dir", root, server.URL + "/page",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	assertPathExists(t, filepath.Join(root, "Deterministic Fixture.bin"), true)
}

func assertPathExists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if want && err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if !want && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s unexpectedly exists: %v", path, err)
	}
}

func TestRunListSubsPrintJSONAndCancellation(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--list-subs", "--print-json", server.URL + "/page"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 || !json.Valid([]byte(lines[len(lines)-1])) {
		t.Fatalf("list/JSON output=%q", stdout.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout.Reset()
	stderr.Reset()
	if code := RunContext(ctx, []string{"--list-subs", server.URL + "/page"}, &stdout, &stderr); code != 130 {
		t.Fatalf("cancel code=%d stderr=%q", code, stderr.String())
	}
}

func TestSubtitleLanguageRules(t *testing.T) {
	if got := strings.Join(subtitleLanguageRules([]string{" en, ,fr ", "-en"}, false), ","); got != "en,fr,-en" {
		t.Fatalf("rules = %q", got)
	}
	if got := strings.Join(subtitleLanguageRules([]string{"en"}, true), ","); got != "all" {
		t.Fatalf("all rules = %q", got)
	}
}

func TestRunConvertSubtitleAliasesAndValidation(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	for _, flag := range []string{"--convert-subs", "--convert-sub", "--convert-subtitles"} {
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"--skip-download", flag, "vtt", server.URL + "/page"}, &stdout, &stderr); code != 0 {
			t.Fatalf("%s: code=%d stderr=%q", flag, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--convert-subs", "mov_text", server.URL + "/page"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "unsupported subtitle format") {
		t.Fatalf("invalid format: code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunTemplateFailure(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--output", "../escape.%(ext)s", server.URL + "/page"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2; stderr = %s", code, stderr.String())
	}
}

func TestRunProgressJSONStaysOnStderr(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--output-dir", t.TempDir(), "--print-json", "--progress-json", server.URL + "/page"}, &stdout, &stderr)
	if code != 0 || !json.Valid(bytes.TrimSpace(stdout.Bytes())) {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("progress line is not JSON: %q", line)
		}
	}
}

func TestRunHLSProtocolDispatch(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--quiet", "--output-dir", root, server.URL + "/hls/master.m3u8"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	contents, err := os.ReadFile(filepath.Join(root, "master.mp4"))
	if err != nil || string(contents) != string(server.HLSMedia()) {
		t.Fatalf("HLS output = %q, error = %v", contents, err)
	}
}

func TestRunWaveTwoCompatibilityFlags(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--skip-download", "--print-json", "-f", "best", "-S", "size",
		"--replace-in-metadata", "title:Deterministic:Native",
		"--match-filter", "title=discarded",
		"--no-match-filters",
		"--match-filters", "title~=Native",
		server.URL + "/page",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d; stderr = %s", code, stderr.String())
	}
	var metadata map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["title"] != "Native Fixture" {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestRunRejectsInvalidWaveTwoLimits(t *testing.T) {
	for _, arguments := range [][]string{
		{"--limit-rate", "invalid", "https://example.invalid/video"},
		{"--concurrent-fragments", "129", "https://example.invalid/video"},
		{"--retry-base-delay", "2s", "--retry-max-delay", "1s", "https://example.invalid/video"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(arguments, &stdout, &stderr); code != 2 {
			t.Errorf("Run(%q) code = %d; stderr = %q", arguments, code, stderr.String())
		}
	}
}

func TestParseYouTubeCommentLimits(t *testing.T) {
	options, err := parseYouTubeCommentLimits("100,20,30,4,3")
	if err != nil {
		t.Fatal(err)
	}
	if options.MaxComments != 100 || options.MaxParents != 20 || options.MaxReplies != 30 ||
		options.MaxRepliesPerThread != 4 || options.MaxDepth != 3 {
		t.Fatalf("options = %#v", options)
	}
	for _, input := range []string{",", "1,", "one", "-1", "10001", "1,2,3,4,9", "1,2,3,4,5,6"} {
		if _, err := parseYouTubeCommentLimits(input); err == nil {
			t.Errorf("parseYouTubeCommentLimits(%q) error = nil", input)
		}
	}
}

func TestRunAcceptsYouTubeCommentAliasesAndClears(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	for _, arguments := range [][]string{
		{"--skip-download", "--get-comments", "--youtube-max-comments", "10,2,3,1,2", "--youtube-comment-sort", "top", server.URL + "/page"},
		{"--skip-download", "--write-comments", "--no-write-comments", server.URL + "/page"},
		{"--skip-download", "--get-comments", "--no-get-comments", server.URL + "/page"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(arguments, &stdout, &stderr); code != 0 {
			t.Fatalf("Run(%q) code=%d stderr=%q", arguments, code, stderr.String())
		}
	}
}

func TestByteSizeFlagSuffixes(t *testing.T) {
	var value byteSizeFlag
	for input, expected := range map[string]int64{"1": 1, "2K": 2 << 10, "3m": 3 << 20, "4G": 4 << 30} {
		if err := value.Set(input); err != nil || int64(value) != expected {
			t.Fatalf("Set(%q) = %d, %v", input, value, err)
		}
	}
}

func TestSecurityErrorExitCode(t *testing.T) {
	err := &ytdlp.Error{Category: ytdlp.ErrorSecurity, Op: "verify", Err: errors.New("rejected")}
	if code := exitCode(err); code != 6 {
		t.Fatalf("exitCode() = %d, want 6", code)
	}
}
