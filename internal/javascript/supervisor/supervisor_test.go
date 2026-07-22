package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/javascript/protocol"
)

var (
	helperPath    string
	crashPath     string
	malformedPath string
)

func TestMain(testMain *testing.M) {
	directory, err := os.MkdirTemp("", "ytdlp-js-supervisor-")
	if err != nil {
		panic(err)
	}
	extension := ""
	if runtime.GOOS == "windows" {
		extension = ".exe"
	}
	helperPath = filepath.Join(directory, "helper"+extension)
	crashPath = filepath.Join(directory, "crash"+extension)
	malformedPath = filepath.Join(directory, "malformed"+extension)
	repository, err := filepath.Abs("../../..")
	if err != nil {
		panic(err)
	}
	for _, build := range []struct{ output, packagePath string }{
		{helperPath, "./cmd/ytdlp-js-helper"},
		{crashPath, "./internal/javascript/supervisor/testdata/crash"},
		{malformedPath, "./internal/javascript/supervisor/testdata/malformed"},
	} {
		command := exec.Command("go", "build", "-o", build.output, build.packagePath)
		command.Dir = repository
		if output, err := command.CombinedOutput(); err != nil {
			panic(string(output))
		}
	}
	code := testMain.Run()
	_ = os.RemoveAll(directory)
	os.Exit(code)
}

func TestSupervisorExecutesAndRetainsCompiledCache(t *testing.T) {
	client := newTestClient(t, helperPath)
	defer client.Close()
	request := evaluateRequest("first", "var state = 0; ++state")
	first := client.Execute(context.Background(), request)
	request.ID = "second"
	second := client.Execute(context.Background(), request)
	assertSupervisorResult(t, first, "1")
	assertSupervisorResult(t, second, "1")
	if first.Stats.CacheHit || !second.Stats.CacheHit {
		t.Fatalf("cache stats = first %v second %v", first.Stats.CacheHit, second.Stats.CacheHit)
	}
}

func TestSupervisorRejectsSearchPathAndSymlinkHelpers(t *testing.T) {
	t.Setenv("PATH", filepath.Dir(helperPath)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := New(Config{Path: filepath.Base(helperPath)}); err == nil {
		t.Fatal("relative helper unexpectedly accepted from PATH")
	}
	link := filepath.Join(t.TempDir(), "helper-link")
	if err := os.Symlink(helperPath, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Path: link}); err == nil {
		t.Fatal("symlink helper unexpectedly accepted")
	}
}

func TestSupervisorCancellationDiscardsAndRestartsHelper(t *testing.T) {
	client := newTestClient(t, helperPath)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	canceled := client.Execute(ctx, evaluateRequest("loop", "for (;;) {}"))
	if canceled.Error == nil || canceled.Error.Code != protocol.CodeTimeout {
		t.Fatalf("canceled response = %#v", canceled)
	}
	restarted := client.Execute(context.Background(), evaluateRequest("restart", "40 + 2"))
	assertSupervisorResult(t, restarted, "42")
	if restarted.Stats.CacheHit {
		t.Fatal("restarted helper unexpectedly retained cache")
	}
}

func TestSupervisorSerializesConcurrentRequests(t *testing.T) {
	client := newTestClient(t, helperPath)
	defer client.Close()
	var wait sync.WaitGroup
	responses := make([]protocol.Response, 8)
	for index := range responses {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			responses[index] = client.Execute(context.Background(), evaluateRequest(string(rune('a'+index)), "6 * 7"))
		}(index)
	}
	wait.Wait()
	for _, response := range responses {
		assertSupervisorResult(t, response, "42")
	}
}

func TestSupervisorCategorizesCrashAndMalformedResponse(t *testing.T) {
	crash := newTestClient(t, crashPath)
	defer crash.Close()
	response := crash.Execute(context.Background(), evaluateRequest("crash", "1"))
	if response.Error == nil || response.Error.Code != protocol.CodeHelperCrash {
		t.Fatalf("crash response = %#v", response)
	}

	malformed := newTestClient(t, malformedPath)
	defer malformed.Close()
	response = malformed.Execute(context.Background(), evaluateRequest("malformed", "1"))
	if response.Error == nil || response.Error.Code != protocol.CodeProtocol {
		t.Fatalf("malformed response = %#v", response)
	}
}

