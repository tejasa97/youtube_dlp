//go:build windows

package archive

import "os"

func writeTestLock(path string, owner []byte) error {
	return os.WriteFile(path, owner, 0o600)
}
