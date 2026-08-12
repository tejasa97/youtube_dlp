package atomicfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
)

func TestWriteCreatesAndReplacesWholeFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")

	writeContent(t, path, "old")
	writeContent(t, path, "new checkpoint")

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new checkpoint" {
		t.Fatalf("content = %q, want new checkpoint", content)
	}
}

func TestWriteWithTempSecuritySecuresTemporaryBeforeEncoding(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	secured := false
	if err := WriteWithTempSecurity(path, 0o600, func(writer io.Writer) error {
		if !secured {
			return errors.New("temporary file was not secured before encoding")
		}
		_, err := io.WriteString(writer, "complete")
		return err
	}, func(temp string) error {
		info, err := os.Stat(temp)
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("temporary mode = %o, want private", info.Mode().Perm())
		}
		secured = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "complete" {
		t.Fatalf("content = %q, err = %v", content, err)
	}
}

func TestWriteReadersObserveOnlyWholeImages(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	oldImage := string(make([]byte, 64*1024))
	newImage := string(bytes.Repeat([]byte{0xff}, 64*1024))
	writeContent(t, path, oldImage)

	var waitGroup sync.WaitGroup
	start := make(chan struct{})
	errorsSeen := make(chan error, 1)
	for range 4 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			for range 100 {
				content, err := os.ReadFile(path)
				if err != nil {
					select {
					case errorsSeen <- err:
					default:
					}
					return
				}
				if string(content) != oldImage && string(content) != newImage {
					select {
					case errorsSeen <- errors.New("reader observed partial image"):
					default:
					}
					return
				}
			}
		}()
	}
	close(start)
	writeContent(t, path, newImage)
	waitGroup.Wait()
	select {
	case err := <-errorsSeen:
		t.Fatal(err)
	default:
	}
}

func TestWriteFailureCommitOutcomes(t *testing.T) {
	injected := errors.New("injected failure")
	tests := []struct {
		name      string
		configure func(*fileOps)
		committed bool
	}{
		{
			name: "temporary write",
			configure: func(ops *fileOps) {
				ops.writeTemp = func(io.Writer, func(io.Writer) error) error { return injected }
			},
		},
		{
			name: "file sync",
			configure: func(ops *fileOps) {
				ops.syncFile = func(*os.File) error { return injected }
			},
		},
		{
			name: "replacement",
			configure: func(ops *fileOps) {
				ops.replaceFile = func(string, string) error { return injected }
			},
		},
		{
			name: "parent sync",
			configure: func(ops *fileOps) {
				ops.syncParent = func(string) error { return injected }
			},
			committed: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			ops := productionOps
			test.configure(&ops)
			err := write(path, 0o600, func(writer io.Writer) error {
				_, err := io.WriteString(writer, "new")
				return err
			}, ops)
			assertCommitError(t, err, test.committed, injected)

			content, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			want := "old"
			if test.committed {
				want = "new"
			}
			if string(content) != want {
				t.Fatalf("content = %q, want %q", content, want)
			}
		})
	}
}

