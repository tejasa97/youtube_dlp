// Package validate contains the common bounded validation used by browser
// cookie importers before values are turned into net/http cookies.
package validate

import (
	"strings"

	"golang.org/x/net/http/httpguts"
)

const (
	MaxHostBytes  = 255
	MaxNameBytes  = 4096
	MaxValueBytes = 16 << 20
	MaxPathBytes  = 4096
)

// CookieFields accepts only bounded, HTTP-safe cookie fields. Cookie names use
// the HTTP token grammar. Values and paths use the printable-byte rules used by
// net/http, which preserve ordinary spaces and commas in real-world values but
// reject controls and delimiters that cannot be emitted safely.
func CookieFields(host, name, value, path string) bool {
	return validHost(host) && len(name) <= MaxNameBytes && validName(name) &&
		len(value) <= MaxValueBytes && validValue(value) && validPath(path)
}

func validHost(host string) bool {
	if host == "" || len(host) > MaxHostBytes {
		return false
	}
	for index := 0; index < len(host); index++ {
		if host[index] <= ' ' || host[index] == 0x7f {
			return false
		}
	}
	return true
}

func validName(name string) bool {
	return name != "" && httpguts.ValidHeaderFieldName(name)
}

func validValue(value string) bool {
	for index := 0; index < len(value); index++ {
		byteValue := value[index]
		if byteValue < 0x20 || byteValue >= 0x7f || byteValue == '"' || byteValue == ';' || byteValue == '\\' {
			return false
		}
	}
	return true
}

func validPath(path string) bool {
	if path == "" || !strings.HasPrefix(path, "/") {
		return false
	}
	for index := 0; index < len(path); index++ {
		byteValue := path[index]
		if byteValue < 0x20 || byteValue >= 0x7f || byteValue == ';' {
			return false
		}
	}
	return true
}
