//go:build windows

package ffmpeg

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processIsolation struct {
	job windows.Handle
}

// configureCommand starts the child CREATE_SUSPENDED so no user code runs
// until attachCommand assigns it to a Job Object and resumes the primary
// thread. That closes the G2-S01 start-then-assign race.
func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
}

// attachCommand creates a KILL_ON_JOB_CLOSE Job, assigns the already-started
// (suspended) process to it, then resumes the primary thread. On any failure
// the suspended process is terminated via the Job when assigned, otherwise via
// Process.Kill by the caller after this returns an error.
func attachCommand(command *exec.Cmd) (*processIsolation, error) {
	if command.Process == nil || command.Process.Pid <= 0 {
		return nil, ErrMediaFailure
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create Windows job: %v", err)
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
		return nil, fmt.Errorf("set Windows job info: %v", err)
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("open Windows process %d: %v", command.Process.Pid, err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		windows.CloseHandle(job)
		return nil, fmt.Errorf("assign process to Windows job: %v", err)
	}
	if err := resumeSuspendedProcess(uint32(command.Process.Pid)); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
		return nil, fmt.Errorf("resume suspended process: %v", err)
	}
	return &processIsolation{job: job}, nil
}

// resumeSuspendedProcess resumes the primary thread of a CREATE_SUSPENDED
// process. Go's os/exec closes the CreateProcess thread handle, so the thread
// is located via a Toolhelp snapshot; with CREATE_SUSPENDED no user code has
// run yet, so the first owned thread is the primary thread.
func resumeSuspendedProcess(processID uint32) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("create thread snapshot: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != processID {
			continue
		}
		thread, openErr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if openErr != nil {
			return fmt.Errorf("open thread %d: %w", entry.ThreadID, openErr)
		}
		defer windows.CloseHandle(thread)
		if _, resumeErr := windows.ResumeThread(thread); resumeErr != nil {
			return fmt.Errorf("resume thread %d: %w", entry.ThreadID, resumeErr)
		}
		return nil
	}
	return fmt.Errorf("no thread found for process %d: %w", processID, err)
}

func terminateCommand(command *exec.Cmd, isolation *processIsolation) {
	if isolation == nil || isolation.job == 0 {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		return
	}
	_ = windows.TerminateJobObject(isolation.job, 1)
}

func closeCommand(isolation *processIsolation) {
	if isolation == nil || isolation.job == 0 {
		return
	}
	_ = windows.CloseHandle(isolation.job)
	isolation.job = 0
}
