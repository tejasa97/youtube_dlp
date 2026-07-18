package wasm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/plugin"
)

type wasmApprover struct {
	request plugin.ApprovalRequest
}

func (approver *wasmApprover) Approve(_ context.Context, request plugin.ApprovalRequest) (plugin.Approval, error) {
	approver.request = request
	return plugin.Approval{Granted: append([]plugin.Permission(nil), request.Requested...)}, nil
}

func TestWASMExtract(t *testing.T) {
	request := plugin.ExtractRequest{ID: "one", URL: "https://fixture.invalid/video"}
	output, _ := json.Marshal(plugin.ExtractResponse{ID: "one", Metadata: map[string]any{"title": "WASM fixture"}})
	response, err := (Host{}).Extract(context.Background(), fixtureModule(plugin.ProtocolV1_0, output, false), fixtureConfig(), request)
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
	duplicate := []byte(`{"id":"one","metadata":{"title":"one","title":"two"}}`)
	if _, err := (Host{}).Extract(context.Background(), fixtureModule(1, duplicate, false), fixtureConfig(), request); !errors.Is(err, plugin.ErrMalformedMessage) {
		t.Fatalf("duplicate output error = %v", err)
	}
}

func TestWASMRequiresTrustedPackageOrExplicitTestModeAndVerifiesDigest(t *testing.T) {
	request := plugin.ExtractRequest{ID: "one", URL: "https://fixture.invalid/video"}
	output, _ := json.Marshal(plugin.ExtractResponse{ID: "one"})
	module := fixtureModule(plugin.ProtocolV1_0, output, false)
	config := fixtureConfig()
	config.UnsafeTestOnly = false
	if _, err := (Host{}).Extract(context.Background(), module, config, request); !errors.Is(err, plugin.ErrUntrustedPath) {
		t.Fatalf("untrusted module error = %v", err)
	}
	config.UnsafeTestOnly = true
	digest := sha256.Sum256([]byte("different module"))
	config.ModuleDigest = fmt.Sprintf("%x", digest[:])
	if _, err := (Host{}).Extract(context.Background(), module, config, request); !errors.Is(err, plugin.ErrUntrustedPath) {
		t.Fatalf("digest mismatch error = %v", err)
	}
}

func TestWASMTrustedPackageApprovalAndExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure discovery fails closed until Windows ACL ownership verification is available")
	}
	request := plugin.ExtractRequest{ID: "one", URL: "https://fixture.invalid/video"}
	output, _ := json.Marshal(plugin.ExtractResponse{ID: "one", Metadata: map[string]any{"title": "trusted WASM"}})
	module := fixtureModule(plugin.ProtocolV1_0, output, false)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "fixture")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := fixtureConfig().Manifest
	manifest.Entrypoint = "fixture.wasm"
	manifest.Permissions = []plugin.Permission{plugin.PermissionNetwork}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "plugin.json"), manifestBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "fixture.wasm"), module, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := plugin.LoadPackage(root, directory, 0)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Signer = "fixture-wasm-signer"
	approver := &wasmApprover{}
	config := fixtureConfig()
	config.Package = &loaded
	config.UnsafeTestOnly = false
	config.Manifest = manifest
	config.Approver = approver
	response, err := (Host{}).Extract(context.Background(), module, config, request)
	if err != nil || response.Metadata["title"] != "trusted WASM" {
		t.Fatalf("response, error = %#v, %v", response, err)
	}
	if approver.request.Signer != loaded.Signer || approver.request.ExecutableDigest != loaded.ExecutableDigest ||
		approver.request.ABI != plugin.ProtocolV1_0 || len(approver.request.Requested) != 1 {
		t.Fatalf("approval request = %#v", approver.request)
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
		UnsafeTestOnly: true,
		Manifest: plugin.Manifest{
			Schema: plugin.ManifestSchema, ID: "fixture.wasm", Name: "WASM fixture", Release: "1.0.0",
			Runtime: "wasm", Entrypoint: "fixture.wasm",
			ABIRange:     plugin.VersionRange{Minimum: plugin.ProtocolV1_0, Maximum: plugin.ProtocolV1_0},
			Capabilities: []plugin.Capability{plugin.CapabilityExtractor},
		},
		Limits: plugin.Limits{Timeout: time.Second, MaxMessageBytes: 1 << 20, MemoryLimitPages: 2},
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
