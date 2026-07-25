// Package youtubecanary provides an opt-in, disabled-by-default YouTube
// interoperability harness for later operator evidence. Ordinary go test
// suites must not enable it.
package youtubecanary

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	EnvEnable = "YTDLP_YOUTUBE_CANARY"

	MaxTimeout      = 30 * time.Second
	MaxBytes        = 4 << 20
	MaxRequests     = 8
	MaxOutputBytes  = 64 << 10
	MaxSecretHandle = 128
	MaxTargetRef    = 256
)

var (
	ErrDisabled       = errors.New("YouTube canary requires explicit opt-in")
	ErrInvalidConfig  = errors.New("invalid YouTube canary configuration")
	ErrResourceLimit  = errors.New("YouTube canary resource limit exceeded")
	ErrSecretRequired = errors.New("YouTube canary secret handle required for credential class")
)

// Config is the operator-facing, secret-free canary request.
type Config struct {
	Class        string // public | credential
	TargetRef    string // opaque fixture/target handle, never a signed URL
	SecretHandle string // keychain-style handle; never a raw cookie/token
	Timeout      time.Duration
	MaxBytes     int64
	MaxRequests  int
}

// Result is a redacted, reviewable canary outcome. It must never contain
// tokens, cookies, visitor data, signed CDN URLs, or player responses.
type Result struct {
	Enabled      bool   `json:"enabled"`
	Class        string `json:"class"`
	TargetRef    string `json:"target_ref"`
	SecretHandle string `json:"secret_handle,omitempty"`
	Outcome      string `json:"outcome"`
	FailureClass string `json:"failure_class"`
	DurationMS   int64  `json:"duration_ms"`
	Requests     int    `json:"requests"`
	BytesRead    int64  `json:"bytes_read"`
	Notes        string `json:"notes,omitempty"`
}

// Enabled reports whether the process opted into live canary execution.
func Enabled() bool {
	value := strings.TrimSpace(os.Getenv(EnvEnable))
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// Validate bounds the config without performing network I/O.
func Validate(config Config) error {
	if !Enabled() {
		return ErrDisabled
	}
	switch config.Class {
	case "public", "credential":
	default:
		return fmt.Errorf("%w: class", ErrInvalidConfig)
	}
	if config.TargetRef == "" || len(config.TargetRef) > MaxTargetRef ||
		strings.ContainsAny(config.TargetRef, "\x00\r\n") || looksLikeSecret(config.TargetRef) {
		return fmt.Errorf("%w: target_ref", ErrInvalidConfig)
	}
	if config.Class == "credential" {
		if config.SecretHandle == "" || len(config.SecretHandle) > MaxSecretHandle ||
			strings.ContainsAny(config.SecretHandle, "\x00\r\n") || looksLikeSecret(config.SecretHandle) {
			return ErrSecretRequired
		}
	} else if config.SecretHandle != "" {
		return fmt.Errorf("%w: secret_handle on public canary", ErrInvalidConfig)
	}
	if config.Timeout <= 0 || config.Timeout > MaxTimeout {
		return fmt.Errorf("%w: timeout", ErrResourceLimit)
	}
	if config.MaxBytes <= 0 || config.MaxBytes > MaxBytes {
		return fmt.Errorf("%w: max bytes", ErrResourceLimit)
	}
	if config.MaxRequests <= 0 || config.MaxRequests > MaxRequests {
		return fmt.Errorf("%w: max requests", ErrResourceLimit)
	}
	return nil
}

// Run validates and returns a normalized dry-run result. Live network execution
// is deliberately not implemented here: operators wire a runner after opt-in.
// This keeps ordinary tests and CI free of service dependency while preserving
// the redaction and resource contract.
func Run(ctx context.Context, config Config) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := Validate(config); err != nil {
		return Result{}, err
	}
	start := time.Now()
	result := Result{
		Enabled:      true,
		Class:        config.Class,
		TargetRef:    config.TargetRef,
		SecretHandle: config.SecretHandle,
		Outcome:      "not_executed",
		FailureClass: "none",
		DurationMS:   time.Since(start).Milliseconds(),
		Notes:        "opt-in harness validated; live runner not invoked",
	}
	if err := redactResult(&result); err != nil {
		return Result{}, err
	}
	return result, nil
}

func redactResult(result *Result) error {
	if result == nil {
		return ErrInvalidConfig
	}
	for _, value := range []string{result.TargetRef, result.SecretHandle, result.Notes, result.Outcome} {
		if looksLikeSecret(value) || looksLikeSignedURL(value) {
			return fmt.Errorf("%w: secretful canary field", ErrInvalidConfig)
		}
	}
	if len(result.Notes) > MaxOutputBytes {
		return ErrResourceLimit
	}
	return nil
}

func looksLikeSecret(value string) bool {
	lower := strings.ToLower(value)
	for _, needle := range []string{"sapisid", "authorization", "cookie=", "po_token", "pot=", "visitor", "ya29.", "oauth"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func looksLikeSignedURL(value string) bool {
	if !strings.Contains(strings.ToLower(value), "googlevideo.com") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Host != ""
}
