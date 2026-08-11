//go:build windows

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

type nativeLease struct {
	overlapped *windows.Overlapped
}

func tryNativeLock(file *os.File) (nativeLease, error) {
	overlapped := &windows.Overlapped{}
	err := windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		^uint32(0),
		^uint32(0),
		overlapped,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nativeLease{}, ErrLeaseContended
		}
		return nativeLease{}, ErrLeaseUnavailable
	}
	return nativeLease{overlapped: overlapped}, nil
}

func releaseNativeLock(file *os.File, lease nativeLease) error {
	if lease.overlapped == nil {
		return nil
	}
	if err := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, ^uint32(0), ^uint32(0), lease.overlapped); err != nil {
		return ErrLeaseUnavailable
	}
	return nil
}
