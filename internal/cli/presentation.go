package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

type progressMode uint8

const (
	progressAuto progressMode = iota
	progressShow
	progressHide
)

type colorPolicy string

const (
	colorAuto       colorPolicy = "auto"
	colorAlways     colorPolicy = "always"
	colorNever      colorPolicy = "never"
	colorNoColor    colorPolicy = "no_color"
	colorAutoTTY    colorPolicy = "auto-tty"
	colorNoColorTTY colorPolicy = "no_color-tty"
	ansiReset                   = "\x1b[0m"
	ansiYellow                  = "\x1b[33m"
	ansiCyan                    = "\x1b[36m"
	ansiGreen                   = "\x1b[32m"
)

type colorConfig struct {
	stdout colorPolicy
	stderr colorPolicy
}

func newColorConfig() colorConfig {
	return colorConfig{stdout: colorAuto, stderr: colorAuto}
}

// set parses the reference [STREAM:]POLICY form. Policies are retained per
// stream so repeated options have deterministic last-wins semantics.
func (config *colorConfig) set(input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return fmt.Errorf("--color requires a policy")
	}
	stream, policy := "", input
	if index := strings.IndexByte(input, ':'); index >= 0 {
		stream, policy = strings.TrimSpace(input[:index]), strings.TrimSpace(input[index+1:])
		if stream == "" || policy == "" {
			return fmt.Errorf("invalid --color value %q", input)
		}
	}
	parsed := colorPolicy(policy)
	switch parsed {
	case colorAlways, colorAuto, colorNever, colorNoColor, colorAutoTTY, colorNoColorTTY:
	default:
		return fmt.Errorf("invalid --color policy %q", policy)
	}
	switch stream {
	case "", "stdout", "stderr":
	default:
		return fmt.Errorf("invalid --color stream %q", stream)
	}
	if stream == "" || stream == "stdout" {
		config.stdout = parsed
	}
	if stream == "" || stream == "stderr" {
		config.stderr = parsed
	}
	return nil
}

func (config *colorConfig) disable() {
	config.stdout = colorNoColor
	config.stderr = colorNoColor
}

type terminalPresentationConfig struct {
	quiet         bool
	verbose       bool
	noWarnings    bool
	progressJSON  bool
	progressMode  progressMode
	newline       bool
	progressDelta time.Duration
	stderrTTY     bool
	colors        colorConfig
	now           func() time.Time
}

// terminalPresentation owns all terminal-facing event state. Client events
// can arrive concurrently for multi-track operations, so both rendering and
// progress-delta bookkeeping are serialized here.
type terminalPresentation struct {
	config       terminalPresentationConfig
	stderr       io.Writer
	encoder      *json.Encoder
	mu           sync.Mutex
	lastProgress map[string]time.Time
	inlinePath   string
}

func newTerminalPresentation(stderr io.Writer, config terminalPresentationConfig) *terminalPresentation {
	if config.now == nil {
		config.now = time.Now
	}
	return &terminalPresentation{
		config:       config,
		stderr:       stderr,
		encoder:      json.NewEncoder(stderr),
		lastProgress: make(map[string]time.Time),
	}
}

func (presentation *terminalPresentation) handle(ctx context.Context, event ytdlp.Event) error {
	presentation.mu.Lock()
	defer presentation.mu.Unlock()

	if presentation.config.progressJSON {
		if presentation.config.noWarnings && event.Kind == ytdlp.EventMetadataWarning {
			return nil
		}
		return presentation.encoder.Encode(event)
	}
	if event.Kind == ytdlp.EventMetadataWarning {
		return presentation.writeWarning(event.Message)
	}

	switch event.Kind {
	case ytdlp.EventDownloadProgress:
		return presentation.writeProgress(event)
	case ytdlp.EventExtracting:
		if !presentation.showLifecycle() {
			return nil
		}
		return presentation.writeLine(
			fmt.Sprintf("[%s] Extracting %s", event.Extractor, event.URL), ansiCyan,
		)
	case ytdlp.EventDownloadStarting:
		if !presentation.showLifecycle() {
			return nil
		}
		return presentation.writeLine("[download] Destination: "+event.Path, ansiCyan)
	case ytdlp.EventDownloadRetry:
		if !presentation.showLifecycle() {
			return nil
		}
		presentation.flushInline()
		return presentation.writeLine(fmt.Sprintf("[download] Retry %d: %s", event.Attempt, event.Message), ansiYellow)
	case ytdlp.EventDownloadCompleted:
		presentation.flushInline()
		if !presentation.showLifecycle() {
			return nil
		}
		return presentation.writeLine("[download] Completed: "+event.Path, ansiGreen)
	case ytdlp.EventDownloadCancelled:
		presentation.flushInline()
		if !presentation.showLifecycle() || event.Message == "" {
			return nil
		}
		return presentation.writeLine("[download] "+event.Message, ansiYellow)
	case ytdlp.EventBrowserCookies, ytdlp.EventExtracted, ytdlp.EventExtractorRetry,
		ytdlp.EventFragmentStarting, ytdlp.EventFragmentCompleted,
		ytdlp.EventPostprocessStarting, ytdlp.EventPostprocessProgress, ytdlp.EventPostprocessCompleted,
		ytdlp.EventArchiveMatch, ytdlp.EventMatchFilterSkipped:
		if !presentation.config.verbose {
			return nil
		}
		return presentation.writeLine("[debug] "+formatDebugEvent(event), ansiCyan)
	default:
		return nil
	}
}

