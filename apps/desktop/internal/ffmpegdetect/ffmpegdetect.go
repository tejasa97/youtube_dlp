// Package ffmpegdetect probes the ffmpeg tool pair used by the desktop app.
// The resolved setting is passed to the focused engine composition at each
// analyze/download.
// request; this package does not mutate PATH or any process-global state.
package ffmpegdetect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Status describes the resolved ffmpeg state.
type Status struct {
	Available   bool   `json:"available"`
	Path        string `json:"path"`
	Version     string `json:"version"`
	FFprobePath string `json:"ffprobePath"`
	Message     string `json:"message"`
}

// Probe inspects the requested path (if non-empty) and then falls back
// to PATH discovery. A short context timeout bounds the call so the UI
// stays responsive on misconfigured machines.
func Probe(ctx context.Context, requested string) Status {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	if strings.TrimSpace(requested) != "" {
		return probeConfigured(ctx, requested)
	}

	return probePATH(ctx)
}

func probeConfigured(ctx context.Context, requested string) Status {
	ffmpegPath, ffprobePath := configuredTools(requested)
	status := Status{Path: ffmpegPath, FFprobePath: ffprobePath}
	if ok, version := probeVersion(ctx, ffmpegPath); !ok {
		status.Message = "ffmpeg was not found at the configured path"
		return status
	} else {
		status.Version = version
	}
	if !probeRunnable(ctx, ffprobePath) {
		status.Message = "ffmpeg was found, but matching ffprobe was not found beside it"
		return status
	}
	status.Available = true
	status.Message = "ffmpeg configured"
	return status
}

func probePATH(ctx context.Context) Status {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return Status{Message: "ffmpeg was not found on PATH"}
	}
	status := Status{Path: ffmpegPath}
	if ok, version := probeVersion(ctx, ffmpegPath); !ok {
		status.Message = "ffmpeg on PATH did not run successfully"
		return status
	} else {
		status.Version = version
	}
	ffprobePath, err := exec.LookPath("ffprobe")
	if err != nil {
		status.Message = "ffmpeg was detected, but ffprobe was not found on PATH"
		return status
	}
	status.FFprobePath = ffprobePath
	if !probeRunnable(ctx, ffprobePath) {
		status.Message = "ffmpeg was detected, but ffprobe on PATH did not run successfully"
		return status
	}
	status.Available = true
	status.Message = "ffmpeg detected"
	return status
}

// ConfigurePath validates a user-supplied path without persisting it.
// Persistence is the responsibility of the caller (the settings store).
func ConfigurePath(ctx context.Context, path string) Status {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	path = strings.TrimSpace(path)
	if path == "" {
		return Status{Message: "no path provided"}
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return Status{Available: false, Path: path, Message: "could not resolve that path"}
	}
	return probeConfigured(ctx, resolved)
}

func configuredTools(requested string) (string, string) {
	path := requested
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return filepath.Join(path, executableName("ffmpeg")), filepath.Join(path, executableName("ffprobe"))
	}
	return path, filepath.Join(filepath.Dir(path), executableName("ffprobe"))
}

func executableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func probeRunnable(ctx context.Context, path string) bool {
	if path == "" {
		return false
	}
	ok, _ := probeVersion(ctx, path)
	return ok
}

func probeVersion(ctx context.Context, path string) (bool, string) {
	cmd := exec.CommandContext(ctx, path, "-version")
	out, err := cmd.Output()
	if err != nil {
		return false, ""
	}
	first := firstNonEmptyLine(string(out))
	if first == "" {
		return false, ""
	}
	return true, first
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}
