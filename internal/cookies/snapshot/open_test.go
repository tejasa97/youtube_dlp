//go:build darwin || linux

package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenReadOnlyNoFollowRejectsFinalSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	if err := os.WriteFile(target, []byte("cookie snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	file, err := OpenReadOnlyNoFollow(link)
	if file != nil {
		file.Close()
	}
	if !errors.Is(err, ErrUnsafeSource) {
		t.Fatalf("OpenReadOnlyNoFollow() error = %v", err)
	}
}

func TestOpenReadOnlyNoFollowOpensRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("cookie snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := OpenReadOnlyNoFollow(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("opened info = %v, %v", info, err)
	}
}
