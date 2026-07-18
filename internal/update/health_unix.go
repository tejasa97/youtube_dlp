//go:build !windows

package update

import (
	"errors"
	"os/exec"
	"syscall"
)

type healthIsolation struct{ processGroup int }

func configureHealthCommand(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func attachHealthCommand(command *exec.Cmd) (*healthIsolation, error) {
	if command.Process == nil || command.Process.Pid <= 0 {
		return nil, ErrHealth
	}
	return &healthIsolation{processGroup: command.Process.Pid}, nil
}

func (isolation *healthIsolation) Terminate() error {
	if isolation == nil || isolation.processGroup <= 0 {
		return ErrHealth
	}
	if err := syscall.Kill(-isolation.processGroup, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func (*healthIsolation) Close() error { return nil }

func healthEnvironment() []string { return []string{"LANG=C", "LC_ALL=C"} }
