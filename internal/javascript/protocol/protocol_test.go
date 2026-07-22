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

func TestTrustedWallTimeAllowance(t *testing.T) {
	base := Request{Version: Version, ID: "walltime", Operation: OperationEvaluate, Script: "1+1"}
	// EJS preprocessing call — the only operation eligible for trusted wall time.
	ejsBase := Request{Version: Version, ID: "walltime-ejs", Operation: OperationCall,
		Script: `function jsc(i){return "{}"}`, Function: "jsc",
		Arguments: []json.RawMessage{json.RawMessage(`{}`)},
	}

	// Untrusted request at HardMaxWallTime should succeed.
	req := withRequest(base, func(r *Request) { r.Limits.WallTimeMS = HardMaxWallTime.Milliseconds() })
	if _, err := req.Normalize(); err != nil {
		t.Fatalf("untrusted request at HardMaxWallTime should succeed: %v", err)
	}

	// Untrusted request above HardMaxWallTime should fail.
	req = withRequest(base, func(r *Request) { r.Limits.WallTimeMS = HardMaxWallTime.Milliseconds() + 1 })
	if _, err := req.Normalize(); err == nil {
		t.Fatal("untrusted request above HardMaxWallTime should fail")
	}

	// Trusted EJS call above HardMaxWallTime but within TrustedMaxWallTime should succeed.
	req = withRequest(ejsBase, func(r *Request) {
		r.Limits.WallTimeMS = TrustedMaxWallTime.Milliseconds()
		r.Limits.Trusted = true
	})
	if _, err := req.Normalize(); err != nil {
		t.Fatalf("trusted EJS call at TrustedMaxWallTime should succeed: %v", err)
	}

	// Trusted EJS call above TrustedMaxWallTime should fail.
	req = withRequest(ejsBase, func(r *Request) {
		r.Limits.WallTimeMS = TrustedMaxWallTime.Milliseconds() + 1
		r.Limits.Trusted = true
	})
	if _, err := req.Normalize(); err == nil {
		t.Fatal("trusted EJS call above TrustedMaxWallTime should fail")
	}

	// Trusted flag on a generic evaluate is stripped (restricted to EJS).
	req = withRequest(base, func(r *Request) {
		r.Limits.WallTimeMS = TrustedMaxWallTime.Milliseconds()
		r.Limits.Trusted = true
	})
	if _, err := req.Normalize(); err == nil {
		t.Fatal("trusted generic evaluate above HardMaxWallTime should fail (Trusted stripped)")
	}

	// Trusted flag is not serialized over the wire (json:"-").
	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "trusted") || strings.Contains(string(encoded), "Trusted") {
		t.Fatalf("Trusted field leaked into JSON: %s", encoded)
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

// FuzzRequestNormalize exercises the request validation boundary with
// arbitrary inputs to ensure no panics or unbounded behavior.
func FuzzRequestNormalize(f *testing.F) {
	f.Add([]byte(`{"version":1,"id":"x","operation":"evaluate","script":"1"}`))
	f.Add([]byte(`{"version":1,"id":"y","operation":"call","script":"function f(){}","function":"f"}`))
	f.Add([]byte(`{"version":2,"id":"z","operation":"evaluate","script":""}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"version":1,"id":"","operation":"evaluate","script":"x"}`))
	f.Add([]byte(`{"version":1,"id":"a","operation":"bad","script":"1"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var request Request
		if err := json.Unmarshal(data, &request); err != nil {
			return // Invalid JSON is not a protocol boundary concern.
		}
		// Normalize must never panic regardless of input.
		normalized, err := request.Normalize()
		if err != nil {
			return
		}
		// If normalization succeeds, invariants must hold.
		if normalized.Version != Version {
			t.Fatal("normalized version mismatch")
		}
		if normalized.Limits.WallTimeMS <= 0 || normalized.Limits.WallTimeMS > HardMaxWallTime.Milliseconds() {
			t.Fatal("normalized wall time out of bounds")
		}
		if normalized.Limits.MemoryBytes <= 0 || normalized.Limits.MemoryBytes > HardMaxMemoryBytes {
			t.Fatal("normalized memory out of bounds")
		}
		if normalized.ScriptHash == "" {
			t.Fatal("normalized script hash is empty")
		}
	})
}

// FuzzFrameRoundTrip exercises the frame encoding boundary.
func FuzzFrameRoundTrip(f *testing.F) {
	f.Add([]byte(`{"version":1}`))
	f.Add([]byte(""))
	f.Add(make([]byte, 1024))
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > MaxFrameBytes {
			return // Skip oversized payloads.
		}
		var buffer bytes.Buffer
		if err := WriteFrame(&buffer, payload); err != nil {
			return
		}
		got, err := ReadFrame(&buffer, MaxFrameBytes)
		if err != nil {
			t.Fatalf("ReadFrame failed after WriteFrame: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatal("frame round-trip mismatch")
		}
	})
}
