package ejs

import (
	"context"
	"encoding/json"
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
