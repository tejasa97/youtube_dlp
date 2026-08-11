package fragment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/atomicfile"
	"github.com/tejasa97/youtube_dlp/internal/network"
)

func TestCheckpointResumesAcrossRefreshedCredentialsWithoutPersistingSecrets(t *testing.T) {
	oldKey := []byte("0123456789abcdef")
	newKey := []byte("fedcba9876543210")
	oldIV := []byte("abcdef0123456789")
	newIV := []byte("9876543210fedcba")
	newEncrypted := encrypt(t, []byte("two"), newKey, newIV)
	var resumed atomic.Bool
	var firstHits atomic.Int32
	secondStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/one":
			firstHits.Add(1)
			_, _ = writer.Write([]byte("one-"))
		case "/two":
			if !resumed.Load() {
				select {
				case secondStarted <- struct{}{}:
				default:
				}
				<-request.Context().Done()
				return
			}
			_, _ = writer.Write(newEncrypted)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "resume.bin")
	job := Job{
		OutputRoot: root, Destination: destination, Concurrency: 1,
		Checkpoint: &Checkpoint{Directory: filepath.Join(root, "checkpoints", "stable"), ResumeIdentity: "asset:stable-id"},
		Headers: http.Header{
			"Authorization": {"Bearer old-header-secret"},
			"Cookie":        {"session=old-cookie-secret"},
			"X-Visitor":     {"old-visitor-secret"},
		},
		Segments: []Segment{
			{URL: server.URL + "/one?token=old-url-secret"},
			{URL: server.URL + "/two?signature=old-signature-secret", AES128: &AES128{Key: oldKey, IV: oldIV}},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := New(transport).Download(ctx, job, nil)
		done <- err
	}()
	<-secondStarted
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}

	ledger, err := os.ReadFile(filepath.Join(job.Checkpoint.Directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"old-url-secret", "old-signature-secret", "old-header-secret", "old-cookie-secret",
		"old-visitor-secret", string(oldKey), string(oldIV), server.URL,
	} {
		if strings.Contains(string(ledger), secret) {
			t.Fatalf("checkpoint ledger persisted secret or raw manifest data %q: %s", secret, ledger)
		}
	}
	if !strings.Contains(string(ledger), "asset:stable-id") {
		t.Fatalf("checkpoint ledger does not contain caller identity: %s", ledger)
	}

	resumed.Store(true)
	job.Headers = http.Header{"Authorization": {"Bearer refreshed-header-secret"}}
	job.Segments = []Segment{
		{URL: server.URL + "/one?token=refreshed-url-secret"},
		{URL: server.URL + "/two?signature=refreshed-signature-secret", AES128: &AES128{Key: newKey, IV: newIV}},
	}
	result, err := New(transport).Download(context.Background(), job, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(destination)
	if string(contents) != "one-two" || result.Reused != 1 || result.Downloaded != 1 || firstHits.Load() != 1 {
		t.Fatalf("contents=%q result=%#v first hits=%d", contents, result, firstHits.Load())
	}
}

func TestCheckpointRejectsMissingChangedIdentityAndPlan(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "out")
	job := checkpointTestJob(root, destination, "asset=one;format=mp4;plan=video-only", []Segment{{URL: "https://example.test/one"}})
	writeCheckpointFixture(t, job, map[int][]byte{0: []byte("committed")})

	missing := job
	missing.Checkpoint = &Checkpoint{Directory: job.Checkpoint.Directory}
	if _, err := New(nil).Download(context.Background(), missing, nil); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("missing identity error = %v", err)
	}
	missingDirectory := job
	missingDirectory.Checkpoint = &Checkpoint{ResumeIdentity: "stable"}
	if _, err := New(nil).Download(context.Background(), missingDirectory, nil); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("missing checkpoint directory error = %v", err)
	}
	unsafeBoundary := job
	unsafeBoundary.Checkpoint = &Checkpoint{Directory: root, ResumeIdentity: job.Checkpoint.ResumeIdentity}
	if _, err := New(nil).Download(context.Background(), unsafeBoundary, nil); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("checkpoint boundary containing destination error = %v", err)
	}
	reverseBoundary := job
	reverseBoundary.Checkpoint = &Checkpoint{Directory: filepath.Join(destination, "checkpoint"), ResumeIdentity: job.Checkpoint.ResumeIdentity}
	if _, err := New(nil).Download(context.Background(), reverseBoundary, nil); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("destination boundary containing checkpoint error = %v", err)
	}
	oversized := job
	oversized.Checkpoint = &Checkpoint{Directory: job.Checkpoint.Directory, ResumeIdentity: strings.Repeat("x", maxResumeIdentityBytes+1)}
	if _, err := New(nil).Download(context.Background(), oversized, nil); !errors.Is(err, ErrInvalidCheckpoint) {
		t.Fatalf("oversized identity error = %v", err)
	}

	changed := job
	changed.Checkpoint = &Checkpoint{Directory: job.Checkpoint.Directory, ResumeIdentity: "asset=one;format=webm;plan=video-only"}
	changed.Segments = []Segment{{URL: "https://refreshed.example/same-shape-url-only-plan"}}
	beforeMismatch := snapshotCheckpointTree(t, job.Checkpoint.Directory)
	if _, err := New(nil).Download(context.Background(), changed, nil); !errors.Is(err, ErrCheckpointReconciliation) {
		t.Fatalf("changed identity error = %v", err)
	}
	if after := snapshotCheckpointTree(t, job.Checkpoint.Directory); !reflect.DeepEqual(after, beforeMismatch) {
		t.Fatalf("identity mismatch changed checkpoint state: before=%v after=%v", beforeMismatch, after)
	}

	changedPlan := job
	changedPlan.Segments = []Segment{{URL: "https://refreshed.example/one", RangeStart: 4, RangeLength: 8}}
	if _, err := New(nil).Download(context.Background(), changedPlan, nil); !errors.Is(err, ErrCheckpointReconciliation) {
		t.Fatalf("changed plan error = %v", err)
	}
	if contents, err := os.ReadFile(fragmentPath(job.Checkpoint.Directory, 0)); err != nil || string(contents) != "committed" {
		t.Fatalf("prior work changed: %q, %v", contents, err)
	}
}

