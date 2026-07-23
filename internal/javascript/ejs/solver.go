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

	"github.com/ytdlp-go/ytdlp/internal/javascript/protocol"
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

type ChallengeType string

const (
	ChallengeN   ChallengeType = "n"
	ChallengeSig ChallengeType = "sig"
)

type Executor interface {
	Execute(context.Context, protocol.Request) protocol.Response
}

type ChallengeRequest struct {
	Type       ChallengeType `json:"type"`
	Challenges []string      `json:"challenges"`
}

type ChallengeResponse struct {
	Type  ChallengeType
	Data  map[string]string
	Error string
}

type Result struct {
	Responses          []ChallengeResponse
	PreprocessedPlayer string
}

// Solver executes EJS challenge solving through an isolated JavaScript helper.
// It caches preprocessed players so that repeated videos sharing the same
// player script skip the expensive meriyah-based preprocessing phase.
// Concurrent requests for the same uncached player are coalesced via
// singleflight coordination so preprocessing runs exactly once.
type Solver struct {
	executor Executor
	script   string

	mu     sync.Mutex
	cache  map[string]string // player SHA-256 → preprocessed player
	order  []string          // LRU eviction order (oldest first)
	flight map[string]*call  // in-flight preprocessing coordination
}

// call represents an in-flight preprocessing operation owned by the flight,
// independent of any individual caller's context. Waiters select between
// flight completion and their own context. When all waiters cancel, the
// shared preprocessing is canceled to avoid orphaned work.
//
// All waiter admission, departure, and abandonment decisions are coordinated
// under solver.mu to prevent races between joining and cancellation.
type call struct {
	done      chan struct{}      // closed when preprocessing completes
	cancel    context.CancelFunc // cancels the shared preprocessing goroutine
	waiters   int32              // active waiters (mutated under solver.mu)
	abandoned bool               // true when all waiters left (set under solver.mu)
	val       string
	err       error
}

func New(executor Executor) (*Solver, error) {
	if executor == nil {
		return nil, errors.New("EJS executor is required")
	}
	script, err := bundledScript()
	if err != nil {
		return nil, err
	}
	return &Solver{
		executor: executor,
		script:   script,
		cache:    make(map[string]string, MaxCachedPlayers),
		flight:   make(map[string]*call),
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
		return Result{}, fmt.Errorf("player source must contain 1-%d bytes", MaxPlayerBytes)
	}
	if err := validateChallenges(requests); err != nil {
		return Result{}, err
	}

	playerHash := protocol.HashScript(player)
	preprocessed, err := solver.getPreprocessed(ctx, id, playerHash, player)
	if err != nil {
		return Result{}, err
	}

	return solver.solve(ctx, id, preprocessed, requests, outputPreprocessed, player)
}

// getPreprocessed returns the cached preprocessed player or coalesces
// concurrent preprocessing via a flight-owned lifecycle. Preprocessing runs
// in a dedicated goroutine independent of any individual caller's context.
// Every caller (including the first) selects between flight completion and
// its own context. When all waiters cancel, the shared preprocessing is
// canceled to avoid orphaned work. The result is cached atomically before
// the flight entry is removed to prevent duplicate preprocessing.
//
// Waiter admission, departure, and abandonment are all coordinated under
// solver.mu to prevent races between joining and cancellation.
func (solver *Solver) getPreprocessed(ctx context.Context, id, playerHash, player string) (string, error) {
	// Fast path: cache hit.
	if preprocessed, ok := solver.lookupPreprocessed(playerHash); ok {
		return preprocessed, nil
	}

	// Singleflight: coalesce concurrent misses for the same player.
	solver.mu.Lock()
	if preprocessed, ok := solver.cache[playerHash]; ok {
		solver.mu.Unlock()
		return preprocessed, nil
	}
	if inflight, ok := solver.flight[playerHash]; ok {
		if inflight.abandoned {
			// Flight is being canceled; do not join. Fall through to
			// start a new flight below.
			delete(solver.flight, playerHash)
		} else {
			// Join existing flight as a waiter.
			inflight.waiters++
			solver.mu.Unlock()
			return solver.waitForFlight(ctx, inflight)
		}
	}
	// Register a new flight. This goroutine is the first waiter.
	preprocessCtx, cancel := context.WithCancel(context.Background())
	inflight := &call{done: make(chan struct{}), cancel: cancel, waiters: 1}
	solver.flight[playerHash] = inflight
	solver.mu.Unlock()

	// Start shared preprocessing in a dedicated goroutine.
	go func() {
		preprocessed, err := solver.preprocess(preprocessCtx, id, player)
		inflight.val = preprocessed
		inflight.err = err

		// Cache the result and remove the flight entry only if this flight
		// still owns the map slot. An abandoned flight may have been replaced
		// by a newer flight for the same hash; deleting unconditionally would
		// remove the replacement.
		solver.mu.Lock()
		if err == nil {
			solver.storePreprocessedLocked(playerHash, preprocessed)
		}
		if solver.flight[playerHash] == inflight {
			delete(solver.flight, playerHash)
		}
		solver.mu.Unlock()
		close(inflight.done)
	}()

	return solver.waitForFlight(ctx, inflight)
}

