//go:build !darwin && !linux && !windows

package ytdlp

import "os"

func openPrintAppendFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
}
