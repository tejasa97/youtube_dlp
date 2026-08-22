package ejs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tejasa97/ytdlp-go/engine/provider"
	"github.com/tejasa97/ytdlp-go/internal/cache"
	"github.com/tejasa97/ytdlp-go/internal/javascript/protocol"
)

type preprocessExecutor struct {
	mu    sync.Mutex
	calls int
}

func (executor *preprocessExecutor) Execute(_ context.Context, request protocol.Request) protocol.Response {
	executor.mu.Lock()
	executor.calls++
	executor.mu.Unlock()
	return protocol.Response{Version: protocol.Version, ID: request.ID, Result: json.RawMessage(`{"type":"result","preprocessed_player":"var generated = true;"}`)}
}

func (executor *preprocessExecutor) count() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.calls
}

func newPersistentTestSolver(t *testing.T, directory string, options PersistentPlayerCacheOptions, executor *preprocessExecutor) *Solver {
	t.Helper()
	options.Directory = directory
	solver, err := NewWithPersistentPlayerCache(executor, NewPreprocessedPlayerCache(), options)
	if err != nil {
		t.Fatal(err)
	}
	return solver
}

func preprocessForDisk(t *testing.T, solver *Solver, player string) {
	t.Helper()
	value, cacheResult, _, err := solver.preprocess(context.Background(), "persistent-test", protocol.HashScript(player), player, false)
	if err != nil || value == "" || cacheResult != provider.ChallengeCacheMiss {
		t.Fatalf("preprocess = %q, %q, %v", value, cacheResult, err)
	}
}

func TestPersistentPlayerCacheDisabledByDefault(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "ejs-cache")
	if err := ClearPersistentPlayerCache(context.Background(), PersistentPlayerCacheOptions{Directory: directory}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clear created disabled cache root: %v", err)
	}
	solver, err := NewWithPersistentPlayerCache(&preprocessExecutor{}, NewPreprocessedPlayerCache(), PersistentPlayerCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if solver.persistent != nil {
		t.Fatal("default solver unexpectedly enabled disk cache")
	}
}

func TestPersistentPlayerCacheReusesAcrossFreshSolvers(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "ejs-cache")
	firstExecutor := &preprocessExecutor{}
	first := newPersistentTestSolver(t, directory, PersistentPlayerCacheOptions{}, firstExecutor)
	player := "player-version-one"
	preprocessForDisk(t, first, player)
	if firstExecutor.count() != 1 {
		t.Fatalf("first executor calls = %d", firstExecutor.count())
	}

	secondExecutor := &preprocessExecutor{}
	second := newPersistentTestSolver(t, directory, PersistentPlayerCacheOptions{}, secondExecutor)
	value, cacheResult, _, _, err := second.getPreprocessed(context.Background(), "fresh", protocol.HashScript(player), player, false)
	if err != nil || value != "var generated = true;" || cacheResult != provider.ChallengeCacheHit {
		t.Fatalf("fresh cache lookup = %q, %q, %v", value, cacheResult, err)
	}
	if secondExecutor.count() != 0 {
		t.Fatalf("disk hit invoked preprocessing %d times", secondExecutor.count())
	}
}

type semanticFallbackExecutor struct {
	mu         sync.Mutex
	preprocess int
}

func (executor *semanticFallbackExecutor) Execute(_ context.Context, request protocol.Request) protocol.Response {
	if strings.HasSuffix(request.ID, preprocessRequestIDSuffix) {
		executor.mu.Lock()
		executor.preprocess++
		executor.mu.Unlock()
		return protocol.Response{Version: protocol.Version, ID: request.ID, Result: json.RawMessage(`{"type":"result","preprocessed_player":"fresh-generated"}`)}
	}
	if len(request.Arguments) != 1 || strings.Contains(string(request.Arguments[0]), "invalid-generated") {
		return protocol.FailureResponse(request.ID, protocol.CodeExecution, errors.New("invalid generated player"))
	}
	return protocol.Response{Version: protocol.Version, ID: request.ID, Result: json.RawMessage(`{"type":"result","responses":[{"type":"result","data":{"abc":"ok"}}]}`)}
}

func (executor *semanticFallbackExecutor) count() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.preprocess
}

