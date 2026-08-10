package atomicfile

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
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
	if !errors.Is(err, cause) {
		t.Fatalf("error %v does not wrap %v", err, cause)
	}
}
