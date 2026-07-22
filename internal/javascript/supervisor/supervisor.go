// Package supervisor manages the isolated JavaScript helper process.
package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/javascript/protocol"
)

const DefaultStderrBytes = 64 << 10

type Config struct {
	Path           string
	MemoryBytes    int64
	MaxStderrBytes int
}

// Client serializes requests through one helper so compiled-program caching is
// retained. Faults and cancellation discard the process before the next call.
type Client struct {
	config Config
	gate   chan struct{}

	command  *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	waitDone chan error
	stderr   *boundedBuffer
}

func New(config Config) (*Client, error) {
	path, err := resolveHelper(config.Path)
	if err != nil {
		return nil, err
	}
	config.Path = path
	if config.MemoryBytes == 0 {
		config.MemoryBytes = protocol.DefaultMemoryBytes
	}
	if config.MemoryBytes <= 0 || config.MemoryBytes > protocol.HardMaxMemoryBytes {
		return nil, fmt.Errorf("helper memory_bytes must be between 1 and %d", protocol.HardMaxMemoryBytes)
	}
	if config.MaxStderrBytes == 0 {
		config.MaxStderrBytes = DefaultStderrBytes
	}
	if config.MaxStderrBytes < 0 || config.MaxStderrBytes > protocol.MaxFrameBytes {
		return nil, errors.New("invalid helper stderr limit")
	}
	client := &Client{config: config, gate: make(chan struct{}, 1)}
	client.gate <- struct{}{}
	return client, nil
}

// resolveHelper deliberately does not consult PATH. A helper is native code,
// so implicit executable search would turn an extractor challenge into code
// execution from any writable search-path directory. An explicit path must be
// absolute; the only implicit location is beside the running application.
func resolveHelper(configured string) (string, error) {
	path := configured
	if path == "" {
		name := "ytdlp-js-helper"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		executable, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("locate JavaScript helper: %w", err)
		}
		path = filepath.Join(filepath.Dir(executable), name)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("JavaScript helper path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect JavaScript helper: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("JavaScript helper must be a regular non-symlink file")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("JavaScript helper path cannot be canonicalized")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("JavaScript helper is not executable")
	}
	return canonical, nil
}

func (client *Client) Execute(ctx context.Context, request protocol.Request) protocol.Response {
	// Strip any caller-provided TrustedWallTimeMS at the supervisor
	// boundary. Callers must not forge the serialized grant.
	callerTrusted := request.Limits.Trusted
	request.Limits.TrustedWallTimeMS = 0

	normalized, err := request.Normalize()
	if err != nil {
		code := protocol.CodeInvalidRequest
		if request.Version != protocol.Version {
			code = protocol.CodeIncompatibleVersion
		}
		return protocol.FailureResponse(request.ID, code, err)
	}
	// Mint the serialized trusted wall-time grant only for approved EJS
	// preprocessing calls (operation=call, function="jsc", Trusted=true).
	// Generic evaluate/call requests cannot obtain more than 30 s.
	if callerTrusted && normalized.Operation == protocol.OperationCall && normalized.Function == "jsc" {
		normalized.Limits.TrustedWallTimeMS = protocol.TrustedMaxWallTime.Milliseconds()
	}
	if normalized.Limits.MemoryBytes > client.config.MemoryBytes {
		return protocol.FailureResponse(normalized.ID, protocol.CodeMemoryLimit, errors.New("request memory budget exceeds helper process limit"))
	}
	select {
	case <-ctx.Done():
		return contextFailure(normalized.ID, ctx.Err())
	case <-client.gate:
	}
	defer func() { client.gate <- struct{}{} }()

	if client.command == nil {
		if err := client.startLocked(); err != nil {
			return protocol.FailureResponse(normalized.ID, protocol.CodeHelperCrash, errors.New("JavaScript helper failed to start"))
		}
	}
	type result struct {
		response protocol.Response
		err      error
	}
	completed := make(chan result, 1)
	stdin, stdout := client.stdin, client.stdout
	go func() {
		response, err := roundTrip(stdin, stdout, normalized)
		completed <- result{response: response, err: err}
	}()

	select {
	case <-ctx.Done():
		client.stopLocked()
		<-completed
		return contextFailure(normalized.ID, ctx.Err())
	case outcome := <-completed:
		if outcome.err != nil {
			client.stopLocked()
			return protocol.FailureResponse(normalized.ID, classifyRoundTrip(outcome.err), errors.New("JavaScript helper communication failed"))
		}
		return outcome.response
	}
}