func TestWriteKeepsTemporaryOwnerOnlyUntilEncodingCompletes(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	if err := Write(path, 0o644, func(writer io.Writer) error {
		matches, err := filepath.Glob(filepath.Join(directory, ".atomic-*"))
		if err != nil {
			return err
		}
		if len(matches) != 1 {
			return fmt.Errorf("temporary file count = %d, want 1", len(matches))
		}
		info, err := os.Stat(matches[0])
		if err != nil {
			return err
		}
		if info.Mode().Perm() != 0o600 {
			return fmt.Errorf("temporary mode = %o, want 600", info.Mode().Perm())
		}
		_, err = io.WriteString(writer, "complete")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("committed mode = %o, want 644", info.Mode().Perm())
	}
}

func TestWriteRetainsTemporaryOnIndeterminateReplacement(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ops := productionOps
	var temporaryPath string
	cleanupCalled := false
	replacementErr := errors.New("authority unknown")
	ops.replaceFile = func(source, destination string) error {
		temporaryPath = source
		return indeterminateFailure("injected replacement", replacementErr)
	}
	ops.cleanupTemp = func(string) error {
		cleanupCalled = true
		return errors.New("indeterminate evidence must not be cleaned")
	}
	err := write(path, 0o600, func(writer io.Writer) error {
		_, err := io.WriteString(writer, "candidate")
		return err
	}, ops)
	assertCommitOutcome(t, err, false, true, replacementErr)
	if temporaryPath == "" {
		t.Fatal("replacement seam was not called")
	}
	if cleanupCalled {
		t.Fatal("cleanup was called for indeterminate evidence")
	}
	content, readErr := os.ReadFile(temporaryPath)
	if readErr != nil {
		t.Fatalf("indeterminate candidate was not retained: %v", readErr)
	}
	if string(content) != "candidate" {
		t.Fatalf("retained candidate = %q, want candidate", content)
	}
	old, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(old) != "old" {
		t.Fatalf("destination = %q, want old in injected scenario", old)
	}
	if removeErr := os.Remove(temporaryPath); removeErr != nil {
		t.Fatal(removeErr)
	}
}

func TestWriteReportsPreCommitCleanupFailure(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	operationErr := errors.New("sync failed")
	cleanupErr := errors.New("cleanup failed")
	ops := productionOps
	ops.syncFile = func(*os.File) error { return operationErr }
	ops.cleanupTemp = func(string) error { return cleanupErr }

	err := write(path, 0o400, func(writer io.Writer) error {
		_, err := io.WriteString(writer, "candidate")
		return err
	}, ops)
	assertCommitOutcome(t, err, false, false, operationErr)
	if !errors.Is(err, cleanupErr) {
		t.Fatalf("error %v does not wrap cleanup failure", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(directory, ".atomic-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 1 {
		t.Fatalf("temporary file count = %d, want 1 after injected cleanup failure", len(matches))
	}
	if chmodErr := os.Chmod(matches[0], 0o600); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if removeErr := os.Remove(matches[0]); removeErr != nil {
		t.Fatal(removeErr)
	}
}

func TestHandleExistingReplaceFailureRecoversOldAuthority(t *testing.T) {
	restored := false
	err := handleExistingReplaceResult(errorUnableToMoveReplacement2, "backup", "destination", replaceRecoveryOps{
		backupExists:      func(string) (bool, error) { return true, nil },
		destinationExists: func(string) (bool, error) { return true, nil },
		restoreBackup: func(backup, destination string) error {
			restored = backup == "backup" && destination == "destination"
			return nil
		},
		removeBackup: func(string) error { return nil },
	})
	assertCommitOutcome(t, err, false, false, errorUnableToMoveReplacement2)
	if !restored {
		t.Fatal("backup was not restored")
	}
}

func TestHandleExistingReplaceFailureReportsIndeterminateRecovery(t *testing.T) {
	recoveryErr := errors.New("recovery failed")
	err := handleExistingReplaceResult(errorUnableToMoveReplacement2, "backup", "destination", replaceRecoveryOps{
		backupExists:      func(string) (bool, error) { return true, nil },
		destinationExists: func(string) (bool, error) { return true, nil },
		restoreBackup:     func(string, string) error { return recoveryErr },
		removeBackup:      func(string) error { return nil },
	})
	assertCommitOutcome(t, err, false, true, errorUnableToMoveReplacement2)
	if !errors.Is(err, recoveryErr) {
		t.Fatalf("error %v does not wrap recovery failure", err)
	}
}

func TestHandleExistingReplaceUnexpectedBackupIsIndeterminateAndNotRestored(t *testing.T) {
	for _, replaceErr := range []error{errorUnableToMoveReplacement, syscall.EIO} {
		t.Run(replaceErr.Error(), func(t *testing.T) {
			restored := false
			err := handleExistingReplaceResult(replaceErr, "backup", "destination", replaceRecoveryOps{
				backupExists:      func(string) (bool, error) { return true, nil },
				destinationExists: func(string) (bool, error) { return true, nil },
				restoreBackup: func(string, string) error {
					restored = true
					return nil
				},
				removeBackup: func(string) error { return nil },
			})
			assertCommitOutcome(t, err, false, true, replaceErr)
			if restored {
				t.Fatal("unexpected backup was restored over destination")
			}
		})
	}
}

func TestHandleExistingReplace1176WithoutBackupConfirmsDestination(t *testing.T) {
	err := handleExistingReplaceResult(errorUnableToMoveReplacement, "backup", "destination", replaceRecoveryOps{
		backupExists:      func(string) (bool, error) { return false, nil },
		destinationExists: func(string) (bool, error) { return true, nil },
		restoreBackup:     func(string, string) error { return nil },
		removeBackup:      func(string) error { return nil },
	})
	assertCommitOutcome(t, err, false, false, errorUnableToMoveReplacement)
}

func TestHandleExistingReplaceReportsIndeterminateMissingAuthority(t *testing.T) {
	for _, test := range []struct {
		name              string
		replaceErr        error
		destinationExists bool
	}{
		{"1176 destination missing", errorUnableToMoveReplacement, false},
		{"1177 backup missing", errorUnableToMoveReplacement2, true},
		{"other error destination missing", syscall.EIO, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := handleExistingReplaceResult(test.replaceErr, "backup", "destination", replaceRecoveryOps{
				backupExists:      func(string) (bool, error) { return false, nil },
				destinationExists: func(string) (bool, error) { return test.destinationExists, nil },
				restoreBackup:     func(string, string) error { return nil },
				removeBackup:      func(string) error { return nil },
			})
			assertCommitOutcome(t, err, false, true, test.replaceErr)
		})
	}
}

func TestHandleExistingReplaceSuccessReportsCommittedCleanupFailure(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	err := handleExistingReplaceResult(nil, "backup", "destination", replaceRecoveryOps{
		backupExists:      func(string) (bool, error) { return true, nil },
		destinationExists: func(string) (bool, error) { return true, nil },
		restoreBackup:     func(string, string) error { return nil },
		removeBackup:      func(string) error { return cleanupErr },
	})
	assertCommitOutcome(t, err, true, false, cleanupErr)
}

func TestReplaceCreatesAndReplaces(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "output")

	for _, content := range []string{"created", "replaced"} {
		source := filepath.Join(directory, "source")
		if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Replace(source, destination); err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(destination)
		if err != nil {
			t.Fatal(err)
		}
		if string(actual) != content {
			t.Fatalf("content = %q, want %q", actual, content)
		}
	}
}

func TestReplaceSyncsBothParentsAfterCrossDirectoryCommit(t *testing.T) {
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "source")
	destinationDirectory := filepath.Join(root, "destination")
	if err := os.Mkdir(sourceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destinationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDirectory, "file")
	destination := filepath.Join(destinationDirectory, "file")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	var synced []string
	ops := productionOps
	ops.syncParent = func(path string) error {
		synced = append(synced, path)
		return nil
	}
	if err := replace(source, destination, ops); err != nil {
		t.Fatal(err)
	}
	if len(synced) != 2 || synced[0] != destinationDirectory || synced[1] != sourceDirectory {
		t.Fatalf("synced parents = %v", synced)
	}
}

