// Package rpc implements the stable length-prefixed stdio Plugin ABI v1.
package rpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/plugin"
	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

type Config struct {
	// Package is the preferred launch source. It must have been returned by
	// plugin.LoadPackage or plugin.Discover from an explicit trusted root.
	Package *plugin.Package
	// Executable is retained solely for deterministic host tests. Production
	// integrations must use Package.
	Executable          string
	UnsafeTestOnly      bool
	Args                []string
	Environment         map[string]string
	GrantedPermissions  []plugin.Permission
	PreviousPermissions []plugin.Permission
	Approver            plugin.PermissionApprover
	SupportedABI        plugin.VersionRange
	Limits              plugin.Limits
}

type Client struct{}

func (Client) Extract(ctx context.Context, config Config, request plugin.ExtractRequest) (plugin.ExtractResponse, error) {
	if request.ID == "" || request.URL == "" {
		return plugin.ExtractResponse{}, plugin.ErrInvalidConfig
	}
	if err := plugin.CheckPayload(request.Options); err != nil {
		return plugin.ExtractResponse{}, err
	}
	response, err := exchange(ctx, config, envelope{Type: "extract", Request: &request}, "result", request.ID, plugin.CapabilityExtractor)
	if err != nil {
		return plugin.ExtractResponse{}, err
	}
	if response.Response == nil || response.Response.ID != request.ID {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: mismatched extractor result", plugin.ErrMalformedMessage)
	}
	if err := plugin.CheckPayload(response.Response.Metadata); err != nil {
		return plugin.ExtractResponse{}, err
	}
	if err := plugin.ResponseError(response.Response.Error); err != nil {
		return *response.Response, err
	}
	return *response.Response, nil
}

func (Client) Postprocess(ctx context.Context, config Config, request plugin.PostprocessRequest) (plugin.PostprocessResponse, error) {
	if request.ID == "" || request.Operation == "" || request.Input.Handle == "" {
		return plugin.PostprocessResponse{}, plugin.ErrInvalidConfig
	}
	if err := plugin.CheckPayload(request.Options); err != nil {
		return plugin.PostprocessResponse{}, err
	}
	if err := plugin.CheckPayload(request.Input); err != nil {
		return plugin.PostprocessResponse{}, err
	}
	response, err := exchange(ctx, config, envelope{Type: "postprocess", PostprocessRequest: &request}, "postprocess_result", request.ID, plugin.CapabilityPostprocessor)
	if err != nil {
		return plugin.PostprocessResponse{}, err
	}
	if response.PostprocessResponse == nil || response.PostprocessResponse.ID != request.ID {
		return plugin.PostprocessResponse{}, fmt.Errorf("%w: mismatched postprocessor result", plugin.ErrMalformedMessage)
	}
	if err := plugin.CheckPayload(response.PostprocessResponse.Artifacts); err != nil {
		return plugin.PostprocessResponse{}, err
	}
	if err := plugin.ResponseError(response.PostprocessResponse.Error); err != nil {
		return *response.PostprocessResponse, err
	}
	return *response.PostprocessResponse, nil
}

func (Client) Provide(ctx context.Context, config Config, request plugin.ProviderRequest) (plugin.ProviderResponse, error) {
	if request.ID == "" || request.Provider == "" || request.Action == "" {
		return plugin.ProviderResponse{}, plugin.ErrInvalidConfig
	}
	if err := plugin.CheckPayload(request.Arguments); err != nil {
		return plugin.ProviderResponse{}, err
	}
	if err := plugin.CheckPayload(request.Secrets); err != nil {
		return plugin.ProviderResponse{}, err
	}
	response, err := exchange(ctx, config, envelope{Type: "provide", ProviderRequest: &request}, "provider_result", request.ID, plugin.CapabilityProvider)
	if err != nil {
		return plugin.ProviderResponse{}, err
	}
	if response.ProviderResponse == nil || response.ProviderResponse.ID != request.ID {
		return plugin.ProviderResponse{}, fmt.Errorf("%w: mismatched provider result", plugin.ErrMalformedMessage)
	}
	if err := plugin.CheckPayload(response.ProviderResponse.Values); err != nil {
		return plugin.ProviderResponse{}, err
	}
	if err := plugin.ResponseError(response.ProviderResponse.Error); err != nil {
		return *response.ProviderResponse, err
	}
	return *response.ProviderResponse, nil
}