func (presentation *terminalPresentation) showLifecycle() bool {
	return presentation.config.verbose || !presentation.config.quiet
}

func (presentation *terminalPresentation) writeWarning(message string) error {
	if presentation.config.noWarnings || message == "" {
		return nil
	}
	return presentation.writeLine("ytdlp-go: WARNING: "+message, ansiYellow)
}

func (presentation *terminalPresentation) writeProgress(event ytdlp.Event) error {
	if presentation.config.progressMode == progressHide ||
		(presentation.config.progressMode == progressAuto && presentation.config.quiet) {
		return nil
	}
	if event.Message == "" && event.Total <= 0 {
		return nil
	}
	if !presentation.progressDue(event) {
		return nil
	}
	message := event.Message
	if message == "" {
		message = fmt.Sprintf("[download] %d/%d bytes", event.Bytes, event.Total)
	}
	message = presentation.colorize(message, ansiGreen, presentation.config.colors.stderr)
	if presentation.config.newline || !presentation.config.stderrTTY {
		presentation.inlinePath = ""
		_, err := io.WriteString(presentation.stderr, message+"\n")
		return err
	}
	if presentation.inlinePath != "" && presentation.inlinePath != event.Path {
		if _, err := io.WriteString(presentation.stderr, "\n"); err != nil {
			return err
		}
	}
	presentation.inlinePath = event.Path
	_, err := io.WriteString(presentation.stderr, "\r"+message)
	return err
}

func (presentation *terminalPresentation) progressDue(event ytdlp.Event) bool {
	if presentation.config.progressDelta <= 0 {
		return true
	}
	key := event.Path
	if key == "" {
		key = event.URL
	}
	now := presentation.config.now()
	last, ok := presentation.lastProgress[key]
	if ok && now.Sub(last) < presentation.config.progressDelta {
		return false
	}
	presentation.lastProgress[key] = now
	return true
}

func (presentation *terminalPresentation) flushInline() {
	if presentation.inlinePath == "" {
		return
	}
	_, _ = io.WriteString(presentation.stderr, "\n")
	presentation.inlinePath = ""
}

func (presentation *terminalPresentation) writeLine(message, style string) error {
	message = presentation.colorize(message, style, presentation.config.colors.stderr)
	_, err := io.WriteString(presentation.stderr, message+"\n")
	return err
}

func (presentation *terminalPresentation) colorize(message, style string, policy colorPolicy) string {
	if shouldColorize(policy, presentation.config.stderrTTY) {
		return style + message + ansiReset
	}
	return message
}

func shouldColorize(policy colorPolicy, tty bool) bool {
	switch policy {
	case colorAlways:
		return true
	case colorAutoTTY:
		return tty
	case colorAuto:
		return tty && strings.ToLower(os.Getenv("TERM")) != "dumb" && os.Getenv("NO_COLOR") == ""
	default:
		return false
	}
}

func formatDebugEvent(event ytdlp.Event) string {
	parts := []string{event.Kind}
	if event.Extractor != "" {
		parts = append(parts, "extractor="+event.Extractor)
	}
	if event.URL != "" {
		parts = append(parts, "url="+event.URL)
	}
	if event.Path != "" {
		parts = append(parts, "path="+event.Path)
	}
	if event.Message != "" {
		parts = append(parts, "message="+event.Message)
	}
	if event.Fragment > 0 {
		parts = append(parts, fmt.Sprintf("fragment=%d/%d", event.Fragment, event.Fragments))
	}
	return strings.Join(parts, " ")
}

func writerIsTerminal(writer io.Writer) bool {
	if terminal, ok := writer.(interface{ IsTerminal() bool }); ok {
		return terminal.IsTerminal()
	}
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