func TestReplaceReportsPostCommitSourceParentSyncFailure(t *testing.T) {
	root := t.TempDir()
	sourceDirectory := filepath.Join(root, "source")
	destinationDirectory := filepath.Join(root, "destination")
	if err := os.Mkdir(sourceDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destinationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceDirectory, "file")
	destination := filepath.Join(destinationDirectory, "file")
	if err := os.WriteFile(source, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("source parent sync")
	ops := productionOps
	ops.syncParent = func(path string) error {
		if path == sourceDirectory {
			return injected
		}
		return nil
	}
	err := replace(source, destination, ops)
	assertCommitError(t, err, true, injected)
}

func TestWindowsImplementationIsCoveredByCrossCompile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Log("running native Windows atomic replacement implementation")
	}
}

func writeContent(t *testing.T, path, content string) {
	t.Helper()
	if err := Write(path, 0o600, func(writer io.Writer) error {
		_, err := io.WriteString(writer, content)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func assertCommitError(t *testing.T, err error, committed bool, cause error) {
	assertCommitOutcome(t, err, committed, false, cause)
}

func assertCommitOutcome(t *testing.T, err error, committed, indeterminate bool, cause error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var commitErr CommitError
	if !errors.As(err, &commitErr) {
		t.Fatalf("error %T does not implement CommitError", err)
	}
	if commitErr.Committed() != committed {
		t.Fatalf("Committed() = %v, want %v", commitErr.Committed(), committed)
	}
	if commitErr.Indeterminate() != indeterminate {
		t.Fatalf("Indeterminate() = %v, want %v", commitErr.Indeterminate(), indeterminate)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error %v does not wrap %v", err, cause)
	}
}
