// Package rpc implements the experimental length-prefixed stdio plugin protocol.
package rpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/plugin"
)

type Config struct {
	Executable         string
	Args               []string
	GrantedPermissions []plugin.Permission
	Limits             plugin.Limits
}

type Client struct{}

func (Client) Extract(ctx context.Context, config Config, request plugin.ExtractRequest) (plugin.ExtractResponse, error) {
	if config.Executable == "" || request.ID == "" || request.URL == "" {
		return plugin.ExtractResponse{}, plugin.ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return plugin.ExtractResponse{}, err
	}
	limits := config.Limits.WithDefaults()
	operationCtx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()

	command := exec.Command(config.Executable, config.Args...)
	stdin, err := command.StdinPipe()
	if err != nil {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: stdin: %v", plugin.ErrCrashed, err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: stdout: %v", plugin.ErrCrashed, err)
	}
	stderr := &boundedBuffer{maximum: limits.MaxStderrBytes}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return plugin.ExtractResponse{}, fmt.Errorf("%w: start: %v", plugin.ErrCrashed, err)
	}

	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	var writeMu sync.Mutex
	send := func(value envelope) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return writeFrame(stdin, value, limits.MaxMessageBytes)
	}

	type result struct {
		response plugin.ExtractResponse
		err      error
	}
	resultCh := make(chan result, 1)
	go func() {
		if err := send(envelope{Type: "hello", Versions: []uint32{plugin.ProtocolVersion}}); err != nil {
			resultCh <- result{err: err}
			return
		}
		var hello envelope
		if err := readFrame(stdout, limits.MaxMessageBytes, &hello); err != nil {
			resultCh <- result{err: err}
			return
		}
		if hello.Type != "hello" || hello.Manifest == nil {
			resultCh <- result{err: fmt.Errorf("%w: expected hello", plugin.ErrMalformedMessage)}
			return
		}
		version, err := plugin.Negotiate([]uint32{plugin.ProtocolVersion}, hello.Manifest.Versions)
		if err != nil {
			resultCh <- result{err: err}
			return
		}
		if err := plugin.CheckPermissions(hello.Manifest.Permissions, config.GrantedPermissions); err != nil {
			resultCh <- result{err: err}
			return
		}
		if err := send(envelope{Type: "extract", Version: version, Request: &request}); err != nil {
			resultCh <- result{err: err}
			return
		}
		var response envelope
		if err := readFrame(stdout, limits.MaxMessageBytes, &response); err != nil {
			resultCh <- result{err: err}
			return
		}
		if response.Type != "result" || response.Response == nil || response.Response.ID != request.ID {
			resultCh <- result{err: fmt.Errorf("%w: mismatched result", plugin.ErrMalformedMessage)}
			return
		}
		resultCh <- result{response: *response.Response}
	}()

	cleanup := func() {
		_ = stdin.Close()
		select {
		case <-wait:
		case <-time.After(limits.CancelGrace):
			_ = command.Process.Kill()
			<-wait
		}
	}
	defer cleanup()

	select {
	case outcome := <-resultCh:
		if outcome.err != nil {
			if errors.Is(outcome.err, io.EOF) || errors.Is(outcome.err, io.ErrUnexpectedEOF) {
				return plugin.ExtractResponse{}, fmt.Errorf("%w: unexpected exit", plugin.ErrCrashed)
			}
			return plugin.ExtractResponse{}, outcome.err
		}
		if err := plugin.ResponseError(outcome.response); err != nil {
			return outcome.response, err
		}
		return outcome.response, nil
	case <-operationCtx.Done():
		_ = send(envelope{Type: "cancel", RequestID: request.ID})
		if errors.Is(operationCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return plugin.ExtractResponse{}, fmt.Errorf("%w: %v", plugin.ErrTimeout, operationCtx.Err())
		}
		return plugin.ExtractResponse{}, operationCtx.Err()
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
