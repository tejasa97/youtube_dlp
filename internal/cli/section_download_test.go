package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunDownloadSectionsValidationIgnoresNetwork(t *testing.T) {
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
	// Unsupported / malformed section syntax must be rejected before any
	// network traffic.
	for _, bad := range []string{"10:15", "*10:15+15:00", "*only-end"} {
		code, stderr := run("--skip-download", "--download-sections", bad, server.URL+"/video.mp4")
		if code != 2 || !strings.Contains(stderr, "download sections") {
			t.Fatalf("bad section %q code=%d stderr=%q", bad, code, stderr)
		}
	}
	if hits.Load() != before {
		t.Fatalf("invalid sections made %d requests", hits.Load()-before)
	}

	// A valid bounded range with --skip-download must parse and reach the
	// (bypassed) download without error.
	code, stderr := run("--skip-download", "--download-sections", "*0-1", server.URL+"/video.mp4")
	if code != 0 {
		t.Fatalf("valid range code=%d stderr=%q", code, stderr)
	}
}

func TestRunDownloadSectionsForceKeyframesRequiresSections(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunContextIO(context.Background(), []string{
		"--skip-download", "--force-keyframes-at-cuts", "https://example.com/video.mp4",
	}, strings.NewReader(""), &stdout, &stderr)
	// force-keyframes without any removal/section consumer must error.
	if code != 2 || !strings.Contains(stderr.String(), "force-keyframes-at-cuts") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
