package ffmpeg

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/events"
)

// MaxForceKeyframes caps unique non-zero force-keyframe timestamps and matches
// internal/sponsorblock.MaxForceKeyframeTimestamps planning.
const MaxForceKeyframes = 512

// MaxConcatRanges caps keep segments for ConcatRanges and matches
// internal/sponsorblock.MaxKeepSegments planning. It equals maxConcatInputs.
const MaxConcatRanges = maxConcatInputs

// ConcatRange is one keep segment expressed as optional in/out points for the
// ffmpeg concat demuxer. Empty strings mean "unbounded" on that side.
type ConcatRange struct {
	InPoint  string
	OutPoint string
}

// ForceKeyframes re-encodes input so the supplied timestamps become keyframes.
// Timestamps are de-duplicated and ordered; a leading zero is dropped to match
// the pinned yt-dlp FFmpegPostProcessor.force_keyframes behavior. The destination
// is finalized atomically.
func (tools *Toolset) ForceKeyframes(ctx context.Context, inputPath, destination string, timestamps []float64, overwrite bool, sink events.Sink) error {
	if err := validateLocalRegularFile(inputPath); err != nil {
		return err
	}
	ordered, err := normalizeForceKeyframeTimestamps(timestamps)
	if err != nil {
		return err
	}
	if len(ordered) == 0 {
		return fmt.Errorf("%w: force keyframes requires timestamps", ErrInvalidOperation)
	}
	joined := strings.Join(ordered, ",")
	if len(joined) > 16<<10 {
		return fmt.Errorf("%w: force keyframes argument too large", ErrInvalidOperation)
	}
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		// Match yt-dlp force_keyframes: re-encode (no -c copy) with
		// stream_copy_opts(copy=False) semantics so cut boundaries land on keyframes.
		args := []string{"-i", inputPath, "-map", "0", "-dn", "-ignore_unknown", "-force_key_frames", joined}
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(destination)), ".")
		if ext == "mp4" || ext == "mov" || ext == "m4a" {
			args = append(args, "-c:s", "mov_text")
		}
		return append(args, "-progress", "pipe:1", "-nostats", temporary)
	})
}

// ConcatRanges concatenates keep ranges from a single input using the concat
// demuxer with inpoint/outpoint directives. Ranges must already omit empty
// leading/trailing chunks.
func (tools *Toolset) ConcatRanges(ctx context.Context, inputPath, destination string, ranges []ConcatRange, overwrite bool, sink events.Sink) error {
	if err := validateLocalRegularFile(inputPath); err != nil {
		return err
	}
	if len(ranges) == 0 || len(ranges) > MaxConcatRanges {
		return fmt.Errorf("%w: concat ranges require 1 to %d segments", ErrInvalidOperation, MaxConcatRanges)
	}
	for index, segment := range ranges {
		if err := validateConcatRange(segment); err != nil {
			return fmt.Errorf("%w: range %d: %v", ErrInvalidOperation, index, err)
		}
	}
	list, err := writeConcatRangeList(destination, inputPath, ranges)
	if err != nil {
		return err
	}
	defer os.Remove(list)
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		return []string{"-f", "concat", "-safe", "0", "-i", list, "-c", "copy", "-progress", "pipe:1", "-nostats", temporary}
	})
}

// CutOutRanges optionally force-keyframes around cut boundaries, then concatenates
// the complementary keep ranges into destination. When forceKeyframes is false,
// the input is stream-copied through the concat demuxer only.
func (tools *Toolset) CutOutRanges(ctx context.Context, inputPath, destination string, ranges []ConcatRange, cutBoundaries []float64, forceKeyframes, overwrite bool, sink events.Sink) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source := inputPath
	var keyframeTemp string
	if forceKeyframes {
		directory := filepath.Dir(destination)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("%w: create keyframe directory: %v", ErrMediaFailure, err)
		}
		temporaryDirectory, err := os.MkdirTemp(directory, ".ytdlp-keyframes-")
		if err != nil {
			return fmt.Errorf("%w: allocate keyframe directory: %v", ErrMediaFailure, err)
		}
		defer os.RemoveAll(temporaryDirectory)
		keyframeTemp = filepath.Join(temporaryDirectory, "keyframes"+filepath.Ext(inputPath))
		if err := tools.ForceKeyframes(ctx, inputPath, keyframeTemp, cutBoundaries, false, sink); err != nil {
			return err
		}
		source = keyframeTemp
	}
	return tools.ConcatRanges(ctx, source, destination, ranges, overwrite, sink)
}

func normalizeForceKeyframeTimestamps(timestamps []float64) ([]string, error) {
	if len(timestamps) == 0 {
		return nil, nil
	}
	if len(timestamps) > MaxForceKeyframes {
		return nil, fmt.Errorf("%w: too many force keyframe timestamps", ErrInvalidOperation)
	}
	seen := make(map[string]struct{}, len(timestamps))
	ordered := make([]string, 0, len(timestamps))
	for _, timestamp := range timestamps {
		if math.IsNaN(timestamp) || math.IsInf(timestamp, 0) || timestamp < 0 {
			return nil, fmt.Errorf("%w: invalid force keyframe timestamp", ErrInvalidOperation)
		}
		if timestamp == 0 {
			continue
		}
		rendered := strconv.FormatFloat(timestamp, 'f', 6, 64)
		if _, exists := seen[rendered]; exists {
			continue
		}
		seen[rendered] = struct{}{}
		ordered = append(ordered, rendered)
	}
	return ordered, nil
}

func validateConcatRange(segment ConcatRange) error {
	if segment.InPoint != "" {
		if err := validateConcatTimestamp(segment.InPoint); err != nil {
			return err
		}
	}
	if segment.OutPoint != "" {
		if err := validateConcatTimestamp(segment.OutPoint); err != nil {
			return err
		}
	}
	return nil
}

func validateConcatTimestamp(value string) error {
	if len(value) == 0 || len(value) > 32 || strings.ContainsAny(value, " \t\r\n'\x00") {
		return fmt.Errorf("unsafe timestamp")
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
		return fmt.Errorf("invalid timestamp")
	}
	return nil
}

func writeConcatRangeList(destination, inputPath string, ranges []ConcatRange) (string, error) {
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("%w: create concat directory: %v", ErrMediaFailure, err)
	}
	file, err := os.CreateTemp(directory, ".ytdlp-concat-ranges-*.ffconcat")
	if err != nil {
		return "", fmt.Errorf("%w: create concat list: %v", ErrMediaFailure, err)
	}
	name := file.Name()
	quoted := quoteFFConcatPath(inputPath)
	var builder strings.Builder
	builder.WriteString("ffconcat version 1.0\n")
	for _, segment := range ranges {
		builder.WriteString("file ")
		builder.WriteString(quoted)
		builder.WriteByte('\n')
		if segment.InPoint != "" {
			builder.WriteString("inpoint ")
			builder.WriteString(segment.InPoint)
			builder.WriteByte('\n')
		}
		if segment.OutPoint != "" {
			builder.WriteString("outpoint ")
			builder.WriteString(segment.OutPoint)
			builder.WriteByte('\n')
		}
	}
	if _, err := io.WriteString(file, builder.String()); err != nil {
		file.Close()
		os.Remove(name)
		return "", fmt.Errorf("%w: write concat list: %v", ErrMediaFailure, err)
	}
	if err := file.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("%w: finalize concat list: %v", ErrMediaFailure, err)
	}
	return name, nil
}

func quoteFFConcatPath(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\\''") + "'"
}
