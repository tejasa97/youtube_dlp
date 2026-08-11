//go:build windows

package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsOwnerOnlyACLIsAppliedAndValidated(t *testing.T) {
	directory := t.TempDir()
	if err := secureDirectoryPath(directory); err != nil {
		t.Fatal(err)
	}
	if !secureWindowsACL(directory) {
		t.Fatal("owner-only directory ACL was not validated")
	}
	filePath := filepath.Join(directory, "state")
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := secureFilePath(filePath); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !ownerOnlyFileAt(filePath, info) {
		t.Fatal("owner-only file ACL was not validated")
	}
}
