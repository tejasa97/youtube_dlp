//go:build windows

package fragment

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
)

func TestWindowsNoClobberPublicationPreservesExistingDestination(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	destination := filepath.Join(directory, "destination")
	if err := os.WriteFile(source, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := publishNoClobber(source, destination)
	var commitErr atomicfile.CommitError
	if !errors.As(err, &commitErr) || commitErr.Committed() || commitErr.Indeterminate() {
		t.Fatalf("existing destination outcome = %#v, %v", commitErr, err)
	}
	contents, readErr := os.ReadFile(destination)
	if readErr != nil || string(contents) != "existing" {
		t.Fatalf("destination = %q, %v", contents, readErr)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("pre-commit source was not retained: %v", err)
	}
}

func TestWindowsNoClobberPublicationMovesNewDestination(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	destination := filepath.Join(directory, "destination")
	if err := os.WriteFile(source, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publishNoClobber(source, destination); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "candidate" {
		t.Fatalf("destination = %q, %v", contents, err)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source remains after committed move: %v", err)
	}
}