func TestCheckpointRejectsForbiddenIdentityControls(t *testing.T) {
	for _, identity := range []string{"stable\x00suffix", "stable\rformat", "stable\nformat"} {
		root := t.TempDir()
		job := checkpointTestJob(root, filepath.Join(root, "out"), identity, []Segment{{URL: "https://example.test/one"}})
		if _, err := New(nil).Download(context.Background(), job, nil); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("identity %q error = %v", identity, err)
		}
	}
}

func TestCheckpointDirectoryProtectionAndEmptyInitialization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("fragment"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	t.Run("caller-precreated empty", func(t *testing.T) {
		root := t.TempDir()
		job := checkpointTestJob(root, filepath.Join(root, "out"), "format=mp4;track=video", []Segment{{URL: server.URL}})
		ensureTestCheckpointDirectory(t, job)
		if _, err := New(transport).Download(context.Background(), job, nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("created chain owner only", func(t *testing.T) {
		root := t.TempDir()
		job := checkpointTestJob(root, filepath.Join(root, "out"), "format=mp4;track=video", []Segment{{URL: server.URL}})
		job.Checkpoint.Directory = filepath.Join(root, "private", "component")
		engine := New(transport)
		productionWrite := engine.writeAtomic
		engine.writeAtomic = func(path string, mode os.FileMode, encode func(io.Writer) error) error {
			if filepath.Base(path) == "state.json" && runtime.GOOS != "windows" {
				for _, directory := range []string{filepath.Join(root, "private"), job.Checkpoint.Directory} {
					info, err := os.Stat(directory)
					if err != nil {
						t.Fatalf("inspect checkpoint directory %s: %v", directory, err)
					}
					if info.Mode().Perm() != 0o700 {
						t.Fatalf("checkpoint directory %s mode = %v", directory, info.Mode().Perm())
					}
				}
			}
			return productionWrite(path, mode, encode)
		}
		if _, err := engine.Download(context.Background(), job, nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("symlinked chain", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		link := filepath.Join(root, "linked")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		job := checkpointTestJob(root, filepath.Join(root, "out"), "format=mp4;track=video", []Segment{{URL: server.URL}})
		job.Checkpoint.Directory = filepath.Join(link, "component")
		if _, err := New(transport).Download(context.Background(), job, nil); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("symlinked chain error = %v", err)
		}
		if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
			t.Fatalf("symlink target was changed: entries=%v error=%v", entries, err)
		}
	})
	t.Run("unprotected precreated directory", func(t *testing.T) {
		root := t.TempDir()
		job := checkpointTestJob(root, filepath.Join(root, "out"), "format=mp4;track=video", []Segment{{URL: server.URL}})
		if err := os.MkdirAll(job.Checkpoint.Directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" {
			if err := os.Chmod(job.Checkpoint.Directory, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := New(transport).Download(context.Background(), job, nil); !errors.Is(err, ErrInvalidCheckpoint) {
			t.Fatalf("unprotected checkpoint directory error = %v", err)
		}
	})
}

func TestCheckpointInitialLedgerPrecommitFailureAllowsEmptyReinitialization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("fragment"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	job := checkpointTestJob(root, filepath.Join(root, "out"), "format=mp4;track=video", []Segment{{URL: server.URL}})
	engine := New(transport)
	injected := &checkpointCommitError{name: "initial-ledger-precommit"}
	productionWrite := engine.writeAtomic
	engine.writeAtomic = func(path string, mode os.FileMode, encode func(io.Writer) error) error {
		if filepath.Base(path) == "state.json" {
			return injected
		}
		return productionWrite(path, mode, encode)
	}
	if _, err := engine.Download(context.Background(), job, nil); !errors.Is(err, injected) {
		t.Fatalf("initial ledger error = %v", err)
	}
	entries, err := os.ReadDir(job.Checkpoint.Directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("confirmed precommit did not leave a proven-empty boundary: entries=%v error=%v", entries, err)
	}
	if _, err := New(transport).Download(context.Background(), job, nil); err != nil {
		t.Fatalf("proven-empty checkpoint did not reinitialize: %v", err)
	}
}

func TestCheckpointEvidenceIsScopedToCallerOwnedDirectory(t *testing.T) {
	root := t.TempDir()
	sharedAtomicEvidence := filepath.Join(root, ".atomic-unrelated-job")
	if err := os.WriteFile(sharedAtomicEvidence, []byte("other job"), 0o600); err != nil {
		t.Fatal(err)
	}
	jobA := checkpointTestJob(root, filepath.Join(root, "a.out"), "a", []Segment{{URL: "https://example.test/a"}})
	jobA.Checkpoint.Directory = filepath.Join(root, "checkpoints", "a")
	writeCheckpointFixture(t, jobA, map[int][]byte{0: []byte("a")})
	if err := os.WriteFile(filepath.Join(jobA.Checkpoint.Directory, ".atomic-retained"), []byte("a evidence"), 0o600); err != nil {
		t.Fatal(err)
	}

	jobB := checkpointTestJob(root, filepath.Join(root, "b.out"), "b", []Segment{{URL: "https://example.test/b"}})
	jobB.Checkpoint.Directory = filepath.Join(root, "checkpoints", "b")
	writeCheckpointFixture(t, jobB, map[int][]byte{0: []byte("b")})
	result, err := New(nil).Download(context.Background(), jobB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reused != 1 {
		t.Fatalf("result = %#v", result)
	}
	if contents, err := os.ReadFile(jobB.Destination); err != nil || string(contents) != "b" {
		t.Fatalf("job B destination = %q, %v", contents, err)
	}
	for path, want := range map[string]string{
		sharedAtomicEvidence: "other job",
		filepath.Join(jobA.Checkpoint.Directory, ".atomic-retained"): "a evidence",
	} {
		contents, readErr := os.ReadFile(path)
		if readErr != nil || string(contents) != want {
			t.Fatalf("unrelated evidence %s = %q, %v; want %q", path, contents, readErr, want)
		}
	}
}

func TestCheckpointInvalidStatePreservesAllPriorArtifacts(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, Job) []string
		want  error
	}{
		{name: "missing ledger", want: ErrCheckpointReconciliation, setup: func(t *testing.T, job Job) []string {
			ensureTestCheckpointDirectory(t, job)
			path := fragmentPath(job.Checkpoint.Directory, 0)
			if err := os.WriteFile(path, []byte("uncommitted-tail"), 0o600); err != nil {
				t.Fatal(err)
			}
			return []string{path}
		}},
		{name: "unknown ledger field", want: ErrInvalidCheckpoint, setup: func(t *testing.T, job Job) []string {
			expectation := mustExpectation(t, job)
			path := fragmentPath(job.Checkpoint.Directory, 0)
			ensureTestCheckpointDirectory(t, job)
			if err := os.WriteFile(path, []byte("prior-artifact"), 0o600); err != nil {
				t.Fatal(err)
			}
			ledger := fmt.Sprintf(`{"version":1,"resume_identity":"stable","plan_hash":%q,"unknown":true}`, expectation.planHash)
			writeRawLedger(t, job, []byte(ledger))
			return []string{path, filepath.Join(job.Checkpoint.Directory, "state.json")}
		}},
		{name: "unknown workspace entry", want: ErrCheckpointReconciliation, setup: func(t *testing.T, job Job) []string {
			writeCheckpointFixture(t, job, map[int][]byte{0: []byte("committed")})
			path := filepath.Join(job.Checkpoint.Directory, "caller-notes")
			if err := os.WriteFile(path, []byte("do not delete"), 0o600); err != nil {
				t.Fatal(err)
			}
			return []string{fragmentPath(job.Checkpoint.Directory, 0), filepath.Join(job.Checkpoint.Directory, "state.json"), path}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			job := checkpointTestJob(root, filepath.Join(root, "out"), "stable", []Segment{{URL: "https://example.test/one"}})
			paths := test.setup(t, job)
			before := make(map[string][]byte, len(paths))
			for _, path := range paths {
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				before[path] = contents
			}
			if _, err := New(nil).Download(context.Background(), job, nil); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			for path, want := range before {
				contents, err := os.ReadFile(path)
				if err != nil || string(contents) != string(want) {
					t.Fatalf("prior artifact %s changed: %q, %v; want %q", path, contents, err, want)
				}
			}
		})
	}
}

func TestCheckpointRejectsInvalidLedgerAndRetainedEvidence(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string, Job)
		want  error
	}{
		{name: "corrupt", want: ErrInvalidCheckpoint, setup: func(t *testing.T, _ string, job Job) {
			writeRawLedger(t, job, []byte("{"))
		}},
		{name: "trailing", want: ErrInvalidCheckpoint, setup: func(t *testing.T, workDir string, job Job) {
			state := initialManifestState(mustExpectation(t, job))
			encoded := mustJSON(t, state)
			writeRawLedger(t, job, append(encoded, []byte(" trailing")...))
		}},
		{name: "oversized", want: ErrInvalidCheckpoint, setup: func(t *testing.T, _ string, job Job) {
			writeRawLedger(t, job, []byte(strings.Repeat("x", maxManifestBytes+1)))
		}},
		{name: "non-regular", want: ErrInvalidCheckpoint, setup: func(t *testing.T, workDir string, job Job) {
			ensureTestCheckpointDirectory(t, job)
			if err := os.Mkdir(filepath.Join(workDir, "state.json"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", want: ErrInvalidCheckpoint, setup: func(t *testing.T, workDir string, job Job) {
			ensureTestCheckpointDirectory(t, job)
			if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(workDir, "state.json")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
		}},
		{name: "atomic evidence", want: ErrCheckpointReconciliation, setup: func(t *testing.T, workDir string, job Job) {
			writeCheckpointFixture(t, job, nil)
			if err := os.WriteFile(filepath.Join(workDir, ".atomic-retained"), []byte("candidate"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			destination := filepath.Join(root, "out")
			job := checkpointTestJob(root, destination, "stable", []Segment{{URL: "https://example.test/one"}})
			test.setup(t, job.Checkpoint.Directory, job)
			before := snapshotCheckpointTree(t, job.Checkpoint.Directory)
			if _, err := New(nil).Download(context.Background(), job, nil); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if after := snapshotCheckpointTree(t, job.Checkpoint.Directory); !reflect.DeepEqual(after, before) {
				t.Fatalf("invalid checkpoint handling changed prior state: before=%v after=%v", before, after)
			}
		})
	}
}

func TestCheckpointLedgerCommitOutcomes(t *testing.T) {
	tests := []struct {
		name          string
		committed     bool
		indeterminate bool
		wantReconcile bool
	}{
		{name: "pre-commit"},
		{name: "committed-with-error", committed: true, wantReconcile: true},
		{name: "indeterminate", indeterminate: true, wantReconcile: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte("fragment"))
			}))
			defer server.Close()
			transport, _ := network.New(network.Config{})
			root := t.TempDir()
			destination := filepath.Join(root, "out")
			job := checkpointTestJob(root, destination, "stable", []Segment{{URL: server.URL}})
			engine := New(transport)
			productionWrite := engine.writeAtomic
			var stateWrites atomic.Int32
			injected := &checkpointCommitError{name: test.name, committed: test.committed, indeterminate: test.indeterminate}
			engine.writeAtomic = func(path string, mode os.FileMode, encode func(io.Writer) error) error {
				if filepath.Base(path) != "state.json" || stateWrites.Add(1) != 2 {
					return productionWrite(path, mode, encode)
				}
				if test.committed {
					if err := productionWrite(path, mode, encode); err != nil {
						return err
					}
				}
				return injected
			}
			_, err := engine.Download(context.Background(), job, nil)
			if !errors.Is(err, injected) {
				t.Fatalf("error = %v, want injected commit failure", err)
			}
			if errors.Is(err, ErrCheckpointReconciliation) != test.wantReconcile {
				t.Fatalf("reconciliation classification = %v, want %v: %v", errors.Is(err, ErrCheckpointReconciliation), test.wantReconcile, err)
			}
			workDir := job.Checkpoint.Directory
			if _, statErr := os.Stat(fragmentPath(workDir, 0)); statErr != nil {
				t.Fatalf("published fragment evidence missing: %v", statErr)
			}
			_, markerErr := os.Stat(filepath.Join(workDir, reconciliationMarker))
			if test.wantReconcile != (markerErr == nil) {
				t.Fatalf("reconciliation marker error = %v", markerErr)
			}
		})
	}
}

func TestCheckpointFragmentPublicationCommitOutcomes(t *testing.T) {
	for _, test := range []struct {
		name          string
		committed     bool
		indeterminate bool
	}{
		{name: "committed-with-error", committed: true},
		{name: "indeterminate", indeterminate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte("fragment"))
			}))
			defer server.Close()
			transport, _ := network.New(network.Config{})
			root := t.TempDir()
			job := checkpointTestJob(root, filepath.Join(root, "out"), "format=mp4;track=video", []Segment{{URL: server.URL}})
			engine := New(transport)
			productionWrite := engine.writeAtomic
			injected := &checkpointCommitError{name: test.name, committed: test.committed, indeterminate: test.indeterminate}
			engine.writeAtomic = func(path string, mode os.FileMode, encode func(io.Writer) error) error {
				if strings.HasSuffix(path, ".frag") {
					if test.committed {
						if err := productionWrite(path, mode, encode); err != nil {
							return err
						}
					}
					return injected
				}
				return productionWrite(path, mode, encode)
			}
			_, err := engine.Download(context.Background(), job, nil)
			if !errors.Is(err, injected) || !errors.Is(err, ErrCheckpointReconciliation) {
				t.Fatalf("fragment publication error = %v", err)
			}
			if _, markerErr := os.Stat(filepath.Join(job.Checkpoint.Directory, reconciliationMarker)); markerErr != nil {
				t.Fatalf("fragment publication reconciliation marker missing: %v", markerErr)
			}
		})
	}
}

