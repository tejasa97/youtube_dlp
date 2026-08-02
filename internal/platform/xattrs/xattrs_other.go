//go:build !darwin && !linux && !freebsd && !windows

package xattrs

func Supported() bool { return false }

func List(string) (map[string][]byte, error) { return nil, ErrUnsupported }
func Set(string, string, []byte) error       { return ErrUnsupported }
func Remove(string, string) error            { return ErrUnsupported }
