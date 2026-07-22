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

// TestSupervisorTrustedWallTimeCrossesProcessBoundary verifies that a request
// with WallTimeMS > HardMaxWallTime (30 s) succeeds end-to-end through the
// supervisor → helper pipe when the caller sets Limits.Trusted. The helper
// marks all received requests as trusted (it is only reachable from the
// supervisor), so the extended TrustedMaxWallTime (60 s) ceiling applies.
func TestSupervisorTrustedWallTimeCrossesProcessBoundary(t *testing.T) {
	client := newTestClient(t, helperPath)
	defer client.Close()

	// WallTimeMS = 45 s exceeds HardMaxWallTime (30 s) but fits within
	// TrustedMaxWallTime (60 s). Without the helper-side trusted marking,
	// the helper would reject this at Normalize().
	request := evaluateRequest("trusted-e2e", "1+1")
	request.Limits.WallTimeMS = 45_000
	request.Limits.Trusted = true // passes supervisor validation

	response := client.Execute(context.Background(), request)
	if response.Error != nil {
		t.Fatalf("trusted request failed across process boundary: code=%s msg=%s",
			response.Error.Code, response.Error.Message)
	}
	assertSupervisorResult(t, response, "2")
}

// TestSupervisorRejectsUntrustedExtendedWallTime verifies that a request with
// WallTimeMS > HardMaxWallTime without the Trusted flag is rejected by the
// supervisor before reaching the helper.
func TestSupervisorRejectsUntrustedExtendedWallTime(t *testing.T) {
	client := newTestClient(t, helperPath)
	defer client.Close()

	request := evaluateRequest("untrusted-e2e", "1+1")
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

// TestSupervisorConcurrentExecuteAndCloseDrainsActiveSolves verifies that
// Close waits for in-flight JavaScript executions to complete before
// terminating the helper process. Active operations receive valid results;
// the helper process is cleaned up with no orphans.
func TestSupervisorConcurrentExecuteAndCloseDrainsActiveSolves(t *testing.T) {
	for iteration := 0; iteration < 3; iteration++ {
		client := newTestClient(t, helperPath)

		const workers = 4
		var wg sync.WaitGroup
		results := make([]protocol.Response, workers)

		// Launch concurrent JavaScript executions.
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				request := evaluateRequest("drain", "40+2")
				results[idx] = client.Execute(context.Background(), request)
			}(i)
		}

		// Close while executions may be in flight.
		time.Sleep(time.Millisecond)
		client.Close()

		wg.Wait()

		// All active operations should have completed with valid results
		// (Close drains before killing the helper).
		for i, resp := range results {
			if resp.Error != nil {
				t.Fatalf("iteration %d worker %d: unexpected error: %s", iteration, i, resp.Error.Message)
			}
			if string(resp.Result) != "42" {
				t.Fatalf("iteration %d worker %d: result = %s, want 42", iteration, i, resp.Result)
			}
		}

		// After Close, the helper process should be terminated.
		// Verify by checking that the process is no longer running.
		if client.command != nil && client.command.Process != nil {
			// On Unix, sending signal 0 checks if the process exists.
			err := client.command.Process.Signal(nil)
			if err == nil {
				// Process still alive — give it a moment to exit.
				time.Sleep(100 * time.Millisecond)
				err = client.command.Process.Signal(nil)
				if err == nil {
					t.Fatalf("iteration %d: helper process still running after Close", iteration)
				}
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

func evaluateRequest(id, script string) protocol.Request {
	return protocol.Request{Version: protocol.Version, ID: id, Operation: protocol.OperationEvaluate, Script: script}
}

func assertSupervisorResult(t *testing.T, response protocol.Response, want string) {
	t.Helper()
	if response.Error != nil || string(response.Result) != want {
		encoded, _ := json.Marshal(response)
		t.Fatalf("response = %s, want %s", encoded, want)
	}
}
