//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package session

import (
	"os"

	"golang.org/x/sys/unix"
)

func openDiscardRecordFile(path string, expected os.FileInfo) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if err := validateDiscardRecordHandle(path, expected, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
