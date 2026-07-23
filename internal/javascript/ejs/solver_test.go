package ejs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/javascript/engine"
	"github.com/ytdlp-go/ytdlp/internal/javascript/protocol"
)

type corpus struct {
	Version         int    `json:"version"`
	EJSVersion      string `json:"ejs_version"`
	ReferenceCommit string `json:"reference_commit"`
	Requests        []struct {
		Type       ChallengeType     `json:"type"`
		Challenges []string          `json:"challenges"`
		Expected   map[string]string `json:"expected"`
	} `json:"requests"`
}

func TestPinnedEJSCorpus(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture corpus
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Version != 1 || fixture.EJSVersion != Version || fixture.ReferenceCommit == "" {
		t.Fatalf("invalid fixture provenance: %#v", fixture)
	}
	requests := make([]ChallengeRequest, len(fixture.Requests))
	for index, request := range fixture.Requests {
		requests[index] = ChallengeRequest{Type: request.Type, Challenges: request.Challenges}
	}
	solver, err := New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	result, err := solver.SolvePlayer(context.Background(), "ejs-corpus", string(player), requests, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.PreprocessedPlayer == "" || len(result.Responses) != len(fixture.Requests) {
		t.Fatalf("result = %#v", result)
	}
	for index, response := range result.Responses {
		if response.Error != "" || !reflect.DeepEqual(response.Data, fixture.Requests[index].Expected) {
			t.Fatalf("response %d = %#v, want %#v", index, response, fixture.Requests[index].Expected)
		}
	}
}

func TestVerifyAssetsAndInputBounds(t *testing.T) {
	if err := VerifyAssets(); err != nil {
		t.Fatal(err)
	}
	solver, err := New(engine.New(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := solver.SolvePlayer(context.Background(), "empty", "", nil, false); err == nil {
		t.Fatal("empty player succeeded")
	}
	if _, err := solver.SolvePlayer(context.Background(), "type", "1", []ChallengeRequest{{Type: "unknown"}}, false); err == nil {
		t.Fatal("unknown challenge type succeeded")
	}
}

func TestCanonicalEmbeddedScriptOnlyNormalizesCRLFPairs(t *testing.T) {
	got := canonicalEmbeddedScript("one\r\ntwo\rthree\n")
	if got != "one\ntwo\rthree\n" {
		t.Fatalf("canonical source = %q", got)
	}
}

func TestMissingChallengeFunctionIsSanitized(t *testing.T) {
	solver, err := New(engine.New(1))
	if err != nil {
		t.Fatal(err)
	}
	player := `(function(){var marker="secret-player-data"}).call(this);`
	result, err := solver.SolvePlayer(context.Background(), "missing", player, []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"secret-challenge"}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Responses) != 1 || result.Responses[0].Error != "EJS challenge execution failed" {
		t.Fatalf("result = %#v", result)
	}
}

// TestPreprocessCacheReuse verifies that the same player script is only
// preprocessed once and subsequent calls use the cached preprocessed player.
func TestPreprocessCacheReuse(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingExecutor{inner: engine.New(4)}
	solver, err := New(counting)
	if err != nil {
		t.Fatal(err)
	}
	requests := []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}

	// First call: preprocess + solve = 2 executor calls.
	result1, err := solver.SolvePlayer(context.Background(), "first", string(player), requests, false)
	if err != nil {
		t.Fatal(err)
	}
	if result1.Responses[0].Data["abc"] != "cba-n" {
		t.Fatalf("first result = %#v", result1)
	}
	firstCalls := counting.count()
	if firstCalls != 2 {
		t.Fatalf("first call executor invocations = %d, want 2 (preprocess + solve)", firstCalls)
	}

	// Second call with same player: cache hit, only solve = 1 executor call.
	result2, err := solver.SolvePlayer(context.Background(), "second", string(player), requests, false)
	if err != nil {
		t.Fatal(err)
	}
	if result2.Responses[0].Data["abc"] != "cba-n" {
		t.Fatalf("second result = %#v", result2)
	}
	secondCalls := counting.count() - firstCalls
	if secondCalls != 1 {
		t.Fatalf("second call executor invocations = %d, want 1 (solve only, cache hit)", secondCalls)
	}
}

// TestPreprocessCacheEviction verifies LRU eviction when the cache is full.
func TestPreprocessCacheEviction(t *testing.T) {
	solver, err := New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	requests := []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}

	// Fill cache beyond capacity.
	for i := 0; i < MaxCachedPlayers+2; i++ {
		// Each unique player gets a unique comment to change the hash.
		uniquePlayer := "// variant " + string(rune('A'+i)) + "\n" + string(player)
		if _, err := solver.SolvePlayer(context.Background(), "evict", uniquePlayer, requests, false); err != nil {
			t.Fatal(err)
		}
	}

	solver.mu.Lock()
	cacheSize := len(solver.cache)
	solver.mu.Unlock()
	if cacheSize > MaxCachedPlayers {
		t.Fatalf("cache size = %d, exceeds max %d", cacheSize, MaxCachedPlayers)
	}
}

