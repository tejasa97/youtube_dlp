// Package engine executes bounded JavaScript requests in fresh pure-Go
// runtimes while caching only immutable compiled programs.
package engine

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
	"github.com/ytdlp-go/ytdlp/internal/javascript/protocol"
)

const (
	Name             = "goja"
	PinnedRevision   = "cfe4039cb6d77b297d8b637182f774fa4a54b7d5"
	DefaultCacheSize = 64
)

var errFunctionMissing = errors.New("JavaScript function is missing")

type cacheEntry struct {
	hash    string
	program *goja.Program
}

// Engine is safe for concurrent callers. Every execution gets a new runtime;
// the shared cache contains immutable compiled programs only.
type Engine struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*list.Element
	order    *list.List
}

func New(cacheSize int) *Engine {
	if cacheSize <= 0 {
		cacheSize = DefaultCacheSize
	}
	return &Engine{capacity: cacheSize, entries: make(map[string]*list.Element, cacheSize), order: list.New()}
}

// Execute normalizes and runs one request, always returning a protocol
// response rather than exposing engine-specific errors.
func (engine *Engine) Execute(ctx context.Context, request protocol.Request) protocol.Response {
	normalized, err := request.Normalize()
	if err != nil {
		code := protocol.CodeInvalidRequest
		if request.Version != protocol.Version {
			code = protocol.CodeIncompatibleVersion
		}
		return protocol.FailureResponse(request.ID, code, err)
	}
	if len(normalized.Modules) != 0 {
		return protocol.FailureResponse(normalized.ID, protocol.CodeUnsupportedModule, errors.New("goja pilot accepts bundled scripts only"))
	}

	started := time.Now()
	program, cacheHit, err := engine.program(normalized.ScriptHash, normalized.Script)
	if err != nil {
		return responseFailure(normalized, protocol.CodeSyntax, "JavaScript syntax error", started, cacheHit)
	}

	executionContext, cancel := context.WithTimeout(ctx, time.Duration(normalized.Limits.WallTimeMS)*time.Millisecond)
	defer cancel()
	runtime := goja.New()
	completed := make(chan struct{})
	go func() {
		select {
		case <-executionContext.Done():
			runtime.Interrupt(executionContext.Err())
		case <-completed:
		}
	}()

	result, executionErr := runtime.RunProgram(program)
	if executionErr == nil && normalized.Operation == protocol.OperationCall {
		result, executionErr = call(runtime, normalized.Function, normalized.Arguments)
	}
	close(completed)
	if executionErr != nil {
		var interrupted *goja.InterruptedError
		if errors.As(executionErr, &interrupted) {
			if errors.Is(executionContext.Err(), context.Canceled) {
				return responseFailure(normalized, protocol.CodeCanceled, "JavaScript execution canceled", started, cacheHit)
			}
			return responseFailure(normalized, protocol.CodeTimeout, "JavaScript execution timed out", started, cacheHit)
		}
		if errors.Is(executionErr, errFunctionMissing) {
			return responseFailure(normalized, protocol.CodeFunctionMissing, "JavaScript function is missing", started, cacheHit)
		}
		return responseFailure(normalized, protocol.CodeExecution, "JavaScript execution failed", started, cacheHit)
	}

	encoded, err := json.Marshal(result.Export())
	if err != nil {
		return responseFailure(normalized, protocol.CodeOutputLimit, "JavaScript result is not JSON-serializable", started, cacheHit)
	}
	if len(encoded) > int(normalized.Limits.OutputBytes) {
		return responseFailure(normalized, protocol.CodeOutputLimit, "JavaScript result exceeds output budget", started, cacheHit)
	}
	return protocol.Response{
		Version: protocol.Version, ID: normalized.ID, Result: encoded,
		Stats: stats(normalized.ScriptHash, started, cacheHit),
	}
}

func (engine *Engine) program(hash, source string) (*goja.Program, bool, error) {
	engine.mu.Lock()
	if element, exists := engine.entries[hash]; exists {
		engine.order.MoveToFront(element)
		program := element.Value.(cacheEntry).program
		engine.mu.Unlock()
		return program, true, nil
	}
	engine.mu.Unlock()

	program, err := goja.Compile("challenge.js", source, false)
	if err != nil {
		return nil, false, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if element, exists := engine.entries[hash]; exists {
		engine.order.MoveToFront(element)
		return element.Value.(cacheEntry).program, true, nil
	}
	element := engine.order.PushFront(cacheEntry{hash: hash, program: program})
	engine.entries[hash] = element
	if engine.order.Len() > engine.capacity {
		oldest := engine.order.Back()
		entry := oldest.Value.(cacheEntry)
		delete(engine.entries, entry.hash)
		engine.order.Remove(oldest)
	}
	return program, false, nil
}

func call(runtime *goja.Runtime, path string, arguments []json.RawMessage) (goja.Value, error) {
	parts := strings.Split(path, ".")
	current := runtime.Get(parts[0])
	for _, part := range parts[1:] {
		if goja.IsUndefined(current) || goja.IsNull(current) {
			return nil, errFunctionMissing
		}
		object := current.ToObject(runtime)
		if object == nil {
			return nil, errFunctionMissing
		}
		current = object.Get(part)
	}
	callable, ok := goja.AssertFunction(current)
	if !ok {
		return nil, errFunctionMissing
	}
	values := make([]goja.Value, len(arguments))
	for index, raw := range arguments {
		var argument any
		if err := json.Unmarshal(raw, &argument); err != nil {
			return nil, fmt.Errorf("decode argument %d: %w", index, err)
		}
		values[index] = runtime.ToValue(argument)
	}
	return callable(goja.Undefined(), values...)
}

func responseFailure(request protocol.Request, code protocol.ErrorCode, message string, started time.Time, cacheHit bool) protocol.Response {
	response := protocol.FailureResponse(request.ID, code, errors.New(message))
	response.Stats = stats(request.ScriptHash, started, cacheHit)
	return response
}

func stats(hash string, started time.Time, cacheHit bool) protocol.Stats {
	return protocol.Stats{
		Engine: Name + "@" + PinnedRevision[:12], ScriptHash: hash,
		CacheHit: cacheHit, ExecutionUS: time.Since(started).Microseconds(),
	}
}
