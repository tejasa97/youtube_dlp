// Package ytdlp defines the supported embedding boundary for the Go port.
//
// The API is intentionally small during Phase 0. New capability should first
// be implemented behind internal interfaces and promoted here only when its
// compatibility and lifecycle semantics are stable.
package ytdlp

import (
	"context"
	"errors"
	"fmt"
)

// ErrorCategory is a stable, machine-readable class of operation failure.
type ErrorCategory string

const (
	ErrorUnsupported  ErrorCategory = "unsupported"
	ErrorInvalidInput ErrorCategory = "invalid_input"
	ErrorNetwork      ErrorCategory = "network"
	ErrorCancelled    ErrorCategory = "cancelled"
	ErrorInternal     ErrorCategory = "internal"
)

// Error wraps an operation failure with a stable category.
type Error struct {
	Category ErrorCategory
	Op       string
	Err      error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Op == "" {
		return fmt.Sprintf("%s: %v", e.Category, e.Err)
	}
	return fmt.Sprintf("%s: %s: %v", e.Category, e.Op, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// IsCategory reports whether err or a wrapped error has category.
func IsCategory(err error, category ErrorCategory) bool {
	var target *Error
	return errors.As(err, &target) && target.Category == category
}

// Request describes one extraction/download operation.
type Request struct {
	URL string
}

// Result is the stable envelope returned by a completed operation.
// Fields will be extended as Phase 0 components become available.
type Result struct {
	Downloaded bool
	Filename   string
}

// Client is the cancellable operation boundary used by embedders and the CLI.
type Client interface {
	Run(context.Context, Request) (Result, error)
}
