//go:build !windows && !aix && !android && !darwin && !dragonfly && !freebsd && !illumos && !ios && !linux && !netbsd && !openbsd && !solaris

package session

import "os"

func openDiscardRecordFile(string, os.FileInfo) (*os.File, error) {
	return nil, ErrUnsafePath
}
