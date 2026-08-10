//go:build !windows

package atomicfile

import "os"

func platformReplace(source, destination string) error {
	return os.Rename(source, destination)
}

func platformSyncParent(directory string) error {
	parent, err := os.Open(directory)
	if err != nil {
		return err
	}
	if err := parent.Sync(); err != nil {
		_ = parent.Close()
		return err
	}
	return parent.Close()
}
