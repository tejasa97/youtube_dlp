package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/testserver"
	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

func TestParseByteQuantity(t *testing.T) {
	for _, test := range []struct {
		input string
		want  int64
	}{
		{"44.6M", 46766490}, // round(44.6 * 2^20) with round-to-even
		{"50k", 51200},
		{"1G", 1 << 30},
		{"2", 2},
		{"1.5T", 1649267441664},
		{"0.5m", 524288},
		{"10", 10},
		{"1K", 1024},
		{"1P", 1 << 50},             // 1024^5
		{"1E", 1 << 60},             // 1024^6
		{"7E", 8070450532247928832}, // largest int64-representable E multiple
		{"1.5P", 1688849860263936},  // round(1.5 * 2^50)
	} {
		got, ok := parseByteQuantity(test.input)
		if !ok || got != test.want {
			t.Fatalf("parseByteQuantity(%q) = %d, %v; want %d", test.input, got, ok, test.want)
		}
	}
	for _, input := range []string{"", "abc", "-1M", "1.5.2M", "1MB", "1 B", "k", "44.6M "} {
		if _, ok := parseByteQuantity(input); ok {
			t.Fatalf("parseByteQuantity(%q) accepted", input)
		}
	}
	// Z/Y units and any product beyond int64 must be rejected, never wrapped
	// to zero (which downstream would treat as unlimited).
	for _, input := range []string{"1Z", "1Y", "0.1Y", "8E", "9E", "8192P", "1.5Z"} {
		if parsed, ok := parseByteQuantity(input); ok {
			t.Fatalf("parseByteQuantity(%q) = %d; want rejection (exceeds int64)", input, parsed)
		}
	}
}

func TestRunDatePrecedenceWarning(t *testing.T) {
	runner := &recordingCLIRunner{}
	var stdout, stderr bytes.Buffer
	code := runContextIOWithDependencies(
		context.Background(), []string{"--date", "20240101", "--dateafter", "20240201", "--datebefore", "20240301", "https://fixture.invalid/video"},
		strings.NewReader(""), &stdout, &stderr,
		runDependencies{newRunner: func([]ytdlp.Option) cliRunner { return runner }},
	)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--dateafter is ignored since --date was given") ||
		!strings.Contains(stderr.String(), "--datebefore is ignored since --date was given") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunForceWriteArchiveAliases(t *testing.T) {
	for _, flag := range []string{"--force-write-archive", "--force-write-download-archive", "--force-download-archive"} {
		t.Run(flag, func(t *testing.T) {
			runner := &captureCLIRunner{}
			var stdout, stderr bytes.Buffer
			code := runContextIOWithDependencies(
				context.Background(), []string{flag, "--skip-download", "https://fixture.invalid/video"},
				strings.NewReader(""), &stdout, &stderr,
				runDependencies{newRunner: func([]ytdlp.Option) cliRunner { return runner }},
			)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if !runner.request.ForceWriteArchive {
				t.Fatalf("%s did not set ForceWriteArchive: %+v", flag, runner.request)
			}
		})
	}
}

func TestRunHelpOmitsHiddenFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	// Go's flag package renders names with a single dash.
	help := stdout.String() + stderr.String()
	for _, visible := range []string{"-max-downloads", "-break-on-existing", "-age-limit", "-min-filesize", "-dateafter"} {
		if !strings.Contains(help, visible) {
			t.Fatalf("help omits visible flag %q", visible)
		}
	}
	for _, hidden := range []string{"-match-title", "-reject-title", "-min-views", "-max-views", "-break-on-reject"} {
		if strings.Contains(help, hidden) {
			t.Fatalf("help exposes hidden flag %q", hidden)
		}
	}
}

