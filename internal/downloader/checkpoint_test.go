package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
	"github.com/tejasa97/youtube_dlp/internal/events"
)

const checkpointChunkSize = int(minDirectCheckpointBytes)

func TestCheckpointAuthoritativeBoundaryTruncatesLongerPartial(t *testing.T) {
	data := checkpointData(128 << 10)
	destination := filepath.Join(t.TempDir(), "media.bin")
	partPath := destination + ".part"
	identity := "direct:fixture:1"
	boundaryBytes := int64(64 << 10)
	if err := os.WriteFile(partPath, append(append([]byte(nil), data[:boundaryBytes]...), bytes.Repeat([]byte("tail"), 4096)...), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCheckpointState(t, checkpointStatePath(destination), partialState{
		ResumeIdentity: identity,
		ETag:           `"fixture"`,
		Total:          int64(len(data)),
		CommittedBytes: boundaryBytes,
	})

	doer := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: 32 << 10}
	job := checkpointJob(destination, identity, &CheckpointOptions{
		ResumeBoundary: &Checkpoint{ResumeIdentity: identity, ETag: `"fixture"`, Total: int64(len(data)), CommittedBytes: boundaryBytes},
		EveryBytes:     minDirectCheckpointBytes,
	})
	client := New(doer)
	client.writePartialState = savePartialStateOnce
	result, err := client.Download(context.Background(), job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed {
		t.Fatalf("result = %#v, want resumed", result)
	}
	if got := doer.requestRange(0); got != "bytes=65536-" {
		t.Fatalf("range = %q, want authoritative boundary", got)
	}
	contents, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, data) {
		t.Fatal("downloaded bytes differ from source")
	}
}

func TestCheckpointBoundaryReconstructsMissingLocalState(t *testing.T) {
	data := checkpointData(int64(2 * checkpointChunkSize))
	destination := filepath.Join(t.TempDir(), "media.bin")
	boundaryBytes := minDirectCheckpointBytes
	if err := os.WriteFile(destination+".part", data[:boundaryBytes], 0o600); err != nil {
		t.Fatal(err)
	}
	identity := "direct:fixture:missing-state"
	doer := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: int64(checkpointChunkSize)}
	job := checkpointJob(destination, identity, &CheckpointOptions{
		ResumeBoundary: &Checkpoint{ResumeIdentity: identity, ETag: `"fixture"`, Total: int64(len(data)), CommittedBytes: boundaryBytes},
		EveryBytes:     minDirectCheckpointBytes,
	})
	result, err := New(doer).Download(context.Background(), job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed || doer.requestRange(0) != fmt.Sprintf("bytes=%d-", boundaryBytes) {
		t.Fatalf("missing-state boundary was not resumed: result=%#v range=%q", result, doer.requestRange(0))
	}
}

func TestCheckpointBoundaryRequiresExactJobIdentity(t *testing.T) {
	root := t.TempDir()
	job := Job{
		URL:            "https://example.invalid/media",
		ResumeIdentity: "direct:fixture:1",
		OutputRoot:     root,
		Destination:    filepath.Join(root, "media.bin"),
		Checkpoint: &CheckpointOptions{StateDirectory: filepath.Join(root, "checkpoint"), ResumeBoundary: &Checkpoint{
			ResumeIdentity: "",
			CommittedBytes: minDirectCheckpointBytes,
		}},
	}
	if err := validateJob(job); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("empty boundary identity error = %v, want ErrInvalidCheckpoint", err)
	}

	job.Checkpoint.ResumeBoundary.ResumeIdentity = "direct:other:1"
	if err := validateJob(job); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("different boundary identity error = %v, want ErrInvalidCheckpoint", err)
	}
}

func TestCheckpointBoundaryDoesNotResumeMismatchedLocalIdentity(t *testing.T) {
	data := checkpointData(96 << 10)
	destination := filepath.Join(t.TempDir(), "media.bin")
	identity := "direct:fixture:1"
	partPath := destination + ".part"
	if err := os.WriteFile(partPath, data[:minDirectCheckpointBytes], 0o600); err != nil {
		t.Fatal(err)
	}
	writeCheckpointState(t, checkpointStatePath(destination), partialState{
		ResumeIdentity: "direct:other:1",
		ETag:           `"fixture"`,
		Total:          int64(len(data)),
		CommittedBytes: minDirectCheckpointBytes,
	})
	writeCheckpointOwner(t, destination, identity)
	doer := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: 32 << 10}
	job := checkpointJob(destination, identity, &CheckpointOptions{
		ResumeBoundary: &Checkpoint{ResumeIdentity: identity, ETag: `"fixture"`, Total: int64(len(data)), CommittedBytes: minDirectCheckpointBytes},
		EveryBytes:     minDirectCheckpointBytes,
	})
	result, err := New(doer).Download(context.Background(), job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed {
		t.Fatal("exact caller boundary did not independently authorize prefix")
	}
	if got := doer.requestRange(0); got != fmt.Sprintf("bytes=%d-", minDirectCheckpointBytes) {
		t.Fatalf("range = %q, want caller boundary", got)
	}
}

func TestCheckpointRequiresStableIdentityAndDedicatedStateDirectory(t *testing.T) {
	root := t.TempDir()
	job := Job{
		URL:         "https://signed.example/media?token=secret",
		OutputRoot:  root,
		Destination: filepath.Join(root, "media.bin"),
		Checkpoint:  &CheckpointOptions{StateDirectory: filepath.Join(root, "checkpoint")},
	}
	if err := validateJob(job); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("empty identity error = %v, want ErrInvalidCheckpoint", err)
	}
	job.ResumeIdentity = "direct:fixture:1"
	job.Checkpoint.StateDirectory = ""
	if err := validateJob(job); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("empty state directory error = %v, want ErrInvalidCheckpoint", err)
	}
}

