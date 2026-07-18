package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	ErrExternalUnavailable = errors.New("external downloader executable is unavailable")
	ErrExternalFailed      = errors.New("external downloader failed")
	ErrUnsafeExternalArg   = errors.New("unsafe external downloader argument")
	ErrInvalidExternalURL  = errors.New("invalid external downloader URL")
	ErrUnsafeExternalTool  = errors.New("unsafe external downloader executable")
)

// ExternalRequest is a typed, shell-free boundary for optional download
// tools. Arguments are passed directly to exec; they are never interpolated
// into a shell command. The adapter deliberately has no automatic fallback:
// a requested external tool either completes or returns a categorized error.
type ExternalRequest struct {
	Executable  string
	Arguments   []string
	URL         string
	OutputRoot  string
	Destination string
}

type ExternalResult struct {
	Path   string
	Stderr string
}

type Process interface {
	Run(context.Context, string, []string, string) ([]byte, error)
}
type OSProcess struct{}

func (OSProcess) Run(ctx context.Context, binary string, arguments []string, directory string) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Dir = directory
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err := command.Run()
	return stderr.Bytes(), err
}

type ExternalAdapter struct {
	process   Process
	maxStderr int
}

func NewExternalAdapter(process Process) *ExternalAdapter {
	if process == nil {
		process = OSProcess{}
	}
	return &ExternalAdapter{process: process, maxStderr: 64 << 10}
}

func (adapter *ExternalAdapter) Download(ctx context.Context, request ExternalRequest) (ExternalResult, error) {
	if err := validateDestination(request.OutputRoot, request.Destination); err != nil {
		return ExternalResult{}, err
	}
	if request.Executable == "" || strings.ContainsRune(request.Executable, '\x00') {
		return ExternalResult{}, ErrExternalUnavailable
	}
	if interpreter(request.Executable) {
		return ExternalResult{}, ErrUnsafeExternalTool
	}
	if len(request.Arguments) > 128 {
		return ExternalResult{}, ErrUnsafeExternalArg
	}
	totalArgumentBytes := len(request.URL) + len(request.Destination)
	binary, err := exec.LookPath(request.Executable)
	if err != nil {
		return ExternalResult{}, fmt.Errorf("%w: %s", ErrExternalUnavailable, filepath.Base(request.Executable))
	}
	if interpreter(binary) {
		return ExternalResult{}, ErrUnsafeExternalTool
	}
	if err := os.MkdirAll(filepath.Dir(request.Destination), 0o755); err != nil {
		return ExternalResult{}, fmt.Errorf("create external destination: %w", err)
	}
	for _, argument := range append(append([]string(nil), request.Arguments...), request.URL, request.Destination) {
		totalArgumentBytes += len(argument)
		if strings.ContainsRune(argument, '\x00') || strings.ContainsAny(argument, "\r\n") {
			return ExternalResult{}, ErrUnsafeExternalArg
		}
	}
	if totalArgumentBytes > 32<<10 {
		return ExternalResult{}, ErrUnsafeExternalArg
	}
	parsed, parseErr := url.Parse(request.URL)
	if parseErr != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "ftp" && parsed.Scheme != "ftps") {
		return ExternalResult{}, ErrInvalidExternalURL
	}
	// A conventional argv contract allows callers to choose curl, aria2c, or a
	// controlled bespoke binary without embedding tool-specific string parsing.
	arguments := append([]string{}, request.Arguments...)
	arguments = append(arguments, request.URL, request.Destination)
	stderr, err := adapter.process.Run(ctx, binary, arguments, request.OutputRoot)
	if len(stderr) > adapter.maxStderr {
		stderr = stderr[:adapter.maxStderr]
	}
	if err != nil {
		if ctx.Err() != nil {
			return ExternalResult{}, ctx.Err()
		}
		return ExternalResult{Stderr: safeDiagnostic(stderr)}, fmt.Errorf("%w: %s", ErrExternalFailed, filepath.Base(binary))
	}
	if info, statErr := os.Stat(request.Destination); statErr != nil || info.IsDir() {
		return ExternalResult{Stderr: safeDiagnostic(stderr)}, fmt.Errorf("%w: tool did not create destination", ErrExternalFailed)
	}
	return ExternalResult{Path: request.Destination, Stderr: safeDiagnostic(stderr)}, nil
}

func interpreter(path string) bool {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
	switch name {
	case "sh", "bash", "zsh", "fish", "cmd", "powershell", "pwsh", "python", "python2", "python3", "pypy", "node", "perl", "ruby", "lua", "php", "env", "busybox":
		return true
	}
	return strings.HasPrefix(name, "python")
}

func safeDiagnostic(stderr []byte) string {
	if len(stderr) == 0 {
		return ""
	}
	return "external downloader emitted diagnostics (redacted)"
}
