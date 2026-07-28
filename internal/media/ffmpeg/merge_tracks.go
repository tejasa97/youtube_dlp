package ffmpeg

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/format"
)

// MergeInput describes one local media file participating in a multi-track merge.
// Stream presence flags come from the planner's selected-format metadata.
type MergeInput struct {
	Path         string
	HasAudio     bool
	HasVideo     bool
	Protocol     string
	AudioCodec   string
}

// BuildMergeArguments returns the ffmpeg argument vector for merging the
// supplied inputs in order. The temporary output path is appended when
// destination is nonempty.
func BuildMergeArguments(inputs []MergeInput, destination string) ([]string, error) {
	if err := validateMergeInputs(inputs); err != nil {
		return nil, err
	}
	args := make([]string, 0, len(inputs)*6+16)
	for _, input := range inputs {
		args = append(args, "-i", input.Path)
	}
	audioStreams := 0
	for index, input := range inputs {
		prefix := strconv.Itoa(index)
		if input.HasAudio {
			args = append(args, "-map", prefix+":a:0")
			if hlsAACFixup(input) {
				args = append(args, "-bsf:a:"+strconv.Itoa(audioStreams), "aac_adtstoasc")
			}
			audioStreams++
		}
		if input.HasVideo {
			args = append(args, "-map", prefix+":v:0")
		}
	}
	args = append(args, "-c", "copy", "-progress", "pipe:1", "-nostats")
	if destination != "" {
		args = append(args, destination)
	}
	return args, nil
}

// MergeTracks merges an ordered list of local media inputs into destination
// using stream-copy semantics. Inputs are mapped in planner order: for each
// input, the first audio stream (when present) is mapped before the first
// video stream (when present).
func (tools *Toolset) MergeTracks(
	ctx context.Context,
	inputs []MergeInput,
	destination string,
	overwrite bool,
	sink events.Sink,
) error {
	if err := validateMergeInputs(inputs); err != nil {
		return err
	}
	for _, input := range inputs {
		if err := regularMediaInput(input.Path); err != nil {
			return err
		}
	}
	mergeArgs, err := BuildMergeArguments(inputs, "")
	if err != nil {
		return err
	}
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		args := append(append([]string(nil), mergeArgs...), temporary)
		return args
	})
}

func validateMergeInputs(inputs []MergeInput) error {
	if len(inputs) == 0 {
		return fmt.Errorf("%w: merge requires at least one input", ErrInvalidOperation)
	}
	if len(inputs) > format.MaxMergeTracks {
		return fmt.Errorf("%w: merge exceeds %d track limit", ErrInvalidOperation, format.MaxMergeTracks)
	}
	for index, input := range inputs {
		if strings.TrimSpace(input.Path) == "" {
			return fmt.Errorf("%w: merge input %d has empty path", ErrInvalidOperation, index)
		}
		if !input.HasAudio && !input.HasVideo {
			return fmt.Errorf("%w: merge input %d has no audio or video stream", ErrInvalidOperation, index)
		}
	}
	return nil
}

func hlsAACFixup(input MergeInput) bool {
	if !strings.HasPrefix(input.Protocol, "m3u8") {
		return false
	}
	codec := strings.ToLower(strings.TrimSpace(input.AudioCodec))
	if codec == "" {
		return false
	}
	base := strings.Split(codec, ".")[0]
	return base == "aac" || strings.HasPrefix(base, "mp4a")
}
