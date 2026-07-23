package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/testserver"
)

func TestPrintToFileAppendsStagesAndReportsUniqueArtifact(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, SkipDownload: true,
		PrintRules: []PrintRule{
			{Stage: PrintVideo, Template: "%(title)s", FileTemplate: "logs/%(id)s.txt"},
			{Stage: PrintAfterVideo, Template: "%(id)s", FileTemplate: "logs/%(id)s.txt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "logs", "fixture-direct.txt")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "Deterministic Fixture\nfixture-direct\n"
	if string(body) != want {
		t.Fatalf("body=%q want=%q", body, want)
	}
	if len(result.Prints) != 0 || len(result.Artifacts) != 1 ||
		result.Artifacts[0] != (Artifact{Path: path, Kind: "print"}) ||
		result.Bytes != int64(len(want)) || !result.Downloaded {
		t.Fatalf("result=%#v", result)
	}

	if _, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, SkipDownload: true,
		PrintRules: []PrintRule{{
			Stage: PrintVideo, Template: "%(id)s", FileTemplate: "logs/%(id)s.txt",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(path)
	if string(body) != want+"fixture-direct\n" {
		t.Fatalf("append body=%q", body)
	}
}

func TestPrintToFileSimulationSuppressesDiskButKeepsConsoleCapture(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, Simulate: true,
		PrintRules: []PrintRule{
			{Stage: PrintVideo, Template: "%(title)s"},
			{Stage: PrintVideo, Template: "%(title)s", FileTemplate: "report.txt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Prints) != 1 || result.Prints[0].Text != "Deterministic Fixture" ||
		len(result.Artifacts) != 0 || result.Downloaded {
		t.Fatalf("result=%#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "report.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("simulation wrote report: %v", err)
	}
}

func TestPrintToFileRejectsUnsafePathsAndSymlinks(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	_, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, SkipDownload: true,
		PrintRules: []PrintRule{{
			Stage: PrintVideo, Template: "%(title)s", FileTemplate: "../escape.txt",
		}},
	})
	if err == nil || !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("unsafe template error=%v", err)
	}

	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "report.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err = NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, SkipDownload: true,
		PrintRules: []PrintRule{{
			Stage: PrintVideo, Template: "%(title)s", FileTemplate: "report.txt",
		}},
	})
	if err == nil || !IsCategory(err, ErrorSecurity) {
		t.Fatalf("symlink destination error=%v", err)
	}
	body, _ := os.ReadFile(target)
	if string(body) != "secret" {
		t.Fatalf("symlink target changed: %q", body)
	}
}

func TestAppendPrintLineCancellationLimitAndConcurrentRecords(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "records.txt")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := appendPrintLine(ctx, path, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	if _, err := appendPrintLine(context.Background(), path, strings.Repeat("x", 1<<20)); err == nil {
		t.Fatal("oversized line accepted")
	}

	const records = 32
	var wait sync.WaitGroup
	errorsSeen := make(chan error, records)
	for index := 0; index < records; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := appendPrintLine(context.Background(), path, "bounded-record")
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(lines) != records {
		t.Fatalf("record count=%d body=%q", len(lines), body)
	}
	for _, line := range lines {
		if line != "bounded-record" {
			t.Fatalf("interleaved record=%q", line)
		}
	}
}
