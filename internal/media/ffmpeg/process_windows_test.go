//go:build windows

package ffmpeg

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var (
	windowsJobMode   = flag.String("ytdlp-ffmpeg-windows-job-mode", "", "internal ffmpeg job test mode")
	windowsJobGo     = flag.String("ytdlp-ffmpeg-windows-job-go", "", "internal ffmpeg job start signal")
	windowsJobReady  = flag.String("ytdlp-ffmpeg-windows-job-ready", "", "internal ffmpeg job ready signal")
	windowsJobMarker = flag.String("ytdlp-ffmpeg-windows-job-marker", "", "internal ffmpeg job descendant marker")
)

func TestWindowsJobHelper(t *testing.T) {
	switch *windowsJobMode {
	case "parent":
		waitForFile(*windowsJobGo, 5*time.Second)
		command := exec.Command(os.Args[0], "-test.run=TestWindowsJobHelper", "-ytdlp-ffmpeg-windows-job-mode=descendant", "-ytdlp-ffmpeg-windows-job-marker="+*windowsJobMarker)
		if err := command.Start(); err != nil {
			os.Exit(21)
		}
		if err := os.WriteFile(*windowsJobReady, []byte("ready"), 0o600); err != nil {
			os.Exit(22)
		}
		select {}
	case "descendant":
		time.Sleep(750 * time.Millisecond)
		if err := os.WriteFile(*windowsJobMarker, []byte("escaped"), 0o600); err != nil {
			os.Exit(23)
		}
		os.Exit(0)
	}
}

func TestWindowsJobTerminatesDescendants(t *testing.T) {
	directory := t.TempDir()
	goPath := filepath.Join(directory, "go")
	readyPath := filepath.Join(directory, "ready")
	markerPath := filepath.Join(directory, "marker")
	command := exec.Command(os.Args[0], "-test.run=TestWindowsJobHelper", "-ytdlp-ffmpeg-windows-job-mode=parent", "-ytdlp-ffmpeg-windows-job-go="+goPath, "-ytdlp-ffmpeg-windows-job-ready="+readyPath, "-ytdlp-ffmpeg-windows-job-marker="+markerPath)
	configureCommand(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	isolation, err := attachCommand(command)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal(err)
	}
	defer closeCommand(isolation)
	if err := os.WriteFile(goPath, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitForFile(readyPath, 5*time.Second) {
		terminateCommand(command, isolation)
		_ = command.Wait()
		t.Fatal("descendant did not start")
	}
	terminateCommand(command, isolation)
	_ = command.Wait()
	time.Sleep(time.Second)
	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("ffmpeg descendant escaped the Windows Job Object")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func waitForFile(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
