package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunChapterRemovalValidationAndResetOrdering(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", "4")
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte("data"))
		}
	}))
	defer server.Close()

	run := func(arguments ...string) (int, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := RunContextIO(context.Background(), arguments, strings.NewReader(""), &stdout, &stderr)
		return code, stderr.String()
	}

	before := hits.Load()
	code, stderr := run("--skip-download", "--remove-chapters", "(", server.URL+"/video.mp4")
	if code != 2 || !strings.Contains(stderr, "invalid_input") {
		t.Fatalf("invalid regex code=%d stderr=%q", code, stderr)
	}
	if hits.Load() != before {
		t.Fatalf("invalid regex made %d requests", hits.Load()-before)
	}

	code, stderr = run(
		"--skip-download",
		"--remove-chapters", "(",
		"--no-remove-chapters",
		server.URL+"/video.mp4",
	)
	if code != 0 {
		t.Fatalf("reset code=%d stderr=%q", code, stderr)
	}

	before = hits.Load()
	code, stderr = run(
		"--skip-download",
		"--no-remove-chapters",
		"--remove-chapters", "(",
		server.URL+"/video.mp4",
	)
	if code != 2 || !strings.Contains(stderr, "invalid_input") {
		t.Fatalf("later regex code=%d stderr=%q", code, stderr)
	}
	if hits.Load() != before {
		t.Fatalf("later invalid regex made %d requests", hits.Load()-before)
	}
}

func TestRunForceKeyframesAcceptsChapterRemovalAndRejectsNoCuts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", "4")
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte("data"))
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := RunContextIO(
		context.Background(),
		[]string{"--skip-download", "--remove-chapters", "*0-1", "--force-keyframes-at-cuts", server.URL + "/video.mp4"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("chapter force code=%d stderr=%q", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = RunContextIO(
		context.Background(),
		[]string{"--force-keyframes-at-cuts", server.URL + "/video.mp4"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 2 || !strings.Contains(stderr.String(), "requires --remove-chapters or --sponsorblock-remove") {
		t.Fatalf("missing cuts code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunNoSponsorBlockPreservesIndependentChapterCutForceFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", "4")
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte("data"))
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := RunContextIO(
		context.Background(),
		[]string{
			"--skip-download",
			"--sponsorblock-remove", "sponsor",
			"--remove-chapters", "*0-1",
			"--force-keyframes-at-cuts",
			"--no-sponsorblock",
			server.URL + "/video.mp4",
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunChapterRemovalConfigAndCommandLineResetPrecedence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", "4")
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte("data"))
		}
	}))
	defer server.Close()
	run := func(config string, commandLine ...string) (int, string) {
		t.Helper()
		configPath := filepath.Join(t.TempDir(), "yt-dlp.conf")
		if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		args := []string{"--config-location", configPath}
		args = append(args, commandLine...)
		args = append(args, "--skip-download", server.URL+"/video.mp4")
		var stdout, stderr bytes.Buffer
		code := RunContextIO(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
		return code, stderr.String()
	}
	if code, stderr := run("--remove-chapters '('\n", "--no-remove-chapters"); code != 0 {
		t.Fatalf("CLI reset did not clear config rule: code=%d stderr=%q", code, stderr)
	}
	if code, stderr := run("--no-remove-chapters\n", "--remove-chapters", "("); code != 2 ||
		!strings.Contains(stderr, "invalid_input") {
		t.Fatalf("CLI rule did not override config reset: code=%d stderr=%q", code, stderr)
	}
}
