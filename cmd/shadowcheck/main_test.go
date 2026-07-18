package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunEqualAndCriticalMismatch(t *testing.T) {
	reference := filepath.Join("..", "..", "conformance", "differential", "phase3", "reference.json")
	actual := filepath.Join("..", "..", "conformance", "differential", "phase3", "go.json")
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-expected", reference, "-actual", actual}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"equal":true`) {
		t.Fatalf("equal code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	body, err := os.ReadFile(actual)
	if err != nil {
		t.Fatal(err)
	}
	body = bytes.Replace(body, []byte(`"extractor": "synthetic"`), []byte(`"extractor": "other"`), 1)
	mismatch := filepath.Join(t.TempDir(), "actual.json")
	if err := os.WriteFile(mismatch, body, 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"-expected", reference, "-actual", mismatch}, &stdout, &stderr); code != 3 || !strings.Contains(stdout.String(), `"severity":"critical"`) {
		t.Fatalf("mismatch code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunRedactsInvalidInputPaths(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "token-secret.json")
	if err := os.WriteFile(secretPath, []byte(`{"bad":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-expected", secretPath, "-actual", secretPath}, &stdout, &stderr)
	if code == 0 || strings.Contains(stderr.String(), "token-secret") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
