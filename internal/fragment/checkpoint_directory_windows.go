//go:build windows

package fragment

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func createProtectedCheckpointDirectory(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get checkpoint directory owner: %w", err)
	}
	sid := user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString(
		"O:" + sid + "G:" + sid + "D:P" +
			"(A;OICI;FA;;;" + sid + ")" +
			"(A;OICI;FA;;;SY)" +
			"(A;OICI;FA;;;BA)",
	)
	if err != nil {
		return fmt.Errorf("build checkpoint directory ACL: %w", err)
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	return windows.CreateDirectory(pathPointer, &attributes)
}

func checkpointDirectoryProtected(path string, _ os.FileInfo) (bool, error) {
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return false, err
	}
	if descriptor == nil {
		return false, nil
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return false, err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return false, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return false, err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false, err
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return false, err
	}
	ownerAllowed := false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			return false, err
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			return false, nil
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case aceSID.Equals(user.User.Sid):
			ownerAllowed = true
		case aceSID.Equals(system), aceSID.Equals(administrators):
		default:
			return false, nil
		}
	}
	return ownerAllowed, nil
}
