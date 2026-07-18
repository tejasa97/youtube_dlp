//go:build !windows

package netrc

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func openSecure(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

func validateSecureHandle(_ *os.File, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 || info.Mode().Perm()&0o077 != 0 {
		return ErrUnsafeFile
	}
	return nil
}
