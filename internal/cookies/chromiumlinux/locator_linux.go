//go:build linux

package chromiumlinux

import (
	"os"
	"path/filepath"
)

func defaultProfileRoot(directory string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ErrNotFound
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	return filepath.Join(config, directory), nil
}
