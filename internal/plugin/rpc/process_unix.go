//go:build unix

package rpc

import (
	"fmt"
	"os/exec"
	"syscall"

	"github.com/ytdlp-go/ytdlp/internal/plugin"
)

type processIsolation struct {
	processGroup int
}

func configureIsolation(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func attachIsolation(command *exec.Cmd) (*processIsolation, error) {
	if command.Process == nil || command.Process.Pid <= 0 {
		return nil, fmt.Errorf("%w: missing process group", plugin.ErrIsolationUnavailable)
	}
	return &processIsolation{processGroup: command.Process.Pid}, nil
}

func (isolation *processIsolation) Terminate() error {
	if isolation == nil || isolation.processGroup <= 0 {
		return plugin.ErrIsolationUnavailable
	}
	if err := syscall.Kill(-isolation.processGroup, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

func (*processIsolation) Close() error { return nil }
