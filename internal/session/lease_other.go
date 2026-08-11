//go:build !windows && !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris

package session

import "os"

type nativeLease struct{}

func tryNativeLock(*os.File) (nativeLease, error) { return nativeLease{}, ErrLeaseUnavailable }

func releaseNativeLock(*os.File, nativeLease) error { return ErrLeaseUnavailable }
