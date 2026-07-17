//go:build windows

package ffmpeg

import "os/exec"

func configureCommand(*exec.Cmd) {}

func terminateCommand(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}
