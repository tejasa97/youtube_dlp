//go:build windows

package update

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var (
	windowsJobMode   = flag.String("ytdlp-windows-job-mode", "", "internal updater job test mode")
	windowsJobGo     = flag.String("ytdlp-windows-job-go", "", "internal updater job start signal")
	windowsJobReady  = flag.String("ytdlp-windows-job-ready", "", "internal updater job ready signal")
	windowsJobMarker = flag.String("ytdlp-windows-job-marker", "", "internal updater job descendant marker")
)

func TestWindowsJobHelper(t *testing.T) {
	switch *windowsJobMode {
	case "parent":
		waitForFile(*windowsJobGo, 5*time.Second)
		command := exec.Command(os.Args[0], "-test.run=TestWindowsJobHelper", "-ytdlp-windows-job-mode=descendant", "-ytdlp-windows-job-marker="+*windowsJobMarker)
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
	command := exec.Command(os.Args[0], "-test.run=TestWindowsJobHelper", "-ytdlp-windows-job-mode=parent", "-ytdlp-windows-job-go="+goPath, "-ytdlp-windows-job-ready="+readyPath, "-ytdlp-windows-job-marker="+markerPath)
	if err := configureHealthCommand(command); err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	isolation, err := attachHealthCommand(command)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal(err)
	}
	defer isolation.Close()
	if err := os.WriteFile(goPath, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitForFile(readyPath, 5*time.Second) {
		_ = isolation.Terminate()
		_ = command.Wait()
		t.Fatal("descendant did not start")
	}
	if err := isolation.Terminate(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	time.Sleep(time.Second)
	if _, err := os.Stat(markerPath); err == nil {
		t.Fatal("health-check descendant escaped the Windows Job Object")
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