// waitForFlight blocks until the flight completes or the caller's context is
// canceled. Cancellation coordinates under solver.mu: the waiter count is
// decremented and abandonment is decided atomically with respect to new
// joiners, preventing the race where a new waiter joins a flight that is
// about to be canceled.
func (solver *Solver) waitForFlight(ctx context.Context, inflight *call) (string, error) {
	select {
	case <-inflight.done:
		return inflight.val, inflight.err
	case <-ctx.Done():
		// Coordinate departure under the solver lock so that no new waiter
		// can join between our decrement and the cancellation decision.
		solver.mu.Lock()
		inflight.waiters--
		if inflight.waiters == 0 {
			inflight.abandoned = true
			inflight.cancel()
		}
		solver.mu.Unlock()
		return "", ctx.Err()
	}
}

// preprocess runs the expensive player parsing phase with an extended wall time.
func (solver *Solver) preprocess(ctx context.Context, id, player string) (string, error) {
	input := struct {
		Type               string             `json:"type"`
		Player             string             `json:"player"`
		Requests           []ChallengeRequest `json:"requests"`
		OutputPreprocessed bool               `json:"output_preprocessed"`
	}{"player", player, []ChallengeRequest{}, true}
	argument, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("encode EJS preprocess input: %w", err)
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
		return "", fmt.Errorf("EJS helper %s: %s", response.Error.Code, response.Error.Message)
	}
	var output struct {
		Type               string `json:"type"`
		Error              string `json:"error"`
		PreprocessedPlayer string `json:"preprocessed_player"`
	}
	if err := json.Unmarshal(response.Result, &output); err != nil {
		return "", errors.New("EJS returned malformed preprocess JSON")
	}
	if output.Type != "result" {
		return "", errors.New("EJS preprocess failed")
	}
	if output.PreprocessedPlayer == "" {
		return "", errors.New("EJS preprocess returned empty player")
	}
	return output.PreprocessedPlayer, nil
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
		return Result{}, fmt.Errorf("encode EJS solve input: %w", err)
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
		return Result{}, fmt.Errorf("EJS helper %s: %s", response.Error.Code, response.Error.Message)
	}
	result, err := decodeOutput(response.Result, requests)
	if err != nil {
		return Result{}, err
	}
	if outputPreprocessed {
		result.PreprocessedPlayer = preprocessed
	}
	return result, nil
}

func (solver *Solver) lookupPreprocessed(hash string) (string, bool) {
	solver.mu.Lock()
	defer solver.mu.Unlock()
	value, ok := solver.cache[hash]
	if ok {
		// Move to end (most recently used).
		for i, h := range solver.order {
			if h == hash {
				solver.order = append(solver.order[:i], solver.order[i+1:]...)
				solver.order = append(solver.order, hash)
				break
			}
		}
	}
	return value, ok
}

// storePreprocessedLocked stores a preprocessed player in the LRU cache.
// The caller must hold solver.mu.
func (solver *Solver) storePreprocessedLocked(hash, preprocessed string) {
	if _, exists := solver.cache[hash]; exists {
		return
	}
	if len(solver.cache) >= MaxCachedPlayers {
		oldest := solver.order[0]
		solver.order = solver.order[1:]
		delete(solver.cache, oldest)
	}
	solver.cache[hash] = preprocessed
	solver.order = append(solver.order, hash)
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