func TestPersistentPlayerCacheSemanticFailureFallsBackToFreshPreprocess(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "ejs-cache")
	executor := &semanticFallbackExecutor{}
	solver, err := NewWithPersistentPlayerCache(executor, NewPreprocessedPlayerCache(), PersistentPlayerCacheOptions{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	player := "semantic-fallback-player"
	hash := protocol.HashScript(player)
	if err := solver.persistent.store.Store(context.Background(), persistentCacheNamespace, solver.persistentKey(hash), []byte("invalid-generated"), solver.persistent.ttl); err != nil {
		t.Fatal(err)
	}
	result, err := solver.SolvePlayer(context.Background(), "semantic", player, []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}, false)
	if err != nil || result.Responses[0].Data["abc"] != "ok" {
		t.Fatalf("semantic fallback result=%#v err=%v", result, err)
	}
	if executor.count() != 1 {
		t.Fatalf("fresh preprocessing calls=%d, want 1", executor.count())
	}
	value, persistent, ok := solver.lookupPreprocessed(hash)
	if !ok || persistent || value != "fresh-generated" {
		t.Fatalf("memory origin after fallback = %q persistent=%v present=%v", value, persistent, ok)
	}
}

func TestFreshPreprocessReplacesRacingPersistentMemoryEntry(t *testing.T) {
	memory := NewPreprocessedPlayerCache()
	memory.storeLocked("player", "invalid-disk-transform", true)
	memory.storeLocked("player", "fresh-transform", false)

	entry, ok := memory.entries["player"]
	if !ok || entry.value != "fresh-transform" || entry.persistent {
		t.Fatalf("fresh entry = %#v, present=%v", entry, ok)
	}

	// A late disk promotion cannot replace a transform already generated by
	// this process or mark it as disk-origin.
	memory.storeLocked("player", "stale-disk-transform", true)
	entry = memory.entries["player"]
	if entry.value != "fresh-transform" || entry.persistent {
		t.Fatalf("entry after late disk promotion = %#v", entry)
	}
}

func TestPersistentPlayerCacheSemanticFailureDoesNotRetryCancellation(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "ejs-cache")
	ctx, cancel := context.WithCancel(context.Background())
	executor := &cancelingSemanticExecutor{cancel: cancel}
	solver, err := NewWithPersistentPlayerCache(executor, NewPreprocessedPlayerCache(), PersistentPlayerCacheOptions{Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	player := "semantic-cancellation-player"
	hash := protocol.HashScript(player)
	if err := solver.persistent.store.Store(context.Background(), persistentCacheNamespace, solver.persistentKey(hash), []byte("invalid-generated"), solver.persistent.ttl); err != nil {
		t.Fatal(err)
	}
	_, err = solver.SolvePlayer(ctx, "semantic-cancel", player, []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}, false)
	if err == nil {
		t.Fatal("canceled solve unexpectedly succeeded")
	}
	if executor.count() != 0 {
		t.Fatalf("canceled semantic failure retried preprocessing %d times", executor.count())
	}
}

type cancelingSemanticExecutor struct {
	semanticFallbackExecutor
	cancel context.CancelFunc
}

func (executor *cancelingSemanticExecutor) Execute(ctx context.Context, request protocol.Request) protocol.Response {
	if strings.HasSuffix(request.ID, preprocessRequestIDSuffix) {
		executor.mu.Lock()
		executor.preprocess++
		executor.mu.Unlock()
		return protocol.Response{Version: protocol.Version, ID: request.ID, Result: json.RawMessage(`{"type":"result","preprocessed_player":"fresh-generated"}`)}
	}
	if strings.Contains(string(request.Arguments[0]), "invalid-generated") {
		executor.cancel()
		return protocol.FailureResponse(request.ID, protocol.CodeExecution, context.Canceled)
	}
	return protocol.Response{Version: protocol.Version, ID: request.ID, Result: json.RawMessage(`{"type":"result","responses":[{"type":"result","data":{"abc":"ok"}}]}`)}
}

func TestPersistentPlayerCacheExpiryCorruptionAndVersionFallback(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "ejs-cache")
	player := "player-version-two"
	first := newPersistentTestSolver(t, directory, PersistentPlayerCacheOptions{TTL: time.Nanosecond}, &preprocessExecutor{})
	preprocessForDisk(t, first, player)
	time.Sleep(time.Millisecond)

	expiredExecutor := &preprocessExecutor{}
	expired := newPersistentTestSolver(t, directory, PersistentPlayerCacheOptions{}, expiredExecutor)
	if _, cacheResult, _, _, err := expired.getPreprocessed(context.Background(), "expired", protocol.HashScript(player), player, false); err != nil || cacheResult != provider.ChallengeCacheMiss || expiredExecutor.count() != 1 {
		t.Fatalf("expired fallback cache=%q calls=%d err=%v", cacheResult, expiredExecutor.count(), err)
	}

	key := expired.persistentKey(protocol.HashScript(player))
	path := filepath.Join(directory, persistentCacheNamespace, key+".cache")
	if err := os.WriteFile(path, []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	corruptExecutor := &preprocessExecutor{}
	corrupt := newPersistentTestSolver(t, directory, PersistentPlayerCacheOptions{}, corruptExecutor)
	if _, cacheResult, _, _, err := corrupt.getPreprocessed(context.Background(), "corrupt", protocol.HashScript(player), player, false); err != nil || cacheResult != provider.ChallengeCacheMiss || corruptExecutor.count() != 1 {
		t.Fatalf("corrupt fallback cache=%q calls=%d err=%v", cacheResult, corruptExecutor.count(), err)
	}

	// A schema/solver identity change uses a distinct opaque key and is a miss.
	versionExecutor := &preprocessExecutor{}
	versioned := newPersistentTestSolver(t, directory, PersistentPlayerCacheOptions{}, versionExecutor)
	versioned.persistent.identity += "-incompatible"
	if _, cacheResult, _, _, err := versioned.getPreprocessed(context.Background(), "version", protocol.HashScript(player), player, false); err != nil || cacheResult != provider.ChallengeCacheMiss || versionExecutor.count() != 1 {
		t.Fatalf("version fallback cache=%q calls=%d err=%v", cacheResult, versionExecutor.count(), err)
	}
}

