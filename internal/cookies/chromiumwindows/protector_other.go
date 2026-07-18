//go:build !windows

package chromiumwindows

import "context"

type unsupportedProtector struct{}

func (unsupportedProtector) Unprotect(context.Context, []byte) ([]byte, error) {
	return nil, ErrUnsupportedPlatform
}

func defaultProtector() DataProtector { return unsupportedProtector{} }
