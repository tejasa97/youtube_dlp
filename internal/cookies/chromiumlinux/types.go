// Package chromiumlinux imports Linux Chromium-family cookies without Python,
// cgo, or a live browser process.
package chromiumlinux

import (
	"context"
	"errors"
	"net/http"
)

type Browser string

const (
	Chrome   Browser = "chrome"
	Chromium Browser = "chromium"
	Brave    Browser = "brave"
)

var (
	ErrUnsupportedBrowser  = errors.New("unsupported Linux Chromium browser")
	ErrUnsupportedPlatform = errors.New("Linux Chromium profile discovery unsupported on this platform")
	ErrNotFound            = errors.New("Linux Chromium cookie database not found")
	ErrUnsafePath          = errors.New("unsafe Linux Chromium cookie database path")
	ErrInvalidDatabase     = errors.New("invalid Linux Chromium cookie database")
	ErrSnapshot            = errors.New("Linux Chromium cookie database snapshot failed")
	ErrKeyUnavailable      = errors.New("Linux Chromium cookie key unavailable")
	ErrDecrypt             = errors.New("Linux Chromium cookie decryption failed")
	ErrLimit               = errors.New("Linux Chromium cookie import exceeds safety limits")
)

// PasswordProvider retrieves the browser's Safe Storage password from a
// platform credential store. The returned buffer must be caller-owned; Import
// zeroes it after deriving a key. Provider error text is never propagated.
type PasswordProvider interface {
	Password(context.Context, string) ([]byte, error)
}

type Options struct {
	Browser          Browser
	Profile          string
	ProfileDir       string
	DatabasePath     string
	PasswordProvider PasswordProvider
	TempRoot         string
	MaxCookies       int
}

type Result struct {
	Cookies     []*http.Cookie
	MetaVersion int
	Total       int
	Imported    int
	Encrypted   int
	Failed      int
	Session     int
	Persistent  int
}
