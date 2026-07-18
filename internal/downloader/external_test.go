package downloader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type recordingProcess struct {
	args  []string
	err   error
	write bool
}

func (process *recordingProcess) Run(_ context.Context, _ string, args []string, _ string) ([]byte, error) {
	process.args = append([]string(nil), args...)
	if process.write {
		return []byte("token=secret"), os.WriteFile(args[len(args)-1], []byte("media"), 0o600)
	}
	return []byte("tool failed token=secret"), process.err
}

func TestExternalAdapterUsesArgvAndCategorizesFailures(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "media.bin")
	process := &recordingProcess{write: true}
	result, err := NewExternalAdapter(process).Download(context.Background(), ExternalRequest{Executable: "true", Arguments: []string{"--fixture"}, URL: "https://example.test/media?token=secret", OutputRoot: root, Destination: destination})
	if err != nil || result.Path != destination {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if got, want := process.args[len(process.args)-2:], []string{"https://example.test/media?token=secret", destination}; got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("argv=%q", process.args)
	}
	process = &recordingProcess{err: errors.New("exit 2")}
	_, err = NewExternalAdapter(process).Download(context.Background(), ExternalRequest{Executable: "true", URL: "https://example.test", OutputRoot: root, Destination: filepath.Join(root, "bad")})
	if !errors.Is(err, ErrExternalFailed) {
		t.Fatalf("error=%v", err)
	}
	result, err = NewExternalAdapter(process).Download(context.Background(), ExternalRequest{Executable: "true", URL: "https://example.test", OutputRoot: root, Destination: filepath.Join(root, "bad-again")})
	if err == nil || result.Stderr != "external downloader emitted diagnostics (redacted)" {
		t.Fatalf("diagnostic=%q err=%v", result.Stderr, err)
	}
}
func TestExternalAdapterRejectsUnsafeInputs(t *testing.T) {
	root := t.TempDir()
	_, err := NewExternalAdapter(&recordingProcess{}).Download(context.Background(), ExternalRequest{Executable: "true", URL: "x\ny", OutputRoot: root, Destination: filepath.Join(root, "x")})
	if !errors.Is(err, ErrUnsafeExternalArg) {
		t.Fatalf("error=%v", err)
	}
}
func TestExternalAdapterRejectsInterpreters(t *testing.T) {
	root := t.TempDir()
	for _, tool := range []string{"sh", "bash", "python3", "powershell.exe"} {
		_, err := NewExternalAdapter(&recordingProcess{}).Download(context.Background(), ExternalRequest{Executable: tool, URL: "https://example.test", OutputRoot: root, Destination: filepath.Join(root, "out")})
		if !errors.Is(err, ErrUnsafeExternalTool) {
			t.Fatalf("%s: %v", tool, err)
		}
	}
}