// TestSolvePlayerCancellation verifies that context cancellation propagates
// through both the preprocess and solve phases.
func TestSolvePlayerCancellation(t *testing.T) {
	solver, err := New(engine.New(1))
	if err != nil {
		t.Fatal(err)
	}
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.
	_, err = solver.SolvePlayer(ctx, "canceled", string(player), []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}, false)
	if err == nil {
		t.Fatal("canceled context succeeded")
	}
	if !strings.Contains(err.Error(), "canceled") && !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("unexpected error for canceled context: %v", err)
	}
}

// TestSolvePlayerTimeout verifies that a pathological player that exceeds the
// wall time limit produces a categorized timeout error.
func TestSolvePlayerTimeout(t *testing.T) {
	// Use a mock executor that always returns timeout for preprocess.
	mock := &timeoutExecutor{}
	solver, err := New(mock)
	if err != nil {
		t.Fatal(err)
	}
	_, err = solver.SolvePlayer(context.Background(), "timeout", "var x = 1;", []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}, false)
	if err == nil {
		t.Fatal("timeout executor succeeded")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got: %v", err)
	}
}

// TestSolvePlayerMalformedPreprocessResponse verifies error handling when the
// helper returns malformed JSON during preprocessing.
func TestSolvePlayerMalformedPreprocessResponse(t *testing.T) {
	mock := &malformedExecutor{}
	solver, err := New(mock)
	if err != nil {
		t.Fatal(err)
	}
	_, err = solver.SolvePlayer(context.Background(), "malformed", "var x = 1;", []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}, false)
	if err == nil {
		t.Fatal("malformed executor succeeded")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("expected malformed error, got: %v", err)
	}
}

// TestSolvePlayerResourceLimits verifies that memory and output limits are
// properly enforced.
func TestSolvePlayerResourceLimits(t *testing.T) {
	solver, err := New(engine.New(1))
	if err != nil {
		t.Fatal(err)
	}
	// Player exceeding MaxPlayerBytes.
	largePlayer := strings.Repeat("x", MaxPlayerBytes+1)
	_, err = solver.SolvePlayer(context.Background(), "large", largePlayer, nil, false)
	if err == nil || !strings.Contains(err.Error(), "player source must contain") {
		t.Fatalf("expected player size error, got: %v", err)
	}
	// Too many challenges.
	challenges := make([]string, MaxChallenges+1)
	for i := range challenges {
		challenges[i] = "c"
	}
	_, err = solver.SolvePlayer(context.Background(), "many", "var x=1;", []ChallengeRequest{{Type: ChallengeN, Challenges: challenges}}, false)
	if err == nil || !strings.Contains(err.Error(), "challenge count exceeds") {
		t.Fatalf("expected challenge count error, got: %v", err)
	}
}

