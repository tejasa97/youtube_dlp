// Package protocol defines the versioned, engine-neutral JavaScript helper
// boundary.
package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	Version            = 1
	MaxRequestIDLength = 128
)

type Operation string

const (
	OperationEvaluate Operation = "evaluate"
	OperationCall     Operation = "call"
)

type ErrorCode string

const (
	CodeInvalidRequest      ErrorCode = "invalid_request"
	CodeIncompatibleVersion ErrorCode = "incompatible_version"
	CodeSyntax              ErrorCode = "syntax_error"
	CodeExecution           ErrorCode = "execution_error"
	CodeFunctionMissing     ErrorCode = "function_missing"
	CodeTimeout             ErrorCode = "timeout"
	CodeCanceled            ErrorCode = "canceled"
	CodeInputLimit          ErrorCode = "input_limit"
	CodeOutputLimit         ErrorCode = "output_limit"
	CodeMemoryLimit         ErrorCode = "memory_limit"
	CodeUnsupportedModule   ErrorCode = "unsupported_module"
	CodeHelperCrash         ErrorCode = "helper_crash"
	CodeProtocol            ErrorCode = "protocol_error"
)

const (
	DefaultWallTime    = 2 * time.Second
	DefaultMemoryBytes = 64 << 20
	DefaultOutputBytes = 1 << 20
	DefaultSourceBytes = 2 << 20
	DefaultModuleBytes = 2 << 20
	DefaultMaxModules  = 16

	// HardMaxWallTime bounds untrusted protocol requests.
	HardMaxWallTime = 30 * time.Second

	// TrustedMaxWallTime is the extended ceiling for explicitly trusted
	// internal callers (e.g., EJS player preprocessing that runs a JS parser
	// inside the pure-Go goja engine). Requests must opt in via Limits.Trusted.
	TrustedMaxWallTime = 60 * time.Second

	HardMaxMemoryBytes = 512 << 20
	HardMaxOutputBytes = 8 << 20
	HardMaxSourceBytes = 8 << 20
	HardMaxModuleBytes = 8 << 20
	HardMaxModules     = 64
)

