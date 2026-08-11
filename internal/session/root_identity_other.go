//go:build !windows && !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris

package session

func directoryIdentity(string) (string, error) { return "", ErrWorkspaceUnavailable }
func validRootIdentity(string) bool            { return false }
