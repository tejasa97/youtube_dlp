// Package chromiumwindows imports Chromium-family cookies using native Go and
// Windows DPAPI. No browser process, shell, or Python runtime is invoked.
package chromiumwindows

import (
	"context"
	"errors"
	"net/http"
)

type Browser string

const (
	Chrome   Browser = "chrome"
	Chromium Browser = "chromium"
	Edge     Browser = "edge"
	Brave    Browser = "brave"
	Vivaldi  Browser = "vivaldi"
	Opera    Browser = "opera"
)

var (
	ErrUnsupportedBrowser  = errors.New("unsupported Windows Chromium browser")
	ErrUnsupportedPlatform = errors.New("Windows cookie protection is unavailable on this platform")
	ErrNotFound            = errors.New("Windows Chromium cookie database not found")
	ErrUnsafePath          = errors.New("unsafe Windows Chromium cookie path")
	ErrSnapshot            = errors.New("Windows Chromium cookie snapshot failed")
	ErrInvalidDatabase     = errors.New("invalid Windows Chromium cookie database")
	ErrInvalidLocalState   = errors.New("invalid Windows Chromium Local State")
	ErrKeyUnavailable      = errors.New("Windows Chromium encryption key unavailable")
	ErrAppBound            = errors.New("Windows Chromium app-bound cookie unavailable")
	ErrDecrypt             = errors.New("Windows Chromium cookie decryption failed")
	ErrLimit               = errors.New("Windows Chromium cookie import limit exceeded")
)

type DataProtector interface {
	Unprotect(context.Context, []byte) ([]byte, error)
}

// AppBoundDecryptor is deliberately injectable because Chromium v20 keys are
// bound to browser/application identity and cannot be portably recovered by a
// standalone process. Implementations receive only the payload after "v20".
type AppBoundDecryptor interface {
	DecryptAppBound(context.Context, []byte) ([]byte, error)
}

type Options struct {
	Browser            Browser
	Profile            string
	ProfileRoot        string
	DatabasePath       string
	LocalStatePath     string
	TempRoot           string
	Protector          DataProtector
	AppBound           AppBoundDecryptor
	MaxCookies         int
	MaxDatabaseBytes   int64
	MaxLocalStateBytes int64
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
	Legacy      int
	V10         int
	V11         int
	V20         int
}
