package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	writeCheckpointState(t, partPath+".json", partialState{
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
		Checkpoint: &CheckpointOptions{ResumeBoundary: &Checkpoint{
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
	partPath := destination + ".part"
	if err := os.WriteFile(partPath, data[:minDirectCheckpointBytes], 0o600); err != nil {
		t.Fatal(err)
	}
	writeCheckpointState(t, partPath+".json", partialState{
		ResumeIdentity: "direct:other:1",
		ETag:           `"fixture"`,
		Total:          int64(len(data)),
		CommittedBytes: minDirectCheckpointBytes,
	})
	doer := &checkpointDoer{data: data, etag: `"fixture"`, chunkSize: 32 << 10}
	identity := "direct:fixture:1"
	job := checkpointJob(destination, identity, &CheckpointOptions{
		ResumeBoundary: &Checkpoint{ResumeIdentity: identity, ETag: `"fixture"`, Total: int64(len(data)), CommittedBytes: minDirectCheckpointBytes},
		EveryBytes:     minDirectCheckpointBytes,
	})
	result, err := New(doer).Download(context.Background(), job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed {
		t.Fatal("mismatched local identity was resumed")
	}
	if got := doer.requestRange(0); got != "" {
		t.Fatalf("range = %q, want fresh download", got)
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
		OnCommit: func(state Checkpoint) error {
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
		OnCommit: func(state Checkpoint) error {
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
		OnCommit: func(state Checkpoint) error {
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
	state := readCheckpointState(t, destination+".part.json")
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
		OnCommit:   func(Checkpoint) error { return callbackCause },
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
	state := readCheckpointState(t, destination+".part.json")
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
	state := readCheckpointState(t, destination+".part.json")
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
		OnCommit:   func(Checkpoint) error { return nil },
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
	encoded, readErr := os.ReadFile(destination + ".part.json")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(encoded), "signed.example") || strings.Contains(string(encoded), "secret") || !strings.Contains(string(encoded), identity) {
		t.Fatalf("state contains unsafe URL material: %s", encoded)
	}
	boundary := readCheckpointState(t, destination+".part.json")
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
			job := Job{Checkpoint: &CheckpointOptions{}}
			state := partialState{ResumeIdentity: "direct:fixture:1", Total: 2 * minDirectCheckpointBytes, CommittedBytes: minDirectCheckpointBytes}
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
	job := Job{URL: "https://example.invalid/media", OutputRoot: root, Destination: filepath.Join(root, "media.bin"), NoPart: true, Checkpoint: &CheckpointOptions{}}
	if err := validateJob(job); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("NoPart checkpoint error = %v", err)
	}

	destination := filepath.Join(root, "media.bin")
	statePath := destination + ".part.json"
	if err := os.WriteFile(filepath.Join(root, ".atomic-retained"), []byte("candidate"), 0o600); err != nil {
		t.Fatal(err)
	}
	job.NoPart = false
	job.URL = "https://example.invalid/media"
	job.Checkpoint = &CheckpointOptions{}
	if _, err := New(&checkpointDoer{data: checkpointData(minDirectCheckpointBytes), etag: `"fixture"`}).Download(context.Background(), job, nil); !errors.Is(err, ErrCheckpointReconciliation) {
		t.Fatalf("retained evidence error = %v, want reconciliation", err)
	}
	if _, statErr := os.Stat(statePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("state path = %v, evidence failure should not create state", statErr)
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
		if err := validateJob(Job{Checkpoint: &options}); !errors.Is(err, ErrInvalidCheckpoint) {
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
				OnCommit: func(state Checkpoint) error {
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
	return Job{
		URL:            "https://fixture.invalid/media",
		ResumeIdentity: identity,
		OutputRoot:     filepath.Dir(destination),
		Destination:    destination,
		Checkpoint:     options,
	}
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
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
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
