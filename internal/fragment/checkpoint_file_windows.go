//go:build windows

package fragment

import (
	"errors"
	"io"
	"os"
	"unsafe"

	"github.com/tejasa97/ytdlp-go/internal/atomicfile"
	"golang.org/x/sys/windows"
)

const checkpointFileAllAccess windows.ACCESS_MASK = 0x1f01ff

func writeProtectedCheckpointArtifactPlatform(path string, mode os.FileMode, encode func(io.Writer) error) error {
	err := atomicfile.WriteWithTempSecurity(path, mode, encode, secureCheckpointFile)
	var outcome atomicfile.CommitError
	if err != nil {
		if !errors.As(err, &outcome) || !outcome.Committed() || outcome.Indeterminate() {
			return err
		}
		if hardenErr := secureCheckpointFile(path); hardenErr != nil {
			return &checkpointArtifactCommitError{cause: errors.Join(err, hardenErr), committed: true}
		}
		return err
	}
	if hardenErr := secureCheckpointFile(path); hardenErr != nil {
		return &checkpointArtifactCommitError{cause: hardenErr, committed: true}
	}
	return nil
}

func secureCheckpointFile(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return errors.New("current Windows user SID unavailable")
	}
	sid := user.User.Sid.String()
	descriptor, err := windows.SecurityDescriptorFromString("O:" + sid + "G:" + sid + "D:P" +
		"(A;;FA;;;" + sid + ")(A;;FA;;;SY)(A;;FA;;;BA)")
	if err != nil {
		return err
	}
	if descriptor == nil {
		return errors.New("checkpoint file security descriptor unavailable")
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if acl == nil {
		return errors.New("checkpoint file DACL unavailable")
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user.User.Sid, nil, acl, nil); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !checkpointFileProtected(path, info) {
		if err == nil {
			err = errors.New("checkpoint file ACL did not validate")
		}
		return err
	}
	return nil
}

func checkpointFileProtected(path string, info os.FileInfo) bool {
	if info == nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || descriptor == nil || !descriptor.IsValid() {
		return false
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
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
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return false
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 3 {
		return false
	}
	seen := make(map[string]bool, 3)
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 || ace.Mask != checkpointFileAllAccess || ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			return false
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !aceSID.IsValid() || (!aceSID.Equals(owner) && !aceSID.Equals(system) && !aceSID.Equals(administrators)) {
			return false
		}
		key := aceSID.String()
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return len(seen) == 3 && seen[owner.String()] && seen[system.String()] && seen[administrators.String()]
}
