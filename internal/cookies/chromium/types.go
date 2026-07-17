// Package chromium imports Chromium-family cookies without Python or cgo.
package chromium

import (
	"context"
	"errors"
	"net/http"
)

type Browser string

const Chrome Browser = "chrome"

var (
	ErrUnsupportedBrowser  = errors.New("unsupported Chromium browser")
	ErrUnsupportedPlatform = errors.New("browser cookie import unsupported on this platform")
	ErrDatabaseNotFound    = errors.New("browser cookie database not found")
	ErrInvalidDatabase     = errors.New("invalid browser cookie database")
	ErrUnsafeDatabase      = errors.New("unsafe browser cookie database")
	ErrSnapshot            = errors.New("browser cookie database snapshot failed")
	ErrKeyUnavailable      = errors.New("browser cookie key unavailable")
	ErrDecrypt             = errors.New("browser cookie decryption failed")
)

type KeychainItem struct {
	Account string
	Service string
}

type KeyProvider interface {
	// Password returns a caller-owned buffer. Import zeroes it after deriving the
	// database key, so providers must return a fresh copy for each call.
	Password(context.Context, KeychainItem) ([]byte, error)
}

type Options struct {
	Browser      Browser
	Profile      string
	ProfileDir   string
	DatabasePath string
	KeyProvider  KeyProvider
	TempRoot     string
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
