//go:build windows

package update

import "os"

func writeTestLock(path string, owner []byte) error {
	return os.WriteFile(path, owner, 0o600)
}

func replaceTestLockOwner(path string, owner []byte) error {
	return os.WriteFile(path, owner, 0o600)
}
