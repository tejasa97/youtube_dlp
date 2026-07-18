//go:build !linux

package chromiumlinux

func defaultProfileRoot(string) (string, error) { return "", ErrUnsupportedPlatform }
