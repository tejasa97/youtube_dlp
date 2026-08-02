//go:build !windows

package ffmpeg

import (
	"os/exec"
	"syscall"
)

type processIsolation struct{}

func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attachCommand(*exec.Cmd) (*processIsolation, error) {
	return &processIsolation{}, nil
}

func terminateCommand(command *exec.Cmd, isolation *processIsolation) {
	if command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
}

func closeCommand(*processIsolation) {}