func (client *Client) Close() error {
	<-client.gate
	defer func() { client.gate <- struct{}{} }()
	client.stopLocked()
	return nil
}

func (client *Client) startLocked() error {
	command := exec.Command(client.config.Path)
	configureProcess(command)
	command.Env = helperEnvironment(client.config.MemoryBytes)
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		stdin.Close()
		return err
	}
	stderr := &boundedBuffer{maximum: client.config.MaxStderrBytes}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return err
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	client.command, client.stdin, client.stdout = command, stdin, stdout
	client.waitDone, client.stderr = waitDone, stderr
	return nil
}

func roundTrip(stdin io.Writer, stdout io.Reader, request protocol.Request) (protocol.Response, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return protocol.Response{}, fmt.Errorf("encode request: %w", err)
	}
	if err := protocol.WriteFrame(stdin, payload); err != nil {
		return protocol.Response{}, err
	}
	responsePayload, err := protocol.ReadFrame(stdout, protocol.MaxFrameBytes)
	if err != nil {
		return protocol.Response{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(responsePayload))
	decoder.DisallowUnknownFields()
	var response protocol.Response
	if err := decoder.Decode(&response); err != nil {
		return protocol.Response{}, fmt.Errorf("decode response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return protocol.Response{}, errors.New("response contains trailing JSON")
	}
	if response.Version != protocol.Version || response.ID != request.ID {
		return protocol.Response{}, errors.New("response version or id mismatch")
	}
	if (response.Error == nil) == (response.Result == nil) {
		return protocol.Response{}, errors.New("response must contain exactly one result or error")
	}
	if response.Error != nil && !validErrorCode(response.Error.Code) {
		return protocol.Response{}, errors.New("response contains unknown error code")
	}
	return response, nil
}

func (client *Client) stopLocked() {
	if client.command == nil {
		return
	}
	_ = killProcess(client.command)
	_ = client.stdin.Close()
	_ = client.stdout.Close()
	<-client.waitDone
	client.command, client.stdin, client.stdout = nil, nil, nil
	client.waitDone, client.stderr = nil, nil
}

func contextFailure(id string, err error) protocol.Response {
	if errors.Is(err, context.DeadlineExceeded) {
		return protocol.FailureResponse(id, protocol.CodeTimeout, errors.New("JavaScript helper deadline exceeded"))
	}
	return protocol.FailureResponse(id, protocol.CodeCanceled, errors.New("JavaScript helper canceled"))
}

func classifyRoundTrip(err error) protocol.ErrorCode {
	if errors.Is(err, protocol.ErrFrameTooLarge) || strings.Contains(err.Error(), "decode response") || strings.Contains(err.Error(), "response ") {
		return protocol.CodeProtocol
	}
	return protocol.CodeHelperCrash
}

func helperEnvironment(memoryBytes int64) []string {
	environment := []string{
		"GOMEMLIMIT=" + strconv.FormatInt(memoryBytes, 10) + "B",
		"YTDLP_JS_MEMORY_BYTES=" + strconv.FormatInt(memoryBytes, 10),
	}
	if runtime.GOOS == "windows" {
		for _, key := range []string{"SYSTEMROOT", "WINDIR"} {
			if value := os.Getenv(key); value != "" {
				environment = append(environment, key+"="+value)
			}
		}
	}
	return environment
}

func validErrorCode(code protocol.ErrorCode) bool {
	switch code {
	case protocol.CodeInvalidRequest, protocol.CodeIncompatibleVersion, protocol.CodeSyntax,
		protocol.CodeExecution, protocol.CodeFunctionMissing, protocol.CodeTimeout,
		protocol.CodeCanceled, protocol.CodeInputLimit, protocol.CodeOutputLimit,
		protocol.CodeMemoryLimit, protocol.CodeUnsupportedModule, protocol.CodeHelperCrash,
		protocol.CodeProtocol:
		return true
	default:
		return false
	}
}

type boundedBuffer struct {
	maximum int
	buffer  []byte
}

func (buffer *boundedBuffer) Write(payload []byte) (int, error) {
	remaining := buffer.maximum - len(buffer.buffer)
	if remaining > 0 {
		if len(payload) < remaining {
			remaining = len(payload)
		}
		buffer.buffer = append(buffer.buffer, payload[:remaining]...)
	}
	return len(payload), nil
}
