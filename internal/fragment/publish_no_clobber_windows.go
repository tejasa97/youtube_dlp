//go:build windows

package fragment

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func publishNoClobber(source, destination string) error {
	file, err := os.OpenFile(source, os.O_WRONLY, 0)
	if err != nil {
		return noClobberFailure("open source", err, false, false)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return noClobberFailure("sync source", err, false, false)
	}
	if err := file.Close(); err != nil {
		return noClobberFailure("close source", err, false, false)
	}
	sourcePointer, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return noClobberFailure("encode source path", err, false, false)
	}
	destinationPointer, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return noClobberFailure("encode destination path", err, false, false)
	}
	moveErr := windows.MoveFileEx(sourcePointer, destinationPointer, windows.MOVEFILE_WRITE_THROUGH)
	if moveErr == nil {
		return nil
	}
	sourceExists, sourceErr := noClobberPathExists(source)
	destinationExists, destinationErr := noClobberPathExists(destination)
	if sourceErr != nil || destinationErr != nil {
		return noClobberFailure("inspect failed move", errors.Join(moveErr, sourceErr, destinationErr), false, true)
	}
	switch {
	case !sourceExists && destinationExists:
		return noClobberFailure("move reported failure after commit", moveErr, true, false)
	case sourceExists:
		return noClobberFailure("move destination", moveErr, false, false)
	default:
		return noClobberFailure("locate files after failed move", moveErr, false, true)
	}
}

func noClobberPathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