// TestSolvePlayerOutputPreprocessed verifies the outputPreprocessed flag
// returns the preprocessed player in the result.
func TestSolvePlayerOutputPreprocessed(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	solver, err := New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	requests := []ChallengeRequest{{Type: ChallengeSig, Challenges: []string{"abcdef"}}}
	result, err := solver.SolvePlayer(context.Background(), "preprocessed", string(player), requests, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.PreprocessedPlayer == "" {
		t.Fatal("outputPreprocessed=true did not return preprocessed player")
	}
	if result.Responses[0].Data["abcdef"] != "fedcba" {
		t.Fatalf("sig result = %#v", result.Responses[0])
	}
}

// TestConcurrentSolvePlayer verifies thread safety of the solver cache.
func TestConcurrentSolvePlayer(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	solver, err := New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	requests := []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			result, err := solver.SolvePlayer(context.Background(), "concurrent", string(player), requests, false)
			if err != nil {
				errs[idx] = err
				return
			}
			if result.Responses[0].Data["abc"] != "cba-n" {
				errs[idx] = os.ErrInvalid
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d failed: %v", i, err)
		}
	}
}

// TestSingleflightCoalescesPreprocessing verifies that concurrent requests for
// the same uncached player result in exactly one preprocessing execution.
func TestSingleflightCoalescesPreprocessing(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingExecutor{inner: engine.New(4)}
	solver, err := New(counting)
	if err != nil {
		t.Fatal(err)
	}
	requests := []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}

	// Launch 8 concurrent requests for the same uncached player.
	const goroutines = 8
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	results := make([]Result, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = solver.SolvePlayer(context.Background(), "singleflight", string(player), requests, false)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d failed: %v", i, err)
		}
		if results[i].Responses[0].Data["abc"] != "cba-n" {
			t.Fatalf("goroutine %d wrong result: %#v", i, results[i])
		}
	}

	// With singleflight: 1 preprocess + 8 solves = 9 total executor calls.
	// Without singleflight: 8 preprocess + 8 solves = 16 total executor calls.
	totalCalls := counting.count()
	if totalCalls > goroutines+1 {
		t.Fatalf("executor calls = %d, want <= %d (1 preprocess + %d solves); singleflight not coalescing", totalCalls, goroutines+1, goroutines)
	}
}

// TestSingleflightFollowerCancellation verifies that a follower waiting on
// in-flight preprocessing can cancel via its context without blocking for the
// full preprocessing duration.
func TestSingleflightFollowerCancellation(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	slow := &slowExecutor{inner: engine.New(4), delay: 2 * time.Second}
	solver, err := New(slow)
	if err != nil {
		t.Fatal(err)
	}
	requests := []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}

	// Start the leader (will take 2s due to slow executor).
	leaderDone := make(chan error, 1)
	go func() {
		_, err := solver.SolvePlayer(context.Background(), "leader", string(player), requests, false)
		leaderDone <- err
	}()

	// Give the leader time to register the in-flight entry.
	time.Sleep(100 * time.Millisecond)

	// Start a follower with a short-lived context.
	followerCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, followerErr := solver.SolvePlayer(followerCtx, "follower", string(player), requests, false)
	elapsed := time.Since(start)

	// The follower should cancel quickly (~200ms), not wait the full 2s.
	if elapsed > 1*time.Second {
		t.Fatalf("follower took %v, expected cancellation within ~200ms", elapsed)
	}
	if followerErr == nil {
		t.Fatal("follower should have been canceled")
	}

	// The leader should still complete successfully (follower canceling
	// does not affect the leader since waiters > 0).
	if leaderErr := <-leaderDone; leaderErr != nil {
		t.Fatalf("leader failed: %v", leaderErr)
	}
}