func TestCheckpointInvalidStatePreservesArtifacts(t *testing.T) {
	identity := "direct:fixture:strict-state"
	validOtherIdentity := fmt.Sprintf(`{"version":1,"resumeIdentity":"direct:other","etag":"fixture","committedBytes":%d}`, minDirectCheckpointBytes)
	for _, test := range []struct {
		name    string
		encoded []byte
	}{
		{name: "malformed", encoded: []byte(`{"version":`)},
		{name: "oversized", encoded: bytes.Repeat([]byte("x"), maxCheckpointStateBytes+1)},
		{name: "trailing object", encoded: []byte(`{"version":1,"resumeIdentity":"direct:fixture:strict-state","committedBytes":65536}{}`)},
		{name: "unknown field", encoded: []byte(`{"version":1,"resumeIdentity":"direct:fixture:strict-state","committedBytes":65536,"authorization":"Bearer secret"}`)},
		{name: "url field", encoded: []byte(`{"version":1,"resumeIdentity":"direct:fixture:strict-state","url":"https://signed.example/media?token=secret","committedBytes":65536}`)},
		{name: "identity mismatch", encoded: []byte(validOtherIdentity)},
	} {
		t.Run(test.name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "media.bin")
			partial := checkpointData(minDirectCheckpointBytes)
			if err := os.WriteFile(destination+".part", partial, 0o600); err != nil {
				t.Fatal(err)
			}
			statePath := checkpointStatePath(destination)
			if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
				t.Fatal(err)
			}
			writeCheckpointOwner(t, destination, identity)
			if err := os.WriteFile(statePath, test.encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := protectCheckpointCreated(statePath, false); err != nil {
				t.Fatal(err)
			}
			doer := &checkpointDoer{data: checkpointData(2 * minDirectCheckpointBytes), etag: `"fixture"`}
			job := checkpointJob(destination, identity, &CheckpointOptions{})
			_, err := New(doer).Download(context.Background(), job, nil)
			if !errors.Is(err, ErrInvalidCheckpointState) {
				t.Fatalf("error = %v, want ErrInvalidCheckpointState", err)
			}
			if strings.Contains(err.Error(), "authorization") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("state contents leaked through error: %q", err)
			}
			gotPartial, readErr := os.ReadFile(destination + ".part")
			if readErr != nil || !bytes.Equal(gotPartial, partial) {
				t.Fatalf("partial changed: err=%v size=%d", readErr, len(gotPartial))
			}
			gotState, readErr := os.ReadFile(statePath)
			if readErr != nil || !bytes.Equal(gotState, test.encoded) {
				t.Fatalf("state changed: err=%v", readErr)
			}
			if doer.requestCount() != 0 {
				t.Fatalf("requests = %d, invalid local authority must stop before network", doer.requestCount())
			}
		})
	}
}

func TestCheckpointUnreadableStateReturnsFilesystemError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACL read denial is covered by native ACL tests")
	}
	destination := filepath.Join(t.TempDir(), "media.bin")
	partial := checkpointData(minDirectCheckpointBytes)
	if err := os.WriteFile(destination+".part", partial, 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := checkpointStatePath(destination)
	writeCheckpointState(t, statePath, partialState{ResumeIdentity: "direct:fixture:unreadable", CommittedBytes: minDirectCheckpointBytes})
	if err := os.Chmod(statePath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(statePath, 0o600) })
	if file, openErr := os.Open(statePath); openErr == nil {
		_ = file.Close()
		t.Skip("platform permits reading mode-000 files")
	}
	_, err := New(&checkpointDoer{}).Download(context.Background(), checkpointJob(destination, "direct:fixture:unreadable", &CheckpointOptions{}), nil)
	if !errors.Is(err, ErrCheckpointReconciliation) {
		t.Fatalf("error = %v, want reconciliation with filesystem cause", err)
	}
	if info, statErr := os.Stat(destination + ".part"); statErr != nil || info.Size() != minDirectCheckpointBytes {
		t.Fatalf("partial was changed: info=%v err=%v", info, statErr)
	}
}

