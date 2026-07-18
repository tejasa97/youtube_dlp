package update

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"time"
)

// CommandHealthChecker runs the staged binary directly, never through a shell,
// and requires its bounded output to identify the selected version exactly.
type CommandHealthChecker struct {
	Arguments    []string
	OutputPrefix string
	Timeout      time.Duration
	MaxOutput    int
	attach       func(*exec.Cmd) (healthProcessIsolation, error)
}

type healthProcessIsolation interface {
	Terminate() error
	Close() error
}

func (checker CommandHealthChecker) Check(ctx context.Context, path string, target Target) error {
	if checker.Timeout <= 0 || checker.Timeout > time.Minute || checker.MaxOutput <= 0 || checker.MaxOutput > 1<<20 || len(checker.Arguments) > 64 || len(checker.OutputPrefix) > 256 {
		return ErrHealth
	}
	for _, argument := range checker.Arguments {
		if len(argument) > 4096 || strings.IndexByte(argument, 0) >= 0 {
			return ErrHealth
		}
	}
	deadlineContext, cancel := context.WithTimeout(ctx, checker.Timeout)
	defer cancel()
	command := exec.Command(path, checker.Arguments...)
	command.Env = healthEnvironment()
	if err := configureHealthCommand(command); err != nil {
		return ErrHealth
	}
	output := &limitedBuffer{maximum: checker.MaxOutput}
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		return ErrHealth
	}
	attach := checker.attach
	if attach == nil {
		attach = func(command *exec.Cmd) (healthProcessIsolation, error) { return attachHealthCommand(command) }
	}
	isolation, err := attach(command)
	if err != nil {
		killAndReap(command)
		return ErrHealth
	}
	defer isolation.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-deadlineContext.Done():
		_ = isolation.Terminate()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-done:
			timer.Stop()
		case <-timer.C:
		}
		return ErrHealth
	case err := <-done:
		if err != nil || output.exceeded || strings.TrimSpace(output.String()) != checker.OutputPrefix+target.Version {
			return ErrHealth
		}
		return nil
	}
}

func killAndReap(command *exec.Cmd) {
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	done := make(chan struct{}, 1)
	go func() {
		_ = command.Wait()
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
	}
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	maximum  int
	exceeded bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	if buffer.exceeded {
		return len(value), nil
	}
	remaining := buffer.maximum - buffer.buffer.Len()
	if len(value) > remaining {
		if remaining > 0 {
			_, _ = buffer.buffer.Write(value[:remaining])
		}
		buffer.exceeded = true
		return len(value), nil
	}
	return buffer.buffer.Write(value)
}

func (buffer *limitedBuffer) String() string { return buffer.buffer.String() }
