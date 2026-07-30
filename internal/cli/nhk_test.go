package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunNHKAreaFlagRecognized(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunContextIO(
		context.Background(),
		[]string{"--nhk-area", "sapporo", "not-a-url"},
		strings.NewReader(""),
		&stdout, &stderr,
	)
	if code != 3 {
		t.Fatalf("Run() code = %d, want 3; stderr=%q", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("--nhk-area not recognized: %q", stderr.String())
	}
}

func TestRunNHKAreaRejectsHostileValue(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunContextIO(
		context.Background(),
		[]string{"--nhk-area", "tokyo/../evil", "https://www.nhk.or.jp/radio/player/?ch=r1"},
		strings.NewReader(""),
		&stdout, &stderr,
	)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2; stderr=%q", code, stderr.String())
	}
}
