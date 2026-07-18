//go:build !windows

package update

import (
	"os"
	"path/filepath"
)

func writeTestLock(path string, owner []byte) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	if owner == nil {
		return nil
	}
	return replaceTestLockOwner(path, owner)
}

func replaceTestLockOwner(path string, owner []byte) error {
	return os.WriteFile(filepath.Join(path, "owner"), owner, 0o600)
}
