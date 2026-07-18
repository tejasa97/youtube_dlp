//go:build !windows

package update

import (
	"os/exec"
	"syscall"
)

func configureHealthCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateHealthCommand(command *exec.Cmd) {
	if command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
}

func healthEnvironment() []string { return []string{"LANG=C", "LC_ALL=C"} }
