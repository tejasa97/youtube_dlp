//go:build darwin || linux || freebsd

package xattrs

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	maxListBytes = 16 << 10
	maxListNames = 32
	maxListName  = 128
	maxListValue = 4096
	maxListTotal = 16 << 10
)

func Supported() bool { return true }

func List(path string) (map[string][]byte, error) {
	count, err := unix.Listxattr(path, nil)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return map[string][]byte{}, nil
	}
	if count < 0 || count > maxListBytes {
		return nil, fmt.Errorf("%w: xattr name list exceeds bound", ErrUnsupported)
	}
	buffer := make([]byte, count)
	count, err = unix.Listxattr(path, buffer)
	if err != nil {
		return nil, err
	}
	if count < 0 || count > len(buffer) {
		return nil, fmt.Errorf("%w: xattr name list changed", ErrUnsupported)
	}
	result := make(map[string][]byte)
	total := 0
	names := 0
	for _, name := range strings.Split(string(buffer[:count]), "\x00") {
		if name == "" {
			continue
		}
		names++
		if names > maxListNames || len(name) > maxListName {
			return nil, fmt.Errorf("%w: xattr name count or size exceeds bound", ErrUnsupported)
		}
		size, sizeErr := unix.Getxattr(path, name, nil)
		if sizeErr != nil {
			return nil, sizeErr
		}
		if size < 0 || size > maxListValue || total+size > maxListTotal {
			return nil, fmt.Errorf("%w: xattr value size exceeds bound", ErrUnsupported)
		}
		value := make([]byte, size)
		if _, getErr := unix.Getxattr(path, name, value); getErr != nil {
			return nil, getErr
		}
		result[name] = value
		total += size
	}
	return result, nil
}

func Set(path, name string, value []byte) error {
	return unix.Setxattr(path, name, value, 0)
}

func Remove(path, name string) error {
	err := unix.Removexattr(path, name)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
