package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const emptyDocument = `{
  "schema_version": 1,
  "metadata": %s,
  "formats": [],
  "playlists": [],
  "events": [],
  "selection": {},
  "outputs": []
}`

func TestRunExitCodesAndReportFiles(t *testing.T) {
	directory := t.TempDir()
	expected := filepath.Join(directory, "expected.json")
	actual := filepath.Join(directory, "actual.json")
	jsonReport := filepath.Join(directory, "report.json")
	markdownReport := filepath.Join(directory, "report.md")
	writeTestFile(t, expected, strings.Replace(emptyDocument, "%s", `{"id":"a"}`, 1))
	writeTestFile(t, actual, strings.Replace(emptyDocument, "%s", `{"id":"b"}`, 1))

	var stdout, stderr bytes.Buffer
	code := run([]string{"-json", jsonReport, "-markdown", markdownReport, expected, actual}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("run() = %d, stderr = %s", code, stderr.String())
	}
	for _, path := range []string{jsonReport, markdownReport} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(contents), "$.metadata.id") {
			t.Fatalf("%s missing difference: %s", path, contents)
		}
	}
}

func TestRunRejectsInvalidInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("run() = %d, want 2", code)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
