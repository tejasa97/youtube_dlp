package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/pkg/ytdlp"
)

type discoveryRunner struct {
	called *bool
}

func (runner discoveryRunner) Run(context.Context, ytdlp.Request) (ytdlp.Result, error) {
	*runner.called = true
	return ytdlp.Result{}, errors.New("discovery unexpectedly constructed a runner")
}

type trackingReader struct {
	called bool
	data   []byte
}

func (reader *trackingReader) Read(buffer []byte) (int, error) {
	reader.called = true
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	count := copy(buffer, reader.data)
	reader.data = reader.data[count:]
	return count, nil
}

type failingDiscoveryWriter struct{}

func (failingDiscoveryWriter) Write([]byte) (int, error) { return 0, errors.New("writer failed") }

func TestRunExtractorDiscoveryUsesOfflineSuitableMatchingAndStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var called bool
	stdin := &trackingReader{data: []byte("https://input.example.invalid/ignored\n")}
	code := runContextIOWithDependencies(context.Background(), []string{
		"--ignore-config", "--list-extractors", "--no-simulate", "--skip-download",
		"--batch-file", "-", "https://www.youtube.com/watch?v=fixture0001",
	}, stdin, &stdout, &stderr, runDependencies{
		newRunner: func([]ytdlp.Option) cliRunner { return discoveryRunner{called: &called} },
	})
	if code != 0 {
		t.Fatalf("code = %d; stderr = %q", code, stderr.String())
	}
	if called {
		t.Fatal("normal runner was constructed for extractor discovery")
	}
	if !stdin.called {
		t.Fatal("extractor discovery did not read batch stdin")
	}
	if stderr.Len() != 0 || !strings.Contains(stdout.String(), "  https://input.example.invalid/ignored\n") ||
		!strings.Contains(stdout.String(), "youtube\n  https://www.youtube.com/watch?v=fixture0001\n") {
		t.Fatalf("channels/suitable matching: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.HasPrefix(line, "  ") {
			names = append(names, line)
		}
	}
	if len(names) == 0 || names[len(names)-1] != "generic" {
		t.Fatalf("listing order ends with %q", names[len(names)-1])
	}
	for index := 1; index < len(names); index++ {
		if strings.ToLower(names[index-1]) > strings.ToLower(names[index]) && names[index] != "generic" {
			t.Fatalf("listing order is unstable at %d: %q before %q", index, names[index-1], names[index])
		}
	}

	var noRunnerOutput, noRunnerErrors bytes.Buffer
	if code := runContextIOWithDependencies(context.Background(), []string{"--ignore-config", "--list-extractors"}, strings.NewReader(""), &noRunnerOutput, &noRunnerErrors, runDependencies{}); code != 0 {
		t.Fatalf("discovery without a runner code=%d stdout=%q stderr=%q", code, noRunnerOutput.String(), noRunnerErrors.String())
	}
}

func TestRunExtractorDescriptionsAreOfflineAndBounded(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--ignore-config", "--extractor-descriptions", "not-a-url"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) == 0 || lines[0] != "abcotvs" || lines[len(lines)-1] != "generic: Generic downloader that works on some sites" {
		t.Fatalf("description boundaries = %#v", lines[:min(len(lines), 2)])
	}
	for _, line := range lines {
		if len(line) > 256+len("generic: ") {
			t.Fatalf("unbounded description line length=%d: %q", len(line), line)
		}
	}
}

func TestWriteExtractorDiscoveryOmitsAliasesAndPropagatesWriterErrors(t *testing.T) {
	metadata := []ytdlp.ExtractorMetadata{{Name: "fixture", Aliases: []string{"fixture-alias"}, Description: "bounded description"}}
	var output bytes.Buffer
	if err := writeExtractorDiscovery(context.Background(), false, metadata, &output); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "fixture: bounded description\n"; got != want {
		t.Fatalf("output=%q want=%q", got, want)
	}
	if err := writeExtractorDiscovery(context.Background(), false, metadata, failingDiscoveryWriter{}); err == nil || !strings.Contains(err.Error(), "writer failed") {
		t.Fatalf("writer error = %v", err)
	}
}

func TestRunExtractorDiscoveryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	code := runExtractorDiscovery(ctx, true, ytdlp.BuiltInExtractorMetadata(), &stdout, &stderr)
	if code != 130 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
