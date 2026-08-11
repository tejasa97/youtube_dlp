//go:build !windows && !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris

package session

import "os"

func openDiscardRoot(string, string) (*discardDirectory, error) { return nil, ErrWorkspaceUnavailable }

func openDiscardEntry(*discardDirectory, string, os.FileInfo, bool) (*discardEntryHandle, error) {
	return nil, ErrWorkspaceUnavailable
}

func (entry *discardEntryHandle) remove() error {
	return ErrWorkspaceUnavailable
}

func syncDiscardDirectoryHandle(*discardDirectory) error { return ErrWorkspaceUnavailable }
