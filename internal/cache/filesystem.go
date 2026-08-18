//go:build !windows

package cache

import (
	"errors"
	"fmt"
	"os"
)

func secureDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("%w: create directory", ErrIO)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("%w: inspect directory", ErrIO)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("%w: secure directory", ErrIO)
	}
	return nil
}

func secureExistingFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func secureFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("%w: secure file", ErrIO)
	}
	return nil
}

func secureExistingDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	return nil
}

func rejectExistingNonRegular(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect entry", ErrIO)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	return secureFile(path)
}

func removeRegular(path string) error {
	if err := rejectExistingNonRegular(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: remove entry", ErrIO)
	}
	return nil
}