var (
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	functionPattern  = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(?:\.[A-Za-z_$][A-Za-z0-9_$]*)*$`)
	modulePattern    = regexp.MustCompile(`^[A-Za-z0-9@._/-]{1,256}$`)
)

// Limits are caller-requested budgets. Zero values receive safe defaults.
type Limits struct {
	WallTimeMS  int64 `json:"wall_time_ms,omitempty"`
	MemoryBytes int64 `json:"memory_bytes,omitempty"`
	OutputBytes int64 `json:"output_bytes,omitempty"`
	SourceBytes int64 `json:"source_bytes,omitempty"`
	ModuleBytes int64 `json:"module_bytes,omitempty"`
	MaxModules  int   `json:"max_modules,omitempty"`
	// Trusted opts into the extended TrustedMaxWallTime ceiling for
	// in-process validation (supervisor side). Never serialized.
	// Only honored for EJS preprocessing calls (function "jsc").
	Trusted bool `json:"-"`
	// TrustedWallTimeMS is the serialized wall-time ceiling minted by the
	// supervisor for approved EJS preprocessing calls. Callers must not
	// provide this field; the supervisor strips it at the boundary and
	// mints it only for operation=call, function="jsc" with Trusted=true.
	// The helper validates WallTimeMS against this value when present.
	TrustedWallTimeMS int64 `json:"trusted_wall_time_ms,omitempty"`
}

// Module is an explicitly supplied source module. The helper never resolves
// modules from the network or filesystem.
type Module struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// Request is one isolated evaluation or function call.
type Request struct {
	Version    int               `json:"version"`
	ID         string            `json:"id"`
	Operation  Operation         `json:"operation"`
	Script     string            `json:"script"`
	ScriptHash string            `json:"script_hash,omitempty"`
	Function   string            `json:"function,omitempty"`
	Arguments  []json.RawMessage `json:"arguments,omitempty"`
	Modules    []Module          `json:"modules,omitempty"`
	Limits     Limits            `json:"limits,omitempty"`
}

// Response is exactly one result or categorized failure.
type Response struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Failure        `json:"error,omitempty"`
	Stats   Stats           `json:"stats,omitempty"`
}

type Failure struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

type Stats struct {
	Engine      string `json:"engine,omitempty"`
	ScriptHash  string `json:"script_hash,omitempty"`
	CacheHit    bool   `json:"cache_hit,omitempty"`
	ExecutionUS int64  `json:"execution_us,omitempty"`
}

// Normalize validates a request and fills bounded defaults.
func (request Request) Normalize() (Request, error) {
	if request.Version != Version {
		return Request{}, fmt.Errorf("version %d is incompatible with %d", request.Version, Version)
	}
	if len(request.ID) > MaxRequestIDLength || !requestIDPattern.MatchString(request.ID) {
		return Request{}, errors.New("id must contain 1-128 safe characters")
	}
	switch request.Operation {
	case OperationEvaluate:
		if request.Function != "" {
			return Request{}, errors.New("evaluate request must not name a function")
		}
	case OperationCall:
		if !functionPattern.MatchString(request.Function) {
			return Request{}, errors.New("call request has an invalid function path")
		}
	default:
		return Request{}, fmt.Errorf("unsupported operation %q", request.Operation)
	}
	request.Limits = request.Limits.withDefaults()
	// The extended trusted wall-time allowance is restricted to EJS
	// preprocessing calls (operation=call, function="jsc"). Strip any
	// caller-provided TrustedWallTimeMS or Trusted flag from other
	// operations to prevent generic evaluate/call requests from
	// obtaining more than HardMaxWallTime (30 s).
	if request.Operation != OperationCall || request.Function != "jsc" {
		request.Limits.TrustedWallTimeMS = 0
		request.Limits.Trusted = false
	}
	if err := request.Limits.validate(); err != nil {
		return Request{}, err
	}
	if len(request.Script) > int(request.Limits.SourceBytes) {
		return Request{}, fmt.Errorf("script exceeds %d bytes", request.Limits.SourceBytes)
	}
	if request.ScriptHash == "" {
		request.ScriptHash = HashScript(request.Script)
	} else if !strings.EqualFold(request.ScriptHash, HashScript(request.Script)) {
		return Request{}, errors.New("script_hash does not match script")
	} else {
		request.ScriptHash = strings.ToLower(request.ScriptHash)
	}
	if len(request.Modules) > request.Limits.MaxModules {
		return Request{}, fmt.Errorf("module count exceeds %d", request.Limits.MaxModules)
	}
	moduleBytes := 0
	seenModules := make(map[string]struct{}, len(request.Modules))
	for _, module := range request.Modules {
		if !modulePattern.MatchString(module.Name) || strings.Contains(module.Name, "..") || strings.HasPrefix(module.Name, "/") {
			return Request{}, fmt.Errorf("invalid module name %q", module.Name)
		}
		if _, exists := seenModules[module.Name]; exists {
			return Request{}, fmt.Errorf("duplicate module %q", module.Name)
		}
		seenModules[module.Name] = struct{}{}
		moduleBytes += len(module.Source)
		if moduleBytes > int(request.Limits.ModuleBytes) {
			return Request{}, fmt.Errorf("module sources exceed %d bytes", request.Limits.ModuleBytes)
		}
	}
	for index, argument := range request.Arguments {
		if !json.Valid(argument) {
			return Request{}, fmt.Errorf("argument %d is not valid JSON", index)
		}
	}
	return request, nil
}

func (limits Limits) withDefaults() Limits {
	if limits.WallTimeMS == 0 {
		limits.WallTimeMS = DefaultWallTime.Milliseconds()
	}
	if limits.MemoryBytes == 0 {
		limits.MemoryBytes = DefaultMemoryBytes
	}
	if limits.OutputBytes == 0 {
		limits.OutputBytes = DefaultOutputBytes
	}
	if limits.SourceBytes == 0 {
		limits.SourceBytes = DefaultSourceBytes
	}
	if limits.ModuleBytes == 0 {
		limits.ModuleBytes = DefaultModuleBytes
	}
	if limits.MaxModules == 0 {
		limits.MaxModules = DefaultMaxModules
	}
	return limits
}

func (limits Limits) validate() error {
	wallTimeMax := HardMaxWallTime.Milliseconds()
	if limits.Trusted {
		wallTimeMax = TrustedMaxWallTime.Milliseconds()
	} else if limits.TrustedWallTimeMS > 0 {
		// Serialized trusted ceiling from the supervisor (helper side).
		if limits.TrustedWallTimeMS > TrustedMaxWallTime.Milliseconds() {
			return fmt.Errorf("trusted_wall_time_ms exceeds %d", TrustedMaxWallTime.Milliseconds())
		}
		wallTimeMax = limits.TrustedWallTimeMS
	}
	checks := []struct {
		name       string
		value, max int64
	}{
		{"wall_time_ms", limits.WallTimeMS, wallTimeMax},
		{"memory_bytes", limits.MemoryBytes, HardMaxMemoryBytes},
		{"output_bytes", limits.OutputBytes, HardMaxOutputBytes},
		{"source_bytes", limits.SourceBytes, HardMaxSourceBytes},
		{"module_bytes", limits.ModuleBytes, HardMaxModuleBytes},
		{"max_modules", int64(limits.MaxModules), HardMaxModules},
	}
	for _, check := range checks {
		if check.value <= 0 || check.value > check.max {
			return fmt.Errorf("%s must be between 1 and %d", check.name, check.max)
		}
	}
	return nil
}

// HashScript returns the lowercase SHA-256 cache key for source.
func HashScript(source string) string {
	digest := sha256.Sum256([]byte(source))
	return hex.EncodeToString(digest[:])
}

// FailureResponse creates a protocol-valid categorized failure.
func FailureResponse(id string, code ErrorCode, err error) Response {
	message := ""
	if err != nil {
		message = err.Error()
	}
	return Response{Version: Version, ID: id, Error: &Failure{Code: code, Message: message}}
}
