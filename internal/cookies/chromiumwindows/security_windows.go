//go:build windows

package chromiumwindows

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsACLHeader struct {
	revision, reserved     byte
	size, count, reserved2 uint16
}

func openSecureSource(path string, maximum int64) (*os.File, os.FileInfo, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, nil, ErrUnsafePath
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		if err == windows.ERROR_FILE_NOT_FOUND || err == windows.ERROR_PATH_NOT_FOUND {
			return nil, nil, ErrNotFound
		}
		return nil, nil, ErrSnapshot
	}
	file := os.NewFile(uintptr(handle), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maximum {
		file.Close()
		if info != nil && info.Size() > maximum {
			return nil, nil, ErrLimit
		}
		return nil, nil, ErrUnsafePath
	}
	var details windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &details) != nil ||
		details.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || details.NumberOfLinks != 1 {
		file.Close()
		return nil, nil, ErrUnsafePath
	}
	if !secureWindowsACL(handle) {
		file.Close()
		return nil, nil, ErrUnsafePath
	}
	return file, info, nil
}

func secureWindowsACL(handle windows.Handle) bool {
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := descriptor.Owner()
	user, userErr := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || userErr != nil || owner == nil || user == nil || user.User.Sid == nil || !owner.Equals(user.User.Sid) {
		return false
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return false
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	header := (*windowsACLHeader)(unsafe.Pointer(dacl))
	for index := uint32(0); index < uint32(header.count); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(dacl, index, &ace) != nil || ace == nil {
			return false
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(owner) && !sid.Equals(system) && !sid.Equals(admins) {
			return false
		}
	}
	return true
}
