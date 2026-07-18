//go:build darwin || linux

package sandbox

import (
	"os"
	"syscall"
)

func ownedByCurrentUser(info os.FileInfo) bool {
	status, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(status.Uid) == os.Geteuid()
}

func trustedReadOwner(info os.FileInfo) bool {
	status, ok := info.Sys().(*syscall.Stat_t)
	return ok && (int(status.Uid) == os.Geteuid() || status.Uid == 0) && info.Mode().Perm()&0o022 == 0
}