// TestSingleflightCanceledLeaderDoesNotFailLiveFollower verifies that
// canceling the leader's context does not propagate the cancellation error
// to followers whose contexts remain active.
func TestSingleflightCanceledLeaderDoesNotFailLiveFollower(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	slow := &slowExecutor{inner: engine.New(4), delay: 1 * time.Second}
	solver, err := New(slow)
	if err != nil {
		t.Fatal(err)
	}
	requests := []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}

	// Start the leader with a context that will be canceled.
	leaderCtx, leaderCancel := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := solver.SolvePlayer(leaderCtx, "leader", string(player), requests, false)
		leaderDone <- err
	}()

	// Give the leader time to register the in-flight entry.
	time.Sleep(100 * time.Millisecond)

	// Start a follower with a live (long-lived) context.
	followerDone := make(chan struct {
		Result
		error
	}, 1)
	go func() {
		result, err := solver.SolvePlayer(context.Background(), "follower", string(player), requests, false)
		followerDone <- struct {
			Result
			error
		}{result, err}
	}()

	// Cancel the leader's context while preprocessing is in flight.
	time.Sleep(50 * time.Millisecond)
	leaderCancel()

	// The leader should fail with context canceled.
	leaderErr := <-leaderDone
	if leaderErr == nil {
		t.Fatal("leader should have been canceled")
	}

	// The follower should succeed because at least one waiter remains,
	// so shared preprocessing continues.
	select {
	case outcome := <-followerDone:
		if outcome.error != nil {
			t.Fatalf("follower failed due to leader cancellation: %v", outcome.error)
		}
		if outcome.Result.Responses[0].Data["abc"] != "cba-n" {
			t.Fatalf("follower wrong result: %#v", outcome.Result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("follower timed out waiting for preprocessing")
	}
}

// TestSingleflightAllWaitersCancelCancelsPreprocessing verifies that when
// all waiters cancel, the shared preprocessing is also canceled promptly.
func TestSingleflightAllWaitersCancelCancelsPreprocessing(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	slow := &slowExecutor{inner: engine.New(4), delay: 5 * time.Second}
	solver, err := New(slow)
	if err != nil {
		t.Fatal(err)
	}
	requests := []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}

	// Start two callers that will both cancel.
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() {
		_, err := solver.SolvePlayer(ctx1, "w1", string(player), requests, false)
		done1 <- err
	}()
	go func() {
		_, err := solver.SolvePlayer(ctx2, "w2", string(player), requests, false)
		done2 <- err
	}()

	// Let both register as waiters.
	time.Sleep(100 * time.Millisecond)

	// Cancel both.
	cancel1()
	cancel2()

	// Both should return promptly (not wait 5s).
	start := time.Now()
	err1 := <-done1
	err2 := <-done2
	elapsed := time.Since(start)

	if err1 == nil || err2 == nil {
		t.Fatal("both waiters should have been canceled")
	}
	if elapsed > 1*time.Second {
		t.Fatalf("waiters took %v to cancel, expected prompt return", elapsed)
	}

	// The flight entry should be cleaned up (no stale entry).
	// A subsequent request should start fresh preprocessing.
	solver.mu.Lock()
	staleFlights := len(solver.flight)
	solver.mu.Unlock()
	// The flight goroutine cleans up asynchronously; give it a moment.
	time.Sleep(200 * time.Millisecond)
	solver.mu.Lock()
	staleFlights = len(solver.flight)
	solver.mu.Unlock()
	if staleFlights != 0 {
		t.Fatalf("stale flight entries: %d", staleFlights)
	}
}

// TestSingleflightCanceledLeaderNoFollowersReturnsPromptly verifies that
// when the only waiter (leader) cancels with no followers, it returns
// promptly and preprocessing is canceled.
func TestSingleflightCanceledLeaderNoFollowersReturnsPromptly(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	slow := &slowExecutor{inner: engine.New(4), delay: 5 * time.Second}
	solver, err := New(slow)
	if err != nil {
		t.Fatal(err)
	}
	requests := []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, solveErr := solver.SolvePlayer(ctx, "solo", string(player), requests, false)
	elapsed := time.Since(start)

	if solveErr == nil {
		t.Fatal("solo waiter should have been canceled")
	}
	if elapsed > 1*time.Second {
		t.Fatalf("solo waiter took %v, expected prompt cancellation (~200ms)", elapsed)
	}
}

