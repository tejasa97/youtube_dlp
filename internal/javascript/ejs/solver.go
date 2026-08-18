// Package ejs integrates the pinned yt-dlp EJS solver bundle with the isolated
// JavaScript helper.
package ejs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tejasa97/youtube_dlp/engine/provider"
	"github.com/tejasa97/youtube_dlp/internal/javascript/protocol"
)

const (
	MaxPlayerBytes    = 8 << 20
	MaxChallenges     = 256
	MaxChallengeBytes = 16 << 10
	SolverMemoryBytes = 128 << 20
	SolverOutputBytes = 8 << 20
	SolverSourceBytes = 2 << 20

	// PreprocessWallTimeMS bounds the player preprocessing phase (meriyah
	// parse + AST extraction + code generation). Real YouTube player scripts
	// (~1-2 MB) executed through the pure-Go goja engine require substantially
	// more time than native V8/SpiderMonkey runtimes. This uses the protocol
	// hard max to give valid preprocessing adequate headroom.
	PreprocessWallTimeMS = 55_000

	// SolveWallTimeMS bounds the challenge-solving phase (executing extracted
	// transforms against challenge values). This phase operates on the compact
	// preprocessed player and completes quickly.
	SolveWallTimeMS = 10_000

	// MaxCachedPlayers bounds the preprocessed-player cache to prevent
	// unbounded memory growth across many distinct player versions.
	MaxCachedPlayers = 8
)

type ChallengeType = provider.ChallengeType

const (
	ChallengeN   = provider.ChallengeN
	ChallengeSig = provider.ChallengeSig
)

type Executor interface {
	Execute(context.Context, protocol.Request) protocol.Response
}

type ChallengeRequest = provider.ChallengeRequest
type ChallengeResponse = provider.ChallengeResponse
type Result = provider.ChallengeResult

// Solver executes EJS challenge solving through an isolated JavaScript helper.
// It caches preprocessed players so that repeated videos sharing the same
// player script skip the expensive meriyah-based preprocessing phase.
// Concurrent requests for the same uncached player are coalesced via
// singleflight coordination so preprocessing runs exactly once.
//
// Applications may create one solver per download job, so expensive distinct
// player preprocessing is serialized process-wide rather than per Solver.
var playerPreprocessSlot = make(chan struct{}, 1)

// PreprocessedPlayerCache owns the bounded completed-transform LRU. A cache
// may be shared by multiple Solver instances that use the same authenticated
// EJS bundle, allowing short-lived helper clients to reuse derived transforms
// without writing them to disk.
type PreprocessedPlayerCache struct {
	mu      sync.Mutex
	entries map[string]string // player SHA-256 → preprocessed player
	order   []string          // LRU eviction order (oldest first)
}

// NewPreprocessedPlayerCache creates an empty, bounded in-memory cache.
func NewPreprocessedPlayerCache() *PreprocessedPlayerCache {
	return &PreprocessedPlayerCache{entries: make(map[string]string, MaxCachedPlayers)}
}

type Solver struct {
	executor     Executor
	script       string
	preprocessed *PreprocessedPlayerCache

	// Flights stay solver-local because their goroutines execute through this
	// solver's helper. Sharing an in-flight call across short-lived helpers
	// could let the owner close while another client's waiter remains.
	mu     sync.Mutex
	flight map[string]*call
}

// call represents an in-flight preprocessing operation owned by the flight,
// independent of any individual caller's context. Waiters select between
// flight completion and their own context. When all waiters cancel, the
// shared preprocessing is canceled to avoid orphaned work.
//
// All waiter admission, departure, and abandonment decisions are coordinated
// under the owning solver lock to prevent races between joining and cancellation.
type call struct {
	done      chan struct{}      // closed when preprocessing completes
	cancel    context.CancelFunc // cancels the shared preprocessing goroutine
	waiters   int32              // active waiters (mutated under solver.mu)
	abandoned bool               // true when all waiters left (set under solver.mu)
	val       string
	err       error
	cache     string
	elapsed   time.Duration
}

func New(executor Executor) (*Solver, error) {
	return NewWithPreprocessedPlayerCache(executor, NewPreprocessedPlayerCache())
}

// NewWithPreprocessedPlayerCache creates a solver backed by cache. Callers may
// share one cache across short-lived clients only when they use this package's
// identical authenticated EJS bundle. A nil cache is rejected rather than
// silently changing the requested lifetime.
func NewWithPreprocessedPlayerCache(executor Executor, cache *PreprocessedPlayerCache) (*Solver, error) {
	if executor == nil {
		return nil, errors.New("EJS executor is required")
	}
	if cache == nil {
		return nil, errors.New("EJS preprocessed-player cache is required")
	}
	script, err := bundledScript()
	if err != nil {
		return nil, err
	}
	return &Solver{
		executor: executor, script: script, preprocessed: cache,
		flight: make(map[string]*call),
	}, nil
}

