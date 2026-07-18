//go:build windows

package update

import (
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

type healthIsolation struct{ job windows.Handle }

func configureHealthCommand(*exec.Cmd) error { return nil }

func attachHealthCommand(command *exec.Cmd) (*healthIsolation, error) {
	if command.Process == nil || command.Process.Pid <= 0 {
		return nil, ErrHealth
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&information)), uint32(unsafe.Sizeof(information))); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		windows.CloseHandle(job)
		return nil, err
	}
	return &healthIsolation{job: job}, nil
}

func (isolation *healthIsolation) Terminate() error {
	if isolation == nil || isolation.job == 0 {
		return ErrHealth
	}
	return windows.TerminateJobObject(isolation.job, 1)
}

func (isolation *healthIsolation) Close() error {
	if isolation == nil || isolation.job == 0 {
		return nil
	}
	err := windows.CloseHandle(isolation.job)
	isolation.job = 0
	return err
}

func healthEnvironment() []string { return []string{} }
