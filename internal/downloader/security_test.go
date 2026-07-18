package downloader

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestValidateJobRejectsInvalidResourceFields(t *testing.T) {
	invalid := []Job{
		{RateLimit: -1}, {MaxBytes: -1}, {ThrottleRate: -1}, {ThrottleWindow: -time.Second}, {ThrottleRestarts: -1}, {FileAttempts: -1}, {FileAttempts: maxDirectFileRetries + 1}, {RetryBaseDelay: maxDirectRetryDelay + time.Second}, {RetryMaxDelay: maxDirectRetryDelay + time.Second}, {RetryBaseDelay: time.Second, RetryMaxDelay: time.Millisecond},
	}
	for _, job := range invalid {
		if !errors.Is(validateJob(job), ErrInvalidLimits) {
			t.Fatalf("job %#v was accepted", job)
		}
	}
}

func TestThrottleDetectorAvoidsOverflow(t *testing.T) {
	now := time.Unix(0, 0)
	detector := newThrottleDetector(math.MaxInt64, time.Nanosecond, func() time.Time { return now })
	now = now.Add(time.Duration(math.MaxInt64))
	if !detector.Observe(1) {
		t.Fatal("large threshold should remain throttled")
	}
}

func TestPartialStateIgnoresPredictableTempAndRejectsUnsafeFinal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "download.part.json")
	if err := os.Symlink(filepath.Join(dir, "target"), path+".tmp"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := savePartialStateOnce(path, partialState{URL: "https://example.test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	unsafe := filepath.Join(dir, "unsafe.json")
	if err := os.Symlink(path, unsafe); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := savePartialStateOnce(unsafe, partialState{}); !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("unsafe state error=%v", err)
	}
}

func TestDestinationRejectsExistingSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "out")
	if err := os.Symlink(target, destination); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := New(nil).Download(context.Background(), Job{URL: "https://example.test", OutputRoot: root, Destination: destination, Overwrite: true}, nil)
	if !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("error=%v", err)
	}
}

func TestFinalizePreservesExistingWindowsDestination(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows rename semantics")
	}
	dir := t.TempDir()
	part := filepath.Join(dir, "part")
	destination := filepath.Join(dir, "out")
	_ = os.WriteFile(part, []byte("new"), 0o600)
	_ = os.WriteFile(destination, []byte("old"), 0o600)
	if err := finalizeOnce(part, destination, true); err == nil {
		t.Fatal("Windows overwrite unexpectedly replaced destination")
	}
	body, _ := os.ReadFile(destination)
	if string(body) != "old" {
		t.Fatalf("destination=%q", body)
	}
}