// SolvePlayer preprocesses one player and solves ordered n/sig request groups.
// The operation is split into two protocol calls:
//  1. Preprocess: parse the player and extract transform functions (expensive,
//     cached by player hash).
//  2. Solve: execute the extracted transforms against challenge values (fast).
//
// This split ensures the expensive meriyah-based parsing only occurs once per
// unique player script, and the solve phase completes within a tight timeout.
func (solver *Solver) SolvePlayer(ctx context.Context, id, player string, requests []ChallengeRequest, outputPreprocessed bool) (Result, error) {
	if len(player) == 0 || len(player) > MaxPlayerBytes {
		return Result{}, challengeFailure(provider.ChallengeHelperInvalidInput, provider.ChallengePhasePreprocess, fmt.Errorf("player source must contain 1-%d bytes", MaxPlayerBytes))
	}
	if err := validateChallenges(requests); err != nil {
		return Result{}, challengeFailure(provider.ChallengeHelperInvalidInput, provider.ChallengePhasePreprocess, err)
	}

	playerHash := protocol.HashScript(player)
	preprocessed, cache, preprocessDuration, err := solver.getPreprocessed(ctx, id, playerHash, player)
	diagnostics := provider.ChallengeDiagnostics{
		Cache:            cache,
		PreprocessBucket: provider.PreprocessBucket(cache, preprocessDuration),
		SolveBucket:      provider.ChallengeBucketNone,
		Phase:            provider.ChallengePhasePreprocess,
	}
	if err != nil {
		return Result{}, annotateChallengeFailure(err, diagnostics)
	}

	started := time.Now()
	result, err := solver.solve(ctx, id, preprocessed, requests, outputPreprocessed, player)
	diagnostics.SolveBucket = provider.ChallengeDurationBucket(time.Since(started))
	if err != nil {
		diagnostics.Phase = provider.ChallengePhaseSolve
		return Result{}, annotateChallengeFailure(err, diagnostics)
	}
	diagnostics.Phase = provider.ChallengePhaseNone
	diagnostics.HelperCategory = provider.ChallengeHelperNone
	result.Diagnostics = diagnostics.Sanitize()
	return result, nil
}

// getPreprocessed returns a completed shared-cache entry or coalesces misses
// through a solver-local flight. Keeping flights local ensures their owning
// helper remains protected by that client's active-call drain. A second solver
// waiting on the process-wide slot rechecks the completed cache before running.
func (solver *Solver) getPreprocessed(ctx context.Context, id, playerHash, player string) (string, string, time.Duration, error) {
	if preprocessed, ok := solver.lookupPreprocessed(playerHash); ok {
		return preprocessed, provider.ChallengeCacheHit, 0, nil
	}

	solver.mu.Lock()
	if preprocessed, ok := solver.lookupPreprocessed(playerHash); ok {
		solver.mu.Unlock()
		return preprocessed, provider.ChallengeCacheHit, 0, nil
	}
	if inflight, ok := solver.flight[playerHash]; ok {
		if inflight.abandoned {
			delete(solver.flight, playerHash)
		} else {
			inflight.waiters++
			solver.mu.Unlock()
			return solver.waitForFlight(ctx, inflight)
		}
	}
	preprocessCtx, cancel := context.WithCancel(context.Background())
	inflight := &call{done: make(chan struct{}), cancel: cancel, waiters: 1}
	solver.flight[playerHash] = inflight
	solver.mu.Unlock()

	go func() {
		started := time.Now()
		preprocessed, cache, err := solver.preprocess(preprocessCtx, id, playerHash, player)
		inflight.val = preprocessed
		inflight.err = err
		inflight.cache = cache
		if cache == provider.ChallengeCacheMiss {
			inflight.elapsed = time.Since(started)
		}
		solver.mu.Lock()
		if solver.flight[playerHash] == inflight {
			delete(solver.flight, playerHash)
		}
		solver.mu.Unlock()
		close(inflight.done)
	}()

	return solver.waitForFlight(ctx, inflight)
}

// waitForFlight blocks until the flight completes or the caller's context is
// canceled. Cancellation coordinates under solver.mu so waiter departure and
// abandonment remain atomic with respect to new joiners.
func (solver *Solver) waitForFlight(ctx context.Context, inflight *call) (string, string, time.Duration, error) {
	select {
	case <-inflight.done:
		return inflight.val, inflight.cache, inflight.elapsed, inflight.err
	case <-ctx.Done():
		solver.mu.Lock()
		inflight.waiters--
		if inflight.waiters == 0 {
			inflight.abandoned = true
			inflight.cancel()
		}
		solver.mu.Unlock()
		category := provider.ChallengeHelperCanceled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			category = provider.ChallengeHelperTimeout
		}
		return "", provider.ChallengeCacheMiss, -1, challengeFailure(category, provider.ChallengePhasePreprocess, ctx.Err())
	}
}