func TestSupervisorEnforcesProcessMemoryBudget(t *testing.T) {
	client, err := New(Config{Path: helperPath, MemoryBytes: 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request := evaluateRequest("memory", "1")
	request.Limits.MemoryBytes = 64 << 20
	response := client.Execute(context.Background(), request)
	if response.Error == nil || response.Error.Code != protocol.CodeMemoryLimit {
		t.Fatalf("response = %#v", response)
	}
}

// TestSupervisorTrustedWallTimeCrossesProcessBoundary verifies that the pinned
// EJS preprocessing call (operation=call, function="jsc", matching script hash)
// with WallTimeMS > HardMaxWallTime (30 s) succeeds end-to-end through the
// supervisor → helper pipe when the caller sets Limits.Trusted.
func TestSupervisorTrustedWallTimeCrossesProcessBoundary(t *testing.T) {
	script := `function jsc(input){return JSON.stringify({preprocessed:true})}`
	client := newTrustedTestClient(t, helperPath, script)
	defer client.Close()

	// EJS preprocessing call with 45 s wall time (exceeds 30 s untrusted).
	request := ejsCallRequest("trusted-e2e", script)
	request.Limits.WallTimeMS = 45_000
	request.Limits.Trusted = true

	response := client.Execute(context.Background(), request)
	if response.Error != nil {
		t.Fatalf("trusted EJS call failed across process boundary: code=%s msg=%s",
			response.Error.Code, response.Error.Message)
	}
	if response.Result == nil {
		t.Fatal("expected non-nil result")
	}
}

// TestSupervisorRejectsUntrustedExtendedWallTime verifies that a request with
// WallTimeMS > HardMaxWallTime without the Trusted flag is rejected by the
// supervisor before reaching the helper.
func TestSupervisorRejectsUntrustedExtendedWallTime(t *testing.T) {
	client := newTestClient(t, helperPath)
	defer client.Close()

	request := ejsCallRequest("untrusted-e2e", `function jsc(input){return "{}"}`)
	request.Limits.WallTimeMS = 45_000
	// Trusted is false — supervisor should reject at Normalize().

	response := client.Execute(context.Background(), request)
	if response.Error == nil {
		t.Fatal("untrusted request with 45 s wall time should be rejected")
	}
	if response.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %s", response.Error.Code)
	}
}

// TestSupervisorRejectsTrustedGenericEvaluate verifies that a generic evaluate
// request cannot obtain the extended timeout even with Trusted=true. The
// allowance is restricted to EJS preprocessing calls (function "jsc").
func TestSupervisorRejectsTrustedGenericEvaluate(t *testing.T) {
	client := newTestClient(t, helperPath)
	defer client.Close()

	request := evaluateRequest("generic-eval", "1+1")
	request.Limits.WallTimeMS = 45_000
	request.Limits.Trusted = true // should be ignored for non-EJS

	response := client.Execute(context.Background(), request)
	if response.Error == nil {
		t.Fatal("generic evaluate with 45 s wall time should be rejected even with Trusted")
	}
	if response.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %s", response.Error.Code)
	}
}

// TestSupervisorRejectsSpoofedTrustedWallTimeMS verifies that a caller cannot
// forge the serialized TrustedWallTimeMS grant. The supervisor strips it at
// the boundary before validation.
func TestSupervisorRejectsSpoofedTrustedWallTimeMS(t *testing.T) {
	client := newTestClient(t, helperPath)
	defer client.Close()

	// Attempt to spoof the grant on a generic evaluate request.
	request := evaluateRequest("spoof-eval", "1+1")
	request.Limits.WallTimeMS = 45_000
	request.Limits.TrustedWallTimeMS = 60_000 // forged grant

	response := client.Execute(context.Background(), request)
	if response.Error == nil {
		t.Fatal("spoofed TrustedWallTimeMS on evaluate should be rejected")
	}

	// Attempt to spoof the grant on an EJS call without Trusted flag.
	ejsReq := ejsCallRequest("spoof-ejs", `function jsc(input){return "{}"}`)
	ejsReq.Limits.WallTimeMS = 45_000
	ejsReq.Limits.TrustedWallTimeMS = 60_000 // forged grant, no Trusted flag

	response = client.Execute(context.Background(), ejsReq)
	if response.Error == nil {
		t.Fatal("spoofed TrustedWallTimeMS without Trusted flag should be rejected")
	}
}

// TestSupervisorRejectsTrustedNonJSCCall verifies that a call request with
// Trusted=true but a function other than "jsc" cannot obtain the extended
// timeout.
func TestSupervisorRejectsTrustedNonJSCCall(t *testing.T) {
	client := newTestClient(t, helperPath)
	defer client.Close()

	request := protocol.Request{
		Version: protocol.Version, ID: "non-jsc", Operation: protocol.OperationCall,
		Script: "function other(x){return x}", Function: "other",
		Arguments: []json.RawMessage{json.RawMessage(`"hello"`)},
		Limits:    protocol.Limits{WallTimeMS: 45_000, Trusted: true},
	}

	response := client.Execute(context.Background(), request)
	if response.Error == nil {
		t.Fatal("trusted non-jsc call with 45 s wall time should be rejected")
	}
	if response.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %s", response.Error.Code)
	}
}

