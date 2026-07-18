//go:build !windows

package downloader

import "os"

func replaceDestination(source, destination string) error {
	return os.Rename(source, destination)
}

// Linking first provides atomic create-if-absent semantics and closes the
// race between the destination recheck and finalization. The part file is in
// the destination directory, so this cannot cross filesystems.
func installDestination(source, destination string) error {
	if err := os.Link(source, destination); err != nil {
		return err
	}
	return os.Remove(source)
}