// preprocess runs the expensive player parsing phase with an extended wall time.
// cache reports whether this solver invoked its helper after a miss or consumed
// a completed entry published while it waited for the process-wide slot.
func (solver *Solver) preprocess(ctx context.Context, id, playerHash, player string) (preprocessed string, cache string, err error) {
	// Meriyah parsing is CPU- and memory-intensive. Running distinct player
	// preprocessors concurrently can make otherwise valid scripts exceed the
	// helper wall-time limit. Same-player calls are already coalesced above;
	// serialize distinct cache misses while allowing cancellation while queued.
	select {
	case playerPreprocessSlot <- struct{}{}:
		defer func() { <-playerPreprocessSlot }()
	case <-ctx.Done():
		category := provider.ChallengeHelperCanceled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			category = provider.ChallengeHelperTimeout
		}
		return "", provider.ChallengeCacheMiss, challengeFailure(category, provider.ChallengePhasePreprocess, ctx.Err())
	}

	// Another solver may have populated the application-scoped cache while
	// this helper waited for the process-wide preprocessing slot.
	if preprocessed, ok := solver.lookupPreprocessed(playerHash); ok {
		return preprocessed, provider.ChallengeCacheHit, nil
	}

	input := struct {
		Type               string             `json:"type"`
		Player             string             `json:"player"`
		Requests           []ChallengeRequest `json:"requests"`
		OutputPreprocessed bool               `json:"output_preprocessed"`
	}{"player", player, []ChallengeRequest{}, true}
	argument, err := json.Marshal(input)
	if err != nil {
		return "", provider.ChallengeCacheMiss, challengeFailure(provider.ChallengeHelperInvalidInput, provider.ChallengePhasePreprocess, fmt.Errorf("encode EJS preprocess input: %w", err))
	}
	response := solver.executor.Execute(ctx, protocol.Request{
		Version: protocol.Version, ID: preprocessRequestID(id), Operation: protocol.OperationCall,
		Script: solver.script, Function: "jsc", Arguments: []json.RawMessage{argument},
		Limits: protocol.Limits{
			WallTimeMS: PreprocessWallTimeMS, MemoryBytes: SolverMemoryBytes,
			OutputBytes: SolverOutputBytes, SourceBytes: SolverSourceBytes,
			Trusted: true, // EJS preprocessing requires extended wall time.
		},
	})
	if response.Error != nil {
		return "", provider.ChallengeCacheMiss, challengeFailure(helperCategory(response.Error.Code), provider.ChallengePhasePreprocess, fmt.Errorf("EJS helper %s", response.Error.Code))
	}
	var output struct {
		Type               string `json:"type"`
		Error              string `json:"error"`
		PreprocessedPlayer string `json:"preprocessed_player"`
	}
	if err := json.Unmarshal(response.Result, &output); err != nil {
		return "", provider.ChallengeCacheMiss, challengeFailure(provider.ChallengeHelperMalformed, provider.ChallengePhasePreprocess, errors.New("EJS returned malformed preprocess JSON"))
	}
	if output.Type != "result" {
		return "", provider.ChallengeCacheMiss, challengeFailure(provider.ChallengeHelperMalformed, provider.ChallengePhasePreprocess, errors.New("EJS preprocess failed"))
	}
	if output.PreprocessedPlayer == "" {
		return "", provider.ChallengeCacheMiss, challengeFailure(provider.ChallengeHelperEmptyPlayer, provider.ChallengePhasePreprocess, errors.New("EJS preprocess returned empty player"))
	}
	// Publish while still owning the process-wide slot so a queued solver
	// observes the completed entry instead of starting duplicate work.
	solver.storePreprocessed(playerHash, output.PreprocessedPlayer)
	return output.PreprocessedPlayer, provider.ChallengeCacheMiss, nil
}

const preprocessRequestIDSuffix = "-preprocess"

// preprocessRequestID keeps the phase marker when it fits, but preserves an
// already-valid full-length caller ID instead of making it invalid at the
// helper boundary. Preprocess and solve execute sequentially, so reusing the
// caller ID at the protocol limit remains unambiguous.
func preprocessRequestID(id string) string {
	if len(id)+len(preprocessRequestIDSuffix) <= protocol.MaxRequestIDLength {
		return id + preprocessRequestIDSuffix
	}
	return id
}

