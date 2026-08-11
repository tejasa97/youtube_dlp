//go:build !windows

package downloader

import (
	"os"
	"syscall"
)

func validateCheckpointOwned(_ string, info os.FileInfo) error {
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(status.Uid) != os.Geteuid() || info.Mode().Perm()&0o077 != 0 || (!info.IsDir() && status.Nlink != 1) {
		return ErrUnsafeDestination
	}
	return nil
}

func protectCheckpointCreated(path string, directory bool) error {
	mode := os.FileMode(0o600)
	if directory {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}
