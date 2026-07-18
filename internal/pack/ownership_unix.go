//go:build darwin || linux

package pack

import (
	"os"
	"syscall"
)

func secureOwnership(info os.FileInfo) bool {
	status, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(status.Uid) == os.Geteuid()
}

func singleLink(info os.FileInfo) bool {
	status, ok := info.Sys().(*syscall.Stat_t)
	return ok && status.Nlink == 1
}

func secureInstallPlatform() bool { return true }
