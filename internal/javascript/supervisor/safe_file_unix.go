//go:build darwin || linux

package supervisor

import (
	"os"
	"path/filepath"
	"syscall"
)

func safeHelperOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}

func safeHelperParents(path string) bool {
	for directory := filepath.Dir(path); ; directory = filepath.Dir(directory) {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 && stat.Uid != uint32(os.Geteuid()) {
			return false
		}
		rootOwnedSticky := stat.Uid == 0 && info.Mode()&os.ModeSticky != 0
		if info.Mode().Perm()&0o022 != 0 && !rootOwnedSticky {
			return false
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return true
		}
	}
}