func TestRunMaxDownloadsBudgetAcrossInputs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "urls.txt")
	if err := os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (int, []string, string) {
		runner := &recordingCLIRunner{}
		var stdout, stderr bytes.Buffer
		code := runContextIOWithDependencies(
			context.Background(), append([]string{"--batch-file", file}, args...), strings.NewReader(""), &stdout, &stderr,
			runDependencies{newRunner: func([]ytdlp.Option) cliRunner {
				return &downloadsCLIRunner{runner: runner}
			}},
		)
		return code, runner.urls, stderr.String()
	}
	// One download per input; without --break-per-input the remaining budget
	// shrinks and the run aborts with 101 after the cap is consumed.
	code, urls, stderr := run("--max-downloads", "2")
	if code != 101 || len(urls) != 2 || !strings.Contains(stderr, "Aborting remaining downloads") {
		t.Fatalf("code=%d urls=%v stderr=%q", code, urls, stderr)
	}
	// --break-per-input resets the budget, so every input downloads.
	code, urls, stderr = run("--max-downloads", "2", "--break-per-input")
	if code != 0 || len(urls) != 3 {
		t.Fatalf("break-per-input code=%d urls=%v stderr=%q", code, urls, stderr)
	}
}

func TestRunStopConditionExits101(t *testing.T) {
	run := func(stopped bool, breakPerInput bool) int {
		var stdout, stderr bytes.Buffer
		args := []string{"--batch-file", "-"}
		if breakPerInput {
			args = append(args, "--break-per-input")
		}
		code := runContextIOWithDependencies(
			context.Background(), args, strings.NewReader("one\ntwo\n"), &stdout, &stderr,
			runDependencies{newRunner: func([]ytdlp.Option) cliRunner {
				return resultCLIRunner{result: ytdlp.Result{Stopped: stopped, StopKind: ytdlp.StopBreakOnReject, StopReason: "rejected"}}
			}},
		)
		return code
	}
	if code := run(true, false); code != 101 {
		t.Fatalf("stopped without break-per-input = %d", code)
	}
	if code := run(true, true); code != 0 {
		t.Fatalf("stopped with break-per-input = %d", code)
	}
}

func TestRunFilesizeAbortPrintsDiagnostic(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--max-filesize", "100k", "--output-dir", t.TempDir(), server.URL + "/page"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "File is larger than max-filesize (262144 bytes > 102400 bytes)") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

// downloadsCLIRunner returns one qualifying download per input and stops
// when the per-Run MaxDownloads cap is consumed, mirroring the real client's
// accounting so the CLI budget behavior is observable.
type downloadsCLIRunner struct {
	runner *recordingCLIRunner
	count  int
}

func (r *downloadsCLIRunner) Run(ctx context.Context, request ytdlp.Request) (ytdlp.Result, error) {
	if request.BreakPerInput {
		r.count = 0
	}
	r.runner.urls = append(r.runner.urls, request.URL)
	r.count++
	result := ytdlp.Result{Downloads: 1}
	if request.MaxDownloads > 0 && r.count >= request.MaxDownloads {
		result.Stopped = true
		result.StopKind = ytdlp.StopMaxDownloads
	}
	return result, nil
}

func TestRunTelemetryWrittenOnStopExit(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--telemetry-json", "--max-downloads", "1", "--output-dir", t.TempDir(), server.URL + "/page", server.URL + "/page"}, &stdout, &stderr)
	if code != 101 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Aborting remaining downloads") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	snapshot, err := ytdlp.DecodeTelemetrySnapshot(context.Background(), bytes.NewReader(stdout.Bytes()), 0)
	if err != nil || len(snapshot.Counts) != 1 || snapshot.Counts[0].Extractor != "fixture" {
		t.Fatalf("stop telemetry snapshot=%+v error=%v stdout=%q", snapshot, err, stdout.String())
	}
}

func TestRunConcurrentInvocationsRegisterFlagsSafely(t *testing.T) {
	// Flag registration (including the hidden parity set) must be safe under
	// concurrent RunContextIO calls; the hidden set is invocation-local.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runner := &recordingCLIRunner{}
			var stdout, stderr bytes.Buffer
			code := runContextIOWithDependencies(
				context.Background(), []string{"--skip-download", "https://fixture.invalid/video"}, strings.NewReader(""), &stdout, &stderr,
				runDependencies{newRunner: func([]ytdlp.Option) cliRunner { return runner }},
			)
			if code != 0 {
				t.Errorf("concurrent code=%d stderr=%q", code, stderr.String())
			}
		}()
	}
	wg.Wait()
}
