//go:build !windows

package ffmpeg

import (
	"os/exec"
	"syscall"
)

type processIsolation struct {
	pgid int
}

func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attachCommand(command *exec.Cmd) (*processIsolation, error) {
	if command.Process == nil || command.Process.Pid <= 0 {
		return nil, ErrMediaFailure
	}
	// Setpgid makes the child its own group leader, so its PID is the stable
	// process-group identifier while the immediate child remains unreaped.
	return &processIsolation{pgid: command.Process.Pid}, nil
}

func terminateCommand(command *exec.Cmd, isolation *processIsolation) {
	pgid := 0
	if isolation != nil {
		pgid = isolation.pgid
	}
	if pgid == 0 && command.Process != nil {
		pgid = command.Process.Pid
	}
	if pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}
}

func closeCommand(isolation *processIsolation) {
	if isolation == nil || isolation.pgid <= 0 {
		return
	}
	// A parent can exit while an ffmpeg protocol helper that closed inherited
	// stdio stays alive. command.Wait only reaps that parent, so terminate any
	// remaining group members before returning to the caller.
	_ = syscall.Kill(-isolation.pgid, syscall.SIGKILL)
	isolation.pgid = 0
}
