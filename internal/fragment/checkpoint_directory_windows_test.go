//go:build windows

package fragment

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsCheckpointDirectoryUsesProtectedOwnerACL(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "private", "component")
	if _, err := ensureProtectedCheckpointDirectory(root, directory); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "private"), directory} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		protected, err := checkpointDirectoryProtected(path, info)
		if err != nil {
			t.Fatal(err)
		}
		if !protected {
			t.Fatalf("checkpoint directory %s does not have a protected owner ACL", path)
		}
	}
}
