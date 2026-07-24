//go:build !windows

package youtubeump

import "os"

func replaceDestination(source, destination string) error {
	return os.Rename(source, destination)
}

func installDestination(source, destination string) error {
	if err := os.Link(source, destination); err != nil {
		return err
	}
	return os.Remove(source)
}
