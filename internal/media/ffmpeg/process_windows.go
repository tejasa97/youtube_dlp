//go:build windows

package ffmpeg

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

type processIsolation struct {
	job windows.Handle
}

func configureCommand(*exec.Cmd) {}

// attachCommand creates a Windows Job Object with KILL_ON_JOB_CLOSE and assigns
// the already-started process to it. There is a residual start-then-assign race
// (G2-S01): descendants spawned between Start and this call are not covered.
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
	return &processIsolation{job: job}, nil
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
