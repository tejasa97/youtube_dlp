//go:build !windows

package chromiumwindows

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSecureSourceRejectsSymlinksAndHardlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openSecureSource(link, 10); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink error=%v", err)
	}
	hard := filepath.Join(dir, "hard")
	if err := os.Link(target, hard); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openSecureSource(target, 10); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("hardlink error=%v", err)
	}
}

func TestSecureSourceRejectsPermissiveMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openSecureSource(path, 10); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("error=%v", err)
	}
}
