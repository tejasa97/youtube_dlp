//go:build windows

package rpc

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"github.com/tejasa97/youtube_dlp/internal/plugin"
	"github.com/tejasa97/youtube_dlp/internal/sandbox"
	"golang.org/x/sys/windows"
)

type processIsolation struct {
	job windows.Handle
}

// configureIsolation creates the process suspended. attachIsolation assigns it
// to the kill-on-close Job and only then resumes its initial thread, closing the
// former start-before-Job escape window.
func configureIsolation(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	return nil
}

func attachIsolation(command *exec.Cmd, limits sandbox.Limits) (*processIsolation, error) {
	if command.Process == nil || command.Process.Pid <= 0 {
		return nil, plugin.ErrIsolationUnavailable
	}
	// Windows Job Objects do not impose a portable file-descriptor/output cap.
	// Refuse a policy that asks for one rather than silently dropping it.
	if limits.OpenFiles != 0 {
		return nil, fmt.Errorf("%w: Windows Job Objects do not support descriptor limits", plugin.ErrIsolationUnavailable)
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: create Windows job: %v", plugin.ErrIsolationUnavailable, err)
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if limits.AddressSpaceBytes != 0 {
		information.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY
		information.ProcessMemoryLimit = uintptr(limits.AddressSpaceBytes)
	}
	if limits.Processes != 0 {
		information.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS
		information.BasicLimitInformation.ActiveProcessLimit = uint32(limits.Processes)
	}
	if limits.CPUSeconds != 0 {
		information.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_PROCESS_TIME
		// Job APIs use 100ns units of user CPU time.
		information.BasicLimitInformation.PerProcessUserTimeLimit = int64(limits.CPUSeconds) * 10_000_000
	}
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
	if err := resumeInitialThread(uint32(command.Process.Pid)); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
		return nil, fmt.Errorf("%w: resume suspended plugin: %v", plugin.ErrIsolationUnavailable, err)
	}
	return &processIsolation{job: job}, nil
}

func resumeInitialThread(processID uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != processID {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return openErr
		}
		_, resumeErr := windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		return resumeErr
	}
	return err
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
