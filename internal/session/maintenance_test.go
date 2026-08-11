package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func strictMaintenanceRef(t *testing.T, workspace *Workspace) WorkspaceRef {
	t.Helper()
	root, err := ValidateOutputRoot(workspace.Ref().OutputRoot)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := NewWorkspaceRefWithIdentity(root.CanonicalPath, root.Identity, workspace.Ref().SessionID)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestDiscardMarkerBlocksContenderAndProtectsReplacement(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	ref := strictMaintenanceRef(t, workspace)
	handle, err := PrepareDiscard(ref)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	previous := discardAfterMarker
	discardAfterMarker = func() {
		close(entered)
		<-release
	}
	defer func() { discardAfterMarker = previous }()
	type discardResultValue struct {
		disposition DiscardDisposition
		err         error
	}
	result := make(chan discardResultValue, 1)
	go func() {
		disposition, discardErr := handle.Discard()
		result <- discardResultValue{disposition: disposition, err: discardErr}
	}()
	<-entered
	if _, err := PrepareDiscard(ref); !errors.Is(err, ErrNeedsReconciliation) && !errors.Is(err, ErrLeaseContended) {
		t.Fatalf("contender prepare error = %v, want contention or discard reconciliation", err)
	}
	if _, err := Open(ref); !errors.Is(err, ErrNeedsReconciliation) && !errors.Is(err, ErrLeaseContended) {
		t.Fatalf("contender open error = %v, want contention or discard reconciliation", err)
	}
	if err := os.RemoveAll(handle.state.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(handle.state.path, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(handle.state.path, "replacement.txt")
	if err := os.WriteFile(replacement, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ref); !errors.Is(err, ErrNeedsReconciliation) {
		t.Fatalf("replacement contender open error = %v, want discard reconciliation", err)
	}
	close(release)
	discardOutcome := <-result
	if !errors.Is(discardOutcome.err, ErrNeedsReconciliation) || discardOutcome.disposition != DiscardReconciliation {
		t.Fatalf("discard replacement result = %#v, want reconciliation", discardOutcome)
	}
	if _, err := os.Stat(replacement); err != nil {
		t.Fatalf("replacement was deleted: %v", err)
	}
}

func TestHelperProcessDiscardGuardContention(t *testing.T) {
	if os.Getenv("YTDLP_DISCARD_GUARD_HELPER") != "1" {
		return
	}
	var ref WorkspaceRef
	if err := json.Unmarshal([]byte(os.Getenv("YTDLP_DISCARD_GUARD_REF")), &ref); err != nil {
		os.Exit(2)
	}
	ref.OutputRootIdentity = os.Getenv("YTDLP_DISCARD_GUARD_ROOT_IDENTITY")
	if _, err := PrepareDiscard(ref); !errors.Is(err, ErrLeaseContended) {
		os.Exit(3)
	}
	_, _ = fmt.Fprintln(os.Stdout, "contended")
	os.Exit(0)
}

func TestDiscardGuardBlocksCrossProcessRecovery(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	ref := strictMaintenanceRef(t, workspace)
	handle, err := PrepareDiscard(ref)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	previous := discardFault
	discardFault = func(point discardFaultPoint) error {
		if point == discardFaultAfterLeaseClose {
			close(entered)
			<-release
		}
		return nil
	}
	restored := false
	t.Cleanup(func() {
		if !restored {
			discardFault = previous
		}
	})
	result := make(chan struct {
		disposition DiscardDisposition
		err         error
	}, 1)
	go func() {
		disposition, discardErr := handle.Discard()
		result <- struct {
			disposition DiscardDisposition
			err         error
		}{disposition, discardErr}
	}()
	<-entered
	encoded, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=TestHelperProcessDiscardGuardContention", "--")
	command.Env = append(os.Environ(),
		"YTDLP_DISCARD_GUARD_HELPER=1",
		"YTDLP_DISCARD_GUARD_REF="+string(encoded),
		"YTDLP_DISCARD_GUARD_ROOT_IDENTITY="+ref.OutputRootIdentity,
	)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "contended" {
		_ = command.Process.Kill()
		t.Fatalf("discard helper output = %q, err = %v", scanner.Text(), scanner.Err())
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	discardFault = previous
	restored = true
	close(release)
	outcome := <-result
	if outcome.disposition != Discarded || outcome.err != nil {
		t.Fatalf("discard = %v, %v; want successful cleanup", outcome.disposition, outcome.err)
	}
}

func TestCollectOrphansRecoversDiscardMarkedWorkspace(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	ref := strictMaintenanceRef(t, workspace)
	if err := writeDiscardMarker(filepath.Join(workspace.Path(), discardMarkerName), discardRecord{
		Version: 1, SessionID: ref.SessionID, RootIdentity: ref.OutputRootIdentity,
		WorkspaceIdentity: func() string {
			identity, identityErr := directoryIdentity(workspace.Path())
			if identityErr != nil {
				t.Fatal(identityErr)
			}
			return identity
		}(),
	}); err != nil {
		t.Fatal(err)
	}
	root, err := ValidateOutputRoot(ref.OutputRoot)
	if err != nil {
		t.Fatal(err)
	}
	result, err := CollectOrphans(root.CanonicalPath, root.Identity, nil, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Collected) != 1 || result.Collected[0] != ref.SessionID || result.Skipped != 0 {
		t.Fatalf("collection result = %#v, want marked workspace collected", result)
	}
	if _, err := os.Stat(workspace.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marked workspace remains after recovery: %v", err)
	}
}

func TestDiscardRecordStrictAndNoFollow(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	ref := strictMaintenanceRef(t, workspace)
	identity, err := directoryIdentity(workspace.Path())
	if err != nil {
		t.Fatal(err)
	}
	record := discardRecord{Version: 1, SessionID: ref.SessionID, RootIdentity: ref.OutputRootIdentity, WorkspaceIdentity: identity}
	markerPath := filepath.Join(workspace.Path(), discardMarkerName)
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	write := func(contents []byte) {
		t.Helper()
		if err := os.WriteFile(markerPath, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func() (discardRecord, bool, bool) {
		t.Helper()
		return readDiscardRecord(markerPath)
	}

	write(encoded)
	if observed, exists, unsafe := read(); !exists || unsafe || observed != record {
		t.Fatalf("valid record read = %#v, %t, %t", observed, exists, unsafe)
	}
	unknown := append(append([]byte(nil), encoded[:len(encoded)-1]...), []byte(`,"unknown":"field"}`)...)
	write(unknown)
	if _, exists, unsafe := read(); !exists || !unsafe {
		t.Fatal("unknown discard-record field was accepted")
	}
	write(append(append([]byte(nil), encoded...), []byte("junk")...))
	if _, exists, unsafe := read(); !exists || !unsafe {
		t.Fatal("valid-prefix discard-record junk was accepted")
	}

	t.Run("symlink", func(t *testing.T) {
		write(encoded)
		target := filepath.Join(workspace.Path(), ManifestFileName)
		if err := os.Remove(markerPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, markerPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, exists, unsafe := read(); !exists || !unsafe {
			t.Fatal("discard-record symlink was accepted")
		}
		if err := os.Remove(markerPath); err != nil {
			t.Fatal(err)
		}
	})

	write(encoded)
	swappedPath := filepath.Join(workspace.Path(), ".swapped-record")
	previous := discardBeforeRecordOpen
	discardBeforeRecordOpen = func(path string) {
		if err := os.Rename(path, swappedPath); err != nil {
			t.Fatal(err)
		}
		write(encoded)
	}
	observed, exists, unsafe := read()
	discardBeforeRecordOpen = previous
	if !exists || !unsafe || observed != (discardRecord{}) {
		t.Fatalf("swapped discard record read = %#v, %t, %t", observed, exists, unsafe)
	}
}

func TestDiscardTreeBudgetsBoundWideAndDeepCleanup(t *testing.T) {
	t.Run("wide and GC bounded", func(t *testing.T) {
		workspace, _ := createTestWorkspace(t)
		for index := 0; index <= maxDiscardTreeEntries; index++ {
			path := filepath.Join(workspace.Path(), fmt.Sprintf("entry-%05d", index))
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := workspace.Close(); err != nil {
			t.Fatal(err)
		}
		ref := strictMaintenanceRef(t, workspace)
		handle, err := PrepareDiscard(ref)
		if err != nil {
			t.Fatal(err)
		}
		disposition, discardErr := handle.Discard()
		if disposition != DiscardCleanupPending || !errors.Is(discardErr, errDiscardTreeBudget) {
			t.Fatalf("wide discard = %v, %v; want bounded cleanup pending", disposition, discardErr)
		}
		quarantine := filepath.Join(ref.OutputRoot, SessionsDirectoryName, discardQuarantinePrefix+ref.SessionID)
		guard := filepath.Join(ref.OutputRoot, SessionsDirectoryName, discardGuardPrefix+ref.SessionID)
		if _, err := os.Stat(quarantine); err != nil {
			t.Fatalf("wide cleanup lost quarantine evidence: %v", err)
		}
		if _, err := os.Stat(guard); err != nil {
			t.Fatalf("wide cleanup lost guard evidence: %v", err)
		}
		root, err := ValidateOutputRoot(ref.OutputRoot)
		if err != nil {
			t.Fatal(err)
		}
		result, err := CollectOrphans(root.CanonicalPath, root.Identity, nil, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Collected) != 1 || result.Collected[0] != ref.SessionID || len(result.CleanupPending) != 0 {
			t.Fatalf("wide GC result = %#v, want one collected session after a second bounded pass", result)
		}
	})

	t.Run("deep", func(t *testing.T) {
		previousDepth := discardTreeDepthLimit
		discardTreeDepthLimit = 8
		t.Cleanup(func() { discardTreeDepthLimit = previousDepth })
		workspace, _ := createTestWorkspace(t)
		path := workspace.Path()
		for index := 0; index <= discardTreeDepthLimit; index++ {
			path = filepath.Join(path, "d")
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := workspace.Close(); err != nil {
			t.Fatal(err)
		}
		ref := strictMaintenanceRef(t, workspace)
		handle, err := PrepareDiscard(ref)
		if err != nil {
			t.Fatal(err)
		}
		disposition, discardErr := handle.Discard()
		if disposition != DiscardReconciliation || !errors.Is(discardErr, ErrNeedsReconciliation) || errors.Is(discardErr, errDiscardTreeBudget) {
			t.Fatalf("deep discard = %v, %v; want fail-closed reconciliation without a no-progress budget error", disposition, discardErr)
		}
		quarantine := filepath.Join(ref.OutputRoot, SessionsDirectoryName, discardQuarantinePrefix+ref.SessionID)
		if _, err := os.Stat(quarantine); err != nil {
			t.Fatalf("deep cleanup lost quarantine evidence: %v", err)
		}
	})
}

func TestDiscardTreeBudgetMakesMonotonicProgressAcrossAttempts(t *testing.T) {
	previousLimit := discardTreeEntryLimit
	discardTreeEntryLimit = 2
	t.Cleanup(func() { discardTreeEntryLimit = previousLimit })
	workspace, _ := createTestWorkspace(t)
	for index := 0; index < 5; index++ {
		if err := os.WriteFile(filepath.Join(workspace.Path(), fmt.Sprintf("payload-%d", index)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	ref := strictMaintenanceRef(t, workspace)
	sawPending := false
	for attempt := 0; attempt < 8; attempt++ {
		handle, err := PrepareDiscard(ref)
		if err != nil {
			t.Fatalf("prepare attempt %d: %v", attempt, err)
		}
		disposition, discardErr := handle.Discard()
		if disposition == DiscardCleanupPending {
			sawPending = true
			if !errors.Is(discardErr, errDiscardTreeBudget) {
				t.Fatalf("attempt %d error = %v; want budget exhaustion", attempt, discardErr)
			}
			continue
		}
		if disposition != Discarded || discardErr != nil {
			t.Fatalf("attempt %d = %v, %v; want final discard", attempt, disposition, discardErr)
		}
		if !sawPending {
			t.Fatal("bounded tree did not require an intermediate cleanup-pending attempt")
		}
		if _, err := os.Stat(workspace.Path()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("workspace remains after monotonic cleanup: %v", err)
		}
		return
	}
	t.Fatal("bounded cleanup did not converge across repeated attempts")
}

func TestDiscardTraversalRejectsDirectorySymlinkSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a directory symlink requires elevated Windows privileges")
	}
	workspace, _ := createTestWorkspace(t)
	nested := filepath.Join(workspace.Path(), "nested")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideSentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(outsideSentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "payload"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	ref := strictMaintenanceRef(t, workspace)
	previous := discardBeforeChildOpen
	fired := false
	discardBeforeChildOpen = func(path string) {
		if filepath.Base(path) != "nested" || fired {
			return
		}
		fired = true
		backup := path + ".original"
		if err := os.Rename(path, backup); err != nil {
			t.Fatalf("swap original directory: %v", err)
		}
		if err := os.Symlink(outside, path); err != nil {
			t.Fatalf("swap directory to symlink: %v", err)
		}
	}
	t.Cleanup(func() { discardBeforeChildOpen = previous })
	handle, err := PrepareDiscard(ref)
	if err != nil {
		t.Fatal(err)
	}
	disposition, discardErr := handle.Discard()
	if !fired {
		t.Fatal("directory swap hook did not fire")
	}
	if disposition == Discarded || discardErr == nil {
		t.Fatalf("symlink swap discard = %v, %v; want durable failure", disposition, discardErr)
	}
	if disposition != DiscardCleanupPending && disposition != DiscardReconciliation {
		t.Fatalf("symlink swap disposition = %v; want pending or reconciliation", disposition)
	}
	if _, err := os.Stat(outsideSentinel); err != nil {
		t.Fatalf("outside sentinel was touched by traversal: %v", err)
	}
}

func TestDiscardTraversalRejectsDirectoryReplacement(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	nested := filepath.Join(workspace.Path(), "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "payload"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	ref := strictMaintenanceRef(t, workspace)
	outside := filepath.Join(filepath.Dir(workspace.Path()), "outside-swap")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideSentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(outsideSentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := discardBeforeChildOpen
	fired := false
	discardBeforeChildOpen = func(path string) {
		if filepath.Base(path) != "nested" || fired {
			return
		}
		fired = true
		if err := os.Rename(path, path+".original"); err != nil {
			t.Fatalf("replace original directory: %v", err)
		}
		if err := os.Rename(outside, path); err != nil {
			t.Fatalf("install replacement directory: %v", err)
		}
	}
	t.Cleanup(func() { discardBeforeChildOpen = previous })
	handle, err := PrepareDiscard(ref)
	if err != nil {
		t.Fatal(err)
	}
	disposition, discardErr := handle.Discard()
	if !fired {
		t.Fatal("directory replacement hook did not fire")
	}
	if disposition == Discarded || discardErr == nil {
		t.Fatalf("directory replacement discard = %v, %v; want durable failure", disposition, discardErr)
	}
	if disposition != DiscardCleanupPending && disposition != DiscardReconciliation {
		t.Fatalf("directory replacement disposition = %v; want pending or reconciliation", disposition)
	}
	quarantine := filepath.Join(ref.OutputRoot, SessionsDirectoryName, discardQuarantinePrefix+ref.SessionID)
	replacement := filepath.Join(quarantine, "nested")
	if _, err := os.Stat(replacement); err != nil {
		t.Fatalf("replacement directory was removed: %v", err)
	}
	if err := os.Rename(replacement, outside); err != nil {
		t.Fatalf("restore replacement directory: %v", err)
	}
	if _, err := os.Stat(outsideSentinel); err != nil {
		t.Fatalf("outside sentinel was touched by traversal: %v", err)
	}
}

func TestDiscardTraversalRejectsHardlinkedFile(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	outside := filepath.Join(t.TempDir(), "outside-file")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(workspace.Path(), "hardlink")
	if err := os.Link(outside, linkPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	ref := strictMaintenanceRef(t, workspace)
	handle, err := PrepareDiscard(ref)
	if err != nil {
		t.Fatal(err)
	}
	disposition, discardErr := handle.Discard()
	if disposition == Discarded || discardErr == nil {
		t.Fatalf("hard-link discard = %v, %v; want durable failure", disposition, discardErr)
	}
	if disposition != DiscardCleanupPending && disposition != DiscardReconciliation {
		t.Fatalf("hard-link disposition = %v; want pending or reconciliation", disposition)
	}
	contents, err := os.ReadFile(outside)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("outside hard-link target changed: contents=%q err=%v", contents, err)
	}
}

func TestRootIdentityRejectsReplacement(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "output")
	if err := os.Mkdir(rootPath, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := ValidateOutputRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(parent, "old-output")
	if err := os.Rename(rootPath, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRootRef(root); err == nil {
		t.Fatal("replaced root identity was accepted")
	}
}

func TestDiscardFaultsAreRestartable(t *testing.T) {
	tests := []struct {
		name      string
		configure func(error) func()
		addChild  bool
	}{
		{
			name: "marker",
			configure: func(fault error) func() {
				previous := discardFault
				fired := false
				discardFault = func(point discardFaultPoint) error {
					if point == discardFaultAfterMarker && !fired {
						fired = true
						return fault
					}
					return nil
				}
				return func() { discardFault = previous }
			},
		},
		{
			name: "lease close",
			configure: func(fault error) func() {
				previous := discardCloseLease
				fired := false
				discardCloseLease = func(lease *workspaceLease) error {
					if !fired {
						fired = true
						return fault
					}
					return lease.Close()
				}
				return func() { discardCloseLease = previous }
			},
		},
		{
			name: "rename",
			configure: func(fault error) func() {
				previous := discardRename
				fired := false
				discardRename = func(oldPath, newPath string) error {
					if !fired {
						fired = true
						return fault
					}
					return previous(oldPath, newPath)
				}
				return func() { discardRename = previous }
			},
		},
		{
			name:     "child removal",
			addChild: true,
			configure: func(fault error) func() {
				previous := discardFault
				fired := false
				discardFault = func(point discardFaultPoint) error {
					if point == discardFaultChildRemoval && !fired {
						fired = true
						return fault
					}
					return nil
				}
				return func() { discardFault = previous }
			},
		},
		{
			name: "directory sync",
			configure: func(fault error) func() {
				previous := discardSyncDirectory
				fired := false
				discardSyncDirectory = func(path string) error {
					if !fired {
						fired = true
						return fault
					}
					return syncDirectory(path)
				}
				return func() { discardSyncDirectory = previous }
			},
		},
		{
			name: "quarantine removal",
			configure: func(fault error) func() {
				previous := discardFault
				fired := false
				discardFault = func(point discardFaultPoint) error {
					if point == discardFaultQuarantineRemoval && !fired {
						fired = true
						return fault
					}
					return nil
				}
				return func() { discardFault = previous }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace, _ := createTestWorkspace(t)
			if test.addChild {
				if err := os.WriteFile(filepath.Join(workspace.Path(), "payload.bin"), []byte("payload"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := workspace.Close(); err != nil {
				t.Fatal(err)
			}
			ref := strictMaintenanceRef(t, workspace)
			handle, err := PrepareDiscard(ref)
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected discard fault")
			restore := test.configure(injected)
			disposition, discardErr := handle.Discard()
			restore()
			if disposition == Discarded || discardErr == nil {
				t.Fatalf("first discard = %v, %v; want durable failed attempt", disposition, discardErr)
			}

			retry, err := PrepareDiscard(ref)
			if err != nil {
				t.Fatalf("reopen after %s fault: %v", test.name, err)
			}
			disposition, discardErr = retry.Discard()
			if disposition != Discarded || discardErr != nil {
				t.Fatalf("retry after %s fault = %v, %v; want discarded", test.name, disposition, discardErr)
			}
			for _, path := range []string{
				workspace.Path(),
				filepath.Join(ref.OutputRoot, SessionsDirectoryName, discardQuarantinePrefix+ref.SessionID),
				filepath.Join(ref.OutputRoot, SessionsDirectoryName, discardGuardPrefix+ref.SessionID),
			} {
				if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("recovery left %q: %v", path, statErr)
				}
			}
		})
	}
}

func TestCollectOrphansSettlesQuarantineAndGuardRecovery(t *testing.T) {
	t.Run("quarantine", func(t *testing.T) {
		workspace, _ := createTestWorkspace(t)
		if err := workspace.Close(); err != nil {
			t.Fatal(err)
		}
		ref := strictMaintenanceRef(t, workspace)
		handle, err := PrepareDiscard(ref)
		if err != nil {
			t.Fatal(err)
		}
		previous := discardFault
		fired := false
		discardFault = func(point discardFaultPoint) error {
			if point == discardFaultQuarantineRemoval && !fired {
				fired = true
				return errors.New("quarantine removal interrupted")
			}
			return nil
		}
		disposition, discardErr := handle.Discard()
		discardFault = previous
		if disposition != DiscardCleanupPending || discardErr == nil {
			t.Fatalf("discard = %v, %v; want cleanup pending", disposition, discardErr)
		}
		quarantine := filepath.Join(ref.OutputRoot, SessionsDirectoryName, discardQuarantinePrefix+ref.SessionID)
		if _, err := os.Stat(quarantine); err != nil {
			t.Fatalf("quarantine evidence missing: %v", err)
		}
		root, err := ValidateOutputRoot(ref.OutputRoot)
		if err != nil {
			t.Fatal(err)
		}
		result, err := CollectOrphans(root.CanonicalPath, root.Identity, nil, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Collected) != 1 || result.Collected[0] != ref.SessionID {
			t.Fatalf("collection result = %#v, want quarantine recovered", result)
		}
		if _, err := os.Stat(quarantine); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("quarantine remains after GC: %v", err)
		}
	})

	t.Run("guard-only", func(t *testing.T) {
		workspace, _ := createTestWorkspace(t)
		if err := workspace.Close(); err != nil {
			t.Fatal(err)
		}
		ref := strictMaintenanceRef(t, workspace)
		guardPath := filepath.Join(ref.OutputRoot, SessionsDirectoryName, discardGuardPrefix+ref.SessionID)
		previous := discardRemove
		discardRemove = func(path string) error {
			if path == guardPath {
				return errors.New("guard removal interrupted")
			}
			return previous(path)
		}
		handle, err := PrepareDiscard(ref)
		if err != nil {
			discardRemove = previous
			t.Fatal(err)
		}
		disposition, discardErr := handle.Discard()
		discardRemove = previous
		if disposition != DiscardCleanupPending || discardErr == nil {
			t.Fatalf("discard = %v, %v; want cleanup pending", disposition, discardErr)
		}
		if _, err := os.Stat(guardPath); err != nil {
			t.Fatalf("guard evidence missing: %v", err)
		}
		// Model a crash after the guard's record and lease entries were
		// removed but before the empty guard directory was removed.
		if err := os.Remove(filepath.Join(guardPath, discardGuardRecordName)); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(guardPath, LeaseFileName)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		root, err := ValidateOutputRoot(ref.OutputRoot)
		if err != nil {
			t.Fatal(err)
		}
		result, err := CollectOrphans(root.CanonicalPath, root.Identity, nil, time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Collected) != 1 || result.Collected[0] != ref.SessionID {
			t.Fatalf("collection result = %#v, want guard recovery", result)
		}
		if _, err := os.Stat(guardPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("guard remains after GC: %v", err)
		}
	})
}

func TestCollectOrphansPreservesMalformedDiscardEvidence(t *testing.T) {
	workspace, _ := createTestWorkspace(t)
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	sessionsRoot := filepath.Join(workspace.Ref().OutputRoot, SessionsDirectoryName)
	unknown := filepath.Join(sessionsRoot, discardQuarantinePrefix+"not-a-session")
	if err := os.Mkdir(unknown, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unknown, "evidence"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := ValidateOutputRoot(workspace.Ref().OutputRoot)
	if err != nil {
		t.Fatal(err)
	}
	result, err := CollectOrphans(root.CanonicalPath, root.Identity, nil, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped == 0 {
		t.Fatalf("collection result = %#v, want malformed evidence skipped", result)
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatalf("malformed evidence was removed: %v", err)
	}
}