func TestCheckpointCancellationWaitsForLedgerCommitAndReusesSettledFragment(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = writer.Write([]byte("fragment"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "out")
	job := checkpointTestJob(root, destination, "stable", []Segment{{URL: server.URL}})
	engine := New(transport)
	productionWrite := engine.writeAtomic
	enteredCommit := make(chan struct{})
	releaseCommit := make(chan struct{})
	var stateWrites atomic.Int32
	engine.writeAtomic = func(path string, mode os.FileMode, encode func(io.Writer) error) error {
		if filepath.Base(path) == "state.json" && stateWrites.Add(1) == 2 {
			close(enteredCommit)
			<-releaseCommit
		}
		return productionWrite(path, mode, encode)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := engine.Download(ctx, job, nil)
		done <- err
	}()
	<-enteredCommit
	cancel()
	select {
	case err := <-done:
		t.Fatalf("download returned before ledger commit settled: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseCommit)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	result, err := New(transport).Download(context.Background(), job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reused != 1 || result.Downloaded != 0 || requests.Load() != 1 {
		t.Fatalf("result=%#v requests=%d", result, requests.Load())
	}
}

func TestCheckpointPublicationOutcomesPreserveEvidenceAndDestination(t *testing.T) {
	tests := []struct {
		name          string
		committed     bool
		indeterminate bool
		want          string
	}{
		{name: "pre-commit", want: "existing"},
		{name: "committed-with-error", committed: true, want: "new"},
		{name: "indeterminate", indeterminate: true, want: "existing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte("new"))
			}))
			defer server.Close()
			transport, _ := network.New(network.Config{})
			root := t.TempDir()
			destination := filepath.Join(root, "out")
			if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
				t.Fatal(err)
			}
			job := checkpointTestJob(root, destination, "stable", []Segment{{URL: server.URL}})
			job.Overwrite = true
			engine := New(transport)
			productionReplace := engine.replaceAtomic
			injected := &checkpointCommitError{name: test.name, committed: test.committed, indeterminate: test.indeterminate}
			engine.replaceAtomic = func(source, destination string) error {
				if test.committed {
					if err := productionReplace(source, destination); err != nil {
						return err
					}
				}
				return injected
			}
			_, err := engine.Download(context.Background(), job, nil)
			if !errors.Is(err, injected) {
				t.Fatalf("error = %v, want publication failure", err)
			}
			contents, readErr := os.ReadFile(destination)
			if readErr != nil || string(contents) != test.want {
				t.Fatalf("destination=%q, %v; want %q", contents, readErr, test.want)
			}
			if _, statErr := os.Stat(job.Checkpoint.Directory); statErr != nil {
				t.Fatalf("recoverable workspace was removed: %v", statErr)
			}
			if _, retryErr := New(transport).Download(context.Background(), job, nil); !errors.Is(retryErr, ErrCheckpointReconciliation) {
				t.Fatalf("retry silently accepted retained evidence: %v", retryErr)
			}
		})
	}
}

