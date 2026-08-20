//go:build !windows

package ffmpeg

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/tejasa97/ytdlp-go/internal/events"
)

var (
	unixProcessMode     = flag.String("ytdlp-ffmpeg-unix-process-mode", "", "internal ffmpeg process test mode")
	unixProcessChildPID = flag.String("ytdlp-ffmpeg-unix-process-child-pid", "", "internal ffmpeg process child pid file")
	unixProcessMarker   = flag.String("ytdlp-ffmpeg-unix-process-marker", "", "internal ffmpeg process marker")
)

// TestUnixProcessHelper is invoked as a subprocess by the cancellation tests.
// The child deliberately remains in the parent's process group, as ffmpeg
// protocol helpers do, so the test verifies group rather than parent-only
// termination.
func TestUnixProcessHelper(t *testing.T) {
	switch *unixProcessMode {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=TestUnixProcessHelper", "-ytdlp-ffmpeg-unix-process-mode=child", "-ytdlp-ffmpeg-unix-process-marker="+*unixProcessMarker)
		if err := child.Start(); err != nil {
			os.Exit(21)
		}
		if err := os.WriteFile(*unixProcessChildPID, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			os.Exit(22)
		}
		if !waitForUnixProcessMarker(*unixProcessMarker, 2*time.Second) {
			os.Exit(24)
		}
		fmt.Println("progress=ready")
		select {}
	case "child":
		if err := os.WriteFile(*unixProcessMarker, []byte("started"), 0o600); err != nil {
			os.Exit(23)
		}
		select {}
	case "parent-exit":
		child := exec.Command(os.Args[0], "-test.run=TestUnixProcessHelper", "-ytdlp-ffmpeg-unix-process-mode=child-detached", "-ytdlp-ffmpeg-unix-process-marker="+*unixProcessMarker)
		if err := child.Start(); err != nil {
			os.Exit(25)
		}
		if err := os.WriteFile(*unixProcessChildPID, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
			os.Exit(26)
		}
		if !waitForUnixProcessMarker(*unixProcessMarker, 2*time.Second) {
			os.Exit(27)
		}
	case "child-detached":
		_ = os.Stdin.Close()
		_ = os.Stdout.Close()
		_ = os.Stderr.Close()
		if err := os.WriteFile(*unixProcessMarker, []byte("started"), 0o600); err != nil {
			os.Exit(28)
		}
		select {}
	}
}

func TestExecuteCancellationTerminatesUnixProcessGroup(t *testing.T) {
	tools := testProcessToolset()
	directory := t.TempDir()
	childPIDPath := filepath.Join(directory, "child.pid")
	markerPath := filepath.Join(directory, "child.started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := tools.execute(ctx, os.Args[0], unixProcessHelperArgs("parent", childPIDPath, markerPath), func(line string) error {
		if line == "progress=ready" {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execute() error = %v", err)
	}
	childPID := readProcessPID(t, childPIDPath)
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("descendant did not start: %v", err)
	}
	assertProcessGone(t, childPID)
}

func TestExecuteCallbackFailureTerminatesUnixProcessGroup(t *testing.T) {
	tools := testProcessToolset()
	directory := t.TempDir()
	childPIDPath := filepath.Join(directory, "child.pid")
	markerPath := filepath.Join(directory, "child.started")
	want := errors.New("progress observer failed")

	_, err := tools.execute(context.Background(), os.Args[0], unixProcessHelperArgs("parent", childPIDPath, markerPath), func(line string) error {
		if line == "progress=ready" {
			return want
		}
		return nil
	})
	if !errors.Is(err, want) || errors.Is(err, context.Canceled) {
		t.Fatalf("execute() error = %v, want callback error", err)
	}
	assertProcessGone(t, readProcessPID(t, childPIDPath))
}

func TestExecuteNormalParentExitTerminatesUnixProcessGroup(t *testing.T) {
	tools := testProcessToolset()
	directory := t.TempDir()
	childPIDPath := filepath.Join(directory, "child.pid")
	markerPath := filepath.Join(directory, "child.started")

	if _, err := tools.execute(context.Background(), os.Args[0], unixProcessHelperArgs("parent-exit", childPIDPath, markerPath), nil); err != nil {
		t.Fatalf("execute() error = %v", err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("detached descendant did not start: %v", err)
	}
	assertProcessGone(t, readProcessPID(t, childPIDPath))
}

func TestExecuteCanceledBeforeStartLaunchesNothing(t *testing.T) {
	tools := testProcessToolset()
	markerPath := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tools.execute(ctx, os.Args[0], []string{"-test.run=TestUnixProcessHelper", "-ytdlp-ffmpeg-unix-process-mode=child", "-ytdlp-ffmpeg-unix-process-marker=" + markerPath}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execute() error = %v", err)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled command launched: marker stat = %v", err)
	}
}

func TestExecuteCancellationExitRace(t *testing.T) {
	tools := testProcessToolset()
	for iteration := 0; iteration < 40; iteration++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancelDone := make(chan struct{})
		go func(delay time.Duration) {
			defer close(cancelDone)
			time.Sleep(delay)
			cancel()
		}(time.Duration(iteration%3) * time.Millisecond)
		_, err := tools.execute(ctx, "/bin/sh", []string{"-c", "printf 'progress=done\\n'"}, nil)
		<-cancelDone
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d: execute() error = %v", iteration, err)
		}
	}
}

func TestAtomicCancellationPreservesSourceAndDestination(t *testing.T) {
	directory := t.TempDir()
	tools := &Toolset{ffmpeg: writeAtomicProcessHelper(t, directory), maxOutput: 1 << 20}
	input := filepath.Join(directory, "source.m4a")
	destination := filepath.Join(directory, "destination.mka")
	if err := os.WriteFile(input, []byte("source media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("published media"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindPostprocessProgress {
			cancel()
		}
		return nil
	})
	err := tools.runAtomic(ctx, destination, true, sink, func(temporary string) []string {
		return []string{"--ytdlp-input=" + input, "--ytdlp-output=" + temporary}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runAtomic() error = %v", err)
	}
	for path, want := range map[string]string{input: "source media", destination: "published media"} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", path, got, readErr, want)
		}
	}
	remaining, err := filepath.Glob(filepath.Join(directory, ".ytdlp-postprocess-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("incomplete processing output remains: %v", remaining)
	}
}

func testProcessToolset() *Toolset {
	return &Toolset{ffmpeg: os.Args[0], maxOutput: 1 << 20}
}

func unixProcessHelperArgs(mode, childPIDPath, markerPath string) []string {
	return []string{
		"-test.run=TestUnixProcessHelper",
		"-ytdlp-ffmpeg-unix-process-mode=" + mode,
		"-ytdlp-ffmpeg-unix-process-child-pid=" + childPIDPath,
		"-ytdlp-ffmpeg-unix-process-marker=" + markerPath,
	}
}

func readProcessPID(t *testing.T, path string) int {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(value))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid child pid %q: %v", value, err)
	}
	return pid
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			t.Fatalf("inspect descendant %d: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant %d survived cancellation", pid)
}

func waitForUnixProcessMarker(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func writeAtomicProcessHelper(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "ffmpeg-helper.sh")
	const script = `#!/bin/sh
for argument do
  case "$argument" in
    --ytdlp-input=*) input=${argument#--ytdlp-input=} ;;
    --ytdlp-output=*) output=${argument#--ytdlp-output=} ;;
  esac
done
test -f "$input" || exit 91
cat "$input" > /dev/null || exit 92
printf partial > "$output"
printf 'progress=ready\n'
while :; do sleep 1; done
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
