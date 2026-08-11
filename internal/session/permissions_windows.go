//go:build windows

package session

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows ACLs are the owner-only primitive. The ACL is protected from
// inheritance and grants access only to the current user, LocalSystem, and
// Builtin Administrators. Directory ACEs inherit only to descendants in this
// workspace tree.
func secureDirectoryPath(path string) error { return setProtectedWindowsACL(path, true) }

func secureFilePath(path string) error { return setProtectedWindowsACL(path, false) }

func ownerOnlyDirectory(info os.FileInfo) bool { return info.IsDir() }

func ownerOnlyDirectoryAt(path string, info os.FileInfo) bool {
	return info.IsDir() && secureWindowsACL(path)
}

func ownerOnlyDirectoryFromPath(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && ownerOnlyDirectoryAt(path, info)
}

func ownerOnlyFile(info os.FileInfo) bool { return info.Mode().IsRegular() }

func ownerOnlyFileAt(path string, info os.FileInfo) bool {
	return info.Mode().IsRegular() && secureWindowsACL(path)
}

type windowsACLHeader struct {
	revision, reserved     byte
	size, count, reserved2 uint16
}

func setProtectedWindowsACL(path string, directory bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return ErrPermissionUnavailable
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return ErrPermissionUnavailable
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return ErrPermissionUnavailable
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(system),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_ALIAS,
				TrusteeValue: windows.TrusteeValueFromSID(administrators),
			},
		},
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return ErrPermissionUnavailable
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
		return ErrPermissionUnavailable
	}
	return nil
}

func secureWindowsACL(path string) bool {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		return false
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return false
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil || !owner.Equals(user.User.Sid) {
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
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	header := (*windowsACLHeader)(unsafe.Pointer(dacl))
	for index := uint32(0); index < uint32(header.count); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return false
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return false
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(owner) && !sid.Equals(system) && !sid.Equals(administrators) {
			return false
		}
	}
	return true
}
