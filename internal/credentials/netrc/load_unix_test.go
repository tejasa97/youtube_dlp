//go:build !windows

package netrc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSecureFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".netrc")
	if err := os.WriteFile(path, fixture(t), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := Load(context.Background(), path, Limits{})
	if err != nil || store.Count() != 4 {
		t.Fatalf("count=%d error=%v", store.Count(), err)
	}
}

func TestLoadRejectsPermissionsSymlinkHardlinkDirectoryAndOversize(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "credentials")
	if err := os.WriteFile(path, fixture(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), path, Limits{}); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("permission error=%v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "symlink")
	if err := os.Symlink(path, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), symlink, Limits{}); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("symlink error=%v", err)
	}
	hardlink := filepath.Join(directory, "hardlink")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), path, Limits{}); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("hardlink error=%v", err)
	}
	if _, err := Load(context.Background(), directory, Limits{}); !errors.Is(err, ErrUnsafeFile) {
		t.Fatalf("directory error=%v", err)
	}
	large := filepath.Join(directory, "large")
	if err := os.WriteFile(large, make([]byte, 65), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), large, Limits{MaxBytes: 64}); !errors.Is(err, ErrLimit) {
		t.Fatalf("size error=%v", err)
	}
}