func TestCheckpointPublicationAtomicallyReplacesExistingDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("replacement"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "out")
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	job := checkpointTestJob(root, destination, "stable", []Segment{{URL: server.URL}})
	job.Overwrite = true
	if _, err := New(transport).Download(context.Background(), job, nil); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "replacement" {
		t.Fatalf("destination=%q, %v", contents, err)
	}
	if _, err := os.Stat(job.Checkpoint.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed workspace remains: %v", err)
	}
}

func TestCheckpointNoClobberWhenDestinationAppearsBeforeCommit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("downloaded"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "out")
	job := checkpointTestJob(root, destination, "format=mp4;track=video", []Segment{{URL: server.URL}})
	engine := New(transport)
	engine.replaceAtomic = func(string, string) error {
		t.Fatal("overwrite=false called replacement publisher")
		return nil
	}
	productionPublish := engine.publishNoClobber
	lateMedia := []byte("late-existing-media")
	engine.publishNoClobber = func(source, destination string) error {
		if err := os.WriteFile(destination, lateMedia, 0o600); err != nil {
			return err
		}
		return productionPublish(source, destination)
	}
	_, err := engine.Download(context.Background(), job, nil)
	if !errors.Is(err, ErrCheckpointReconciliation) {
		t.Fatalf("late destination error = %v", err)
	}
	contents, readErr := os.ReadFile(destination)
	if readErr != nil || string(contents) != string(lateMedia) {
		t.Fatalf("late destination was damaged: %q, %v", contents, readErr)
	}
	if _, markerErr := os.Stat(filepath.Join(job.Checkpoint.Directory, publicationMarker)); markerErr != nil {
		t.Fatalf("no-clobber failure lost publication evidence: %v", markerErr)
	}
}