func TestPersistentPlayerCacheBoundedConcurrentAndPrivate(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "ejs-cache")
	options := PersistentPlayerCacheOptions{MaxEntries: 2}
	first := newPersistentTestSolver(t, directory, options, &preprocessExecutor{})
	for _, player := range []string{"one", "two", "three"} {
		preprocessForDisk(t, first, player)
	}
	entries, err := os.ReadDir(filepath.Join(directory, persistentCacheNamespace))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("persistent entries = %d, want 2", len(entries))
	}
	for _, path := range []string{directory, filepath.Join(directory, persistentCacheNamespace)} {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory mode %s = %v, %v", path, info.Mode(), statErr)
		}
	}
	for _, entry := range entries {
		info, statErr := entry.Info()
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("entry mode %s = %v, %v", entry.Name(), info.Mode(), statErr)
		}
	}

	// Separate solver instances miss together but only one invokes preprocessing:
	// the second rechecks the disk tier after acquiring the process-wide slot.
	sharedDirectory := filepath.Join(t.TempDir(), "ejs-cache")
	leftExecutor, rightExecutor := &preprocessExecutor{}, &preprocessExecutor{}
	left := newPersistentTestSolver(t, sharedDirectory, PersistentPlayerCacheOptions{}, leftExecutor)
	right := newPersistentTestSolver(t, sharedDirectory, PersistentPlayerCacheOptions{}, rightExecutor)
	player := "concurrent"
	errs := make(chan error, 2)
	for index, solver := range []*Solver{left, right} {
		index, solver := index, solver
		go func() {
			_, _, _, _, err := solver.getPreprocessed(context.Background(), "concurrent", protocol.HashScript(player), player, false)
			if err != nil {
				errs <- err
				return
			}
			_ = index
			errs <- nil
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if got := leftExecutor.count() + rightExecutor.count(); got != 1 {
		t.Fatalf("concurrent preprocess calls = %d, want 1", got)
	}
}

func TestPersistentPlayerCacheTightensExistingBoundOnOpen(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "ejs-cache")
	first := newPersistentTestSolver(t, directory, PersistentPlayerCacheOptions{MaxEntries: 3}, &preprocessExecutor{})
	for _, player := range []string{"one", "two", "three"} {
		preprocessForDisk(t, first, player)
	}
	// Constructor pruning must apply before a later write or lookup.
	_ = newPersistentTestSolver(t, directory, PersistentPlayerCacheOptions{MaxEntries: 2}, &preprocessExecutor{})
	entries, err := os.ReadDir(filepath.Join(directory, persistentCacheNamespace))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("tightened cache entries = %d, want 2", len(entries))
	}
}

func TestPersistentPlayerCacheRejectsUnsafeRootAndCancellationAndClears(t *testing.T) {
	outside := t.TempDir()
	root := filepath.Join(t.TempDir(), "ejs-cache")
	if err := os.Symlink(outside, root); err == nil {
		_, err = NewWithPersistentPlayerCache(&preprocessExecutor{}, NewPreprocessedPlayerCache(), PersistentPlayerCacheOptions{Directory: root})
		if !errors.Is(err, cache.ErrUnsafePath) {
			t.Fatalf("symlink root = %v", err)
		}
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}

	directory := filepath.Join(t.TempDir(), "ejs-cache")
	solver := newPersistentTestSolver(t, directory, PersistentPlayerCacheOptions{}, &preprocessExecutor{})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	solver.storePersistent(cancelled, protocol.HashScript("cancelled"), "var generated = true;")
	if entries, err := os.ReadDir(filepath.Join(directory, persistentCacheNamespace)); err == nil && len(entries) != 0 {
		t.Fatalf("cancelled write persisted entries: %v", entries)
	}
	preprocessForDisk(t, solver, "clear")
	if err := solver.ClearPreprocessedPlayers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, persistentCacheNamespace)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clear leaves namespace: %v", err)
	}
}