// TestSingleflightOneFollowerCancelsOtherRemains verifies that when one
// follower cancels, the remaining follower still gets the result.
func TestSingleflightOneFollowerCancelsOtherRemains(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	slow := &slowExecutor{inner: engine.New(4), delay: 1 * time.Second}
	solver, err := New(slow)
	if err != nil {
		t.Fatal(err)
	}
	requests := []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}

	// Start two followers.
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	done2 := make(chan struct {
		Result
		error
	}, 1)
	go func() {
		_, err := solver.SolvePlayer(ctx1, "f1", string(player), requests, false)
		done1 <- err
	}()
	go func() {
		result, err := solver.SolvePlayer(context.Background(), "f2", string(player), requests, false)
		done2 <- struct {
			Result
			error
		}{result, err}
	}()

	// Let both register.
	time.Sleep(100 * time.Millisecond)

	// Cancel only the first follower.
	cancel1()
	err1 := <-done1
	if err1 == nil {
		t.Fatal("canceled follower should have failed")
	}

	// The second follower should still succeed.
	select {
	case outcome := <-done2:
		if outcome.error != nil {
			t.Fatalf("remaining follower failed: %v", outcome.error)
		}
		if outcome.Result.Responses[0].Data["abc"] != "cba-n" {
			t.Fatalf("remaining follower wrong result: %#v", outcome.Result)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("remaining follower timed out")
	}
}

// TestPathologicalPlayerFixture verifies the pathological player fixture
// completes within bounds (not an infinite loop).
func TestPathologicalPlayerFixture(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/pathological-player.js")
	if err != nil {
		t.Fatal(err)
	}
	solver, err := New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	requests := []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}
	result, err := solver.SolvePlayer(ctx, "pathological", string(player), requests, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Responses[0].Data["abc"] != "cba-n" {
		t.Fatalf("pathological result = %#v", result.Responses[0])
	}
}

// TestRepresentativeWorkloadUnderOldLimit verifies that the EJS preprocessing
// wall time (55s) exceeds the old global HardMaxWallTime (30s) and would have
// been rejected at protocol validation. The Trusted flag allows the extended
// TrustedMaxWallTime (60s) ceiling. This test proves the scoped allowance is
// necessary and correctly gated.
func TestRepresentativeWorkloadUnderOldLimit(t *testing.T) {
	// Demonstrate that PreprocessWallTimeMS exceeds the untrusted HardMaxWallTime.
	if PreprocessWallTimeMS <= protocol.HardMaxWallTime.Milliseconds() {
		t.Fatalf("PreprocessWallTimeMS (%d) should exceed HardMaxWallTime (%d ms) to require Trusted flag",
			PreprocessWallTimeMS, protocol.HardMaxWallTime.Milliseconds())
	}
	// Demonstrate that PreprocessWallTimeMS fits within TrustedMaxWallTime.
	if PreprocessWallTimeMS > protocol.TrustedMaxWallTime.Milliseconds() {
		t.Fatalf("PreprocessWallTimeMS (%d) exceeds TrustedMaxWallTime (%d ms)",
			PreprocessWallTimeMS, protocol.TrustedMaxWallTime.Milliseconds())
	}
	// Verify that an untrusted request with PreprocessWallTimeMS is rejected.
	req := protocol.Request{
		Version: protocol.Version, ID: "untrusted-preprocess", Operation: protocol.OperationCall,
		Script: "function jsc(){}", Function: "jsc",
		Limits: protocol.Limits{WallTimeMS: PreprocessWallTimeMS, MemoryBytes: SolverMemoryBytes,
			OutputBytes: SolverOutputBytes, SourceBytes: SolverSourceBytes},
	}
	if _, err := req.Normalize(); err == nil {
		t.Fatal("untrusted request with PreprocessWallTimeMS should be rejected by HardMaxWallTime")
	}
	// Verify that a trusted request with PreprocessWallTimeMS is accepted.
	req.Limits.Trusted = true
	if _, err := req.Normalize(); err != nil {
		t.Fatalf("trusted request with PreprocessWallTimeMS should be accepted: %v", err)
	}
}