func TestCheckpointNoClobberPublicationCommitOutcomes(t *testing.T) {
	for _, test := range []struct {
		name          string
		committed     bool
		indeterminate bool
	}{
		{name: "committed-with-error", committed: true},
		{name: "indeterminate", indeterminate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte("downloaded"))
			}))
			defer server.Close()
			transport, _ := network.New(network.Config{})
			root := t.TempDir()
			destination := filepath.Join(root, "out")
			job := checkpointTestJob(root, destination, "format=mp4;track=video", []Segment{{URL: server.URL}})
			engine := New(transport)
			injected := &checkpointCommitError{name: test.name, committed: test.committed, indeterminate: test.indeterminate}
			engine.publishNoClobber = func(string, string) error { return injected }
			_, err := engine.Download(context.Background(), job, nil)
			if !errors.Is(err, injected) || !errors.Is(err, ErrCheckpointReconciliation) {
				t.Fatalf("no-clobber publication error = %v", err)
			}
			var commitErr atomicfile.CommitError
			if !errors.As(err, &commitErr) || commitErr.Committed() != test.committed || commitErr.Indeterminate() != test.indeterminate {
				t.Fatalf("no-clobber outcome = %#v, %v", commitErr, err)
			}
			if _, markerErr := os.Stat(filepath.Join(job.Checkpoint.Directory, publicationMarker)); markerErr != nil {
				t.Fatalf("publication marker missing: %v", markerErr)
			}
		})
	}
}

