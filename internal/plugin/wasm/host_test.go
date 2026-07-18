package wasm

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/plugin"
)

func TestWASMExtract(t *testing.T) {
	request := plugin.ExtractRequest{ID: "one", URL: "https://fixture.invalid/video"}
	output, _ := json.Marshal(plugin.ExtractResponse{ID: "one", Metadata: map[string]any{"title": "WASM fixture"}})
	response, err := (Host{}).Extract(context.Background(), fixtureModule(plugin.ProtocolVersion, output, false), fixtureConfig(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != "one" || response.Metadata["title"] != "WASM fixture" {
		t.Fatalf("response = %#v", response)
	}
}

func TestWASMVersionPermissionsAndMalformedOutput(t *testing.T) {
	request := plugin.ExtractRequest{ID: "one", URL: "https://fixture.invalid/video"}
	if _, err := (Host{}).Extract(context.Background(), fixtureModule(99, []byte("{\"id\":\"one\"}"), false), fixtureConfig(), request); !errors.Is(err, plugin.ErrIncompatibleVersion) {
		t.Fatalf("version error = %v", err)
	}
	config := fixtureConfig()
	config.Manifest.Permissions = []plugin.Permission{plugin.PermissionNetwork}
	if _, err := (Host{}).Extract(context.Background(), fixtureModule(1, []byte("{\"id\":\"one\"}"), false), config, request); !errors.Is(err, plugin.ErrPermissionDenied) {
		t.Fatalf("permission error = %v", err)
	}
	if _, err := (Host{}).Extract(context.Background(), fixtureModule(1, []byte("{"), false), fixtureConfig(), request); !errors.Is(err, plugin.ErrMalformedMessage) {
		t.Fatalf("malformed error = %v", err)
	}
	if _, err := (Host{}).Extract(context.Background(), []byte("not wasm"), fixtureConfig(), request); !errors.Is(err, plugin.ErrCrashed) {
		t.Fatalf("invalid module error = %v", err)
	}
}

func TestWASMStructuredRemoteError(t *testing.T) {
	request := plugin.ExtractRequest{ID: "one", URL: "https://fixture.invalid/video"}
	output, _ := json.Marshal(plugin.ExtractResponse{ID: "one", Error: &plugin.RemoteError{Category: plugin.RemoteAuthentication, Message: "fixture login required"}})
	response, err := (Host{}).Extract(context.Background(), fixtureModule(1, output, false), fixtureConfig(), request)
	var remote *plugin.RemoteFailure
	if !errors.As(err, &remote) || remote.Detail.Category != plugin.RemoteAuthentication || response.ID != "one" {
		t.Fatalf("response, error = %#v, %v", response, err)
	}
}

func TestWASMTimeoutCancellationAndMemoryLimit(t *testing.T) {
	request := plugin.ExtractRequest{ID: "one", URL: "https://fixture.invalid/video"}
	config := fixtureConfig()
	config.Limits.Timeout = 10 * time.Millisecond
	if _, err := (Host{}).Extract(context.Background(), fixtureModule(1, nil, true), config, request); !errors.Is(err, plugin.ErrTimeout) {
		t.Fatalf("timeout error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Host{}).Extract(ctx, fixtureModule(1, nil, true), fixtureConfig(), request); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	config = fixtureConfig()
	config.Limits.MemoryLimitPages = 1
	request.Options = map[string]any{"padding": string(make([]byte, 40<<10))}
	if _, err := (Host{}).Extract(context.Background(), fixtureModule(1, []byte("{\"id\":\"one\"}"), false), config, request); !errors.Is(err, plugin.ErrResourceLimit) {
		t.Fatalf("memory/output error = %v", err)
	}
}

func FuzzDecodeResponse(f *testing.F) {
	f.Add([]byte("{\"id\":\"one\",\"metadata\":{\"title\":\"fixture\"}}"))
	f.Add([]byte("{"))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = decodeResponse(data) })
}

func fixtureConfig() Config {
	return Config{
		Manifest: plugin.Manifest{Name: "fixture", Versions: []uint32{plugin.ProtocolVersion}},
		Limits:   plugin.Limits{Timeout: time.Second, MaxMessageBytes: 1 << 20, MemoryLimitPages: 2},
	}
}

func fixtureModule(version uint32, output []byte, infinite bool) []byte {
	module := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	module = section(module, 1, []byte{2, 0x60, 0, 1, 0x7f, 0x60, 2, 0x7f, 0x7f, 1, 0x7e})
	module = section(module, 3, []byte{2, 0, 1})
	module = section(module, 5, []byte{1, 1, 1, 2})
	exports := []byte{3}
	exports = appendName(exports, "memory", 2, 0)
	exports = appendName(exports, "ytdlp_protocol_version", 0, 0)
	exports = appendName(exports, "ytdlp_extract", 0, 1)
	module = section(module, 7, exports)
	versionBody := append([]byte{0, 0x41}, signedLEB(int64(version))...)
	versionBody = append(versionBody, 0x0b)
	var extractBody []byte
	if infinite {
		extractBody = []byte{0, 0x03, 0x40, 0x0c, 0x00, 0x0b, 0x42, 0x00, 0x0b}
	} else {
		packed := (uint64(1024) << 32) | uint64(len(output))
		extractBody = append([]byte{0, 0x42}, signedLEB(int64(packed))...)
		extractBody = append(extractBody, 0x0b)
	}
	code := []byte{2}
	code = append(code, unsignedLEB(uint64(len(versionBody)))...)
	code = append(code, versionBody...)
	code = append(code, unsignedLEB(uint64(len(extractBody)))...)
	code = append(code, extractBody...)
	module = section(module, 10, code)
	if output != nil {
		data := []byte{1, 0, 0x41}
		data = append(data, signedLEB(1024)...)
		data = append(data, 0x0b)
		data = append(data, unsignedLEB(uint64(len(output)))...)
		data = append(data, output...)
		module = section(module, 11, data)
	}
	return module
}

func section(module []byte, id byte, payload []byte) []byte {
	module = append(module, id)
	module = append(module, unsignedLEB(uint64(len(payload)))...)
	return append(module, payload...)
}

func appendName(target []byte, name string, kind, index byte) []byte {
	target = append(target, unsignedLEB(uint64(len(name)))...)
	target = append(target, name...)
	return append(target, kind, index)
}

func unsignedLEB(value uint64) []byte {
	var result []byte
	for {
		part := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			part |= 0x80
		}
		result = append(result, part)
		if value == 0 {
			return result
		}
	}
}

func signedLEB(value int64) []byte {
	var result []byte
	for {
		part := byte(value & 0x7f)
		value >>= 7
		done := (value == 0 && part&0x40 == 0) || (value == -1 && part&0x40 != 0)
		if !done {
			part |= 0x80
		}
		result = append(result, part)
		if done {
			return result
		}
	}
}