// solve executes the extracted transforms against challenge values using the
// compact preprocessed player. This phase is fast and uses a tight timeout.
func (solver *Solver) solve(ctx context.Context, id, preprocessed string, requests []ChallengeRequest, outputPreprocessed bool, originalPlayer string) (Result, error) {
	input := struct {
		Type               string             `json:"type"`
		PreprocessedPlayer string             `json:"preprocessed_player"`
		Requests           []ChallengeRequest `json:"requests"`
	}{"preprocessed", preprocessed, requests}
	argument, err := json.Marshal(input)
	if err != nil {
		return Result{}, challengeFailure(provider.ChallengeHelperInvalidInput, provider.ChallengePhaseSolve, fmt.Errorf("encode EJS solve input: %w", err))
	}
	response := solver.executor.Execute(ctx, protocol.Request{
		Version: protocol.Version, ID: id, Operation: protocol.OperationCall,
		Script: solver.script, Function: "jsc", Arguments: []json.RawMessage{argument},
		Limits: protocol.Limits{
			WallTimeMS: SolveWallTimeMS, MemoryBytes: SolverMemoryBytes,
			OutputBytes: SolverOutputBytes, SourceBytes: SolverSourceBytes,
		},
	})
	if response.Error != nil {
		return Result{}, challengeFailure(helperCategory(response.Error.Code), provider.ChallengePhaseSolve, fmt.Errorf("EJS helper %s", response.Error.Code))
	}
	result, err := decodeOutput(response.Result, requests)
	if err != nil {
		return Result{}, challengeFailure(provider.ChallengeHelperMalformed, provider.ChallengePhaseSolve, err)
	}
	if outputPreprocessed {
		result.PreprocessedPlayer = preprocessed
	}
	return result, nil
}

func (solver *Solver) lookupPreprocessed(hash string) (string, bool) {
	cache := solver.preprocessed
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, ok := cache.entries[hash]
	if ok {
		// Move to end (most recently used).
		for i, h := range cache.order {
			if h == hash {
				cache.order = append(cache.order[:i], cache.order[i+1:]...)
				cache.order = append(cache.order, hash)
				break
			}
		}
	}
	return value, ok
}

func (solver *Solver) storePreprocessed(hash, preprocessed string) {
	cache := solver.preprocessed
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.storeLocked(hash, preprocessed)
}

// storeLocked stores a preprocessed player in the LRU cache. The caller must
// hold cache.mu.
func (cache *PreprocessedPlayerCache) storeLocked(hash, preprocessed string) {
	if _, exists := cache.entries[hash]; exists {
		return
	}
	if len(cache.entries) >= MaxCachedPlayers {
		oldest := cache.order[0]
		cache.order = cache.order[1:]
		delete(cache.entries, oldest)
	}
	cache.entries[hash] = preprocessed
	cache.order = append(cache.order, hash)
}

func validateChallenges(requests []ChallengeRequest) error {
	total := 0
	for index, request := range requests {
		if request.Type != ChallengeN && request.Type != ChallengeSig {
			return fmt.Errorf("request %d has unsupported challenge type %q", index, request.Type)
		}
		total += len(request.Challenges)
		if total > MaxChallenges {
			return fmt.Errorf("challenge count exceeds %d", MaxChallenges)
		}
		for _, challenge := range request.Challenges {
			if len(challenge) > MaxChallengeBytes {
				return fmt.Errorf("challenge exceeds %d bytes", MaxChallengeBytes)
			}
		}
	}
	return nil
}

type outputEnvelope struct {
	Type               string `json:"type"`
	Error              string `json:"error"`
	PreprocessedPlayer string `json:"preprocessed_player"`
	Responses          []struct {
		Type  string            `json:"type"`
		Data  map[string]string `json:"data"`
		Error string            `json:"error"`
	} `json:"responses"`
}

func decodeOutput(payload []byte, requests []ChallengeRequest) (Result, error) {
	var output outputEnvelope
	if err := json.Unmarshal(payload, &output); err != nil {
		return Result{}, errors.New("EJS returned malformed JSON")
	}
	if output.Type != "result" {
		return Result{}, errors.New("EJS solver failed")
	}
	if len(output.Responses) != len(requests) {
		return Result{}, errors.New("EJS response count mismatch")
	}
	result := Result{PreprocessedPlayer: output.PreprocessedPlayer, Responses: make([]ChallengeResponse, len(requests))}
	for index, response := range output.Responses {
		result.Responses[index].Type = requests[index].Type
		switch response.Type {
		case "result":
			result.Responses[index].Data = response.Data
		case "error":
			result.Responses[index].Error = sanitizeSolverError(response.Error)
		default:
			return Result{}, fmt.Errorf("EJS response %d has invalid type", index)
		}
	}
	return result, nil
}

func sanitizeSolverError(message string) string {
	if strings.Contains(message, "Failed to extract") {
		return "EJS failed to extract challenge function"
	}
	return "EJS challenge execution failed"
}