func TestCheckpointOrderedCleanupKeepsPublicationMarkerUntilCommit(t *testing.T) {
	for _, test := range []struct {
		name       string
		removeBase string
		failSync   bool
		postCommit bool
	}{
		{name: "fragment removal", removeBase: "00000000.frag"},
		{name: "state removal", removeBase: "state.json"},
		{name: "artifact directory sync", failSync: true},
		{name: "publication marker removal", removeBase: publicationMarker},
		{name: "empty directory removal after commit", removeBase: "checkpoint", postCommit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte("committed-output"))
			}))
			defer server.Close()
			transport, _ := network.New(network.Config{})
			root := t.TempDir()
			destination := filepath.Join(root, "out")
			job := checkpointTestJob(root, destination, "format=mp4;track=video", []Segment{{URL: server.URL}})
			engine := New(transport)
			injected := errors.New("injected " + test.name)
			productionRemove := engine.cleanupOps.remove
			productionSync := engine.cleanupOps.syncDirectory
			var syncCalls int
			engine.cleanupOps.remove = func(path string) error {
				base := filepath.Base(path)
				if test.postCommit && path == job.Checkpoint.Directory {
					return injected
				}
				if !test.postCommit && base == test.removeBase {
					return injected
				}
				return productionRemove(path)
			}
			engine.cleanupOps.syncDirectory = func(path string) error {
				syncCalls++
				if test.failSync && syncCalls == 1 {
					return injected
				}
				return productionSync(path)
			}
			result, err := engine.Download(context.Background(), job, nil)
			if test.postCommit {
				if err != nil {
					t.Fatalf("post-commit directory cleanup became retryable: %v", err)
				}
				if _, markerErr := os.Stat(filepath.Join(job.Checkpoint.Directory, publicationMarker)); !errors.Is(markerErr, os.ErrNotExist) {
					t.Fatalf("publication marker remains after cleanup commit: %v", markerErr)
				}
				job.Overwrite = true
				if _, retryErr := New(transport).Download(context.Background(), job, nil); !errors.Is(retryErr, ErrCheckpointReconciliation) {
					t.Fatalf("retained empty post-commit boundary invited republication: %v", retryErr)
				}
				return
			}
			if !errors.Is(err, ErrCheckpointCleanup) || !errors.Is(err, injected) {
				t.Fatalf("cleanup error = %v", err)
			}
			var commitErr atomicfile.CommitError
			if !errors.As(err, &commitErr) || !commitErr.Committed() || commitErr.Indeterminate() {
				t.Fatalf("cleanup outcome = %#v, %v", commitErr, err)
			}
			if result.Path != destination || result.Bytes != int64(len("committed-output")) {
				t.Fatalf("committed result = %#v", result)
			}
			contents, readErr := os.ReadFile(destination)
			if readErr != nil || string(contents) != "committed-output" {
				t.Fatalf("committed destination = %q, %v", contents, readErr)
			}
			if _, markerErr := os.Stat(filepath.Join(job.Checkpoint.Directory, publicationMarker)); markerErr != nil {
				t.Fatalf("pre-commit cleanup failure lost publication evidence: %v", markerErr)
			}
			job.Overwrite = true
			if _, retryErr := New(transport).Download(context.Background(), job, nil); !errors.Is(retryErr, ErrCheckpointReconciliation) {
				t.Fatalf("committed cleanup failure invited republication: %v", retryErr)
			}
		})
	}
}

