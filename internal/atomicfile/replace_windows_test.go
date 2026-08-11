//go:build windows

package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// This native test covers the writable source handle required by
// FlushFileBuffers and both Windows replacement paths.
func TestReplaceWindowsWritableSyncHandle(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "destination")
	for _, content := range []string{"first creation", "existing replacement"} {
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

func TestWriteWindowsReadOnlyTempCleanupAfterPreCommitFailure(t *testing.T) {
	injected := errors.New("injected pre-commit failure")
	tests := []struct {
		name      string
		configure func(*fileOps)
	}{
		{
			name: "sync failure",
			configure: func(ops *fileOps) {
				ops.syncFile = func(*os.File) error { return injected }
			},
		},
		{
			name: "replacement failure",
			configure: func(ops *fileOps) {
				ops.replaceFile = func(string, string) error { return injected }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "state.json")
			ops := productionOps
			test.configure(&ops)
			err := write(path, 0o400, func(writer io.Writer) error {
				_, err := io.WriteString(writer, "candidate")
				return err
			}, ops)
			assertCommitOutcome(t, err, false, false, injected)
			matches, globErr := filepath.Glob(filepath.Join(directory, ".atomic-*"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("leaked read-only temporary files: %v", matches)
			}
		})
	}
}

func TestHandleMoveFileFailureClassifiesAuthority(t *testing.T) {
	moveErr := syscall.Errno(1234)
	tests := []struct {
		name              string
		sourceExists      bool
		destinationExists bool
		committed         bool
		indeterminate     bool
	}{
		{
			name:              "source remains",
			sourceExists:      true,
			destinationExists: true,
		},
		{
			name:              "destination only",
			destinationExists: true,
			committed:         true,
		},
		{
			name:          "both names missing",
			indeterminate: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := handleMoveFileFailure(moveErr, "source", "destination", moveRecoveryOps{
				sourceExists: func(string) (bool, error) {
					return test.sourceExists, nil
				},
				destinationExists: func(string) (bool, error) {
					return test.destinationExists, nil
				},
			})
			assertCommitOutcome(t, err, test.committed, test.indeterminate, moveErr)
		})
	}
}

func TestHandleMoveFileFailureInspectionErrorIsIndeterminate(t *testing.T) {
	moveErr := errors.New("move failed")
	inspectErr := errors.New("inspection failed")
	err := handleMoveFileFailure(moveErr, "source", "destination", moveRecoveryOps{
		sourceExists: func(string) (bool, error) {
			return false, inspectErr
		},
		destinationExists: func(string) (bool, error) {
			return true, nil
		},
	})
	assertCommitOutcome(t, err, false, true, moveErr)
	if !errors.Is(err, inspectErr) {
		t.Fatalf("error %v does not wrap inspection failure", err)
	}
}
