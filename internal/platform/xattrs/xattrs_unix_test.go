//go:build darwin || linux

package xattrs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixXattrsRoundTripOrFilesystemUnsupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "user.ytdlp_test"
	if err := Set(path, name, []byte("value")); err != nil {
		t.Skipf("filesystem xattrs unavailable: %v", err)
	}
	defer Remove(path, name)
	attrs, err := List(path)
	if err != nil || string(attrs[name]) != "value" {
		t.Fatalf("attrs=%q err=%v", attrs[name], err)
	}
	if err := Remove(path, name); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
