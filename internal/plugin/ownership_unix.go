//go:build unix

package plugin

import (
	"fmt"
	"os"
	"syscall"
)

func verifyTrustedOwner(info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: cannot determine Unix owner", ErrUntrustedPath)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: path is not owned by the current user", ErrUntrustedPath)
	}
	return nil
}
