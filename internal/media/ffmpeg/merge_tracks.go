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
// HLSAACFixup is set by prepareMergeInputs from the probed local audio codec.
type MergeInput struct {
	Path        string
	HasAudio    bool
	HasVideo    bool
	Protocol    string
	HLSAACFixup bool
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
			if input.HLSAACFixup {
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
	prepared, err := tools.prepareMergeInputs(ctx, inputs)
	if err != nil {
		return err
	}
	mergeArgs, err := BuildMergeArguments(prepared, "")
	if err != nil {
		return err
	}
	return tools.runAtomic(ctx, destination, overwrite, sink, func(temporary string) []string {
		args := append(append([]string(nil), mergeArgs...), temporary)
		return args
	})
}

func (tools *Toolset) prepareMergeInputs(ctx context.Context, inputs []MergeInput) ([]MergeInput, error) {
	prepared := append([]MergeInput(nil), inputs...)
	for index := range prepared {
		if !prepared[index].HasAudio || !strings.HasPrefix(prepared[index].Protocol, "m3u8") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		codec, err := tools.firstAudioStreamCodec(ctx, prepared[index].Path)
		if err != nil {
			return nil, err
		}
		prepared[index].HLSAACFixup = codec == "aac"
	}
	return prepared, nil
}

func (tools *Toolset) firstAudioStreamCodec(ctx context.Context, path string) (string, error) {
	probe, err := tools.Probe(ctx, path)
	if err != nil {
		return "", err
	}
	for _, stream := range probe.Streams {
		if stream.CodecType == "audio" {
			return stream.CodecName, nil
		}
	}
	return "", nil
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
