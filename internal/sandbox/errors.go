// Package sandbox constructs fail-closed launch plans for hostile out-of-
// process plugins. It never accepts or transports secret values.
package sandbox

import "errors"

var (
	ErrInvalidConfig       = errors.New("invalid plugin sandbox configuration")
	ErrUnsafePath          = errors.New("unsafe plugin sandbox path")
	ErrResourceLimit       = errors.New("plugin sandbox resource limit exceeded")
	ErrAdapterUnavailable  = errors.New("plugin sandbox adapter is unavailable")
	ErrUnsupportedPlatform = errors.New("plugin sandbox is unsupported on this platform")
	ErrUnsupportedLimit    = errors.New("plugin sandbox resource limit is unsupported")
)
