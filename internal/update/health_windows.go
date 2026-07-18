//go:build windows

package update

import "os/exec"

func configureHealthCommand(*exec.Cmd) {}

func terminateHealthCommand(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}

func healthEnvironment() []string { return []string{} }
