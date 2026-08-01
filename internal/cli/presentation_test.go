package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

type terminalBuffer struct {
	bytes.Buffer
	terminal bool
}

func (buffer *terminalBuffer) IsTerminal() bool { return buffer.terminal }

func TestTerminalPresentationPipeAndTTYProgressAreDeterministic(t *testing.T) {
	event := ytdlp.Event{Kind: ytdlp.EventDownloadProgress, Path: "fixture.bin", Bytes: 2, Total: 10}
	for _, test := range []struct {
		name     string
		terminal bool
		newline  bool
		want     string
	}{
		{name: "pipe", want: "[download] 2/10 bytes\n"},
		{name: "tty", terminal: true, want: "\r[download] 2/10 bytes"},
		{name: "tty newline", terminal: true, newline: true, want: "[download] 2/10 bytes\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output terminalBuffer
			presentation := newTerminalPresentation(&output, terminalPresentationConfig{
				progressMode: progressAuto, newline: test.newline, stderrTTY: test.terminal,
				colors: colorConfig{stderr: colorNever}, now: func() time.Time { return time.Unix(100, 0) },
			})
			if err := presentation.handle(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if got := output.String(); got != test.want {
				t.Fatalf("output=%q want=%q", got, test.want)
			}
		})
	}
}

func TestTerminalPresentationProgressDeltaUsesInjectedClock(t *testing.T) {
	var output bytes.Buffer
	now := time.Unix(100, 0)
	presentation := newTerminalPresentation(&output, terminalPresentationConfig{
		progressMode: progressAuto, progressDelta: time.Second, colors: colorConfig{stderr: colorNever},
		now: func() time.Time { return now },
	})
	event := ytdlp.Event{Kind: ytdlp.EventDownloadProgress, Path: "fixture.bin", Bytes: 1, Total: 3}
	if err := presentation.handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	now = now.Add(500 * time.Millisecond)
	event.Bytes = 2
	if err := presentation.handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	now = now.Add(500 * time.Millisecond)
	event.Bytes = 3
	if err := presentation.handle(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "[download] 1/3 bytes\n[download] 3/3 bytes\n"; got != want {
		t.Fatalf("output=%q want=%q", got, want)
	}
}

func TestTerminalPresentationWarningSuppressionAndVerboseQuiet(t *testing.T) {
	var output bytes.Buffer
	presentation := newTerminalPresentation(&output, terminalPresentationConfig{
		quiet: true, verbose: true, noWarnings: true, colors: newColorConfig(),
	})
	if err := presentation.handle(context.Background(), ytdlp.Event{
		Kind: ytdlp.EventMetadataWarning, Message: "hidden warning",
	}); err != nil {
		t.Fatal(err)
	}
	if err := presentation.handle(context.Background(), ytdlp.Event{
		Kind: ytdlp.EventDownloadStarting, Path: "fixture.bin",
	}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, "Destination: fixture.bin") || strings.Contains(got, "hidden warning") {
		t.Fatalf("output=%q", got)
	}

	output.Reset()
	presentation.config.verbose = false
	if err := presentation.handle(context.Background(), ytdlp.Event{
		Kind: ytdlp.EventDownloadStarting, Path: "fixture.bin",
	}); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("quiet presentation emitted %q", output.String())
	}
}

func TestTerminalPresentationProgressJSONStaysStructuredOnStderr(t *testing.T) {
	var output bytes.Buffer
	presentation := newTerminalPresentation(&output, terminalPresentationConfig{
		progressJSON: true, colors: newColorConfig(),
	})
	if err := presentation.handle(context.Background(), ytdlp.Event{
		Kind: ytdlp.EventDownloadProgress, Path: "fixture.bin", Bytes: 1, Total: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); !strings.Contains(got, `"kind":"download_progress"`) || strings.Contains(got, "[download]") {
		t.Fatalf("structured output=%q", got)
	}
}

func TestColorConfigLastWinsPerStream(t *testing.T) {
	config := newColorConfig()
	if err := config.set("no_color"); err != nil {
		t.Fatal(err)
	}
	if err := config.set("stderr:always"); err != nil {
		t.Fatal(err)
	}
	if config.stdout != colorNoColor || config.stderr != colorAlways {
		t.Fatalf("config=%+v", config)
	}
	if err := config.set("stdout:never"); err != nil {
		t.Fatal(err)
	}
	if config.stdout != colorNever || config.stderr != colorAlways {
		t.Fatalf("config=%+v", config)
	}
}

func TestTerminalPresentationColorPolicyIsExplicit(t *testing.T) {
	var output bytes.Buffer
	config := newColorConfig()
	if err := config.set("stderr:always"); err != nil {
		t.Fatal(err)
	}
	presentation := newTerminalPresentation(&output, terminalPresentationConfig{
		colors: config, stderrTTY: false,
	})
	if err := presentation.handle(context.Background(), ytdlp.Event{
		Kind: ytdlp.EventMetadataWarning, Message: "colored",
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "\x1b[33m") || !strings.Contains(output.String(), "colored") {
		t.Fatalf("output=%q", output.String())
	}
}
