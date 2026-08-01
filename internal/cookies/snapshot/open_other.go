//go:build !darwin && !linux && !windows

package snapshot

import (
	"errors"
	"os"
)

var (
	ErrNoFollowUnsupported = errors.New("snapshot no-follow open is unsupported")
	ErrUnsafeSource        = errors.New("snapshot source is unsafe")
)

// OpenReadOnlyNoFollow fails closed on platforms where this repository does
// not have a portable no-follow primitive.
func OpenReadOnlyNoFollow(string) (*os.File, error) {
	return nil, ErrNoFollowUnsupported
}
