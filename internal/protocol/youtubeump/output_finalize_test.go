package youtubeump

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishOutputOverwriteReplacesDestination(t *testing.T) {
	root := t.TempDir()
	part := filepath.Join(root, "part")
	destination := filepath.Join(root, "out.bin")
	if err := os.WriteFile(part, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishOutput(part, destination, true); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "new" {
		t.Fatalf("body=%q", body)
	}
}

func TestPublishOutputWithoutOverwriteRejectsExistingDestination(t *testing.T) {
	root := t.TempDir()
	part := filepath.Join(root, "part")
	destination := filepath.Join(root, "out.bin")
	if err := os.WriteFile(part, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishOutput(part, destination, false); err == nil {
		t.Fatal("expected failure")
	}
	body, _ := os.ReadFile(destination)
	if string(body) != "existing" {
		t.Fatalf("body=%q", body)
	}
}
