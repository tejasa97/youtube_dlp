// Package xattrs provides the tiny platform capability used by the typed
// post-processing xattrs stage. It deliberately exposes no arbitrary xattr
// command or name expansion surface.
package xattrs

import "errors"

var ErrUnsupported = errors.New("extended attributes are unsupported")