func exchange(ctx context.Context, config Config, request envelope, resultType, requestID string, capability plugin.Capability) (envelope, error) {
	if err := ctx.Err(); err != nil {
		return envelope{}, err
	}
	if err := config.Limits.Validate(); err != nil {
		return envelope{}, err
	}
	hostRange := config.SupportedABI
	if hostRange.Minimum == 0 && hostRange.Maximum == 0 {
		hostRange = plugin.VersionRange{Minimum: plugin.ProtocolV1_0, Maximum: plugin.ProtocolV1_1}
	}
	if _, err := plugin.NegotiateRange(hostRange, hostRange); err != nil {
		return envelope{}, err
	}
	executable, expected, err := launchTarget(config)
	if err != nil {
		return envelope{}, err
	}
	environment, err := sanitizedEnvironment(config.Environment)
	if err != nil {
		return envelope{}, err
	}
	if err := validateArguments(config.Args); err != nil {
		return envelope{}, err
	}
	limits := config.Limits.WithDefaults()
	operationCtx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	var preapprovedVersion uint32
	if expected != nil {
		if config.Approver == nil {
			return envelope{}, fmt.Errorf("%w: trusted packages require an identity-bound approver", plugin.ErrPermissionReview)
		}
		if !plugin.HasCapability(expected.Manifest, capability) {
			return envelope{}, fmt.Errorf("%w: capability %q not declared", plugin.ErrPermissionDenied, capability)
		}
		preapprovedVersion, err = plugin.NegotiateRange(hostRange, plugin.ManifestRange(expected.Manifest))
		if err != nil {
			return envelope{}, err
		}
		if err := plugin.Approve(operationCtx, config.Approver, plugin.ApprovalRequest{
			PluginID: expected.Manifest.ID, Release: expected.Manifest.Release,
			Signer: expected.Signer, ExecutableDigest: expected.ExecutableDigest, ABI: preapprovedVersion,
			Requested: append([]plugin.Permission(nil), expected.Manifest.Permissions...),
			Added:     plugin.AddedPermissions(config.PreviousPermissions, expected.Manifest.Permissions),
		}, nil); err != nil {
			return envelope{}, err
		}
	}

	command := exec.Command(executable, config.Args...)
	command.Env = environment
	if expected != nil {
		command.Dir = expected.Directory
	}
	if err := configureIsolation(command); err != nil {
		return envelope{}, err
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return envelope{}, fmt.Errorf("%w: stdin: %v", plugin.ErrCrashed, err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return envelope{}, fmt.Errorf("%w: stdout: %v", plugin.ErrCrashed, err)
	}
	stderr := &boundedBuffer{maximum: limits.MaxStderrBytes}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return envelope{}, fmt.Errorf("%w: start: %v", plugin.ErrCrashed, plugin.RedactDiagnostic(err.Error()))
	}
	isolation, err := attachIsolation(command)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return envelope{}, err
	}

	var writeMu sync.Mutex
	send := func(value envelope) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeFrame(stdin, value, limits.MaxMessageBytes)
	}

	type result struct {
		response envelope
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		if err := send(envelope{Type: "hello", Versions: []uint32{plugin.ProtocolV1_1, plugin.ProtocolV1_0}, ABIRange: &hostRange}); err != nil {
			resultCh <- result{err: err}
			return
		}
		var hello envelope
		if err := readFrame(stdout, limits.MaxMessageBytes, &hello); err != nil {
			resultCh <- result{err: err}
			return
		}
		if err := validatePluginHello(hello); err != nil {
			resultCh <- result{err: err}
			return
		}
		if err := plugin.ValidateManifest(*hello.Manifest); err != nil {
			resultCh <- result{err: err}
			return
		}
		if expected != nil && !sameManifest(expected.Manifest, *hello.Manifest) {
			resultCh <- result{err: fmt.Errorf("%w: runtime manifest differs from trusted package", plugin.ErrInvalidManifest)}
			return
		}
		if !plugin.HasCapability(*hello.Manifest, capability) {
			resultCh <- result{err: fmt.Errorf("%w: capability %q not declared", plugin.ErrPermissionDenied, capability)}
			return
		}
		version, err := plugin.NegotiateRange(hostRange, plugin.ManifestRange(*hello.Manifest))
		if err != nil {
			resultCh <- result{err: err}
			return
		}
		if expected != nil {
			if version != preapprovedVersion {
				resultCh <- result{err: fmt.Errorf("%w: live ABI differs from pre-launch approval", plugin.ErrInvalidManifest)}
				return
			}
		} else {
			approval := plugin.ApprovalRequest{
				PluginID: hello.Manifest.ID, Release: hello.Manifest.Release, ABI: version,
				Requested: append([]plugin.Permission(nil), hello.Manifest.Permissions...),
				Added:     plugin.AddedPermissions(config.PreviousPermissions, hello.Manifest.Permissions),
			}
			if err := plugin.Approve(operationCtx, config.Approver, approval, config.GrantedPermissions); err != nil {
				resultCh <- result{err: err}
				return
			}
		}
		request.Version = version
		if err := send(request); err != nil {
			resultCh <- result{err: err}
			return
		}
		var response envelope
		if err := readFrame(stdout, limits.MaxMessageBytes, &response); err != nil {
			resultCh <- result{err: err}
			return
		}
		if err := validateResultEnvelope(response, resultType); err != nil {
			resultCh <- result{err: err}
			return
		}
		if responseID(response) != requestID {
			resultCh <- result{err: fmt.Errorf("%w: mismatched result", plugin.ErrMalformedMessage)}
			return
		}
		resultCh <- result{response: response}
	}()

	cleanup := func(force bool) error {
		_ = stdin.Close()
		wait := make(chan error, 1)
		go func() { wait <- command.Wait() }()
		var cleanupErr error
		if force {
			if err := isolation.Terminate(); err != nil {
				cleanupErr = fmt.Errorf("%w: terminate: %v", plugin.ErrIsolationUnavailable, err)
				// Reap the direct child even when process-tree termination failed.
				_ = command.Process.Kill()
			}
		}
		select {
		case waitErr := <-wait:
			_ = isolation.Close()
			if cleanupErr != nil {
				return cleanupErr
			}
			if waitErr != nil && !force {
				return fmt.Errorf("%w: non-zero exit", plugin.ErrCrashed)
			}
			return nil
		case <-time.After(limits.CancelGrace):
			if err := isolation.Terminate(); err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("%w: terminate: %v", plugin.ErrIsolationUnavailable, err))
				_ = command.Process.Kill()
			}
		}
		select {
		case <-wait:
			_ = isolation.Close()
			if cleanupErr != nil {
				return cleanupErr
			}
			if !force {
				return fmt.Errorf("%w: plugin did not exit after response", plugin.ErrCrashed)
			}
			return nil
		case <-time.After(limits.CancelGrace):
			_ = isolation.Close()
			return fmt.Errorf("%w: process tree did not terminate", plugin.ErrIsolationUnavailable)
		}
	}

	select {
	case outcome := <-resultCh:
		cleanupErr := cleanup(outcome.err != nil)
		if outcome.err != nil {
			if errors.Is(outcome.err, io.EOF) || errors.Is(outcome.err, io.ErrUnexpectedEOF) {
				outcome.err = fmt.Errorf("%w: unexpected exit", plugin.ErrCrashed)
			}
			return envelope{}, errors.Join(outcome.err, cleanupErr)
		}
		if cleanupErr != nil {
			return envelope{}, cleanupErr
		}
		return outcome.response, nil
	case <-operationCtx.Done():
		_ = send(envelope{Type: "cancel", RequestID: requestID})
		cleanupErr := cleanup(false)
		if errors.Is(operationCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return envelope{}, errors.Join(fmt.Errorf("%w: %v", plugin.ErrTimeout, operationCtx.Err()), cleanupErr)
		}
		return envelope{}, errors.Join(operationCtx.Err(), cleanupErr)
	}
}

