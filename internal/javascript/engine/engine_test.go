package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/javascript/protocol"
)

func TestExecuteEvaluateAndCall(t *testing.T) {
	engine := New(4)
	evaluated := engine.Execute(context.Background(), request("eval", protocol.OperationEvaluate, "1 + 2", "", nil))
	assertResult(t, evaluated, "3")

	called := engine.Execute(context.Background(), request(
		"call", protocol.OperationCall,
		"function reverse(value) { return value.split('').reverse().join(''); }",
		"reverse", []json.RawMessage{json.RawMessage(`"abc"`)},
	))
	assertResult(t, called, `"cba"`)
}

func TestExecuteCachesProgramsWithoutSharingRuntimeState(t *testing.T) {
	engine := New(4)
	request := request("first", protocol.OperationEvaluate, "var counter = 0; ++counter", "", nil)
	first := engine.Execute(context.Background(), request)
	request.ID = "second"
	second := engine.Execute(context.Background(), request)
	assertResult(t, first, "1")
	assertResult(t, second, "1")
	if first.Stats.CacheHit || !second.Stats.CacheHit {
		t.Fatalf("cache stats = first %v, second %v", first.Stats.CacheHit, second.Stats.CacheHit)
	}
}

func TestExecuteTimeoutCancellationAndFailures(t *testing.T) {
	engine := New(4)
	timeoutRequest := request("timeout", protocol.OperationEvaluate, "for (;;) {}", "", nil)
	timeoutRequest.Limits.WallTimeMS = 20
	assertFailure(t, engine.Execute(context.Background(), timeoutRequest), protocol.CodeTimeout)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assertFailure(t, engine.Execute(ctx, request("canceled", protocol.OperationEvaluate, "for (;;) {}", "", nil)), protocol.CodeCanceled)

	assertFailure(t, engine.Execute(context.Background(), request("syntax", protocol.OperationEvaluate, "function {", "", nil)), protocol.CodeSyntax)
	assertFailure(t, engine.Execute(context.Background(), request("missing", protocol.OperationCall, "var present = 1", "absent", nil)), protocol.CodeFunctionMissing)
	assertFailure(t, engine.Execute(context.Background(), request("throw", protocol.OperationEvaluate, "throw new Error('secret')", "", nil)), protocol.CodeExecution)
	response := engine.Execute(context.Background(), request("throw-2", protocol.OperationEvaluate, "throw new Error('secret')", "", nil))
	if strings.Contains(response.Error.Message, "secret") {
		t.Fatal("execution failure exposed script data")
	}
}

func TestExecuteOutputLimitAndNoAmbientHostAPIs(t *testing.T) {
	engine := New(4)
	hostAPIs := engine.Execute(context.Background(), request("apis", protocol.OperationEvaluate, "typeof require + ':' + typeof fetch", "", nil))
	assertResult(t, hostAPIs, `"undefined:undefined"`)

	limited := request("output", protocol.OperationEvaluate, "Array(100).join('x')", "", nil)
	limited.Limits.OutputBytes = 10
	assertFailure(t, engine.Execute(context.Background(), limited), protocol.CodeOutputLimit)

	modules := request("module", protocol.OperationEvaluate, "1", "", nil)
	modules.Modules = []protocol.Module{{Name: "allowed.js", Source: "var allowed = true"}}
	assertFailure(t, engine.Execute(context.Background(), modules), protocol.CodeUnsupportedModule)
}

func TestExecuteHonorsParentDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)
	response := New(1).Execute(ctx, request("deadline", protocol.OperationEvaluate, "for (;;) {}", "", nil))
	assertFailure(t, response, protocol.CodeTimeout)
}

func request(id string, operation protocol.Operation, script, function string, arguments []json.RawMessage) protocol.Request {
	return protocol.Request{
		Version: protocol.Version, ID: id, Operation: operation,
		Script: script, Function: function, Arguments: arguments,
	}
}

func assertResult(t *testing.T, response protocol.Response, want string) {
	t.Helper()
	if response.Error != nil || string(response.Result) != want {
		t.Fatalf("response = %#v, want result %s", response, want)
	}
	if response.Stats.Engine == "" || response.Stats.ScriptHash == "" || response.Stats.ExecutionUS < 0 {
		t.Fatalf("missing stats: %#v", response.Stats)
	}
}

func assertFailure(t *testing.T, response protocol.Response, code protocol.ErrorCode) {
	t.Helper()
	if response.Error == nil || response.Error.Code != code {
		t.Fatalf("response = %#v, want error %s", response, code)
	}
}
