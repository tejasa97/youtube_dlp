package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/javascript/protocol"
)

type stubExecutor struct{}

func (stubExecutor) Execute(_ context.Context, request protocol.Request) protocol.Response {
	return protocol.Response{Version: protocol.Version, ID: request.ID, Result: json.RawMessage(`42`)}
}

type panicExecutor struct{}

func (panicExecutor) Execute(context.Context, protocol.Request) protocol.Response { panic("secret") }

func TestServeFramesRequestsAndRecoversAfterMalformedJSON(t *testing.T) {
	valid, err := json.Marshal(protocol.Request{Version: 1, ID: "ok", Operation: protocol.OperationEvaluate, Script: "42"})
	if err != nil {
		t.Fatal(err)
	}
	var input, output bytes.Buffer
	if err := protocol.WriteFrame(&input, []byte(`{"unknown":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteFrame(&input, valid); err != nil {
		t.Fatal(err)
	}
	if err := serve(&input, &output, stubExecutor{}); err != nil {
		t.Fatal(err)
	}
	first := readResponse(t, &output)
	if first.Error == nil || first.Error.Code != protocol.CodeProtocol {
		t.Fatalf("malformed response = %#v", first)
	}
	second := readResponse(t, &output)
	if second.ID != "ok" || string(second.Result) != "42" {
		t.Fatalf("valid response = %#v", second)
	}
}

func TestExecuteSafelyCategorizesPanics(t *testing.T) {
	response := executeSafely(panicExecutor{}, protocol.Request{ID: "panic"})
	if response.Error == nil || response.Error.Code != protocol.CodeHelperCrash || response.ID != "panic" {
		t.Fatalf("response = %#v", response)
	}
	if response.Error.Message == "secret" {
		t.Fatal("panic value escaped helper")
	}
}

func TestConfiguredMemoryLimitRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv(memoryLimitEnvironment, "invalid")
	if got := configuredMemoryLimit(); got != protocol.DefaultMemoryBytes {
		t.Fatalf("configuredMemoryLimit() = %d", got)
	}
	t.Setenv(memoryLimitEnvironment, "33554432")
	if got := configuredMemoryLimit(); got != 33554432 {
		t.Fatalf("configuredMemoryLimit() = %d", got)
	}
}

func readResponse(t *testing.T, reader *bytes.Buffer) protocol.Response {
	t.Helper()
	payload, err := protocol.ReadFrame(reader, protocol.MaxFrameBytes)
	if err != nil {
		t.Fatal(err)
	}
	var response protocol.Response
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatal(err)
	}
	return response
}
