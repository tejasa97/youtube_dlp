package cli

import (
	"bytes"
	"strings"
	"testing"
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

func TestRunRequiresURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "Usage: ytdlp-go") {
		t.Fatalf("stderr does not contain usage: %q", stderr.String())
	}
}

func TestRunFoundationURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"https://example.invalid/video"}, &stdout, &stderr); code != 1 {
		t.Fatalf("Run() code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not implemented") {
		t.Fatalf("stderr does not explain status: %q", stderr.String())
	}
}
