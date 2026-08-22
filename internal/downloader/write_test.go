package downloader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/events"
)

func TestWriteRejectsSymlinkDestination(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "out.vtt")
	if err := os.Symlink(target, destination); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := New(nil).Write(context.Background(), WriteJob{
		OutputRoot: root, Destination: destination, Payload: []byte("WEBVTT\n"),
		Overwrite: true,
	}, nil)
	if !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("error=%v", err)
	}
}

func TestWriteRejectsDestinationOutsideOutputRoot(t *testing.T) {
	root := t.TempDir()
	_, err := New(nil).Write(context.Background(), WriteJob{
		OutputRoot: root, Destination: filepath.Join(t.TempDir(), "escape.vtt"),
		Payload: []byte("WEBVTT\n"), Overwrite: true,
	}, nil)
	if !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("error=%v", err)
	}
}

func TestWriteEnforcesMaxBytes(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "out.vtt")
	_, err := New(nil).Write(context.Background(), WriteJob{
		OutputRoot: root, Destination: destination, Payload: make([]byte, 32),
		Overwrite: true, MaxBytes: 16,
	}, nil)
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("error=%v", err)
	}
}

func TestWriteFinalizesPayloadAtomically(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "out.vtt")
	result, err := New(nil).Write(context.Background(), WriteJob{
		OutputRoot: root, Destination: destination,
		Payload:   []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nhello\n"),
		Overwrite: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(destination)
	if err != nil || string(body) != "WEBVTT\n\n00:00.000 --> 00:01.000\nhello\n" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if _, err := os.Lstat(destination + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file leaked: %v", err)
	}
	if result.Path != destination || result.Bytes != int64(len(body)) {
		t.Fatalf("result=%#v", result)
	}
}

func TestWriteEmitsStartingAndCompletedEvents(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "out.vtt")
	payload := []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nhello\n")
	var mu sync.Mutex
	var emitted []events.Event
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		mu.Lock()
		defer mu.Unlock()
		emitted = append(emitted, event)
		return nil
	})
	result, err := New(nil).Write(context.Background(), WriteJob{
		OutputRoot: root, Destination: destination,
		Payload: payload, Overwrite: true,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(emitted) != 2 {
		t.Fatalf("emitted %d events, want 2", len(emitted))
	}
	if emitted[0].Kind != events.KindStarting {
		t.Fatalf("first event kind = %q, want %q", emitted[0].Kind, events.KindStarting)
	}
	if emitted[0].Path != destination {
		t.Fatalf("first event path = %q, want %q", emitted[0].Path, destination)
	}
	if emitted[1].Kind != events.KindCompleted {
		t.Fatalf("second event kind = %q, want %q", emitted[1].Kind, events.KindCompleted)
	}
	if emitted[1].Bytes != result.Bytes || emitted[1].Total != result.Bytes {
		t.Fatalf("second event bytes=%d total=%d, want %d", emitted[1].Bytes, emitted[1].Total, result.Bytes)
	}
}

func TestWriteRejectsDestinationExistsWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "out.vtt")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := New(nil).Write(context.Background(), WriteJob{
		OutputRoot: root, Destination: destination,
		Payload: []byte("new"), Overwrite: false,
	}, nil)
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("error=%v", err)
	}
	body, _ := os.ReadFile(destination)
	if string(body) != "old" {
		t.Fatalf("existing file modified: %q", body)
	}
}

func TestWriteOverwritesExistingDestination(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "out.vtt")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := New(nil).Write(context.Background(), WriteJob{
		OutputRoot: root, Destination: destination,
		Payload: []byte("new"), Overwrite: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(destination)
	if err != nil || string(body) != "new" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if result.Bytes != 3 {
		t.Fatalf("result.Bytes=%d, want 3", result.Bytes)
	}
}

func TestWriteCancellation(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "out.vtt")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New(nil).Write(ctx, WriteJob{
		OutputRoot: root, Destination: destination,
		Payload: []byte("WEBVTT\n"), Overwrite: true,
	}, nil)
	if err != nil {
		t.Fatalf("error=%v", err)
	}
}

func TestWriteNilSinkUsesNop(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "out.vtt")
	_, err := New(nil).Write(context.Background(), WriteJob{
		OutputRoot: root, Destination: destination,
		Payload: []byte("WEBVTT\n"), Overwrite: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestWriteRejectsSymlinkInParentDirectory(t *testing.T) {
	root := t.TempDir()
	parentDir := filepath.Join(root, "subdir")
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(root, "evil")
	if err := os.Symlink(parentDir, symlinkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	destination := filepath.Join(symlinkPath, "out.vtt")
	_, err := New(nil).Write(context.Background(), WriteJob{
		OutputRoot: root, Destination: destination,
		Payload: []byte("WEBVTT\n"), Overwrite: true,
	}, nil)
	if !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("error=%v", err)
	}
}

func TestWriteRejectsInvalidLimits(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "out.vtt")
	_, err := New(nil).Write(context.Background(), WriteJob{
		OutputRoot: root, Destination: destination,
		Payload: []byte("WEBVTT\n"), Overwrite: true,
		MaxBytes: maxDirectBytes + 1,
	}, nil)
	if !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("error=%v", err)
	}
	_, err = New(nil).Write(context.Background(), WriteJob{
		OutputRoot: root, Destination: destination,
		Payload: []byte("WEBVTT\n"), Overwrite: true,
		FileAttempts: maxDirectFileRetries + 1,
	}, nil)
	if !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("error=%v", err)
	}
}
