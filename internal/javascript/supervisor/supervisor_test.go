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
