package ffmpeg

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/events"
)

// SectionInput is one media input delegated to ffmpeg for a sectioned
// download. URL is the direct/HLS/DASH manifest URL to hand to ffmpeg.
// Headers are the allowlisted request headers (rejected if unsafe).
// HasVideo/HasAudio indicate which streams to map when multiple inputs
// are combined; a single input maps all streams.
type SectionInput struct {
	URL      string
	Headers  http.Header
	HasVideo bool
	HasAudio bool
}

// SectionBounds is a single bounded download range in seconds. Start is
// always set; End is nil for an open-ended range.
type SectionBounds struct {
	Start float64
	End   *float64
}

// DownloadSections downloads one or more sections of the given media
// inputs through ffmpeg, mirroring the pinned FFmpegFD behavior: ffmpeg
// receives the selected direct/HLS/DASH URL with -ss/-t and, for separate
// A/V selections, both inputs with -map. Output is atomically finalized
// and removed on cancellation or ffmpeg failure.
//
// The operation reuses the safety, header allowlisting, URL redaction,
// atomic output, and cancellation patterns of DownloadHLS. It never
// invokes a shell. If a selected input cannot be safely delegated to
// ffmpeg (for example credential-isolated headers or cookies), it fails
// before producing output.
func (tools *Toolset) DownloadSections(
	ctx context.Context,
	inputs []SectionInput,
	bounds SectionBounds,
	destination string,
	overwrite bool,
	forceKeyframes bool,
	sink events.Sink,
) error {
	if len(inputs) == 0 {
		return fmt.Errorf("%w: no section inputs", ErrInvalidOperation)
	}
	if err := validateSectionBounds(bounds); err != nil {
		return err
	}
	validated, err := validateSectionInputs(inputs)
	if err != nil {
		return err
	}
	// Header allowlisting is done once across all inputs; any unsafe header
	// aborts before output is produced.
	headerBlocks := make([]string, len(inputs))
	for index := range validated {
		block, headerErr := ffmpegHLSHeaders(validated[index].Headers)
		if headerErr != nil {
			return headerErr
		}
		headerBlocks[index] = block
	}
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		return sectionFFmpegArgs(validated, headerBlocks, bounds, forceKeyframes, temporary)
	})
}

// DownloadSection is a convenience wrapper for a single input section.
func (tools *Toolset) DownloadSection(
	ctx context.Context,
	input SectionInput,
	bounds SectionBounds,
	destination string,
	overwrite bool,
	forceKeyframes bool,
	sink events.Sink,
) error {
	return tools.DownloadSections(ctx, []SectionInput{input}, bounds, destination, overwrite, forceKeyframes, sink)
}

// validateSectionBounds enforces the generic section contract: finite,
// nonnegative, ordered, and at least one bound present.
func validateSectionBounds(bounds SectionBounds) error {
	if math.IsNaN(bounds.Start) || math.IsInf(bounds.Start, 0) || bounds.Start < 0 {
		return fmt.Errorf("%w: invalid section start", ErrInvalidOperation)
	}
	if bounds.End != nil && (math.IsNaN(*bounds.End) || math.IsInf(*bounds.End, 0) || *bounds.End < 0) {
		return fmt.Errorf("%w: invalid section end", ErrInvalidOperation)
	}
	if bounds.End != nil && *bounds.End <= bounds.Start {
		return fmt.Errorf("%w: section end must exceed start", ErrInvalidOperation)
	}
	return nil
}

// validateSectionInputs validates each URL and rejects inputs carrying
// headers that cannot be safely delegated to ffmpeg.
func validateSectionInputs(inputs []SectionInput) ([]SectionInput, error) {
	out := make([]SectionInput, 0, len(inputs))
	for _, input := range inputs {
		parsed, err := url.Parse(input.URL)
		if err != nil || parsed.User != nil || parsed.Hostname() == "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, fmt.Errorf("%w: invalid section input URL", ErrInvalidOperation)
		}
		if len(input.Headers) > 0 {
			for key := range input.Headers {
				if !safeFFmpegHLSHeader(http.CanonicalHeaderKey(key)) {
					return nil, fmt.Errorf("%w: section input uses unsafe header %q", ErrUnsafeHLSHeaders, http.CanonicalHeaderKey(key))
				}
			}
		}
		out = append(out, input)
	}
	return out, nil
}

// sectionFFmpegArgs builds the shell-free ffmpeg argument list for a
// sectioned download. It mirrors FFmpegFD: -ss/-t, -map for separate A/V
// inputs, and -c copy unless forceKeyframes requests re-encode around
// section boundaries.
func sectionFFmpegArgs(
	inputs []SectionInput,
	headerBlocks []string,
	bounds SectionBounds,
	forceKeyframes bool,
	temporary string,
) []string {
	args := []string{
		"-protocol_whitelist", "http,https,tcp,tls,crypto",
	}
	cutArgs := []string{"-ss", formatSectionTime(bounds.Start)}
	if bounds.End != nil {
		cutArgs = append(cutArgs, "-t", formatSectionTime(*bounds.End-bounds.Start))
	}
	if forceKeyframes {
		// Seek-before-input with re-encode.
		args = append(args, cutArgs...)
	}
	for index, input := range inputs {
		args = append(args, "-i", input.URL)
		if headerBlocks[index] != "" {
			args = append(args, "-headers", headerBlocks[index])
		}
	}
	if len(inputs) > 1 {
		for index, input := range inputs {
			if input.HasVideo {
				args = append(args, "-map", fmt.Sprintf("%d:v:0", index))
			}
			if input.HasAudio {
				args = append(args, "-map", fmt.Sprintf("%d:a:0", index))
			}
		}
	} else {
		args = append(args, "-map", "0")
	}
	if !forceKeyframes {
		args = append(args, cutArgs...)
	}
	if forceKeyframes {
		args = append(args, "-c:v", "libx264", "-force_key_frames", formatSectionTime(bounds.Start))
	} else {
		args = append(args, "-c", "copy")
	}
	args = append(args, "-progress", "pipe:1", "-nostats", temporary)
	return args
}

// formatSectionTime formats a float seconds value as an ffmpeg timestamp
// string with up to 3 fractional digits, bounded and nonnegative.
func formatSectionTime(value float64) string {
	if value < 0 {
		value = 0
	}
	text := strconv.FormatFloat(value, 'f', 3, 64)
	return strings.TrimRight(strings.TrimRight(text, "0"), ".")
}
