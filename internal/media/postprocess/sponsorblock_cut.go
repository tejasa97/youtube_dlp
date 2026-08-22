package postprocess

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/events"
	"github.com/tejasa97/ytdlp-go/internal/media/ffmpeg"
	"github.com/tejasa97/ytdlp-go/internal/sponsorblock"
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

// SponsorBlockSubtitleCut remaps a supported subtitle sidecar through cut
// ranges using deterministic cue editing (not ffmpeg concat).
type SponsorBlockSubtitleCut struct {
	Input, Output Artifact
	Cuts          []sponsorblock.Range
	Overwrite     bool
}

func (operation SponsorBlockSubtitleCut) Name() string { return "sponsorblock-subtitle-cut" }

func (operation SponsorBlockSubtitleCut) Run(ctx context.Context, _ *ffmpeg.Toolset, _ events.Sink) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateArtifact(operation.Input, ArtifactSubtitle); err != nil {
		return err
	}
	if err := validateArtifact(operation.Output, ArtifactSubtitle); err != nil {
		return err
	}
	if err := localRegular(operation.Input.Path); err != nil {
		return err
	}
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(operation.Input.Path)), ".")
	if !SupportedSponsorBlockSubtitleExt(extension) {
		return fmt.Errorf("%w: unsupported subtitle cut format", ErrInvalidGraph)
	}
	data, err := os.ReadFile(operation.Input.Path)
	if err != nil {
		return err
	}
	rewritten, err := sponsorblock.CutSubtitle(extension, data, operation.Cuts)
	if err != nil {
		return err
	}
	destination := operation.Output.Path
	if filepath.Clean(operation.Input.Path) == filepath.Clean(destination) {
		if !operation.Overwrite {
			return fmt.Errorf("%w: in-place cut requires overwrite", ErrInvalidGraph)
		}
		temporary := destination + ".ytdlp-sponsorblock-subcut" + filepath.Ext(destination)
		if err := os.WriteFile(temporary, rewritten, 0o600); err != nil {
			return err
		}
		if err := SafeMoveContext(ctx, temporary, destination, true); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		return nil
	}
	if info, err := os.Lstat(destination); err == nil {
		if !operation.Overwrite {
			return fmt.Errorf("%w: destination exists", ffmpeg.ErrDestinationExists)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: output is not regular", ErrUnsafePath)
		}
	} else if !errorsIsNotExist(err) {
		return err
	}
	if err := os.WriteFile(destination, rewritten, 0o600); err != nil {
		return err
	}
	return removeOwned(operation.Input, operation.Output)
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}

// SupportedSponsorBlockSubtitleExt reports whether ext is handled by the
// deterministic SponsorBlock subtitle remapper.
func SupportedSponsorBlockSubtitleExt(ext string) bool {
	return sponsorblock.SupportedSubtitleExt(ext)
}

// FormatConcatTimestamp renders a seconds value the way yt-dlp formats
// concat inpoint/outpoint directives.
func FormatConcatTimestamp(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}
