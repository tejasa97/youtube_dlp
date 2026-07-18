package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAndSummarizeDeterministicEvidence(t *testing.T) {
	root := t.TempDir()
	suite := filepath.Join(root, "suite.json")
	records := filepath.Join(root, "records.json")
	if err := os.WriteFile(suite, []byte(`{"schema_version":1,"canaries":[{"id":"public.youtube","class":"public","extractor":"youtube","target_ref":"youtube.smoke","capabilities":["metadata"],"secret_handle":{"provider":"","name":""},"region":"","timeout_ms":1000}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(records, []byte(`{"schema_version":1,"records":[{"canary_id":"public.youtube","class":"public","extractor":"youtube","outcome":"success","failure_class":"none","started_unix_ms":1,"duration_ms":10}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"validate-suite", "--suite", suite}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"public.youtube"`) {
		t.Fatalf("validate code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"summarize", "--suite", suite, "--records", records}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"success_basis_points":10000`) {
		t.Fatalf("summarize code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestVersionProbe(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, &stdout, &stderr); code != 0 || stdout.String() != "ytdlp-ops 1\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRejectsDuplicateAndDoesNotExposeInputPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret-suite-name.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"schema_version":1,"canaries":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"validate-suite", "--suite", path}, &stdout, &stderr); code == 0 || strings.Contains(stderr.String(), "secret-suite-name") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestValidateBoundedPolicyAndReplayFixtures(t *testing.T) {
	root := filepath.Join("..", "..", "conformance", "operations")
	suite := filepath.Join(root, "canary_suite_v1.json")
	for _, test := range []struct {
		operation string
		flag      string
		file      string
		contains  string
	}{
		{"validate-policy", "--policy", "canary_policy_v1.json", `"max_runs_per_window":2`},
		{"validate-replay", "--replay", "replay_capture_v1.json", `"schema_version":1`},
	} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{test.operation, "--suite", suite, test.flag, filepath.Join(root, test.file)}, &stdout, &stderr)
		if code != 0 || !strings.Contains(stdout.String(), test.contains) {
			t.Fatalf("%s code=%d stdout=%q stderr=%q", test.operation, code, stdout.String(), stderr.String())
		}
	}
}
