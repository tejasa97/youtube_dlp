//go:build darwin || linux

// Package snapshot provides the platform-specific primitives used to copy
// browser databases without following a path replacement or symlink.
package snapshot

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var (
	ErrNoFollowUnsupported = errors.New("snapshot no-follow open is unsupported")
	ErrUnsafeSource        = errors.New("snapshot source is unsafe")
)

// OpenReadOnlyNoFollow opens a regular snapshot source without following a
// final symlink. Callers must still compare the opened handle with their
// pre-open Lstat and recheck it after copying.
func OpenReadOnlyNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, errors.Join(ErrUnsafeSource, err)
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
