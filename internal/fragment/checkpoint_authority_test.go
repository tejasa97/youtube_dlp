package fragment

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckpointCommitSnapshotsFollowContiguousCompletion(t *testing.T) {
	for _, test := range []struct {
		name          string
		completion    []int
		wantSequences []uint64
		wantBytes     []int64
	}{
		{name: "ordered", completion: []int{0, 1, 2}, wantSequences: []uint64{1, 2, 3}, wantBytes: []int64{1, 3, 6}},
		{name: "out of order", completion: []int{2, 0, 1}, wantSequences: []uint64{1, 3}, wantBytes: []int64{1, 6}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			transport := newControlledFragmentTransport(map[int]string{0: "a", 1: "bb", 2: "ccc"})
			job := checkpointTestJob(root, filepath.Join(root, "out"), "asset:ordered", []Segment{
				{URL: "https://example.test/0"},
				{URL: "https://example.test/1"},
				{URL: "https://example.test/2"},
			})
			job.Concurrency = 3
			snapshots := make(chan CommitSnapshot, 3)
			job.Checkpoint.OnCommit = func(localCtx context.Context, snapshot CommitSnapshot) error {
				if _, ok := localCtx.Deadline(); !ok {
					return errors.New("callback context has no deadline")
				}
				if err := snapshot.Validate(); err != nil {
					return fmt.Errorf("snapshot is not canonical: %w", err)
				}
				snapshots <- snapshot
				return nil
			}
			done := make(chan error, 1)
			go func() {
				_, err := New(transport).Download(context.Background(), job, nil)
				done <- err
			}()
			transport.waitStarted(t, 3)

			var got []CommitSnapshot
			released := make(map[int]bool)
			contiguous := 0
			for _, index := range test.completion {
				transport.release(index)
				released[index] = true
				prior := contiguous
				for released[contiguous] {
					contiguous++
				}
				if contiguous > prior {
					select {
					case snapshot := <-snapshots:
						got = append(got, snapshot)
					case <-time.After(2 * time.Second):
						t.Fatalf("no callback after contiguous prefix advanced to %d", contiguous)
					}
				} else {
					waitForLedgerArtifact(t, job.Checkpoint.Directory, index)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if len(got) != len(test.wantSequences) {
				t.Fatalf("snapshots = %#v, want sequences %v", got, test.wantSequences)
			}
			for index, snapshot := range got {
				if snapshot.Sequence != test.wantSequences[index] || snapshot.CommittedBytes != test.wantBytes[index] {
					t.Fatalf("snapshot[%d] = %#v, want sequence=%d bytes=%d", index, snapshot, test.wantSequences[index], test.wantBytes[index])
				}
			}
		})
	}
}

func TestCheckpointCallbackFailureStopsAndKeepsCallerAuthorityOld(t *testing.T) {
	root := t.TempDir()
	transport := newControlledFragmentTransport(map[int]string{0: "fragment"})
	transport.release(0)
	job := checkpointTestJob(root, filepath.Join(root, "out"), "asset:callback", []Segment{{URL: "https://example.test/0"}})
	cause := errors.New("manifest rejected https://secret.example/?cookie=credential")
	var calls atomic.Int32
	job.Checkpoint.OnCommit = func(context.Context, CommitSnapshot) error {
		calls.Add(1)
		return cause
	}
	_, err := New(transport).Download(context.Background(), job, nil)
	if !errors.Is(err, ErrCheckpointCallback) || !errors.Is(err, cause) {
		t.Fatalf("error = %v, want callback failure and cause", err)
	}
	if strings.Contains(err.Error(), "secret.example") || strings.Contains(err.Error(), "credential") {
		t.Fatalf("callback error leaked secret material: %q", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("callback calls = %d, want 1", calls.Load())
	}
	state := readAuthorityState(t, job.Checkpoint.Directory)
	if contiguousSequence(state) != 1 {
		t.Fatalf("local ledger sequence = %d, want committed local-ahead sequence 1", contiguousSequence(state))
	}
	if _, statErr := os.Lstat(job.Destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination was published after callback failure: %v", statErr)
	}
}

func TestCheckpointCancellationWaitsForInFlightCallback(t *testing.T) {
	root := t.TempDir()
	transport := newControlledFragmentTransport(map[int]string{0: "fragment"})
	transport.release(0)
	job := checkpointTestJob(root, filepath.Join(root, "out"), "asset:cancel-callback", []Segment{{URL: "https://example.test/0"}})
	entered := make(chan struct{})
	release := make(chan struct{})
	job.Checkpoint.OnCommit = func(localCtx context.Context, _ CommitSnapshot) error {
		if _, ok := localCtx.Deadline(); !ok {
			return errors.New("callback context is unbounded")
		}
		close(entered)
		<-release
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := New(transport).Download(ctx, job, nil)
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		t.Fatalf("download returned before callback settled: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation after callback settlement", err)
	}
}

func TestCheckpointCancellationCallbackUsesBoundedIndependentContext(t *testing.T) {
	root := t.TempDir()
	transport := newControlledFragmentTransport(map[int]string{0: "fragment"})
	transport.release(0)
	job := checkpointTestJob(root, filepath.Join(root, "out"), "asset:bounded-callback", []Segment{{URL: "https://example.test/0"}})
	entered := make(chan struct{})
	job.Checkpoint.OnCommit = func(localCtx context.Context, _ CommitSnapshot) error {
		close(entered)
		if _, ok := localCtx.Deadline(); !ok {
			return errors.New("callback context is unbounded")
		}
		<-localCtx.Done()
		return localCtx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	engine := New(transport)
	engine.checkpointTimeout = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() {
		_, err := engine.Download(ctx, job, nil)
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		t.Fatalf("outer cancellation reached the independent callback context: %v", err)
	case <-time.After(5 * time.Millisecond):
	}
	err := <-done
	if !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrCheckpointCallback) {
		t.Fatalf("error = %v, want outer cancellation and bounded callback timeout", err)
	}
}

func TestCheckpointCallerBoundaryClampsLocalAheadAfterCallbackFailure(t *testing.T) {
	root := t.TempDir()
	job := checkpointTestJob(root, filepath.Join(root, "out"), "asset:clamp", []Segment{
		{URL: "https://old.example/0"},
		{URL: "https://old.example/1"},
	})
	writeCheckpointFixture(t, job, map[int][]byte{0: []byte("prefix-")})
	boundary := boundaryFromLedger(t, job, 1)
	job.Checkpoint.ResumeBoundary = &boundary
	transport := newControlledFragmentTransport(map[int]string{1: "tail"})
	transport.release(1)
	cause := errors.New("session manifest write failed")
	job.Checkpoint.OnCommit = func(_ context.Context, snapshot CommitSnapshot) error {
		if snapshot.Sequence != 2 {
			return fmt.Errorf("failed callback snapshot = %#v", snapshot)
		}
		return cause
	}
	if _, err := New(transport).Download(context.Background(), job, nil); !errors.Is(err, cause) {
		t.Fatalf("first error = %v, want callback cause", err)
	}
	if got := contiguousSequence(readAuthorityState(t, job.Checkpoint.Directory)); got != 2 {
		t.Fatalf("local sequence after callback failure = %d, want 2", got)
	}

	job.Segments[0].URL = "https://refreshed.example/0?token=secret"
	job.Segments[1].URL = "https://refreshed.example/1?token=secret"
	job.Checkpoint.OnCommit = func(_ context.Context, snapshot CommitSnapshot) error {
		if snapshot.Sequence != 2 || snapshot.CommittedBytes != int64(len("prefix-tail")) {
			return fmt.Errorf("reopen snapshot = %#v", snapshot)
		}
		return nil
	}
	transport.addResponse(1, "tail")
	transport.release(1)
	result, err := New(transport).Download(context.Background(), job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reused != 1 || result.Downloaded != 1 || transport.hits(1) != 2 {
		t.Fatalf("result=%#v tail hits=%d, caller-old boundary did not force tail re-download", result, transport.hits(1))
	}
}

func TestCheckpointCanonicalZeroBoundaryResetsOwnedWork(t *testing.T) {
	root := t.TempDir()
	job := checkpointTestJob(root, filepath.Join(root, "out"), "asset:zero", []Segment{
		{URL: "https://example.test/0"},
		{URL: "https://example.test/1"},
	})
	writeCheckpointFixture(t, job, map[int][]byte{0: []byte("old-0"), 1: []byte("old-1")})
	zero, err := InitialResumeBoundary(job.Checkpoint.ResumeIdentity, job.Segments)
	if err != nil {
		t.Fatal(err)
	}
	job.Checkpoint.ResumeBoundary = &zero
	transport := newControlledFragmentTransport(map[int]string{0: "new-0", 1: "new-1"})
	transport.release(0)
	transport.release(1)
	result, err := New(transport).Download(context.Background(), job, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, readErr := os.ReadFile(job.Destination)
	if readErr != nil || string(contents) != "new-0new-1" || result.Reused != 0 || result.Downloaded != 2 {
		t.Fatalf("contents=%q result=%#v err=%v", contents, result, readErr)
	}
}

func TestCheckpointZeroBoundaryRejectsRetainedPublicationEvidenceAndDestination(t *testing.T) {
	for _, test := range []struct {
		name            string
		evidence        string
		withDestination bool
	}{
		{name: publicationMarker, evidence: publicationMarker},
		{name: reconciliationMarker, evidence: reconciliationMarker},
		{name: finalPublicationEvidence, evidence: finalPublicationEvidence},
		{name: ".atomic-retained", evidence: ".atomic-retained"},
		{name: "destination-only", withDestination: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			job := checkpointTestJob(root, filepath.Join(root, "out"), "asset:zero-protected", []Segment{{URL: "https://example.test/0"}})
			job.Overwrite = true
			writeCheckpointFixture(t, job, map[int][]byte{0: []byte("old")})
			if test.evidence != "" {
				if err := os.WriteFile(filepath.Join(job.Checkpoint.Directory, test.evidence), []byte("protected evidence"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.withDestination {
				if err := os.WriteFile(job.Destination, []byte("old destination"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			zero, err := InitialResumeBoundary(job.Checkpoint.ResumeIdentity, job.Segments)
			if err != nil {
				t.Fatal(err)
			}
			job.Checkpoint.ResumeBoundary = &zero
			before := snapshotCheckpointTree(t, root)
			transport := newControlledFragmentTransport(map[int]string{0: "network"})
			_, err = New(transport).Download(context.Background(), job, nil)
			if !errors.Is(err, ErrCheckpointReconciliation) {
				t.Fatalf("error = %v, want reconciliation", err)
			}
			if transport.totalHits() != 0 {
				t.Fatalf("network hits = %d, want zero", transport.totalHits())
			}
			if after := snapshotCheckpointTree(t, root); !reflect.DeepEqual(after, before) {
				t.Fatalf("protected reset mutated state: before=%v after=%v", before, after)
			}
			if test.withDestination {
				contents, readErr := os.ReadFile(job.Destination)
				if readErr != nil || string(contents) != "old destination" {
					t.Fatalf("destination changed: contents=%q err=%v", contents, readErr)
				}
			}
		})
	}
}

func TestCheckpointBoundaryRejectionIsReadOnlyAndPreNetwork(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, job *Job, boundary *ResumeBoundary)
	}{
		{name: "missing ledger", mutate: func(t *testing.T, job *Job, _ *ResumeBoundary) {
			t.Helper()
			if err := os.Remove(filepath.Join(job.Checkpoint.Directory, "state.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "corrupt ledger", mutate: func(t *testing.T, job *Job, _ *ResumeBoundary) {
			writeRawLedger(t, *job, []byte("{corrupt"))
		}},
		{name: "missing fragment", mutate: func(t *testing.T, job *Job, _ *ResumeBoundary) {
			if err := os.Remove(fragmentPath(job.Checkpoint.Directory, 0)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "corrupt fragment", mutate: func(t *testing.T, job *Job, _ *ResumeBoundary) {
			if err := os.WriteFile(fragmentPath(job.Checkpoint.Directory, 0), []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "identity mismatch", mutate: func(_ *testing.T, _ *Job, boundary *ResumeBoundary) {
			boundary.ResumeIdentity = "asset:other"
		}},
		{name: "plan mismatch", mutate: func(_ *testing.T, job *Job, _ *ResumeBoundary) {
			job.Segments[0].RangeStart = 1
			job.Segments[0].RangeLength = 1
		}},
		{name: "digest tamper", mutate: func(_ *testing.T, _ *Job, boundary *ResumeBoundary) {
			boundary.Digest = strings.Repeat("0", 64)
		}},
		{name: "byte tamper", mutate: func(_ *testing.T, _ *Job, boundary *ResumeBoundary) {
			boundary.CommittedBytes++
		}},
		{name: "sequence tamper", mutate: func(_ *testing.T, _ *Job, boundary *ResumeBoundary) {
			boundary.Sequence++
		}},
		{name: "noncanonical digest", mutate: func(_ *testing.T, _ *Job, boundary *ResumeBoundary) {
			boundary.Digest = strings.ToUpper(boundary.Digest)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			job := checkpointTestJob(root, filepath.Join(root, "destination", "out"), "asset:reject", []Segment{
				{URL: "https://example.test/0"},
				{URL: "https://example.test/1"},
			})
			job.Checkpoint.Directory = filepath.Join(root, "checkpoints", "reject")
			writeCheckpointFixture(t, job, map[int][]byte{0: []byte("prefix")})
			boundary := boundaryFromLedger(t, job, 1)
			test.mutate(t, &job, &boundary)
			job.Checkpoint.ResumeBoundary = &boundary
			before := snapshotCheckpointTree(t, job.Checkpoint.Directory)
			transport := newControlledFragmentTransport(map[int]string{0: "network", 1: "network"})
			_, err := New(transport).Download(context.Background(), job, nil)
			if err == nil || (!errors.Is(err, ErrInvalidCheckpoint) && !errors.Is(err, ErrCheckpointReconciliation)) {
				t.Fatalf("error = %v, want checkpoint rejection", err)
			}
			if transport.totalHits() != 0 {
				t.Fatalf("network hits = %d before boundary rejection", transport.totalHits())
			}
			if after := snapshotCheckpointTree(t, job.Checkpoint.Directory); !reflect.DeepEqual(after, before) {
				t.Fatalf("checkpoint mutated before rejection: before=%v after=%v", before, after)
			}
			if _, statErr := os.Lstat(filepath.Dir(job.Destination)); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("destination parent was created before rejection: %v", statErr)
			}
		})
	}
}

func TestCheckpointAuthorityValuesExcludeRequestSecrets(t *testing.T) {
	identity := "asset:safe"
	oldSegments := []Segment{{
		URL:    "https://old.example/media?token=old-secret",
		AES128: &AES128{Key: []byte("0123456789abcdef"), IV: []byte("abcdef0123456789")},
	}}
	newSegments := []Segment{{
		URL:    "https://new.example/media?token=new-secret",
		AES128: &AES128{Key: []byte("fedcba9876543210"), IV: []byte("9876543210fedcba")},
	}}
	oldBoundary, err := InitialResumeBoundary(identity, oldSegments)
	if err != nil {
		t.Fatal(err)
	}
	newBoundary, err := InitialResumeBoundary(identity, newSegments)
	if err != nil {
		t.Fatal(err)
	}
	if oldBoundary != newBoundary {
		t.Fatalf("credential refresh changed structural authority: old=%#v new=%#v", oldBoundary, newBoundary)
	}
	encoded := string(mustJSON(t, oldBoundary))
	for _, secret := range []string{"old.example", "old-secret", "0123456789abcdef", "abcdef0123456789"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("safe authority persisted %q: %s", secret, encoded)
		}
	}
}

func TestInitialResumeBoundaryValidatesFinitePlanBeforeHashing(t *testing.T) {
	valid := []Segment{{URL: "https://example.test/0"}}
	if _, err := InitialResumeBoundary("asset:empty", nil); !errors.Is(err, ErrInvalidCheckpoint) || !errors.Is(err, ErrNoSegments) {
		t.Fatalf("empty plan error = %v, want invalid checkpoint and no segments", err)
	}
	if _, err := InitialResumeBoundary("asset:oversized", make([]Segment, maxFragmentSegments+1)); !errors.Is(err, ErrInvalidCheckpoint) || !errors.Is(err, ErrTooManySegments) {
		t.Fatalf("oversized plan error = %v, want invalid checkpoint and too many segments", err)
	}
	for _, test := range []struct {
		name    string
		segment Segment
	}{
		{name: "negative start", segment: Segment{URL: valid[0].URL, RangeStart: -1, RangeLength: 1}},
		{name: "negative length", segment: Segment{URL: valid[0].URL, RangeLength: -1}},
		{name: "start without length", segment: Segment{URL: valid[0].URL, RangeStart: 1}},
		{name: "end overflow", segment: Segment{URL: valid[0].URL, RangeStart: int64(^uint64(0) >> 1), RangeLength: 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := InitialResumeBoundary("asset:invalid-range", []Segment{test.segment}); !errors.Is(err, ErrInvalidCheckpoint) || !errors.Is(err, ErrInvalidSegmentRange) {
				t.Fatalf("range error = %v, want invalid checkpoint and range error", err)
			}
		})
	}
}

func TestResumeBoundaryRejectsAllControlIdentityBytes(t *testing.T) {
	controls := []string{"\x00", "\x01", "\x1f", "\x7f", "\u0085"}
	for _, control := range controls {
		t.Run(fmt.Sprintf("U%04X", []rune(control)[0]), func(t *testing.T) {
			identity := "asset:control:" + control + ":suffix"
			if _, err := InitialResumeBoundary(identity, []Segment{{URL: "https://example.test/0"}}); !errors.Is(err, ErrInvalidCheckpoint) {
				t.Fatalf("identity %q error = %v, want invalid checkpoint", identity, err)
			}
		})
	}
	if _, err := InitialResumeBoundary(string([]byte{0xff}), []Segment{{URL: "https://example.test/0"}}); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("invalid UTF-8 error = %v, want invalid checkpoint", err)
	}
}

func TestCheckpointZeroResetFailureOrderingIsRetryable(t *testing.T) {
	for _, test := range []struct {
		name      string
		inject    func(*Engine, string)
		wantState bool
		wantFrag  bool
	}{
		{name: "fragment remove", inject: func(engine *Engine, _ string) {
			engine.resetOps.remove = func(string) error { return errors.New("injected fragment remove failure") }
		}, wantState: true, wantFrag: true},
		{name: "fragment sync", inject: func(engine *Engine, _ string) {
			engine.resetOps.syncDirectory = func(string) error { return errors.New("injected fragment sync failure") }
		}, wantState: true, wantFrag: false},
		{name: "ledger remove", inject: func(engine *Engine, directory string) {
			productionRemove := engine.resetOps.remove
			engine.resetOps.remove = func(path string) error {
				if filepath.Dir(path) == directory && filepath.Base(path) == "state.json" {
					return errors.New("injected ledger remove failure")
				}
				return productionRemove(path)
			}
		}, wantState: true, wantFrag: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			job := checkpointTestJob(root, filepath.Join(root, "out"), "asset:reset-failure", []Segment{{URL: "https://example.test/0"}})
			writeCheckpointFixture(t, job, map[int][]byte{0: []byte("old")})
			zero, err := InitialResumeBoundary(job.Checkpoint.ResumeIdentity, job.Segments)
			if err != nil {
				t.Fatal(err)
			}
			job.Checkpoint.ResumeBoundary = &zero
			transport := newControlledFragmentTransport(map[int]string{0: "network"})
			engine := New(transport)
			test.inject(engine, job.Checkpoint.Directory)
			_, err = engine.Download(context.Background(), job, nil)
			if !errors.Is(err, ErrCheckpointReconciliation) {
				t.Fatalf("error = %v, want reconciliation", err)
			}
			if transport.totalHits() != 0 {
				t.Fatalf("network hits = %d, want zero", transport.totalHits())
			}
			_, stateErr := os.Lstat(filepath.Join(job.Checkpoint.Directory, "state.json"))
			_, fragmentErr := os.Lstat(fragmentPath(job.Checkpoint.Directory, 0))
			if (stateErr == nil) != test.wantState || (fragmentErr == nil) != test.wantFrag {
				t.Fatalf("reset failure state present=%v fragment present=%v, want state=%v fragment=%v", stateErr == nil, fragmentErr == nil, test.wantState, test.wantFrag)
			}
			if transport.totalHits() != 0 {
				t.Fatalf("network started after reset failure: %d", transport.totalHits())
			}
		})
	}
}

func TestCheckpointZeroResetValidatesAllCandidatesBeforeRemoval(t *testing.T) {
	root := t.TempDir()
	job := checkpointTestJob(root, filepath.Join(root, "out"), "asset:reset-validation", []Segment{{URL: "https://example.test/0"}, {URL: "https://example.test/1"}})
	writeCheckpointFixture(t, job, map[int][]byte{0: []byte("first"), 1: []byte("second")})
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fragmentPath(job.Checkpoint.Directory, 1)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, fragmentPath(job.Checkpoint.Directory, 1)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	zero, err := InitialResumeBoundary(job.Checkpoint.ResumeIdentity, job.Segments)
	if err != nil {
		t.Fatal(err)
	}
	job.Checkpoint.ResumeBoundary = &zero
	before := snapshotCheckpointTree(t, root)
	transport := newControlledFragmentTransport(map[int]string{0: "network", 1: "network"})
	_, err = New(transport).Download(context.Background(), job, nil)
	if !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("error = %v, want invalid checkpoint", err)
	}
	if after := snapshotCheckpointTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("candidate validation removed prior state: before=%v after=%v", before, after)
	}
	if transport.totalHits() != 0 {
		t.Fatalf("network hits = %d, want zero", transport.totalHits())
	}
}

func TestCheckpointManifestGuardsDuplicateAndRegressedCallbacks(t *testing.T) {
	root := t.TempDir()
	job := checkpointTestJob(root, filepath.Join(root, "out"), "asset:guards", []Segment{{URL: "https://example.test/0"}})
	writeCheckpointFixture(t, job, nil)
	manifest, err := openArtifactManifest(job.Checkpoint.Directory, mustExpectation(t, job), New(nil).writeAtomic)
	if err != nil {
		t.Fatal(err)
	}
	var callbacks atomic.Int32
	job.Checkpoint.OnCommit = func(context.Context, CommitSnapshot) error {
		callbacks.Add(1)
		return nil
	}
	if err := manifest.configureCallback(job.Checkpoint, time.Second); err != nil {
		t.Fatal(err)
	}
	path := fragmentPath(job.Checkpoint.Directory, 0)
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Record(0, path); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Record(0, path); err != nil {
		t.Fatal(err)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("duplicate record produced %d callbacks", callbacks.Load())
	}
	manifest.notifiedSequence = 2
	if err := manifest.Record(0, path); !errors.Is(err, ErrCheckpointReconciliation) {
		t.Fatalf("regression error = %v", err)
	}

	below, err := openArtifactManifest(job.Checkpoint.Directory, mustExpectation(t, job), New(nil).writeAtomic)
	if err != nil {
		t.Fatal(err)
	}
	below.state.Artifacts = nil
	below.notifiedSequence = 0
	below.callerSequence = 2
	if err := below.Record(0, path); !errors.Is(err, ErrCheckpointReconciliation) {
		t.Fatalf("below-caller error = %v", err)
	}
}

func TestCheckpointSnapshotRejectsCommittedByteOverflow(t *testing.T) {
	state := manifestState{
		Version: checkpointVersion, ResumeIdentity: "asset:overflow", PlanHash: strings.Repeat("1", 64),
		Artifacts: map[int]artifact{
			0: {Bytes: math.MaxInt64, SHA256: strings.Repeat("2", 64)},
			1: {Bytes: 1, SHA256: strings.Repeat("3", 64)},
		},
	}
	if _, err := snapshotForPrefix(state, 2); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("overflow error = %v, want invalid checkpoint", err)
	}
}

func boundaryFromLedger(t *testing.T, job Job, sequence uint64) ResumeBoundary {
	t.Helper()
	snapshot, err := snapshotForPrefix(readAuthorityState(t, job.Checkpoint.Directory), sequence)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.ResumeBoundary()
}

func readAuthorityState(t *testing.T, directory string) manifestState {
	t.Helper()
	state, err := readManifestState(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func waitForLedgerArtifact(t *testing.T, directory string, index int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, err := readManifestState(filepath.Join(directory, "state.json"))
		if err == nil {
			if _, ok := state.Artifacts[index]; ok {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("artifact %d did not reach checkpoint ledger", index)
}

type controlledFragmentTransport struct {
	mu        sync.Mutex
	responses map[int][]string
	gates     map[int]chan struct{}
	started   chan int
	hitCount  map[int]int
}

func newControlledFragmentTransport(responses map[int]string) *controlledFragmentTransport {
	transport := &controlledFragmentTransport{
		responses: make(map[int][]string), gates: make(map[int]chan struct{}),
		started: make(chan int, maxFragmentSegments), hitCount: make(map[int]int),
	}
	for index, response := range responses {
		transport.addResponse(index, response)
	}
	return transport
}

func (transport *controlledFragmentTransport) addResponse(index int, response string) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.responses[index] = append(transport.responses[index], response)
	if transport.gates[index] == nil {
		transport.gates[index] = make(chan struct{}, maxFragmentSegments)
	}
}

func (transport *controlledFragmentTransport) release(index int) {
	transport.mu.Lock()
	gate := transport.gates[index]
	if gate == nil {
		gate = make(chan struct{}, maxFragmentSegments)
		transport.gates[index] = gate
	}
	transport.mu.Unlock()
	gate <- struct{}{}
}

func (transport *controlledFragmentTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	index := int(request.URL.Path[len(request.URL.Path)-1] - '0')
	transport.mu.Lock()
	transport.hitCount[index]++
	hit := transport.hitCount[index]
	gate := transport.gates[index]
	responses := transport.responses[index]
	transport.mu.Unlock()
	transport.started <- index
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-gate:
	}
	if hit > len(responses) {
		return nil, errors.New("missing controlled response")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(responses[hit-1])),
		Header:     make(http.Header),
	}, nil
}

func (transport *controlledFragmentTransport) waitStarted(t *testing.T, count int) {
	t.Helper()
	for index := 0; index < count; index++ {
		select {
		case <-transport.started:
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d/%d requests started", index, count)
		}
	}
}

func (transport *controlledFragmentTransport) hits(index int) int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.hitCount[index]
}

func (transport *controlledFragmentTransport) totalHits() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	var total int
	for _, count := range transport.hitCount {
		total += count
	}
	return total
}
