package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMergesSnapshotsAndEvaluatesCoverage(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.json")
	if err := os.WriteFile(first, []byte(`{"schema_version":1,"counts":[{"extractor":"youtube","capability":"extract","outcome":"success","count":94},{"extractor":"youtube","capability":"extract","outcome":"error","count":1}],"overflow":{"cell_limit":0,"counter_saturation":0}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	second := `{"schema_version":1,"counts":[{"extractor":"unknown","capability":"extract","outcome":"unsupported","count":5}],"overflow":{"cell_limit":0,"counter_saturation":0}}`
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-input", first, "-input", "-", "-minimum-basis-points", "9400", "-require-exact", "-require-zero-fallback"}, strings.NewReader(second), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var decoded report
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Inputs != 2 || decoded.Coverage.Denominator != 100 || decoded.Coverage.Successful != 94 || decoded.Coverage.BasisPoints != 9400 || !decoded.Coverage.Exact {
		t.Fatalf("report=%+v", decoded)
	}
}

func TestRunGateFailuresAndSecretSafeDiagnostics(t *testing.T) {
	input := `{"schema_version":1,"counts":[{"extractor":"youtube","capability":"extract","outcome":"fallback","count":1}],"overflow":{"cell_limit":1,"counter_saturation":0}}`
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-input", "-", "-require-exact", "-require-zero-fallback"}, strings.NewReader(input), &stdout, &stderr); code != 3 {
		t.Fatalf("gate code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	secret := "token=fixture-secret"
	if code := run(context.Background(), []string{"-input", "-"}, strings.NewReader(secret), &stdout, &stderr); code != 1 || strings.Contains(stderr.String(), secret) {
		t.Fatalf("invalid code=%d stderr=%q", code, stderr.String())
	}
}

func TestRunRejectsInvalidConfigurationAndCanceledContext(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), nil, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("missing input code=%d", code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := run(ctx, []string{"-input", "-"}, strings.NewReader("{}"), &stdout, &stderr); code != 130 {
		t.Fatalf("cancel code=%d", code)
	}
}
