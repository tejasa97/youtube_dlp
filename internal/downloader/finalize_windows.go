//go:build windows

package downloader

import "golang.org/x/sys/windows"

func replaceDestination(source, destination string) error {
	return moveDestination(source, destination, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func installDestination(source, destination string) error {
	return moveDestination(source, destination, windows.MOVEFILE_WRITE_THROUGH)
}

func moveDestination(source, destination string, flags uint32) error {
	sourcePointer, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPointer, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourcePointer, destinationPointer, flags)
}