// TestSupervisorRejectsArbitraryJSCScript verifies that an arbitrary script
// defining "jsc" does NOT receive the extended allowance when the supervisor
// is configured with a different TrustedScriptHash. Only the pinned bundled
// EJS script hash qualifies.
func TestSupervisorRejectsArbitraryJSCScript(t *testing.T) {
	// Supervisor trusts a different script (simulating the pinned EJS hash).
	pinnedScript := `function jsc(input){return "pinned"}`
	client := newTrustedTestClient(t, helperPath, pinnedScript)
	defer client.Close()

	// Attacker sends a different script that also defines "jsc".
	attackerScript := `function jsc(input){while(true){}}`
	request := ejsCallRequest("attacker", attackerScript)
	request.Limits.WallTimeMS = 45_000
	request.Limits.Trusted = true

	response := client.Execute(context.Background(), request)
	if response.Error == nil {
		t.Fatal("expected arbitrary jsc script to be rejected for extended wall time")
	}
	if response.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("expected invalid_request, got %s", response.Error.Code)
	}
}

// TestSupervisorConcurrentExecuteAndCloseDrainsActiveSolves verifies that
// Close blocks until in-flight JavaScript executions complete, then
// terminates the helper process. Uses a slow script to guarantee execution
// is active when Close is invoked.
func TestSupervisorConcurrentExecuteAndCloseDrainsActiveSolves(t *testing.T) {
	for iteration := 0; iteration < 3; iteration++ {
		client := newTestClient(t, helperPath)

		// Capture the process handle before Close clears it.
		// Force the helper to start by running a quick request first.
		warmup := evaluateRequest("warmup", "1")
		if resp := client.Execute(context.Background(), warmup); resp.Error != nil {
			t.Fatalf("iteration %d warmup failed: %s", iteration, resp.Error.Message)
		}
		proc := client.command.Process

		// Launch a fixed-time JavaScript execution (~300ms via Date.now).
		// This is portable and hardware-independent.
		activeDone := make(chan protocol.Response, 1)
		go func() {
			request := evaluateRequest("slow", "var t=Date.now()+300;while(Date.now()<t){}42")
			request.Limits.WallTimeMS = 30_000
			activeDone <- client.Execute(context.Background(), request)
		}()

		// Give the execution time to become active in the helper.
		time.Sleep(50 * time.Millisecond)

		// Close should block until the active execution completes.
		closeStart := time.Now()
		client.Close()
		closeElapsed := time.Since(closeStart)

		// The active execution should have completed with a valid result.
		resp := <-activeDone
		if resp.Error != nil {
			t.Fatalf("iteration %d: active execution failed after Close: %s", iteration, resp.Error.Message)
		}
		if resp.Result == nil {
			t.Fatalf("iteration %d: active execution returned nil result", iteration)
		}

		// Close should have waited for the execution (not returned instantly).
		if closeElapsed < 10*time.Millisecond {
			t.Fatalf("iteration %d: Close returned in %v, expected to block for active execution", iteration, closeElapsed)
		}

		// The helper process should be terminated.
		if proc != nil {
			time.Sleep(50 * time.Millisecond)
			err := proc.Signal(nil)
			if err == nil {
				t.Fatalf("iteration %d: helper process still running after Close", iteration)
			}
		}
	}
}

func newTestClient(t *testing.T, path string) *Client {
	t.Helper()
	client, err := New(Config{Path: path, MemoryBytes: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// newTrustedTestClient creates a supervisor that trusts the given script for
// extended wall-time grants. The TrustedScriptHash is computed from the script.
func newTrustedTestClient(t *testing.T, path, script string) *Client {
	t.Helper()
	client, err := New(Config{
		Path: path, MemoryBytes: 128 << 20,
		TrustedScriptHash: protocol.HashScript(script),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func evaluateRequest(id, script string) protocol.Request {
	return protocol.Request{Version: protocol.Version, ID: id, Operation: protocol.OperationEvaluate, Script: script}
}

func ejsCallRequest(id, script string) protocol.Request {
	return protocol.Request{
		Version: protocol.Version, ID: id, Operation: protocol.OperationCall,
		Script: script, Function: "jsc",
		Arguments: []json.RawMessage{json.RawMessage(`{"type":"preprocess","player":"","requests":[]}`)},
	}
}

func assertSupervisorResult(t *testing.T, response protocol.Response, want string) {
	t.Helper()
	if response.Error != nil || string(response.Result) != want {
		encoded, _ := json.Marshal(response)
		t.Fatalf("response = %s, want %s", encoded, want)
	}
}
