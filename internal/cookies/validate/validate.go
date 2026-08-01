// Package validate contains the common bounded validation used by browser
// cookie importers before values are turned into net/http cookies.
package validate

import "strings"

const (
	MaxHostBytes  = 255
	MaxNameBytes  = 4096
	MaxValueBytes = 16 << 20
	MaxPathBytes  = 4096
)

// CookieFields accepts only bounded, header-safe cookie fields. The caller is
// still responsible for validating encrypted blobs separately because they are
// not emitted as HTTP cookie values until after decryption.
func CookieFields(host, name, value, path string) bool {
	return host != "" && len(host) <= MaxHostBytes && len(name) <= MaxNameBytes && len(value) <= MaxValueBytes &&
		path != "" && len(path) <= MaxPathBytes && strings.HasPrefix(path, "/") &&
		!strings.ContainsAny(host, "\r\n\x00") && !strings.ContainsAny(name, "\r\n\x00") &&
		!strings.ContainsAny(value, "\r\n\x00") && !strings.ContainsAny(path, "\r\n\x00")
}
