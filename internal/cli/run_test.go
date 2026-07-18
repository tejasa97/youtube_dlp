package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
		"--match-filter", "title~=Native", server.URL + "/page",
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
