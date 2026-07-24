package postprocess

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
)

// SponsorBlockCut removes sponsor ranges from a media file using typed ffmpeg
// force-keyframe and concat-range operations. Input and Output may share a path
// when Overwrite is true (in-place replace via atomic temp finalize).
type SponsorBlockCut struct {
	Input, Output  Artifact
	Ranges         []ffmpeg.ConcatRange
	Boundaries     []float64
	ForceKeyframes bool
	Overwrite      bool
}

func (operation SponsorBlockCut) Name() string { return "sponsorblock-cut" }

func (operation SponsorBlockCut) Run(ctx context.Context, tools *ffmpeg.Toolset, sink events.Sink) error {
	if err := validateArtifact(operation.Input, ArtifactMedia); err != nil {
		return err
	}
	if err := validateArtifact(operation.Output, ArtifactMedia); err != nil {
		return err
	}
	if err := localRegular(operation.Input.Path); err != nil {
		return err
	}
	if len(operation.Ranges) == 0 {
		return fmt.Errorf("%w: missing keep ranges", ErrInvalidGraph)
	}
	destination := operation.Output.Path
	if destination == "" {
		return fmt.Errorf("%w: missing output", ErrUnsafePath)
	}
	if filepath.Clean(operation.Input.Path) == filepath.Clean(destination) {
		if !operation.Overwrite {
			return fmt.Errorf("%w: in-place cut requires overwrite", ErrInvalidGraph)
		}
		temporary := destination + ".ytdlp-sponsorblock-cut" + filepath.Ext(destination)
		if err := tools.CutOutRanges(ctx, operation.Input.Path, temporary, operation.Ranges, operation.Boundaries, operation.ForceKeyframes, false, sink); err != nil {
			return err
		}
		if err := SafeMoveContext(ctx, temporary, destination, true); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		return nil
	}
	if err := tools.CutOutRanges(ctx, operation.Input.Path, destination, operation.Ranges, operation.Boundaries, operation.ForceKeyframes, operation.Overwrite, sink); err != nil {
		return err
	}
	return removeOwned(operation.Input, operation.Output)
}

// SponsorBlockSubtitleCut applies the same keep ranges to a supported subtitle
// sidecar without force-keyframes. Unsupported formats must be rejected by the
// caller before constructing this operation.
type SponsorBlockSubtitleCut struct {
	Input, Output Artifact
	Ranges        []ffmpeg.ConcatRange
	Overwrite     bool
}

func (operation SponsorBlockSubtitleCut) Name() string { return "sponsorblock-subtitle-cut" }

func (operation SponsorBlockSubtitleCut) Run(ctx context.Context, tools *ffmpeg.Toolset, sink events.Sink) error {
	if err := validateArtifact(operation.Input, ArtifactSubtitle); err != nil {
		return err
	}
	if err := validateArtifact(operation.Output, ArtifactSubtitle); err != nil {
		return err
	}
	if err := localRegular(operation.Input.Path); err != nil {
		return err
	}
	if len(operation.Ranges) == 0 {
		return fmt.Errorf("%w: missing keep ranges", ErrInvalidGraph)
	}
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(operation.Input.Path)), ".")
	if !SupportedSponsorBlockSubtitleExt(extension) {
		return fmt.Errorf("%w: unsupported subtitle cut format", ErrInvalidGraph)
	}
	destination := operation.Output.Path
	if filepath.Clean(operation.Input.Path) == filepath.Clean(destination) {
		if !operation.Overwrite {
			return fmt.Errorf("%w: in-place cut requires overwrite", ErrInvalidGraph)
		}
		temporary := destination + ".ytdlp-sponsorblock-subcut" + filepath.Ext(destination)
		if err := tools.ConcatRanges(ctx, operation.Input.Path, temporary, operation.Ranges, false, sink); err != nil {
			return err
		}
		if err := SafeMoveContext(ctx, temporary, destination, true); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		return nil
	}
	if err := tools.ConcatRanges(ctx, operation.Input.Path, destination, operation.Ranges, operation.Overwrite, sink); err != nil {
		return err
	}
	return removeOwned(operation.Input, operation.Output)
}

// SupportedSponsorBlockSubtitleExt reports whether ext matches the pinned
// FFmpegSubtitlesConvertorPP.SUPPORTED_EXTS set used by ModifyChaptersPP.
func SupportedSponsorBlockSubtitleExt(ext string) bool {
	switch strings.ToLower(ext) {
	case "srt", "vtt", "ass", "lrc":
		return true
	default:
		return false
	}
}

// FormatConcatTimestamp renders a seconds value the way yt-dlp formats
// concat inpoint/outpoint directives.
func FormatConcatTimestamp(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}