func launchTarget(config Config) (string, *plugin.Package, error) {
	if config.Package != nil {
		verified, err := plugin.RevalidatePackage(*config.Package)
		if err != nil {
			return "", nil, err
		}
		if verified.Manifest.Runtime != pluginapi.RuntimeNative || verified.EntrypointPath == "" || !filepath.IsAbs(verified.EntrypointPath) {
			return "", nil, fmt.Errorf("%w: invalid native package", plugin.ErrInvalidConfig)
		}
		if err := rejectPythonExecutable(verified.EntrypointPath); err != nil {
			return "", nil, err
		}
		return verified.EntrypointPath, &verified, nil
	}
	if !config.UnsafeTestOnly || config.Executable == "" || !filepath.IsAbs(config.Executable) {
		return "", nil, fmt.Errorf("%w: trusted package required", plugin.ErrUntrustedPath)
	}
	if err := rejectPythonExecutable(config.Executable); err != nil {
		return "", nil, err
	}
	if err := rejectInterpreterExecutable(config.Executable); err != nil {
		return "", nil, err
	}
	return config.Executable, nil, nil
}

func rejectPythonExecutable(executable string) error {
	base := strings.ToLower(filepath.Base(executable))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if strings.HasPrefix(base, "python") || strings.HasPrefix(base, "pypy") || base == "uv" {
		return plugin.ErrPythonRuntime
	}
	return nil
}

