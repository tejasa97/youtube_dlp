//go:build windows

package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
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