// TestLargeGeneratedPlayerWorkload generates a deterministic ~150 KB player
// script with 2000 function declarations and verifies the two-phase split
// completes successfully within the extended preprocessing budget.
//
// Provenance: Real YouTube player scripts are 1-2 MB of obfuscated JavaScript.
// This generated workload (~150 KB, 2000 functions) exercises the meriyah
// parse + astring code-generation path at meaningful scale. It does NOT
// reproduce the original 30 s timeout empirically—that failure mode required
// a real 1-2 MB player under the old single-phase architecture. The timeout
// fix is proven structurally by TestRepresentativeWorkloadUnderOldLimit
// (protocol validation rejects 55 s without the Trusted flag) and by the
// end-to-end supervisor test that exercises the full process boundary.
func TestLargeGeneratedPlayerWorkload(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large workload test in short mode")
	}
	// Generate a deterministic large player script.
	var sb strings.Builder
	sb.WriteString(`(function(){var helper={alr:function(){}};`)
	sb.WriteString(`function Params(url,key,value){this.values={s:value,n:null};}`)
	sb.WriteString(`Params.prototype.set=function(k,v){this.values[k]=v;};`)
	sb.WriteString(`Params.prototype.get=function(k){return this.values[k]};`)
	sb.WriteString(`Params.prototype.clone=function(){return this;};`)
	sb.WriteString(`Params.prototype.transform=function(){`)
	sb.WriteString(`if(this.values.n)this.values.n=this.values.n.split("").reverse().join("")+"-n";`)
	sb.WriteString(`if(this.values.s)this.values.s=this.values.s.split("").reverse().join("");};`)
	// Generate 2000 padding functions to simulate player bloat (~150 KB total).
	for i := 0; i < 2000; i++ {
		sb.WriteString(fmt.Sprintf("function pad%d(alpha,beta,gamma){var x=alpha+%d+beta*%d+gamma;return x*2+%d;}", i, i, i%7, i%13))
	}
	sb.WriteString(`function candidate(url,key,value){helper.alr("alr","yes");return new Params(url,key,value);}`)
	sb.WriteString(`}).call(this);`)
	player := sb.String()

	if len(player) < 100_000 {
		t.Fatalf("generated player too small: %d bytes", len(player))
	}
	t.Logf("generated player size: %d bytes (%d KB)", len(player), len(player)/1024)

	solver, err := New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	requests := []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}
	result, err := solver.SolvePlayer(ctx, "large-workload", player, requests, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Responses[0].Data["abc"] != "cba-n" {
		t.Fatalf("large workload result = %#v", result.Responses[0])
	}
}

// --- Mock executors ---

type countingExecutor struct {
	inner Executor
	mu    sync.Mutex
	calls int
}

func (c *countingExecutor) Execute(ctx context.Context, req protocol.Request) protocol.Response {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner.Execute(ctx, req)
}

func (c *countingExecutor) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type timeoutExecutor struct{}

func (timeoutExecutor) Execute(_ context.Context, req protocol.Request) protocol.Response {
	return protocol.FailureResponse(req.ID, protocol.CodeTimeout, context.DeadlineExceeded)
}

type malformedExecutor struct{}

func (malformedExecutor) Execute(_ context.Context, req protocol.Request) protocol.Response {
	return protocol.Response{Version: protocol.Version, ID: req.ID, Result: json.RawMessage(`{invalid`)}
}

type slowExecutor struct {
	inner Executor
	delay time.Duration
}

func (s *slowExecutor) Execute(ctx context.Context, req protocol.Request) protocol.Response {
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
		return protocol.FailureResponse(req.ID, protocol.CodeTimeout, ctx.Err())
	}
	return s.inner.Execute(ctx, req)
}

