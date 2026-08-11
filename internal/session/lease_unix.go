//go:build aix || android || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package session

import (
	"errors"
	"os"
	"syscall"
)

type nativeLease struct{}

func tryNativeLock(file *os.File) (nativeLease, error) {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nativeLease{}, ErrLeaseContended
		}
		return nativeLease{}, ErrLeaseUnavailable
	}
	return nativeLease{}, nil
}

func releaseNativeLock(file *os.File, _ nativeLease) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		return ErrLeaseUnavailable
	}
	return nil
}
