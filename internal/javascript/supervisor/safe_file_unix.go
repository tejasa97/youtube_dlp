//go:build darwin || linux

package supervisor

import (
	"os"
	"syscall"
)

func safeHelperOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
