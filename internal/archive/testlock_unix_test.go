//go:build !windows

package archive

import (
	"os"
	"path/filepath"
)

func writeTestLock(path string, owner []byte) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(path, "owner"), owner, 0o600)
}