func rejectInterpreterExecutable(executable string) error {
	file, err := os.Open(executable)
	if err != nil {
		return fmt.Errorf("%w: inspect executable: %v", plugin.ErrUntrustedPath, err)
	}
	defer file.Close()
	header := make([]byte, 256)
	read, err := file.Read(header)
	if err != nil && err != io.EOF {
		return fmt.Errorf("%w: inspect executable: %v", plugin.ErrUntrustedPath, err)
	}
	if bytes.HasPrefix(header[:read], []byte("#!")) {
		lower := strings.ToLower(string(header[:read]))
		if strings.Contains(lower, "python") || strings.Contains(lower, "pypy") {
			return plugin.ErrPythonRuntime
		}
		return fmt.Errorf("%w: interpreter/shebang trampolines are prohibited", plugin.ErrUntrustedPath)
	}
	return nil
}

var allowedEnvironment = map[string]struct{}{
	"LANG": {}, "LC_ALL": {}, "TZ": {}, "TMPDIR": {}, "TMP": {}, "TEMP": {},
	"SYSTEMROOT": {}, "WINDIR": {},
}

func sanitizedEnvironment(input map[string]string) ([]string, error) {
	keys := make([]string, 0, len(input))
	values := make(map[string]string, len(input))
	for key, value := range input {
		upper := strings.ToUpper(key)
		if _, ok := allowedEnvironment[upper]; !ok || key == "" || strings.ContainsAny(key+value, "\x00\r\n") {
			return nil, fmt.Errorf("%w: environment key %q", plugin.ErrInvalidConfig, key)
		}
		if strings.Contains(strings.ToLower(key), "secret") || strings.Contains(strings.ToLower(key), "token") ||
			strings.Contains(strings.ToLower(key), "password") || plugin.RedactDiagnostic(value) != value {
			return nil, plugin.ErrSecretExposure
		}
		if _, duplicate := values[upper]; duplicate {
			return nil, fmt.Errorf("%w: duplicate environment key %q", plugin.ErrInvalidConfig, key)
		}
		values[upper] = value
		keys = append(keys, upper)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result, nil
}

func validateArguments(arguments []string) error {
	if len(arguments) > 128 {
		return fmt.Errorf("%w: too many plugin arguments", plugin.ErrResourceLimit)
	}
	for _, argument := range arguments {
		if len(argument) > 16<<10 || strings.ContainsRune(argument, '\x00') {
			return fmt.Errorf("%w: invalid plugin argument", plugin.ErrInvalidConfig)
		}
		if plugin.RedactDiagnostic(argument) != argument {
			return plugin.ErrSecretExposure
		}
	}
	return nil
}

func sameManifest(left, right plugin.Manifest) bool {
	return reflect.DeepEqual(left, right)
}

func responseID(value envelope) string {
	switch {
	case value.Response != nil:
		return value.Response.ID
	case value.PostprocessResponse != nil:
		return value.PostprocessResponse.ID
	case value.ProviderResponse != nil:
		return value.ProviderResponse.ID
	default:
		return ""
	}
}

type boundedBuffer struct {
	buffer  bytes.Buffer
	maximum int
	mu      sync.Mutex
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	written := len(data)
	remaining := buffer.maximum - buffer.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = buffer.buffer.Write(data)
	}
	return written, nil
}

func (buffer *boundedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}
