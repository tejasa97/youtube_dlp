//go:build windows

package downloader

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

const checkpointFileAllAccess windows.ACCESS_MASK = 0x1f01ff

func protectCheckpointCreated(path string, directory bool) error {
	user, _, _, err := checkpointTrustees()
	if err != nil || user == nil {
		return ErrUnsafeDestination
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	sid := user.String()
	ace := func(principal string) string { return "(A;" + inheritance + ";FA;;;" + principal + ")" }
	descriptor, err := windows.SecurityDescriptorFromString("O:" + sid + "G:" + sid + "D:P" + ace(sid) + ace("SY") + ace("BA"))
	if err != nil || descriptor == nil {
		return ErrUnsafeDestination
	}
	acl, _, err := descriptor.DACL()
	if err != nil || acl == nil {
		return ErrUnsafeDestination
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user,
		nil,
		acl,
		nil,
	); err != nil {
		return ErrUnsafeDestination
	}
	return nil
}

func checkpointTrustees() (user, system, administrators *windows.SID, err error) {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || tokenUser == nil || tokenUser.User.Sid == nil {
		return nil, nil, nil, ErrUnsafeDestination
	}
	system, err = windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, nil, nil, err
	}
	administrators, err = windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, nil, nil, err
	}
	return tokenUser.User.Sid, system, administrators, nil
}

func validateCheckpointOwned(path string, info os.FileInfo) error {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return ErrUnsafeDestination
	}
	flags := uint32(windows.FILE_ATTRIBUTE_NORMAL | windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if info.IsDir() {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(pointer, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return ErrUnsafeDestination
	}
	defer windows.CloseHandle(handle)
	var details windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &details); err != nil || details.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (!info.IsDir() && details.NumberOfLinks != 1) {
		return ErrUnsafeDestination
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return ErrUnsafeDestination
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return ErrUnsafeDestination
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return ErrUnsafeDestination
	}
	user, system, administrators, err := checkpointTrustees()
	if err != nil || !owner.Equals(user) {
		return ErrUnsafeDestination
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return ErrUnsafeDestination
	}
	if dacl.AceCount != 3 {
		return ErrUnsafeDestination
	}
	seen := make(map[string]bool, 3)
	wantFlags := byte(0)
	if info.IsDir() {
		wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != wantFlags || ace.Mask != checkpointFileAllAccess || ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			return ErrUnsafeDestination
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() {
			return ErrUnsafeDestination
		}
		key := sid.String()
		if seen[key] || (!sid.Equals(user) && !sid.Equals(system) && !sid.Equals(administrators)) {
			return ErrUnsafeDestination
		}
		seen[key] = true
	}
	if len(seen) != 3 || !seen[user.String()] || !seen[system.String()] || !seen[administrators.String()] {
		return ErrUnsafeDestination
	}
	return nil
}
