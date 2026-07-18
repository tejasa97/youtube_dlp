//go:build windows

package update

import (
	"os"

	"golang.org/x/sys/windows"
)

// The standard library exposes neither owner nor writable ACL evaluation on
// Windows. Product integration must select a per-user protected root; this
// function still rejects reparse-point roots through Lstat's symlink bit.
func validateDirectorySecurity(root string) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	return nil
}

func secureRegular(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func replaceFile(source, destination string) error {
	sourcePointer, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPointer, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourcePointer, destinationPointer, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// Windows has no portable directory fsync primitive. Atomic pointer updates
// still use a synced temporary file followed by same-directory rename.
func syncDirectory(string) error { return nil }
