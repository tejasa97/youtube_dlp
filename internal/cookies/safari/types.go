// Package safari imports Cookies.binarycookies without Python or cgo.
package safari

import (
	"errors"
	"net/http"
)

var (
	ErrNotFound            = errors.New("Safari cookie database not found")
	ErrInvalidDatabase     = errors.New("invalid Safari cookie database")
	ErrUnsafePath          = errors.New("unsafe Safari cookie database path")
	ErrLimit               = errors.New("Safari cookie import exceeds safety limits")
	ErrUnsupportedPlatform = errors.New("Safari cookie import unsupported on this platform")
)

// Options configures Safari cookie import. HomeDir is a deterministic test and
// embedding seam for default-path discovery; an empty value uses os.UserHomeDir.
type Options struct {
	DatabasePath string
	HomeDir      string
	MaxCookies   int
	MaxBytes     int64
}

// Result contains cookies in page/record order and bounded import counts.
type Result struct {
	Cookies  []*http.Cookie
	Total    int
	Imported int
	Failed   int
}
