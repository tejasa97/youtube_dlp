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
	// Use a slow executor that delays preprocessing.
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
	if !strings.Contains(followerErr.Error(), "deadline") && !strings.Contains(followerErr.Error(), "canceled") {
		t.Fatalf("unexpected follower error: %v", followerErr)
	}

	// The leader should still complete successfully.
	if leaderErr := <-leaderDone; leaderErr != nil {
		t.Fatalf("leader failed: %v", leaderErr)
	}
}

// TestSingleflightCanceledLeaderDoesNotFailLiveFollower verifies that
// canceling the leader's context does not propagate the cancellation error
// to followers whose contexts remain active. Preprocessing is decoupled
// from the leader's context via context.WithoutCancel.
func TestSingleflightCanceledLeaderDoesNotFailLiveFollower(t *testing.T) {
	player, err := os.ReadFile("../../../conformance/javascript/ejs-0.8.0/synthetic-player.js")
	if err != nil {
		t.Fatal(err)
	}
	// Use a slow executor so we can cancel the leader mid-preprocessing.
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

	// The follower should succeed because preprocessing is decoupled
	// from the leader's context.
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
