// Package wasm implements the experimental sandboxed WebAssembly plugin host.
// It deliberately instantiates neither WASI nor any host imports.
package wasm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/ytdlp-go/ytdlp/internal/plugin"
	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

const inputOffset uint32 = 32 << 10

type Config struct {
	Package             *plugin.Package
	UnsafeTestOnly      bool
	Manifest            plugin.Manifest
	GrantedPermissions  []plugin.Permission
	PreviousPermissions []plugin.Permission
	Approver            plugin.PermissionApprover
	Signer              string
	ModuleDigest        string
	Limits              plugin.Limits
}

type Host struct{}

func (Host) Extract(ctx context.Context, moduleBytes []byte, config Config, request plugin.ExtractRequest) (plugin.ExtractResponse, error) {
	if len(moduleBytes) == 0 || request.ID == "" || request.URL == "" {
		return plugin.ExtractResponse{}, plugin.ErrInvalidConfig
	}
	if err := plugin.CheckPayload(request.Options); err != nil {
		return plugin.ExtractResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return plugin.ExtractResponse{}, err
	}
	manifest := config.Manifest
	signer := config.Signer
	expectedDigest := config.ModuleDigest
	if config.Package != nil {
		verified, err := plugin.RevalidatePackage(*config.Package)
		if err != nil {
			return plugin.ExtractResponse{}, err
		}
		if verified.Manifest.Runtime != "wasm" || !reflect.DeepEqual(config.Manifest, verified.Manifest) {
			return plugin.ExtractResponse{}, fmt.Errorf("%w: trusted WASM manifest mismatch", plugin.ErrInvalidManifest)
		}
		if config.Approver == nil {
			return plugin.ExtractResponse{}, fmt.Errorf("%w: trusted packages require an identity-bound approver", plugin.ErrPermissionReview)
		}
		manifest = verified.Manifest
		signer = verified.Signer
		expectedDigest = verified.ExecutableDigest
	} else if !config.UnsafeTestOnly {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: trusted package required", plugin.ErrUntrustedPath)
	}
	moduleHash := sha256.Sum256(moduleBytes)
	moduleDigest := fmt.Sprintf("%x", moduleHash[:])
	if expectedDigest != "" && expectedDigest != moduleDigest {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: WASM module digest changed", plugin.ErrUntrustedPath)
	}
	if err := config.Limits.Validate(); err != nil {
		return plugin.ExtractResponse{}, err
	}
	limits := config.Limits.WithDefaults()
	if err := plugin.ValidateManifest(manifest); err != nil {
		return plugin.ExtractResponse{}, err
	}
	if manifest.Runtime != "wasm" || !plugin.HasCapability(manifest, plugin.CapabilityExtractor) {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: WASM extractor capability required", plugin.ErrInvalidManifest)
	}
	version, err := plugin.NegotiateRange(plugin.VersionRange{Minimum: plugin.ProtocolV1_0, Maximum: plugin.ProtocolV1_1}, plugin.ManifestRange(manifest))
	if err != nil {
		return plugin.ExtractResponse{}, err
	}
	if err := plugin.Approve(ctx, config.Approver, plugin.ApprovalRequest{
		PluginID: manifest.ID, Release: manifest.Release,
		Signer: signer, ExecutableDigest: moduleDigest, ABI: version,
		Requested: manifest.Permissions,
		Added:     plugin.AddedPermissions(config.PreviousPermissions, manifest.Permissions),
	}, config.GrantedPermissions); err != nil {
		return plugin.ExtractResponse{}, err
	}
	input, err := json.Marshal(request)
	if err != nil {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: input: %v", plugin.ErrMalformedMessage, err)
	}
	if uint64(len(input)) > uint64(limits.MaxMessageBytes) {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: input is %d bytes", plugin.ErrResourceLimit, len(input))
	}

	operationCtx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	runtimeConfig := wazero.NewRuntimeConfig().WithCloseOnContextDone(true).WithMemoryLimitPages(limits.MemoryLimitPages)
	runtime := wazero.NewRuntimeWithConfig(operationCtx, runtimeConfig)
	defer runtime.Close(context.Background())
	compiled, err := runtime.CompileModule(operationCtx, moduleBytes)
	if err != nil {
		return plugin.ExtractResponse{}, classifyRuntimeError(ctx, operationCtx, err)
	}
	if len(compiled.ImportedFunctions()) != 0 || len(compiled.ImportedMemories()) != 0 {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: ambient host imports are disabled", plugin.ErrPermissionDenied)
	}
	module, err := runtime.InstantiateModule(operationCtx, compiled, wazero.NewModuleConfig().WithStartFunctions())
	if err != nil {
		return plugin.ExtractResponse{}, classifyRuntimeError(ctx, operationCtx, err)
	}

	versionFunction := module.ExportedFunction("ytdlp_protocol_version")
	extractFunction := module.ExportedFunction("ytdlp_extract")
	memory := module.Memory()
	if versionFunction == nil || extractFunction == nil || memory == nil {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: missing required ABI export", plugin.ErrMalformedMessage)
	}
	versions, err := versionFunction.Call(operationCtx)
	if err != nil {
		return plugin.ExtractResponse{}, classifyRuntimeError(ctx, operationCtx, err)
	}
	if len(versions) != 1 || uint32(versions[0]) != version {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: module=%v", plugin.ErrIncompatibleVersion, versions)
	}
	if !memory.Write(inputOffset, input) {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: guest memory cannot hold input", plugin.ErrResourceLimit)
	}
	results, err := extractFunction.Call(operationCtx, uint64(inputOffset), uint64(len(input)))
	if err != nil {
		return plugin.ExtractResponse{}, classifyRuntimeError(ctx, operationCtx, err)
	}
	if len(results) != 1 {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: extract result arity", plugin.ErrMalformedMessage)
	}
	resultPointer := uint32(results[0] >> 32)
	resultLength := uint32(results[0])
	if resultLength == 0 || resultLength > limits.MaxMessageBytes {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: output declares %d bytes", plugin.ErrResourceLimit, resultLength)
	}
	output, ok := memory.Read(resultPointer, resultLength)
	if !ok {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: output outside guest memory", plugin.ErrMalformedMessage)
	}
	response, err := decodeResponse(output)
	if err != nil {
		return plugin.ExtractResponse{}, err
	}
	if response.ID != request.ID {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: mismatched response id", plugin.ErrMalformedMessage)
	}
	if err := plugin.CheckPayload(response.Metadata); err != nil {
		return plugin.ExtractResponse{}, err
	}
	if err := plugin.ResponseError(response.Error); err != nil {
		return response, err
	}
	return response, nil
}

func decodeResponse(output []byte) (plugin.ExtractResponse, error) {
	if err := pluginapi.ValidateJSONFrame(output); err != nil {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: output: %v", plugin.ErrMalformedMessage, err)
	}
	var response plugin.ExtractResponse
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: output: %v", plugin.ErrMalformedMessage, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: trailing output", plugin.ErrMalformedMessage)
	}
	return response, nil
}

func classifyRuntimeError(parent, operation context.Context, err error) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if errors.Is(operation.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", plugin.ErrTimeout, operation.Err())
	}
	if strings.Contains(err.Error(), "memory") && (strings.Contains(err.Error(), "limit") || strings.Contains(err.Error(), "maximum")) {
		return fmt.Errorf("%w: memory", plugin.ErrResourceLimit)
	}
	return fmt.Errorf("%w: WebAssembly execution: %v", plugin.ErrCrashed, err)
}
