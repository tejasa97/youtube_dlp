//go:build windows

package session

import (
	"os"

	"golang.org/x/sys/windows"
)

func openDiscardRecordFile(path string, expected os.FileInfo) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, ErrUnsafePath
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	var details windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &details); err != nil ||
		details.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || details.NumberOfLinks != 1 {
		_ = file.Close()
		return nil, ErrUnsafePath
	}
	if err := validateDiscardRecordHandle(path, expected, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
