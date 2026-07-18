package config

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// ErrorCategory is stable enough for CLI/API error mapping.
type ErrorCategory string

const (
	ErrorCanceled  ErrorCategory = "canceled"
	ErrorEncoding  ErrorCategory = "encoding"
	ErrorSyntax    ErrorCategory = "syntax"
	ErrorPath      ErrorCategory = "path"
	ErrorIO        ErrorCategory = "io"
	ErrorRecursion ErrorCategory = "recursion"
	ErrorResource  ErrorCategory = "resource_limit"
	ErrorAlias     ErrorCategory = "alias"
)

// Error is a secret-safe, source-located configuration failure.
type Error struct {
	Category ErrorCategory
	Op       string
	Source   string
	Line     int
	Column   int
	Message  string
	Err      error
}

func (e *Error) Error() string {
	location := safeDiagnosticSource(e.Source)
	if e.Line > 0 {
		location = fmt.Sprintf("%s:%d:%d", location, e.Line, e.Column)
	}
	if location == "" {
		return fmt.Sprintf("config %s: %s", e.Op, e.Message)
	}
	return fmt.Sprintf("config %s %s: %s", e.Op, location, e.Message)
}

func safeDiagnosticSource(source string) string {
	return strings.Map(func(value rune) rune {
		if unicode.IsControl(value) {
			return '�'
		}
		return value
	}, source)
}

func (e *Error) Unwrap() error { return e.Err }

// IsCategory reports whether err contains a categorized config failure.
func IsCategory(err error, category ErrorCategory) bool {
	var target *Error
	return errors.As(err, &target) && target.Category == category
}

func configError(category ErrorCategory, op, source, message string, err error) error {
	return &Error{Category: category, Op: op, Source: source, Message: message, Err: err}
}

func tokenError(category ErrorCategory, op string, token Token, message string) error {
	return &Error{Category: category, Op: op, Source: token.Source, Line: token.Line, Column: token.Column, Message: message}
}
