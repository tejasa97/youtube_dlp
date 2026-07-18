//go:build !windows

package chromiumwindows

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openSecureSource(path string, maximum int64) (*os.File, os.FileInfo, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, ErrUnsafePath
	}
	file := os.NewFile(uintptr(descriptor), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		file.Close()
		if info != nil && info.Size() > maximum {
			return nil, nil, ErrLimit
		}
		return nil, nil, ErrUnsafePath
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 || info.Mode().Perm()&0o077 != 0 {
		file.Close()
		return nil, nil, ErrUnsafePath
	}
	return file, info, nil
}