type checkpointCommitError struct {
	name          string
	committed     bool
	indeterminate bool
}

func (err *checkpointCommitError) Error() string       { return "injected " + err.name }
func (err *checkpointCommitError) Committed() bool     { return err.committed }
func (err *checkpointCommitError) Indeterminate() bool { return err.indeterminate }

func checkpointTestJob(root, destination, identity string, segments []Segment) Job {
	return Job{
		OutputRoot: root, Destination: destination, Segments: segments, Concurrency: 1, Attempts: 1,
		Checkpoint: &Checkpoint{Directory: destination + ".checkpoint", ResumeIdentity: identity},
	}
}

func writeCheckpointFixture(t *testing.T, job Job, artifacts map[int][]byte) {
	t.Helper()
	workDir := job.Checkpoint.Directory
	ensureTestCheckpointDirectory(t, job)
	state := initialManifestState(mustExpectation(t, job))
	for index, contents := range artifacts {
		path := fragmentPath(workDir, index)
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			t.Fatal(err)
		}
		bytes, digest, err := digestFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if state.Artifacts == nil {
			state.Artifacts = make(map[int]artifact)
		}
		state.Artifacts[index] = artifact{Bytes: bytes, SHA256: digest}
	}
	if err := writeManifestState(filepath.Join(workDir, "state.json"), state, atomicfile.Write); err != nil {
		t.Fatal(err)
	}
}

func mustExpectation(t *testing.T, job Job) manifestExpectation {
	t.Helper()
	expectation, err := manifestExpectationFor(job)
	if err != nil {
		t.Fatal(err)
	}
	return expectation
}

func writeRawLedger(t *testing.T, job Job, contents []byte) {
	t.Helper()
	ensureTestCheckpointDirectory(t, job)
	workDir := job.Checkpoint.Directory
	if err := os.WriteFile(filepath.Join(workDir, "state.json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func ensureTestCheckpointDirectory(t *testing.T, job Job) {
	t.Helper()
	if _, err := ensureProtectedCheckpointDirectory(job.OutputRoot, job.Checkpoint.Directory); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	var builder strings.Builder
	if err := json.NewEncoder(&builder).Encode(value); err != nil {
		t.Fatal(err)
	}
	return []byte(strings.TrimSpace(builder.String()))
}

func snapshotCheckpointTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += ":" + target
		case info.Mode().IsRegular():
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":" + string(contents)
		}
		snapshot[relative] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

var _ atomicfile.CommitError = (*checkpointCommitError)(nil)
