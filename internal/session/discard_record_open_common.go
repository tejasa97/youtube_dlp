package session

import (
	"os"
)

func validateDiscardRecordHandle(path string, expected os.FileInfo, file *os.File) error {
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !ownerOnlyFile(opened) || !os.SameFile(expected, opened) {
		return ErrUnsafePath
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() ||
		!os.SameFile(expected, current) || !ownerOnlyFileAt(path, current) {
		return ErrUnsafePath
	}
	return nil
}
