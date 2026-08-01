//go:build windows

package snapshot

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

var (
	ErrNoFollowUnsupported = errors.New("snapshot no-follow open is unsupported")
	ErrUnsafeSource        = errors.New("snapshot source is unsafe")
)

// OpenReadOnlyNoFollow uses Windows' reparse-point flag and rejects reparse
// handles. Callers still compare the opened handle with their pre-open state.
func OpenReadOnlyNoFollow(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, ErrUnsafeSource
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	info, statErr := file.Stat()
	var details windows.ByHandleFileInformation
	detailsErr := windows.GetFileInformationByHandle(handle, &details)
	if statErr != nil || !info.Mode().IsRegular() || detailsErr != nil || details.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = file.Close()
		return nil, ErrUnsafeSource
	}
	return file, nil
}