func TestCheckpointCallerBoundaryClampsCorruptState(t *testing.T) {
	data := checkpointData(2 * minDirectCheckpointBytes)
	destination := filepath.Join(t.TempDir(), "media.bin")
	identity := "direct:fixture:boundary"
	if err := os.WriteFile(destination+".part", data, 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := checkpointStatePath(destination)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCheckpointOwner(t, destination, identity)
	if err := os.WriteFile(statePath, []byte(`{"authorization":"Bearer secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := protectCheckpointCreated(statePath, false); err != nil {
		t.Fatal(err)
	}
	doer := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: minDirectCheckpointBytes}
	job := checkpointJob(destination, identity, &CheckpointOptions{ResumeBoundary: &Checkpoint{
		ResumeIdentity: identity, ETag: `"fixture"`, Total: int64(len(data)), CommittedBytes: minDirectCheckpointBytes,
	}})
	result, err := New(doer).Download(context.Background(), job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed || doer.requestRange(0) != fmt.Sprintf("bytes=%d-", minDirectCheckpointBytes) {
		t.Fatalf("boundary did not clamp corrupt state: result=%#v range=%q", result, doer.requestRange(0))
	}
}

func TestCheckpointZeroBoundaryExplicitlyResetsCorruptState(t *testing.T) {
	data := checkpointData(minDirectCheckpointBytes)
	destination := filepath.Join(t.TempDir(), "media.bin")
	identity := "direct:fixture:zero"
	if err := os.WriteFile(destination+".part", bytes.Repeat([]byte("tail"), 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := checkpointStatePath(destination)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	writeCheckpointOwner(t, destination, identity)
	if err := os.WriteFile(statePath, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := protectCheckpointCreated(statePath, false); err != nil {
		t.Fatal(err)
	}
	doer := &checkpointDoer{data: data, etag: `"fixture"`}
	job := checkpointJob(destination, identity, &CheckpointOptions{ResumeBoundary: &Checkpoint{ResumeIdentity: identity}})
	result, err := New(doer).Download(context.Background(), job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed || doer.requestRange(0) != "" {
		t.Fatalf("zero boundary did not reset: result=%#v range=%q", result, doer.requestRange(0))
	}
}

func TestCheckpointMismatchedValidatorsWithoutAuthorizingBoundaryPreservesArtifacts(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "media.bin")
	partial := checkpointData(minDirectCheckpointBytes)
	if err := os.WriteFile(destination+".part", partial, 0o600); err != nil {
		t.Fatal(err)
	}
	identity := "direct:fixture:validator"
	writeCheckpointState(t, checkpointStatePath(destination), partialState{
		ResumeIdentity: identity, ETag: `"local"`, Total: 2 * minDirectCheckpointBytes, CommittedBytes: minDirectCheckpointBytes,
	})
	job := checkpointJob(destination, identity, &CheckpointOptions{ResumeBoundary: &Checkpoint{
		ResumeIdentity: identity, ETag: `"caller"`, Total: 2 * minDirectCheckpointBytes, CommittedBytes: 2 * minDirectCheckpointBytes,
	}})
	_, err := New(&checkpointDoer{}).Download(context.Background(), job, nil)
	if !errors.Is(err, ErrCheckpointReconciliation) {
		t.Fatalf("error = %v, want reconciliation", err)
	}
	got, readErr := os.ReadFile(destination + ".part")
	if readErr != nil || !bytes.Equal(got, partial) {
		t.Fatalf("partial changed: err=%v", readErr)
	}
}

func TestCheckpointOrderingSyncsPayloadBeforeStateAndCallback(t *testing.T) {
	data := checkpointData(2 * minDirectCheckpointBytes)
	destination := filepath.Join(t.TempDir(), "media.bin")
	doer := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: minDirectCheckpointBytes}
	var mu sync.Mutex
	var order []string
	var callbackStates []Checkpoint
	client := New(doer)
	client.syncPartialPayload = func(file *os.File) error {
		mu.Lock()
		order = append(order, "sync")
		mu.Unlock()
		return file.Sync()
	}
	client.writePartialState = func(path string, state partialState) error {
		mu.Lock()
		order = append(order, fmt.Sprintf("state:%d", state.CommittedBytes))
		mu.Unlock()
		return savePartialStateOnce(path, state)
	}
	job := checkpointJob(destination, "direct:fixture:1", &CheckpointOptions{
		EveryBytes: minDirectCheckpointBytes,
		OnCommit: func(_ context.Context, state Checkpoint) error {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, "callback")
			callbackStates = append(callbackStates, state)
			return nil
		},
	})
	if _, err := client.Download(context.Background(), job, nil); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(callbackStates) < 2 {
		t.Fatalf("callback states = %d, want periodic and final checkpoints; order=%v", len(callbackStates), order)
	}
	for index, state := range callbackStates {
		if state.CommittedBytes != int64(index+1)*minDirectCheckpointBytes {
			t.Fatalf("callback[%d] = %#v", index, state)
		}
	}
	for index := 0; index < len(order); index++ {
		if !strings.HasPrefix(order[index], "state:") || order[index] == "state:0" {
			continue
		}
		if index == 0 || order[index-1] != "sync" {
			t.Fatalf("state write was not immediately preceded by payload sync: %v", order)
		}
		if index+1 >= len(order) || order[index+1] != "callback" {
			t.Fatalf("callback did not immediately follow state write: %v", order)
		}
	}
}

func TestCheckpointDurationUsesInjectedClock(t *testing.T) {
	data := checkpointData(int64(checkpointChunkSize / 2))
	destination := filepath.Join(t.TempDir(), "media.bin")
	now := time.Unix(0, 0)
	var callbacks []Checkpoint
	doer := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: int64(len(data))}
	client := NewWithHooks(doer, func() time.Time {
		now = now.Add(10 * time.Millisecond)
		return now
	}, func(context.Context, time.Duration) error { return nil })
	job := checkpointJob(destination, "direct:fixture:clock", &CheckpointOptions{
		EveryBytes:    minDirectCheckpointBytes,
		EveryDuration: minDirectCheckpointInterval,
		OnCommit: func(_ context.Context, state Checkpoint) error {
			callbacks = append(callbacks, state)
			return nil
		},
	})
	if _, err := client.Download(context.Background(), job, nil); err != nil {
		t.Fatal(err)
	}
	if len(callbacks) != 1 || callbacks[0].CommittedBytes != int64(len(data)) {
		t.Fatalf("callbacks = %#v, want deterministic duration checkpoint", callbacks)
	}
}

func TestCheckpointCancellationFinalizesDurableCheckpoint(t *testing.T) {
	data := checkpointData(2 * minDirectCheckpointBytes)
	destination := filepath.Join(t.TempDir(), "media.bin")
	doer := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: minDirectCheckpointBytes}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var callbacks []Checkpoint
	job := checkpointJob(destination, "direct:fixture:1", &CheckpointOptions{
		EveryBytes: minDirectCheckpointBytes * 2,
		OnCommit: func(_ context.Context, state Checkpoint) error {
			callbacks = append(callbacks, state)
			return nil
		},
	})
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindProgress && event.Bytes >= minDirectCheckpointBytes {
			cancel()
		}
		return nil
	})
	_, err := New(doer).Download(ctx, job, sink)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(callbacks) != 1 || callbacks[0].CommittedBytes != minDirectCheckpointBytes {
		t.Fatalf("callbacks = %#v, want final cancellation checkpoint", callbacks)
	}
	state := readCheckpointState(t, checkpointStatePath(destination))
	if state.CommittedBytes != minDirectCheckpointBytes {
		t.Fatalf("state = %#v, want committed cancellation boundary", state)
	}
	if info, statErr := os.Stat(destination + ".part"); statErr != nil || info.Size() != minDirectCheckpointBytes {
		t.Fatalf("partial size = %v, %v", info, statErr)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination = %v, want unpublished", statErr)
	}
}

func TestCheckpointCallbackFailureIsGenericAndStopsTransfer(t *testing.T) {
	data := checkpointData(2 * minDirectCheckpointBytes)
	destination := filepath.Join(t.TempDir(), "media.bin")
	doer := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: minDirectCheckpointBytes}
	callbackCause := errors.New("callback saw https://cdn.example/media?token=secret")
	job := checkpointJob(destination, "direct:fixture:1", &CheckpointOptions{
		EveryBytes: minDirectCheckpointBytes,
		OnCommit:   func(context.Context, Checkpoint) error { return callbackCause },
	})
	_, err := New(doer).Download(context.Background(), job, nil)
	if !errors.Is(err, ErrCheckpointCallback) || !errors.Is(err, callbackCause) {
		t.Fatalf("error = %v, want callback and cause matching", err)
	}
	if strings.Contains(err.Error(), "cdn.example") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("callback cause leaked through Error(): %q", err)
	}
	if doer.requestCount() != 1 {
		t.Fatalf("requests = %d, callback failure should not retry", doer.requestCount())
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination = %v, want unpublished", statErr)
	}
	state := readCheckpointState(t, checkpointStatePath(destination))
	if state.CommittedBytes != minDirectCheckpointBytes {
		t.Fatalf("state = %#v, want last locally committed bytes", state)
	}
}

func TestCheckpointReopenDiscardsUncommittedTail(t *testing.T) {
	data := checkpointData(2 * minDirectCheckpointBytes)
	destination := filepath.Join(t.TempDir(), "media.bin")
	first := &checkpointDoer{
		data:      data,
		etag:      `"fixture"`,
		chunkSize: minDirectCheckpointBytes,
		failAfter: minDirectCheckpointBytes,
		failWith:  io.ErrUnexpectedEOF,
	}
	job := checkpointJob(destination, "direct:fixture:1", &CheckpointOptions{EveryBytes: minDirectCheckpointBytes * 2})
	_, err := New(first).Download(context.Background(), job, nil)
	if err == nil {
		t.Fatal("unclean response unexpectedly completed")
	}
	state := readCheckpointState(t, checkpointStatePath(destination))
	if state.CommittedBytes != 0 {
		t.Fatalf("state = %#v, uncommitted tail was advanced", state)
	}
	if info, statErr := os.Stat(destination + ".part"); statErr != nil || info.Size() != minDirectCheckpointBytes {
		t.Fatalf("unclean tail size = %v, %v", info, statErr)
	}

	second := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: minDirectCheckpointBytes}
	result, err := New(second).Download(context.Background(), job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed || second.requestRange(0) != "" {
		t.Fatalf("reopened uncommitted tail was reused: result=%#v range=%q", result, second.requestRange(0))
	}
	contents, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(contents, data) {
		t.Fatalf("reopened contents differ: %v", err)
	}
}

func TestCheckpointRefreshedURLUsesStableIdentityWithoutPersistingURL(t *testing.T) {
	data := checkpointData(2 * minDirectCheckpointBytes)
	destination := filepath.Join(t.TempDir(), "media.bin")
	identity := "direct:fixture:1"
	secretURL := "https://signed.example/media?token=secret&sig=signature"
	ctx, cancel := context.WithCancel(context.Background())
	first := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: minDirectCheckpointBytes}
	job := checkpointJob(destination, identity, &CheckpointOptions{
		EveryBytes: minDirectCheckpointBytes * 2,
		OnCommit:   func(context.Context, Checkpoint) error { return nil },
	})
	job.URL = secretURL
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindProgress {
			cancel()
		}
		return nil
	})
	_, err := New(first).Download(ctx, job, sink)
	cancel()
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "signature") {
		t.Fatalf("first error = %v, signed URL leaked or cancellation lost", err)
	}
	encoded, readErr := os.ReadFile(checkpointStatePath(destination))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(encoded), "signed.example") || strings.Contains(string(encoded), "secret") || !strings.Contains(string(encoded), identity) {
		t.Fatalf("state contains unsafe URL material: %s", encoded)
	}
	boundary := readCheckpointState(t, checkpointStatePath(destination))
	second := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: minDirectCheckpointBytes}
	job.Checkpoint.ResumeBoundary = &Checkpoint{
		ResumeIdentity: identity,
		ETag:           boundary.ETag,
		Total:          boundary.Total,
		CommittedBytes: boundary.CommittedBytes,
	}
	job.URL = "https://signed.example/media?token=refreshed&sig=new-signature"
	result, err := New(second).Download(context.Background(), job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed || second.requestRange(0) != fmt.Sprintf("bytes=%d-", boundary.CommittedBytes) {
		t.Fatalf("refreshed URL did not resume by identity: result=%#v range=%q", result, second.requestRange(0))
	}
}

func TestCheckpointStateWritePropagatesAtomicOutcomes(t *testing.T) {
	cause := errors.New("injected state write failure")
	for _, test := range []struct {
		name          string
		committed     bool
		indeterminate bool
	}{
		{name: "precommit"},
		{name: "committed", committed: true},
		{name: "indeterminate", indeterminate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			client := New(nil)
			client.writePartialState = func(string, partialState) error {
				return checkpointCommitOutcome{cause: cause, committed: test.committed, indeterminate: test.indeterminate}
			}
			job := Job{ResumeIdentity: "direct:fixture:1", Checkpoint: &CheckpointOptions{StateDirectory: directory}}
			state := newCheckpointPartialState("direct:fixture:1")
			state.Total, state.CommittedBytes = 2*minDirectCheckpointBytes, minDirectCheckpointBytes
			err := client.savePartialState(context.Background(), job, filepath.Join(directory, "state.json"), state)
			var outcome atomicfile.CommitError
			if !errors.As(err, &outcome) {
				t.Fatalf("error %T does not preserve atomic outcome: %v", err, err)
			}
			if outcome.Committed() != test.committed || outcome.Indeterminate() != test.indeterminate || !errors.Is(err, cause) {
				t.Fatalf("outcome = committed %v indeterminate %v err %v", outcome.Committed(), outcome.Indeterminate(), err)
			}
		})
	}
}

func TestCheckpointRejectsNoPartAndRetainedAtomicEvidence(t *testing.T) {
	root := t.TempDir()
	job := Job{URL: "https://example.invalid/media", ResumeIdentity: "direct:fixture:1", OutputRoot: root, Destination: filepath.Join(root, "media.bin"), NoPart: true, Checkpoint: &CheckpointOptions{StateDirectory: filepath.Join(root, "checkpoint")}}
	if err := validateJob(job); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("NoPart checkpoint error = %v", err)
	}

	stateDirectory := filepath.Join(root, "checkpoint")
	statePath := filepath.Join(stateDirectory, "direct.json")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := protectCheckpointCreated(stateDirectory, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDirectory, ".atomic-retained"), []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	job.NoPart = false
	job.URL = "https://example.invalid/media"
	job.Checkpoint = &CheckpointOptions{StateDirectory: stateDirectory}
	if _, err := New(&checkpointDoer{data: checkpointData(minDirectCheckpointBytes), etag: `"fixture"`}).Download(context.Background(), job, nil); !errors.Is(err, ErrCheckpointReconciliation) {
		t.Fatalf("retained evidence error = %v, want reconciliation", err)
	}
	if _, statErr := os.Stat(statePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state path = %v, evidence failure should not create state", statErr)
	}
}

func TestCheckpointAtomicEvidenceIsIsolatedPerJob(t *testing.T) {
	root := t.TempDir()
	firstStateDirectory := filepath.Join(root, "session-a")
	if err := os.MkdirAll(firstStateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(firstStateDirectory, ".atomic-retained"), []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := checkpointData(minDirectCheckpointBytes)
	secondDestination := filepath.Join(root, "second.bin")
	second := checkpointJob(secondDestination, "direct:fixture:second", &CheckpointOptions{
		StateDirectory: filepath.Join(root, "session-b"),
	})
	if _, err := New(&checkpointDoer{data: data, etag: `"fixture"`}).Download(context.Background(), second, nil); err != nil {
		t.Fatalf("other job's atomic evidence caused a false positive: %v", err)
	}
	contents, err := os.ReadFile(secondDestination)
	if err != nil || !bytes.Equal(contents, data) {
		t.Fatalf("second download differs: %v", err)
	}
}

func TestCheckpointStateDirectoryPathAuthority(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, root, destination string) string
	}{
		{name: "relative", setup: func(_ *testing.T, _, _ string) string { return "checkpoint" }},
		{name: "traversal", setup: func(_ *testing.T, root, _ string) string {
			return filepath.Join(root, "session", "..", "checkpoint") + string(filepath.Separator) + ".." + string(filepath.Separator) + "checkpoint"
		}},
		{name: "outside root", setup: func(t *testing.T, _, _ string) string { return filepath.Join(t.TempDir(), "checkpoint") }},
		{name: "symlink parent", setup: func(t *testing.T, root, _ string) string {
			target := filepath.Join(root, "real")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "link")
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			return filepath.Join(link, "checkpoint")
		}},
		{name: "group readable parent", setup: func(t *testing.T, root, _ string) string {
			parent := filepath.Join(root, "session")
			if err := os.Mkdir(parent, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(parent, 0o750); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(parent, "checkpoint")
		}},
		{name: "equals destination", setup: func(_ *testing.T, _, destination string) string { return destination }},
		{name: "equals partial", setup: func(_ *testing.T, _, destination string) string { return destination + ".part" }},
		{name: "contains destination", setup: func(_ *testing.T, root, _ string) string { return root }},
		{name: "inside destination", setup: func(_ *testing.T, _, destination string) string { return filepath.Join(destination, "checkpoint") }},
		{name: "inside partial", setup: func(_ *testing.T, _, destination string) string {
			return filepath.Join(destination+".part", "checkpoint")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			destination := filepath.Join(root, "media.bin")
			stateDirectory := test.setup(t, root, destination)
			job := checkpointJob(destination, "direct:fixture:path", &CheckpointOptions{StateDirectory: stateDirectory})
			_, err := New(&checkpointDoer{data: checkpointData(minDirectCheckpointBytes), etag: `"fixture"`}).Download(context.Background(), job, nil)
			if !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("error = %v, want ErrInvalidCheckpoint", err)
			}
		})
	}
}

func TestCheckpointRejectsRelativeAndNoncanonicalPathsBeforeMutation(t *testing.T) {
	for _, name := range []string{"relative output root", "relative destination", "relative state directory", "noncanonical output root", "noncanonical destination", "noncanonical state directory"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			destination := filepath.Join(root, "media.bin")
			stateDirectory := filepath.Join(root, "checkpoint")
			job := Job{
				URL:            "https://fixture.invalid/media",
				ResumeIdentity: "direct:fixture:canonical-paths",
				OutputRoot:     root,
				Destination:    destination,
				Checkpoint:     &CheckpointOptions{StateDirectory: stateDirectory},
			}
			switch name {
			case "relative output root":
				job.OutputRoot = "checkpoint-relative-root-" + filepath.Base(root)
			case "relative destination":
				job.Destination = "checkpoint-relative-media-" + filepath.Base(root) + ".bin"
			case "relative state directory":
				job.Checkpoint.StateDirectory = "checkpoint-relative-state-" + filepath.Base(root)
			case "noncanonical output root":
				job.OutputRoot = filepath.Join(root, "child") + string(filepath.Separator) + ".."
			case "noncanonical destination":
				job.Destination = filepath.Join(root, "child") + string(filepath.Separator) + ".." + string(filepath.Separator) + "media.bin"
			case "noncanonical state directory":
				job.Checkpoint.StateDirectory = filepath.Join(root, "child") + string(filepath.Separator) + ".." + string(filepath.Separator) + "checkpoint"
			}
			var relativeArtifacts []string
			for _, candidate := range []string{job.OutputRoot, job.Destination, job.Checkpoint.StateDirectory} {
				if !filepath.IsAbs(candidate) {
					if _, statErr := os.Lstat(candidate); !errors.Is(statErr, os.ErrNotExist) {
						t.Fatalf("relative test artifact already exists: %s", candidate)
					}
					relativeArtifacts = append(relativeArtifacts, candidate)
				}
			}
			_, err := New(&checkpointDoer{data: checkpointData(minDirectCheckpointBytes)}).Download(context.Background(), job, nil)
			if !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("error = %v, want ErrInvalidCheckpoint", err)
			}
			for _, path := range []string{destination, destination + ".part", stateDirectory} {
				if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("artifact %s created on rejected path: %v", path, statErr)
				}
			}
			for _, path := range relativeArtifacts {
				if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("relative artifact %s created on rejection: %v", path, statErr)
				}
			}
		})
	}
}

func TestCheckpointStateDirectoryCannotContainPayload(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "session", "media.bin")
	job := Job{
		URL:            "https://fixture.invalid/media",
		ResumeIdentity: "direct:fixture:containment",
		OutputRoot:     root,
		Destination:    destination,
		Checkpoint:     &CheckpointOptions{StateDirectory: filepath.Join(root, "session")},
	}
	_, err := New(&checkpointDoer{data: checkpointData(minDirectCheckpointBytes)}).Download(context.Background(), job, nil)
	if !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("error = %v, want payload containment rejection", err)
	}
}

func TestCheckpointUnknownStateDirectoryEntryRequiresReconciliation(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "media.bin")
	stateDirectory := filepath.Join(root, "checkpoint")
	if err := os.Mkdir(stateDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := protectCheckpointCreated(stateDirectory, true); err != nil {
		t.Fatal(err)
	}
	unknownPath := filepath.Join(stateDirectory, "notes.txt")
	if err := os.WriteFile(unknownPath, []byte("unclassified"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := checkpointJob(destination, "direct:fixture:unknown-entry", &CheckpointOptions{StateDirectory: stateDirectory})
	_, err := New(&checkpointDoer{data: checkpointData(minDirectCheckpointBytes)}).Download(context.Background(), job, nil)
	if !errors.Is(err, ErrCheckpointReconciliation) {
		t.Fatalf("error = %v, want reconciliation", err)
	}
	if contents, readErr := os.ReadFile(unknownPath); readErr != nil || string(contents) != "unclassified" {
		t.Fatalf("unknown evidence changed: contents=%q err=%v", contents, readErr)
	}
}

func TestCheckpointStateDirectoryCannotBeSharedAcrossJobs(t *testing.T) {
	root := t.TempDir()
	stateDirectory := filepath.Join(root, "checkpoint")
	firstDestination := filepath.Join(root, "first.bin")
	first := checkpointJob(firstDestination, "direct:fixture:first", &CheckpointOptions{StateDirectory: stateDirectory})
	if _, err := New(&checkpointDoer{data: checkpointData(minDirectCheckpointBytes), etag: `"fixture"`}).Download(context.Background(), first, nil); err != nil {
		t.Fatal(err)
	}
	second := checkpointJob(filepath.Join(root, "second.bin"), "direct:fixture:second", &CheckpointOptions{StateDirectory: stateDirectory})
	_, err := New(&checkpointDoer{data: checkpointData(minDirectCheckpointBytes), etag: `"fixture"`}).Download(context.Background(), second, nil)
	if !errors.Is(err, ErrCheckpointReconciliation) {
		t.Fatalf("shared directory error = %v, want reconciliation", err)
	}
}

func TestCheckpointWorldReadableStateRequiresReconciliation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ACL permission failures are covered by native ACL tests")
	}
	destination := filepath.Join(t.TempDir(), "media.bin")
	identity := "direct:fixture:permissions"
	if err := os.WriteFile(destination+".part", checkpointData(minDirectCheckpointBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := checkpointStatePath(destination)
	writeCheckpointState(t, statePath, partialState{ResumeIdentity: identity, CommittedBytes: minDirectCheckpointBytes})
	if err := os.Chmod(statePath, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := New(&checkpointDoer{}).Download(context.Background(), checkpointJob(destination, identity, &CheckpointOptions{}), nil)
	if !errors.Is(err, ErrCheckpointReconciliation) {
		t.Fatalf("error = %v, want reconciliation", err)
	}
}

func TestCheckpointRejectsResumedContentLengthRangeMismatch(t *testing.T) {
	data := checkpointData(2 * minDirectCheckpointBytes)
	destination := filepath.Join(t.TempDir(), "media.bin")
	identity := "direct:fixture:range-length"
	if err := os.WriteFile(destination+".part", data[:minDirectCheckpointBytes], 0o600); err != nil {
		t.Fatal(err)
	}
	writeCheckpointState(t, checkpointStatePath(destination), partialState{
		ResumeIdentity: identity, ETag: `"fixture"`, Total: int64(len(data)), CommittedBytes: minDirectCheckpointBytes,
	})
	var mu sync.Mutex
	var ranges []string
	doer := checkpointDoerFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		mu.Lock()
		ranges = append(ranges, request.Header.Get("Range"))
		requestNumber := len(ranges)
		mu.Unlock()
		if requestNumber == 1 {
			body := data[minDirectCheckpointBytes:]
			return &http.Response{
				StatusCode:    http.StatusPartialContent,
				Header:        http.Header{"ETag": []string{`"fixture"`}, "Content-Range": []string{fmt.Sprintf("bytes %d-%d/%d", minDirectCheckpointBytes, len(data)-1, len(data))}},
				ContentLength: int64(len(body) - 1),
				Body:          io.NopCloser(bytes.NewReader(body)),
				Request:       request,
			}, nil
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"ETag": []string{`"fixture"`}, "Content-Length": []string{fmt.Sprint(len(data))}},
			ContentLength: int64(len(data)),
			Body:          io.NopCloser(bytes.NewReader(data)),
			Request:       request,
		}, nil
	})
	result, err := New(doer).Download(context.Background(), checkpointJob(destination, identity, &CheckpointOptions{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotRanges := append([]string(nil), ranges...)
	mu.Unlock()
	if result.Resumed || len(gotRanges) != 2 || gotRanges[0] == "" || gotRanges[1] != "" {
		t.Fatalf("malformed 206 was accepted: result=%#v ranges=%v", result, gotRanges)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(contents, data) {
		t.Fatalf("download differs after safe restart: %v", err)
	}
}

func TestCheckpointCancellationCallbackUsesBoundedLocalContext(t *testing.T) {
	data := checkpointData(minDirectCheckpointBytes)
	destination := filepath.Join(t.TempDir(), "media.bin")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	callbackEntered := make(chan struct{}, 1)
	job := checkpointJob(destination, "direct:fixture:bounded-callback", &CheckpointOptions{
		EveryBytes: 2 * minDirectCheckpointBytes,
		OnCommit: func(localCtx context.Context, _ Checkpoint) error {
			callbackEntered <- struct{}{}
			if _, ok := localCtx.Deadline(); !ok {
				return errors.New("checkpoint callback context has no deadline")
			}
			<-localCtx.Done()
			return localCtx.Err()
		},
	})
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindProgress {
			cancel()
		}
		return nil
	})
	client := New(&checkpointDoer{data: data, etag: `"fixture"`, chunkSize: minDirectCheckpointBytes})
	client.checkpointTimeout = 20 * time.Millisecond
	started := time.Now()
	_, err := client.Download(ctx, job, sink)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrCheckpointCallback) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want cancellation and bounded callback failure", err)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("cancellation settlement exceeded bound: %v", time.Since(started))
	}
	select {
	case <-callbackEntered:
	default:
		t.Fatal("checkpoint callback was not invoked")
	}
}

func TestCheckpointCadenceBounds(t *testing.T) {
	invalid := []CheckpointOptions{
		{EveryBytes: minDirectCheckpointBytes - 1},
		{EveryBytes: maxDirectCheckpointBytes + 1},
		{EveryDuration: minDirectCheckpointInterval - time.Nanosecond},
		{EveryDuration: maxDirectCheckpointInterval + time.Nanosecond},
	}
	for _, options := range invalid {
		options.StateDirectory = t.TempDir()
		if err := validateJob(Job{ResumeIdentity: "direct:fixture:1", Checkpoint: &options}); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("options %#v accepted: %v", options, err)
		}
	}
}

func TestLegacyStateDoesNotGainCheckpointAuthority(t *testing.T) {
	data := checkpointData(minDirectCheckpointBytes)
	destination := filepath.Join(t.TempDir(), "media.bin")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doer := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: minDirectCheckpointBytes}
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindProgress {
			cancel()
		}
		return nil
	})
	_, err := New(doer).Download(ctx, Job{URL: "https://example.invalid/media", OutputRoot: filepath.Dir(destination), Destination: destination}, sink)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("legacy cancellation error = %v", err)
	}
	encoded, readErr := os.ReadFile(destination + ".part.json")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if bytes.Contains(encoded, []byte("committedBytes")) {
		t.Fatalf("legacy state unexpectedly contains checkpoint authority: %s", encoded)
	}
}

func TestLegacyStateWithLastModifiedRemainsCompatible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.part.json")
	state := partialState{URL: "https://legacy.example/media", LastModified: "Tue, 11 Aug 2026 00:00:00 GMT", Total: 42}
	if err := savePartialStateOnce(path, state); err != nil {
		t.Fatalf("legacy state write failed: %v", err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(state.URL)) || bytes.Contains(encoded, []byte("resumeIdentity")) || bytes.Contains(encoded, []byte("committedBytes")) {
		t.Fatalf("legacy state shape changed: %s", encoded)
	}
}

func TestCheckpointRepeatedCancellationBoundaries(t *testing.T) {
	for iteration := 0; iteration < 8; iteration++ {
		t.Run(fmt.Sprintf("iteration-%d", iteration), func(t *testing.T) {
			data := checkpointData(2 * minDirectCheckpointBytes)
			destination := filepath.Join(t.TempDir(), "media.bin")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var callbackCount int
			doer := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: minDirectCheckpointBytes}
			job := checkpointJob(destination, "direct:fixture:1", &CheckpointOptions{
				EveryBytes: minDirectCheckpointBytes * 2,
				OnCommit: func(_ context.Context, state Checkpoint) error {
					callbackCount++
					if state.CommittedBytes != minDirectCheckpointBytes {
						t.Fatalf("callback state = %#v", state)
					}
					return nil
				},
			})
			_, err := New(doer).Download(ctx, job, events.SinkFunc(func(_ context.Context, event events.Event) error {
				if event.Kind == events.KindProgress {
					cancel()
				}
				return nil
			}))
			if !errors.Is(err, context.Canceled) || callbackCount != 1 {
				t.Fatalf("err=%v callbacks=%d", err, callbackCount)
			}
		})
	}
}

type checkpointCommitOutcome struct {
	cause         error
	committed     bool
	indeterminate bool
}

func (err checkpointCommitOutcome) Error() string { return "injected atomic outcome" }

func (err checkpointCommitOutcome) Unwrap() error { return err.cause }

func (err checkpointCommitOutcome) Committed() bool { return err.committed }

func (err checkpointCommitOutcome) Indeterminate() bool { return err.indeterminate }

type checkpointDoer struct {
	data      []byte
	etag      string
	chunkSize int64
	failAfter int64
	failWith  error

	mu       sync.Mutex
	requests []checkpointRequest
}

type checkpointRequest struct{ rangeValue string }

type checkpointDoerFunc func(context.Context, *http.Request) (*http.Response, error)

func (doer checkpointDoerFunc) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return doer(ctx, request)
}

func (doer *checkpointDoer) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := 0
	status := http.StatusOK
	rangeValue := request.Header.Get("Range")
	if rangeValue != "" {
		_, _ = fmt.Sscanf(rangeValue, "bytes=%d-", &start)
		if request.Header.Get("If-Range") != "" && request.Header.Get("If-Range") != doer.etag {
			start = 0
		} else if start >= len(doer.data) {
			return &http.Response{
				StatusCode: http.StatusRequestedRangeNotSatisfiable,
				Header:     http.Header{"Content-Range": []string{fmt.Sprintf("bytes */%d", len(doer.data))}},
				Body:       io.NopCloser(bytes.NewReader(nil)),
				Request:    request,
			}, nil
		} else if start > 0 {
			status = http.StatusPartialContent
		}
	}
	doer.mu.Lock()
	doer.requests = append(doer.requests, checkpointRequest{rangeValue: rangeValue})
	doer.mu.Unlock()
	body := doer.data[start:]
	chunkSize := doer.chunkSize
	if chunkSize <= 0 {
		chunkSize = int64(len(body))
	}
	header := make(http.Header)
	header.Set("ETag", doer.etag)
	header.Set("Content-Length", fmt.Sprint(len(body)))
	if status == http.StatusPartialContent {
		header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(doer.data)-1, len(doer.data)))
	}
	return &http.Response{
		StatusCode:    status,
		Header:        header,
		ContentLength: int64(len(body)),
		Body: &checkpointBody{
			data:      body,
			chunkSize: chunkSize,
			failAfter: doer.failAfter,
			failWith:  doer.failWith,
		},
		Request: request,
	}, nil
}

func (doer *checkpointDoer) requestRange(index int) string {
	doer.mu.Lock()
	defer doer.mu.Unlock()
	if index < 0 || index >= len(doer.requests) {
		return ""
	}
	return doer.requests[index].rangeValue
}

func (doer *checkpointDoer) requestCount() int {
	doer.mu.Lock()
	defer doer.mu.Unlock()
	return len(doer.requests)
}

type checkpointBody struct {
	data      []byte
	chunkSize int64
	read      int
	failAfter int64
	failWith  error
	failed    bool
}

func (body *checkpointBody) Read(target []byte) (int, error) {
	if body.read >= len(body.data) {
		return 0, io.EOF
	}
	count := int(body.chunkSize)
	if remaining := len(body.data) - body.read; count > remaining {
		count = remaining
	}
	if count > len(target) {
		count = len(target)
	}
	copy(target[:count], body.data[body.read:body.read+count])
	body.read += count
	if body.failWith != nil && !body.failed && int64(body.read) >= body.failAfter {
		body.failed = true
		return count, body.failWith
	}
	return count, nil
}

func (body *checkpointBody) Close() error { return nil }

func checkpointJob(destination, identity string, options *CheckpointOptions) Job {
	if options != nil && options.StateDirectory == "" {
		options.StateDirectory = destination + ".checkpoint"
	}
	return Job{
		URL:            "https://fixture.invalid/media",
		ResumeIdentity: identity,
		OutputRoot:     filepath.Dir(destination),
		Destination:    destination,
		Checkpoint:     options,
	}
}

func checkpointStatePath(destination string) string {
	return filepath.Join(destination+".checkpoint", "direct.json")
}

func checkpointData(size int64) []byte {
	data := make([]byte, size)
	for index := range data {
		data[index] = byte((index*17 + index/251) % 251)
	}
	return data
}

func writeCheckpointState(t *testing.T, path string, state partialState) {
	t.Helper()
	if state.Version == 0 {
		state.Version = directCheckpointStateVersion
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if state.ResumeIdentity != "" && strings.HasSuffix(filepath.Dir(path), ".checkpoint") {
		destination := strings.TrimSuffix(filepath.Dir(path), ".checkpoint")
		writeCheckpointOwner(t, destination, state.ResumeIdentity)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := protectCheckpointCreated(path, false); err != nil {
		t.Fatal(err)
	}
}

func writeCheckpointOwner(t *testing.T, destination, identity string) {
	t.Helper()
	directory := destination + ".checkpoint"
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := protectCheckpointCreated(directory, true); err != nil {
		t.Fatal(err)
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	canonicalDestination := filepath.Join(resolvedParent, filepath.Base(destination))
	digest := sha256.Sum256([]byte(identity + "\x00" + canonicalDestination + ".part"))
	if err := os.WriteFile(filepath.Join(directory, "owner"), []byte(fmt.Sprintf("%x\n", digest)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := protectCheckpointCreated(filepath.Join(directory, "owner"), false); err != nil {
		t.Fatal(err)
	}
}

func readCheckpointState(t *testing.T, path string) partialState {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state partialState
	if err := json.Unmarshal(encoded, &state); err != nil {
		t.Fatal(err)
	}
	return state
}
