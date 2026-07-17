package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRequestNormalizeDefaultsAndHash(t *testing.T) {
	request, err := (Request{
		Version: Version, ID: "challenge-1", Operation: OperationCall,
		Script:   "function reverse(value) { return value.split('').reverse().join(''); }",
		Function: "reverse", Arguments: []json.RawMessage{json.RawMessage(`"abc"`)},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if request.ScriptHash != HashScript(request.Script) || request.Limits.WallTimeMS != DefaultWallTime.Milliseconds() {
		t.Fatalf("normalized request = %#v", request)
	}
}

func TestRequestRejectsLimitsHashesModulesAndArguments(t *testing.T) {
	base := Request{Version: Version, ID: "request", Operation: OperationEvaluate, Script: "1+1"}
	tests := []Request{
		withRequest(base, func(request *Request) { request.Version++ }),
		withRequest(base, func(request *Request) { request.Limits.MemoryBytes = HardMaxMemoryBytes + 1 }),
		withRequest(base, func(request *Request) { request.ScriptHash = strings.Repeat("0", 64) }),
		withRequest(base, func(request *Request) { request.Modules = []Module{{Name: "../secret", Source: "x"}} }),
		withRequest(base, func(request *Request) { request.Arguments = []json.RawMessage{json.RawMessage(`{`)} }),
	}
	for index, request := range tests {
		if _, err := request.Normalize(); err == nil {
			t.Fatalf("test %d: Normalize() succeeded", index)
		}
	}
}

func TestFrameRoundTripAndBounds(t *testing.T) {
	payload := []byte(`{"version":1}`)
	var buffer bytes.Buffer
	if err := WriteFrame(&buffer, payload); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&buffer, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("ReadFrame() = %q", got)
	}

	var oversized bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], 1025)
	oversized.Write(header[:])
	if _, err := ReadFrame(&oversized, 1024); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("ReadFrame() error = %v", err)
	}
}

func TestFailureResponseDoesNotExposeUnboundedState(t *testing.T) {
	response := FailureResponse("request", CodeExecution, errors.New("script failed"))
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"execution_error"`) || strings.Contains(string(encoded), "script\"") {
		t.Fatalf("response = %s", encoded)
	}
}

func withRequest(request Request, mutate func(*Request)) Request {
	mutate(&request)
	return request
}
