//go:build darwin || linux

package ytdlp

import (
	"os"

	"golang.org/x/sys/unix"
)

func openPrintAppendFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o644)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
