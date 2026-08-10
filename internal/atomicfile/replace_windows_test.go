//go:build windows

package atomicfile

import (
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
