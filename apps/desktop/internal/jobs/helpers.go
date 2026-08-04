// helpers.go: small os shims so we can stub them in tests.
package jobs

import "os"

var (
	mkdirAll    = os.MkdirAll
	userHomeDir = os.UserHomeDir
)
