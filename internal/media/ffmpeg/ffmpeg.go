// Package ffmpeg supervises ffmpeg and ffprobe without invoking a shell.
package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/ytdlp-go/ytdlp/internal/events"
)

var (
	ErrFFmpegUnavailable  = errors.New("ffmpeg is unavailable")
	ErrFFprobeUnavailable = errors.New("ffprobe is unavailable")
	ErrMediaFailure       = errors.New("ffmpeg media processing failed")
	ErrDestinationExists  = errors.New("postprocessor destination exists")
)

var sensitiveDiagnosticPattern = regexp.MustCompile(`(?i)(authorization|password|signature|token|sig|key)=([^&[:space:]]+)`)

type Config struct {
	FFmpegPath  string
	FFprobePath string
	MaxOutput   int
	Environment []string
}

type Toolset struct {
	ffmpeg      string
	ffprobe     string
	maxOutput   int
	environment []string
}

type Version struct {
	FFmpeg  string
	FFprobe string
}

type Probe struct {
	Streams []Stream `json:"streams"`
	Format  Format   `json:"format"`
}

type Stream struct {
	Index     int    `json:"index"`
	CodecName string `json:"codec_name"`
	CodecType string `json:"codec_type"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	Duration  string `json:"duration,omitempty"`
}

type Format struct {
	Filename   string `json:"filename"`
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
	Size       string `json:"size"`
}

func Discover(config Config) (*Toolset, error) {
	ffmpegPath, err := discover(config.FFmpegPath, "ffmpeg", ErrFFmpegUnavailable)
	if err != nil {
		return nil, err
	}
	ffprobePath, err := discover(config.FFprobePath, "ffprobe", ErrFFprobeUnavailable)
	if err != nil {
		return nil, err
	}
	maxOutput := config.MaxOutput
	if maxOutput <= 0 {
		maxOutput = 1 << 20
	}
	environment := append([]string(nil), config.Environment...)
	if environment == nil {
		environment = []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "LC_ALL=C"}
	}
	return &Toolset{ffmpeg: ffmpegPath, ffprobe: ffprobePath, maxOutput: maxOutput, environment: environment}, nil
}

func (tools *Toolset) Versions(ctx context.Context) (Version, error) {
	ffmpegOutput, err := tools.execute(ctx, tools.ffmpeg, []string{"-version"}, nil)
	if err != nil {
		return Version{}, err
	}
	ffprobeOutput, err := tools.execute(ctx, tools.ffprobe, []string{"-version"}, nil)
	if err != nil {
		return Version{}, err
	}
	return Version{FFmpeg: firstLine(ffmpegOutput), FFprobe: firstLine(ffprobeOutput)}, nil
}

func (tools *Toolset) Probe(ctx context.Context, path string) (Probe, error) {
	output, err := tools.execute(ctx, tools.ffprobe, []string{
		"-v", "error", "-show_streams", "-show_format", "-of", "json", path,
	}, nil)
	if err != nil {
		return Probe{}, err
	}
	var probe Probe
	if err := json.Unmarshal(output, &probe); err != nil {
		return Probe{}, fmt.Errorf("%w: decode ffprobe JSON: %v", ErrMediaFailure, err)
	}
	return probe, nil
}

func (tools *Toolset) Merge(ctx context.Context, videoPath, audioPath, destination string, overwrite bool, sink events.Sink) error {
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		return []string{
			"-i", videoPath, "-i", audioPath,
			"-map", "0:v:0?", "-map", "1:a:0?", "-c", "copy",
			"-progress", "pipe:1", "-nostats", temporary,
		}
	})
}

// Remux changes the media container without re-encoding streams. Output is
// atomically finalized and removed on cancellation or ffmpeg failure.
func (tools *Toolset) Remux(ctx context.Context, inputPath, destination string, overwrite bool, sink events.Sink) error {
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		return []string{
			"-i", inputPath, "-map", "0", "-c", "copy",
			"-map_metadata", "0", "-progress", "pipe:1", "-nostats", temporary,
		}
	})
}

func (tools *Toolset) runAtomic(ctx context.Context, destination string, overwrite bool, sink events.Sink, operation func(string) []string) error {
	if sink == nil {
		sink = events.Nop()
	}
	if _, err := os.Stat(destination); err == nil && !overwrite {
		return fmt.Errorf("%w: %s", ErrDestinationExists, destination)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("%w: create output directory: %v", ErrMediaFailure, err)
	}
	extension := filepath.Ext(destination)
	temporary := strings.TrimSuffix(destination, extension) + ".part" + extension
	_ = os.Remove(temporary)
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error"}
	if overwrite {
		args = append(args, "-y")
	} else {
		args = append(args, "-n")
	}
	args = append(args, operation(temporary)...)
	if err := sink.Emit(ctx, events.Event{Kind: events.KindPostprocessStarting, Path: destination}); err != nil {
		return err
	}
	var totalSize int64
	_, err := tools.execute(ctx, tools.ffmpeg, args, func(line string) error {
		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil
		}
		if key == "total_size" {
			totalSize, _ = strconv.ParseInt(value, 10, 64)
		}
		if key == "progress" {
			return sink.Emit(ctx, events.Event{Kind: events.KindPostprocessProgress, Path: destination, Bytes: totalSize, Message: value})
		}
		return nil
	})
	if err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := replace(temporary, destination, overwrite); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("%w: finalize output: %v", ErrMediaFailure, err)
	}
	return sink.Emit(ctx, events.Event{Kind: events.KindPostprocessCompleted, Path: destination, Bytes: totalSize})
}

func (tools *Toolset) execute(ctx context.Context, binary string, args []string, onLine func(string) error) ([]byte, error) {
	command := exec.CommandContext(ctx, binary, args...)
	command.Env = append([]string(nil), tools.environment...)
	configureCommand(command)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := newBoundedBuffer(tools.maxOutput)
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("%w: start %s: %v", ErrMediaFailure, filepath.Base(binary), err)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			terminateCommand(command)
		case <-done:
		}
	}()
	var output bytes.Buffer
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), tools.maxOutput)
	var callbackErr error
	for scanner.Scan() {
		line := scanner.Text()
		if output.Len()+len(line)+1 <= tools.maxOutput {
			output.WriteString(line)
			output.WriteByte('\n')
		}
		if onLine != nil && callbackErr == nil {
			callbackErr = onLine(line)
			if callbackErr != nil {
				terminateCommand(command)
			}
		}
	}
	scanErr := scanner.Err()
	waitErr := command.Wait()
	close(done)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if callbackErr != nil {
		return nil, callbackErr
	}
	if scanErr != nil {
		return nil, fmt.Errorf("%w: read %s output: %v", ErrMediaFailure, filepath.Base(binary), scanErr)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("%w: %s failed: %v: %s", ErrMediaFailure, filepath.Base(binary), waitErr, redactDiagnostic(strings.TrimSpace(stderr.String())))
	}
	return output.Bytes(), nil
}

func redactDiagnostic(input string) string {
	return sensitiveDiagnosticPattern.ReplaceAllString(input, "$1=[redacted]")
}

func discover(configured, name string, unavailable error) (string, error) {
	if configured != "" {
		info, err := os.Stat(configured)
		if err != nil || info.IsDir() {
			return "", fmt.Errorf("%w: %s", unavailable, configured)
		}
		return configured, nil
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", unavailable
	}
	return path, nil
}

func firstLine(input []byte) string {
	line, _, _ := bytes.Cut(input, []byte{'\n'})
	return string(line)
}

func replace(source, destination string, overwrite bool) error {
	err := os.Rename(source, destination)
	if err == nil || !overwrite {
		return err
	}
	if removeErr := os.Remove(destination); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return errors.Join(err, removeErr)
	}
	return os.Rename(source, destination)
}

type boundedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func newBoundedBuffer(limit int) *boundedBuffer { return &boundedBuffer{remaining: limit} }

func (buffer *boundedBuffer) Write(input []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	original := len(input)
	if len(input) > buffer.remaining {
		input = input[:max(0, buffer.remaining)]
		buffer.truncated = true
	}
	_, _ = buffer.buffer.Write(input)
	buffer.remaining -= len(input)
	return original, nil
}

func (buffer *boundedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.truncated {
		return buffer.buffer.String() + " [truncated]"
	}
	return buffer.buffer.String()
}

var _ io.Writer = (*boundedBuffer)(nil)
