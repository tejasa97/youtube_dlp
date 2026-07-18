// Package plugin defines the common contracts shared by experimental plugin hosts.
package plugin

import (
	"errors"
	"fmt"
	"time"
)

const ProtocolVersion uint32 = 1

var (
	ErrInvalidConfig       = errors.New("invalid plugin configuration")
	ErrIncompatibleVersion = errors.New("incompatible plugin protocol version")
	ErrPermissionDenied    = errors.New("plugin permission denied")
	ErrResourceLimit       = errors.New("plugin resource limit exceeded")
	ErrMalformedMessage    = errors.New("malformed plugin message")
	ErrCrashed             = errors.New("plugin crashed")
	ErrTimeout             = errors.New("plugin timed out")
)

type Permission string

const (
	PermissionNetwork        Permission = "network"
	PermissionCookies        Permission = "cookies"
	PermissionSecrets        Permission = "secrets"
	PermissionFilesystemRead Permission = "filesystem_read"
)

type Manifest struct {
	Name        string       `json:"name"`
	Versions    []uint32     `json:"versions"`
	Permissions []Permission `json:"permissions,omitempty"`
}

type Limits struct {
	Timeout          time.Duration
	CancelGrace      time.Duration
	MaxMessageBytes  uint32
	MaxStderrBytes   int
	MemoryLimitPages uint32
}

func (limits Limits) WithDefaults() Limits {
	if limits.Timeout <= 0 {
		limits.Timeout = 10 * time.Second
	}
	if limits.CancelGrace <= 0 {
		limits.CancelGrace = 100 * time.Millisecond
	}
	if limits.MaxMessageBytes == 0 {
		limits.MaxMessageBytes = 1 << 20
	}
	if limits.MaxStderrBytes <= 0 {
		limits.MaxStderrBytes = 64 << 10
	}
	if limits.MemoryLimitPages == 0 {
		limits.MemoryLimitPages = 256
	}
	return limits
}

func Negotiate(supported, offered []uint32) (uint32, error) {
	var selected uint32
	for _, host := range supported {
		for _, candidate := range offered {
			if host == candidate && host > selected {
				selected = host
			}
		}
	}
	if selected == 0 {
		return 0, fmt.Errorf("%w: host=%v plugin=%v", ErrIncompatibleVersion, supported, offered)
	}
	return selected, nil
}

func CheckPermissions(required, granted []Permission) error {
	allowed := make(map[Permission]struct{}, len(granted))
	for _, permission := range granted {
		allowed[permission] = struct{}{}
	}
	for _, permission := range required {
		if _, ok := allowed[permission]; !ok {
			return fmt.Errorf("%w: %s", ErrPermissionDenied, permission)
		}
	}
	return nil
}

type ExtractRequest struct {
	ID      string         `json:"id"`
	URL     string         `json:"url"`
	Options map[string]any `json:"options,omitempty"`
}

type ExtractResponse struct {
	ID       string         `json:"id"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Error    *RemoteError   `json:"error,omitempty"`
}

type RemoteCategory string

const (
	RemoteAuthentication  RemoteCategory = "authentication"
	RemoteUnavailable     RemoteCategory = "unavailable"
	RemoteInvalidMetadata RemoteCategory = "invalid_metadata"
	RemoteNetwork         RemoteCategory = "network"
	RemoteInternal        RemoteCategory = "internal"
)

type RemoteError struct {
	Category RemoteCategory `json:"category"`
	Message  string         `json:"message"`
}

type RemoteFailure struct{ Detail RemoteError }

func (failure *RemoteFailure) Error() string {
	return fmt.Sprintf("plugin %s error: %s", failure.Detail.Category, failure.Detail.Message)
}

func ResponseError(response ExtractResponse) error {
	if response.Error == nil {
		return nil
	}
	switch response.Error.Category {
	case RemoteAuthentication, RemoteUnavailable, RemoteInvalidMetadata, RemoteNetwork, RemoteInternal:
	default:
		return fmt.Errorf("%w: unknown remote error category", ErrMalformedMessage)
	}
	if response.Error.Message == "" {
		return fmt.Errorf("%w: empty remote error message", ErrMalformedMessage)
	}
	return &RemoteFailure{Detail: *response.Error}
}
