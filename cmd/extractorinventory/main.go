package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/upstreamdelta"
)

func main() {
	reference := flag.String("reference", "", "path to a read-only yt-dlp reference checkout")
	repository := flag.String("repository", ".", "path to the Go repository")
	output := flag.String("output", "", "CSV output path (stdout when empty)")
	flag.Parse()

	if *reference == "" {
		fail("-reference is required")
	}
	commit, err := gitCommit(*reference)
	if err != nil {
		fail("read reference commit: %v", err)
	}
	entries, err := upstreamdelta.BuildExtractorInventory(*reference, *repository)
	if err != nil {
		fail("build inventory: %v", err)
	}

	writer := os.Stdout
	var temporaryPath string
	if *output != "" {
		path, err := filepath.Abs(*output)
		if err != nil {
			fail("resolve output: %v", err)
		}
		file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
		if err != nil {
			fail("create output: %v", err)
		}
		temporaryPath = file.Name()
		defer os.Remove(temporaryPath)
		writer = file
	}
	if err := upstreamdelta.WriteExtractorInventoryCSV(writer, entries); err != nil {
		fail("write inventory: %v", err)
	}
	if *output != "" {
		file := writer
		if err := file.Sync(); err != nil {
			fail("sync output: %v", err)
		}
		if err := file.Chmod(0o644); err != nil {
			fail("set output permissions: %v", err)
		}
		if err := file.Close(); err != nil {
			fail("close output: %v", err)
		}
		path, err := filepath.Abs(*output)
		if err != nil {
			fail("resolve output: %v", err)
		}
		if err := os.Rename(temporaryPath, path); err != nil {
			fail("publish output: %v", err)
		}
		temporaryPath = ""
		summary := upstreamdelta.SummarizeExtractorInventory(commit, entries)
		keys := make([]string, 0, len(summary.Counts))
		for key := range summary.Counts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fmt.Fprintf(os.Stderr, "reference=%s total=%d\n", commit, summary.Total)
		for _, key := range keys {
			fmt.Fprintf(os.Stderr, "%s=%d\n", key, summary.Counts[key])
		}
	}
}

func gitCommit(root string) (string, error) {
	command := exec.Command("git", "-C", root, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func fail(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "extractorinventory: "+format+"\n", arguments...)
	os.Exit(1)
}