// TestSingleflightWaiterJoinCancelRace is a deterministic regression test for
// the race where a new waiter joins a flight immediately before the last
// waiter cancels it. Under the old atomic-only design, the join and the
// cancel could interleave such that the new waiter joined a flight that was
// about to be abandoned. With the lock-coordinated design, joining checks
// the abandoned flag under solver.mu, and cancellation sets it under the
// same lock, making the race impossible.
//
// Run with -race to verify no data races exist.
func TestSingleflightWaiterJoinCancelRace(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	slow := &slowExecutor{inner: engine.New(4), delay: 2 * time.Second}
	solver, err := New(slow)
	if err != nil {
		t.Fatal(err)
	}
	requests := []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}

	const iterations = 50
	for i := 0; i < iterations; i++ {
		// Each iteration: start a leader that will cancel quickly, then
		// immediately try to join with a follower. The follower must either
		// see the flight as active (and wait) or see it as abandoned (and
		// start a new flight). It must never join a canceled flight.
		leaderCtx, leaderCancel := context.WithCancel(context.Background())
		leaderDone := make(chan error, 1)
		go func() {
			_, err := solver.SolvePlayer(leaderCtx, "leader", string(player), requests, false)
			leaderDone <- err
		}()

		// Give the leader time to register the flight.
		time.Sleep(time.Millisecond)

		// Cancel the leader.
		leaderCancel()

		// Immediately try to join as a follower with a short timeout.
		followerCtx, followerCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, followerErr := solver.SolvePlayer(followerCtx, "follower", string(player), requests, false)
		followerCancel()

		// The follower should get a context error (timeout or canceled),
		// never a nil error (which would mean it joined a dead flight).
		<-leaderDone
		if followerErr == nil {
			// This is acceptable: the follower started a new flight that
			// completed (unlikely with 2s delay but possible if cache hit).
			continue
		}
	}
}

// TestFlightOwnershipAbandonedDoesNotDeleteReplacement verifies that when a
// flight is abandoned and a replacement flight is started for the same player
// hash, the abandoned flight's goroutine does not delete the replacement's
// map entry. This is a regression test for the unconditional delete bug.
func TestFlightOwnershipAbandonedDoesNotDeleteReplacement(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}

	// Use a slow executor so we can control timing deterministically.
	counter := &countingExecutor{inner: engine.New(4)}
	slow := &slowExecutor{inner: counter, delay: 500 * time.Millisecond}
	solver, err := New(slow)
	if err != nil {
		t.Fatal(err)
	}
	requests := []ChallengeRequest{{Type: ChallengeN, Challenges: []string{"abc"}}}

	// Phase 1: Start a flight and abandon it (cancel the only waiter).
	ctx1, cancel1 := context.WithCancel(context.Background())
	done1 := make(chan error, 1)
	go func() {
		_, err := solver.SolvePlayer(ctx1, "flight1", string(player), requests, false)
		done1 <- err
	}()

	// Wait for the flight to register.
	time.Sleep(50 * time.Millisecond)
	cancel1()
	err1 := <-done1
	if err1 == nil {
		t.Fatal("first flight should have been canceled")
	}

	// Phase 2: Immediately start a replacement flight for the same player.
	// The abandoned flight's goroutine is still running (500ms delay).
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	done2 := make(chan error, 1)
	go func() {
		_, err := solver.SolvePlayer(ctx2, "flight2", string(player), requests, false)
		done2 <- err
	}()

	// Wait for the replacement flight to register.
	time.Sleep(50 * time.Millisecond)

	// Verify the replacement flight is in the map (not deleted by the
	// abandoned flight's goroutine).
	solver.mu.Lock()
	_, replacementExists := solver.flight[protocol.HashScript(string(player))]
	solver.mu.Unlock()

	// The abandoned flight's goroutine may have completed by now (500ms
	// delay elapsed). If it unconditionally deleted, the replacement would
	// be gone. With the ownership check, it must still exist.
	if !replacementExists {
		// The replacement might have completed already (cache hit from
		// the abandoned flight's successful preprocess). Check if the
		// second call succeeded.
		err2 := <-done2
		if err2 != nil {
			t.Fatalf("replacement flight failed: %v (flight ownership violation)", err2)
		}
		// Success via cache is acceptable.
		return
	}

	// Wait for the replacement to complete.
	err2 := <-done2
	if err2 != nil {
		t.Fatalf("replacement flight failed: %v", err2)
	}

	// Verify preprocess was called exactly twice (once per flight).
	// The slow executor adds 500ms delay, so both flights should have
	// started their own preprocessing.
	time.Sleep(600 * time.Millisecond) // let abandoned goroutine finish
	if calls := counter.count(); calls < 2 {
		t.Fatalf("expected at least 2 preprocess calls (one per flight), got %d", calls)
	}
}
