//go:build windows

package rpc

import (
	"fmt"
	"os/exec"
	"unsafe"

	"github.com/ytdlp-go/ytdlp/internal/plugin"
	"golang.org/x/sys/windows"
)

type processIsolation struct {
	job windows.Handle
}

func configureIsolation(*exec.Cmd) error { return nil }

func attachIsolation(command *exec.Cmd) (*processIsolation, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: create Windows job: %v", plugin.ErrIsolationUnavailable, err)
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("%w: configure Windows job: %v", plugin.ErrIsolationUnavailable, err)
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("%w: open plugin process: %v", plugin.ErrIsolationUnavailable, err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("%w: assign Windows job: %v", plugin.ErrIsolationUnavailable, err)
	}
	return &processIsolation{job: job}, nil
}

func (isolation *processIsolation) Terminate() error {
	if isolation == nil || isolation.job == 0 {
		return plugin.ErrIsolationUnavailable
	}
	return windows.TerminateJobObject(isolation.job, 1)
}

func (isolation *processIsolation) Close() error {
	if isolation == nil || isolation.job == 0 {
		return nil
	}
	err := windows.CloseHandle(isolation.job)
	isolation.job = 0
	return err
}
