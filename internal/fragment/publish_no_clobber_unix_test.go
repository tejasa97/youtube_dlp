//go:build !windows

package fragment

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/atomicfile"
)

func TestUnixNoClobberPublicationPreservesExistingDestination(t *testing.T) {
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

func TestUnixNoClobberPublicationClassifiesPostLinkFailuresCommitted(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	destination := filepath.Join(directory, "destination")
	if err := os.WriteFile(source, []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected destination directory sync")
	ops := productionNoClobberUnixOps
	ops.syncDirectory = func(path string) error {
		if path == filepath.Dir(destination) {
			return injected
		}
		return nil
	}
	err := publishNoClobberUnix(source, destination, ops)
	var commitErr atomicfile.CommitError
	if !errors.Is(err, injected) || !errors.As(err, &commitErr) || !commitErr.Committed() || commitErr.Indeterminate() {
		t.Fatalf("post-link outcome = %#v, %v", commitErr, err)
	}
	contents, readErr := os.ReadFile(destination)
	if readErr != nil || string(contents) != "candidate" {
		t.Fatalf("committed destination = %q, %v", contents, readErr)
	}
}
