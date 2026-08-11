//go:build windows

package downloader

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type checkpointACLHeader struct {
	revision  byte
	reserved  byte
	size      uint16
	count     uint16
	reserved2 uint16
}

func protectCheckpointCreated(path string, directory bool) error {
	user, system, administrators, err := checkpointTrustees()
	if err != nil {
		return ErrUnsafeDestination
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entries := []windows.EXPLICIT_ACCESS{
		checkpointAccessEntry(user, windows.TRUSTEE_IS_USER, inheritance),
		checkpointAccessEntry(system, windows.TRUSTEE_IS_USER, inheritance),
		checkpointAccessEntry(administrators, windows.TRUSTEE_IS_GROUP, inheritance),
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return ErrUnsafeDestination
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		return ErrUnsafeDestination
	}
	return nil
}

func checkpointAccessEntry(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE, inheritance uint32) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
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
	if err != nil {
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
	header := (*checkpointACLHeader)(unsafe.Pointer(dacl))
	seenUser, seenSystem, seenAdministrators := false, false, false
	for index := uint32(0); index < uint32(header.count); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return ErrUnsafeDestination
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case sid.Equals(user):
			seenUser = true
		case sid.Equals(system):
			seenSystem = true
		case sid.Equals(administrators):
			seenAdministrators = true
		default:
			return ErrUnsafeDestination
		}
	}
	if !seenUser || !seenSystem || !seenAdministrators {
		return ErrUnsafeDestination
	}
	return nil
}
