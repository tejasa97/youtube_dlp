package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	healthHelper = flag.Bool("ytdlp-health-helper", false, "run deterministic updater health helper")
	healthBlock  = flag.Bool("ytdlp-health-block", false, "run blocking updater health helper")
)

func managerOptions(public ed25519.PublicKey, goos, goarch string, health HealthChecker) Options {
	return Options{
		Trust:           testRoot(public),
		Product:         "ytdlp-go",
		Channel:         ChannelStable,
		GOOS:            goos,
		GOARCH:          goarch,
		Clock:           func() time.Time { return testNow },
		Health:          health,
		MaxArtifactSize: 64 << 20,
		LockPoll:        time.Millisecond,
		StaleLockAfter:  time.Minute,
	}
}

func signedMetadata(t *testing.T, private ed25519.PrivateKey, metadata Metadata) []byte {
	t.Helper()
	public := private.Public().(ed25519.PublicKey)
	envelope, err := Sign(metadata, map[string]ed25519.PrivateKey{KeyID(public): private})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestApplyUpdateRollbackAndHealthFailure(t *testing.T) {
	public, private := testKey("release-1")
	healthFailure := atomic.Bool{}
	health := HealthCheckFunc(func(_ context.Context, path string, target Target) error {
		if healthFailure.Load() {
			return errors.New("secret-token=must-not-be-rendered")
		}
		encoded, err := os.ReadFile(path)
		if err != nil || digestString(encoded) != target.SHA256 {
			return errors.New("bad artifact")
		}
		return nil
	})
	root := filepath.Join(t.TempDir(), "updates")
	manager, err := Open(context.Background(), root, managerOptions(public, "linux", "amd64", health))
	if err != nil {
		t.Fatal(err)
	}
	one := []byte("release-one")
	state, err := manager.Apply(context.Background(), signedMetadata(t, private, testMetadata(one, "1.0.0", 1)), bytes.NewReader(one))
	if err != nil {
		t.Fatal(err)
	}
	if state.Active == nil || state.Active.Version != "1.0.0" || state.Previous != nil {
		t.Fatalf("first state = %#v", state)
	}
	two := []byte("release-two")
	state, err = manager.Apply(context.Background(), signedMetadata(t, private, testMetadata(two, "1.1.0", 2)), bytes.NewReader(two))
	if err != nil {
		t.Fatal(err)
	}
	if state.Active.Version != "1.1.0" || state.Previous == nil || state.Previous.Version != "1.0.0" {
		t.Fatalf("updated state = %#v", state)
	}
	state, err = manager.Rollback(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Active.Version != "1.0.0" || state.Previous.Version != "1.1.0" || state.HighestGeneration != 2 {
		t.Fatalf("rollback state = %#v", state)
	}
	// Return to the newer verified release, then reject a broken update.
	if _, err := manager.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	healthFailure.Store(true)
	three := []byte("release-three")
	if _, err := manager.Apply(context.Background(), signedMetadata(t, private, testMetadata(three, "1.2.0", 3)), bytes.NewReader(three)); !errors.Is(err, ErrHealth) || strings.Contains(fmt.Sprint(err), "secret-token") {
		t.Fatalf("health error = %v", err)
	}
	state, err = manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Active.Version != "1.1.0" || state.HighestGeneration != 2 {
		t.Fatalf("failed health changed state = %#v", state)
	}
	if _, err := os.Stat(filepath.Join(root, "releases", "1.2.0")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed release remains: %v", err)
	}
}

func TestArtifactVerificationAndInputCancellation(t *testing.T) {
	public, private := testKey("release-1")
	health := HealthCheckFunc(func(context.Context, string, Target) error { return nil })
	manager, err := Open(context.Background(), filepath.Join(t.TempDir(), "updates"), managerOptions(public, "linux", "amd64", health))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("expected")
	envelope := signedMetadata(t, private, testMetadata(content, "1.0.0", 1))
	if _, err := manager.Apply(context.Background(), envelope, bytes.NewReader([]byte("tampered"))); !errors.Is(err, ErrHash) {
		t.Fatalf("hash error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Apply(ctx, envelope, bytes.NewReader(content)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	state, err := manager.Snapshot(context.Background())
	if err != nil || state.Active != nil {
		t.Fatalf("failed input changed state: %#v, %v", state, err)
	}
}

func TestApplySerializesConcurrentWriters(t *testing.T) {
	public, private := testKey("release-1")
	var checks atomic.Int32
	health := HealthCheckFunc(func(context.Context, string, Target) error { checks.Add(1); return nil })
	manager, err := Open(context.Background(), filepath.Join(t.TempDir(), "updates"), managerOptions(public, "linux", "amd64", health))
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("release")
	envelope := signedMetadata(t, private, testMetadata(content, "1.0.0", 1))
	var successes atomic.Int32
	var unexpected atomic.Value
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := manager.Apply(context.Background(), envelope, bytes.NewReader(content))
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrFreeze) {
				unexpected.Store(err)
			}
		}()
	}
	wait.Wait()
	if value := unexpected.Load(); value != nil {
		t.Fatalf("unexpected error = %v", value)
	}
	if successes.Load() != 1 || checks.Load() != 1 {
		t.Fatalf("successes=%d checks=%d", successes.Load(), checks.Load())
	}
}

func TestManagerClonesTrustConfiguration(t *testing.T) {
	public, private := testKey("release-1")
	health := HealthCheckFunc(func(context.Context, string, Target) error { return nil })
	options := managerOptions(public, "linux", "amd64", health)
	manager, err := Open(context.Background(), filepath.Join(t.TempDir(), "updates"), options)
	if err != nil {
		t.Fatal(err)
	}
	// Mutating every caller-owned reference after Open must not change or race
	// with the manager's authorization snapshot.
	for index := range public {
		public[index] ^= 0xff
	}
	for keyID := range options.Trust.Keys {
		delete(options.Trust.Keys, keyID)
	}
	options.Trust.Channels[0] = ChannelNightly
	options.Trust.Platforms[0] = Platform{GOOS: "plan9", GOARCH: "amd64"}
	content := []byte("release")
	if _, err := manager.Apply(context.Background(), signedMetadata(t, private, testMetadata(content, "1.0.0", 1)), bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryRestoresLastVerifiedState(t *testing.T) {
	public, private := testKey("release-1")
	health := HealthCheckFunc(func(context.Context, string, Target) error { return nil })
	root := filepath.Join(t.TempDir(), "updates")
	manager, err := Open(context.Background(), root, managerOptions(public, "linux", "amd64", health))
	if err != nil {
		t.Fatal(err)
	}
	one := []byte("release-one")
	before, err := manager.Apply(context.Background(), signedMetadata(t, private, testMetadata(one, "1.0.0", 1)), bytes.NewReader(one))
	if err != nil {
		t.Fatal(err)
	}
	two := []byte("release-two")
	metadata := testMetadata(two, "1.1.0", 2)
	target := metadata.Targets[0]
	installed := Installed{Version: target.Version, Artifact: target.Artifact, SHA256: target.SHA256, Size: target.Size, Generation: 2}
	after := before
	after.HighestGeneration = 2
	after.Previous = cloneInstalled(before.Active)
	after.Active = &installed
	release := filepath.Join(root, "releases", "1.1.0")
	if err := os.Mkdir(release, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, target.Artifact), two, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := manager.writeJournal(journal{Operation: "apply", Before: before, After: after, Release: "1.1.0"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.writeState(after); err != nil {
		t.Fatal(err)
	}
	manager, err = Open(context.Background(), root, managerOptions(public, "linux", "amd64", health))
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !equalState(recovered, before) {
		t.Fatalf("recovered state = %#v, want %#v", recovered, before)
	}
	if _, err := os.Stat(release); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unverified release remains: %v", err)
	}
}

func TestHostilePathsLocksAndJournal(t *testing.T) {
	public, _ := testKey("release-1")
	health := HealthCheckFunc(func(context.Context, string, Target) error { return nil })
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), link, managerOptions(public, "linux", "amd64", health)); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink root error = %v", err)
	}
	root := filepath.Join(parent, "updates")
	manager, err := Open(context.Background(), root, managerOptions(public, "linux", "amd64", health))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "journal.json"), []byte(`{"payload":{},"sha256":"bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), root, managerOptions(public, "linux", "amd64", health)); !errors.Is(err, ErrRecovery) {
		t.Fatalf("corrupt journal error = %v", err)
	}
	_ = os.Remove(filepath.Join(root, "journal.json"))
	if err := os.Mkdir(filepath.Join(root, ".update.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := manager.Snapshot(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock cancellation error = %v", err)
	}
}

func TestStaleLockRequiresDeadOwner(t *testing.T) {
	public, _ := testKey("release-1")
	health := HealthCheckFunc(func(context.Context, string, Target) error { return nil })
	options := managerOptions(public, "linux", "amd64", health)
	root := filepath.Join(t.TempDir(), "updates")
	manager, err := Open(context.Background(), root, options)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(manager.root, ".update.lock")
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	old := testNow.Add(-2 * time.Hour).UnixNano()
	owner := fmt.Sprintf("owner\n%d\n%d\n", old, os.Getpid())
	if err := os.WriteFile(filepath.Join(lockPath, "owner"), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := manager.Snapshot(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("live old lock error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockPath, "owner"), []byte(fmt.Sprintf("owner\n%d\n%d\n", old, 1<<30)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Snapshot(context.Background()); err != nil {
		t.Fatalf("dead stale lock not recovered: %v", err)
	}
}

func TestPlatformTargetInstallFixtures(t *testing.T) {
	public, private := testKey("release-1")
	content := []byte("portable-artifact")
	metadata := testMetadata(content, "1.0.0", 1)
	envelope := signedMetadata(t, private, metadata)
	for _, platform := range []Platform{{GOOS: "linux", GOARCH: "amd64"}, {GOOS: "darwin", GOARCH: "arm64"}, {GOOS: "windows", GOARCH: "amd64"}} {
		t.Run(platform.GOOS+"-"+platform.GOARCH, func(t *testing.T) {
			health := HealthCheckFunc(func(_ context.Context, path string, target Target) error {
				if filepath.Base(path) != target.Artifact {
					return errors.New("wrong artifact")
				}
				return nil
			})
			manager, err := Open(context.Background(), filepath.Join(t.TempDir(), "updates"), managerOptions(public, platform.GOOS, platform.GOARCH, health))
			if err != nil {
				t.Fatal(err)
			}
			state, err := manager.Apply(context.Background(), envelope, bytes.NewReader(content))
			if err != nil {
				t.Fatal(err)
			}
			if state.Active == nil || state.Active.Version != "1.0.0" {
				t.Fatalf("state = %#v", state)
			}
		})
	}
}

func TestCommandHealthCheckerRunsArtifactWithoutShell(t *testing.T) {
	if *healthHelper {
		fmt.Print("ytdlp-go 9.8.7")
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=TestCommandHealthCheckerRunsArtifactWithoutShell", "-ytdlp-health-helper")
	// Verify the helper shape before using the same executable through the
	// health checker, whose environment is intentionally inherited unchanged.
	if output, err := command.Output(); err != nil || string(output) != "ytdlp-go 9.8.7" {
		t.Fatalf("helper output=%q err=%v", output, err)
	}
	checker := CommandHealthChecker{Arguments: []string{"-test.run=TestCommandHealthCheckerRunsArtifactWithoutShell", "-ytdlp-health-helper"}, OutputPrefix: "ytdlp-go ", Timeout: 5 * time.Second, MaxOutput: 1024}
	target := Target{Version: "9.8.7"}
	if err := checker.Check(context.Background(), executable, target); err != nil {
		t.Fatal(err)
	}
	checker.OutputPrefix = "wrong "
	if err := checker.Check(context.Background(), executable, target); !errors.Is(err, ErrHealth) {
		t.Fatalf("identity error = %v", err)
	}
}

func TestCommandHealthCheckerCancellation(t *testing.T) {
	if *healthBlock {
		select {}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	checker := CommandHealthChecker{Arguments: []string{"-test.run=TestCommandHealthCheckerCancellation", "-ytdlp-health-block"}, OutputPrefix: "unused", Timeout: 30 * time.Millisecond, MaxOutput: 1024}
	started := time.Now()
	if err := checker.Check(context.Background(), executable, Target{Version: "1.0.0"}); !errors.Is(err, ErrHealth) {
		t.Fatalf("cancellation error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("health cancellation took %v", elapsed)
	}
}

func FuzzRecordDecoder(f *testing.F) {
	f.Add([]byte(`{"payload":{"product":"x"},"sha256":"00"}`))
	f.Fuzz(func(t *testing.T, encoded []byte) {
		path := filepath.Join(t.TempDir(), "record")
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		var state State
		_ = readRecord(path, 128<<10, &state)
	})
}
