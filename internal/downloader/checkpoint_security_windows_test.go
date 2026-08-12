//go:build windows

package downloader

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCheckpointWindowsCreatedArtifactsHaveProtectedACL(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "media.bin")
	job := checkpointJob(destination, "direct:windows:acl", &CheckpointOptions{StateDirectory: filepath.Join(root, "checkpoint")})
	plan, err := checkpointPlanForJob(job)
	if err != nil {
		t.Fatal(err)
	}
	job.OutputRoot = plan.outputRoot
	job.Destination = plan.destination
	if err := prepareCheckpointStateDirectory(job, plan, plan.partPath); err != nil {
		t.Fatal(err)
	}
	state := newCheckpointPartialState(job.ResumeIdentity)
	if err := savePartialStateOnce(plan.statePath, state); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{plan.stateDirectory, filepath.Join(plan.stateDirectory, "owner"), plan.statePath} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateCheckpointOwned(path, info); err != nil {
			t.Fatalf("%s ACL is not protected owner-only: %v", path, err)
		}
		if !checkpointWindowsDACLProtected(t, path) {
			t.Fatalf("%s DACL is not protected", path)
		}
	}
}

func TestCheckpointWindowsRejectsUnprotectedDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	acl := checkpointWindowsACL(t, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT, nil)
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCheckpointOwned(path, info); err == nil {
		t.Fatal("unprotected DACL was accepted")
	}
}

func TestCheckpointWindowsRejectsUnauthorizedInheritedTrustees(t *testing.T) {
	world, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		mode windows.ACCESS_MODE
	}{
		{name: "inherit-only allow", mode: windows.GRANT_ACCESS},
		{name: "deny", mode: windows.DENY_ACCESS},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "checkpoint")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			unauthorized := windows.EXPLICIT_ACCESS{
				AccessPermissions: windows.GENERIC_READ,
				AccessMode:        test.mode,
				Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT | windows.INHERIT_ONLY,
				Trustee: windows.TRUSTEE{
					TrusteeForm:  windows.TRUSTEE_IS_SID,
					TrusteeType:  windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
					TrusteeValue: windows.TrusteeValueFromSID(world),
				},
			}
			acl := checkpointWindowsACL(t, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT, &unauthorized)
			if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
				windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateCheckpointOwned(path, info); err == nil {
				t.Fatalf("unauthorized %s ACE was accepted", test.name)
			}
		})
	}
}

func checkpointWindowsACL(t *testing.T, inheritance uint32, additional *windows.EXPLICIT_ACCESS) *windows.ACL {
	t.Helper()
	user, system, administrators, err := checkpointTrustees()
	if err != nil {
		t.Fatal(err)
	}
	entries := []windows.EXPLICIT_ACCESS{
		checkpointTestAccessEntry(user, windows.TRUSTEE_IS_USER, inheritance),
		checkpointTestAccessEntry(system, windows.TRUSTEE_IS_USER, inheritance),
		checkpointTestAccessEntry(administrators, windows.TRUSTEE_IS_GROUP, inheritance),
	}
	if additional != nil {
		entries = append(entries, *additional)
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	return acl
}

func checkpointTestAccessEntry(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE, inheritance uint32) windows.EXPLICIT_ACCESS {
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

func checkpointWindowsDACLProtected(t *testing.T, path string) bool {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatal(err)
	}
	return control&windows.SE_DACL_PROTECTED != 0
}
