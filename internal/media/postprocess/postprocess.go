// Package postprocess builds a typed, observable media post-processing graph.
// It deliberately exposes media transformations as Go values rather than a
// free-form command string.
package postprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
)

var (
	ErrInvalidGraph    = errors.New("invalid postprocess graph")
	ErrMissingArtifact = errors.New("postprocess artifact is missing")
	ErrUnsafePath      = errors.New("unsafe postprocess path")
)

type ArtifactKind string

const (
	ArtifactMedia     ArtifactKind = "media"
	ArtifactSubtitle  ArtifactKind = "subtitle"
	ArtifactThumbnail ArtifactKind = "thumbnail"
)

type Artifact struct {
	Path  string
	Kind  ArtifactKind
	Owned bool // whether the graph may remove it after a successful transform
}

type Operation interface {
	Name() string
	Run(context.Context, *ffmpeg.Toolset, events.Sink) error
}

// Graph keeps lifecycle ownership explicit: sources are retained until their
// replacement succeeds, then owned sources may be cleaned up by the operation.
type Graph struct {
	Operations []Operation
}

func (graph Graph) Validate() error {
	if len(graph.Operations) == 0 {
		return fmt.Errorf("%w: graph has no operations", ErrInvalidGraph)
	}
	if len(graph.Operations) > 64 {
		return fmt.Errorf("%w: graph exceeds 64 operations", ErrInvalidGraph)
	}
	for _, operation := range graph.Operations {
		if operation == nil || strings.TrimSpace(operation.Name()) == "" {
			return fmt.Errorf("%w: nil or unnamed operation", ErrInvalidGraph)
		}
	}
	return nil
}

func (graph Graph) Run(ctx context.Context, tools *ffmpeg.Toolset, sink events.Sink) error {
	if err := graph.Validate(); err != nil {
		return err
	}
	for _, operation := range graph.Operations {
		if tools == nil && needsToolset(operation) {
			return fmt.Errorf("%w: ffmpeg toolset", ErrInvalidGraph)
		}
		if err := operation.Run(ctx, tools, sink); err != nil {
			return fmt.Errorf("%s: %w", operation.Name(), err)
		}
	}
	return nil
}

func needsToolset(operation Operation) bool {
	switch operation.(type) {
	case Move, *Move:
		return false
	default:
		return true
	}
}

type AudioExtract struct {
	Input, Output Artifact
	Options       ffmpeg.AudioOptions
	Overwrite     bool
}

func (operation AudioExtract) Name() string { return "extract-audio" }
func (operation AudioExtract) Run(ctx context.Context, tools *ffmpeg.Toolset, sink events.Sink) error {
	if err := validateTransform(operation.Input, operation.Output, ArtifactMedia, ArtifactMedia); err != nil {
		return err
	}
	if err := tools.ExtractAudio(ctx, operation.Input.Path, operation.Output.Path, operation.Options, operation.Overwrite, sink); err != nil {
		return err
	}
	return removeOwned(operation.Input, operation.Output)
}

type SubtitleConvert struct {
	Input, Output Artifact
	Options       ffmpeg.SubtitleOptions
	Overwrite     bool
}

func (operation SubtitleConvert) Name() string { return "convert-subtitle" }
func (operation SubtitleConvert) Run(ctx context.Context, tools *ffmpeg.Toolset, sink events.Sink) error {
	if err := validateTransform(operation.Input, operation.Output, ArtifactSubtitle, ArtifactSubtitle); err != nil {
		return err
	}
	if err := tools.ConvertSubtitle(ctx, operation.Input.Path, operation.Output.Path, operation.Options, operation.Overwrite, sink); err != nil {
		return err
	}
	return removeOwned(operation.Input, operation.Output)
}

type ThumbnailConvert struct {
	Input, Output Artifact
	Options       ffmpeg.ImageOptions
	Overwrite     bool
}

func (operation ThumbnailConvert) Name() string { return "convert-thumbnail" }
func (operation ThumbnailConvert) Run(ctx context.Context, tools *ffmpeg.Toolset, sink events.Sink) error {
	if err := validateTransform(operation.Input, operation.Output, ArtifactThumbnail, ArtifactThumbnail); err != nil {
		return err
	}
	if err := tools.ConvertImage(ctx, operation.Input.Path, operation.Output.Path, operation.Options, operation.Overwrite, sink); err != nil {
		return err
	}
	return removeOwned(operation.Input, operation.Output)
}

type MetadataEmbed struct {
	Input, Output Artifact
	Metadata      ffmpeg.Metadata
	Overwrite     bool
}

type ChapterEmbed struct {
	Input, Output Artifact
	Chapters      []ffmpeg.Chapter
	Overwrite     bool
}

func (operation ChapterEmbed) Name() string { return "embed-chapters" }
func (operation ChapterEmbed) Run(ctx context.Context, tools *ffmpeg.Toolset, sink events.Sink) error {
	if err := validateTransform(operation.Input, operation.Output, ArtifactMedia, ArtifactMedia); err != nil {
		return err
	}
	if err := tools.EmbedChapters(ctx, operation.Input.Path, operation.Output.Path, operation.Chapters, operation.Overwrite, sink); err != nil {
		return err
	}
	return removeOwned(operation.Input, operation.Output)
}

func (operation MetadataEmbed) Name() string { return "embed-metadata" }
func (operation MetadataEmbed) Run(ctx context.Context, tools *ffmpeg.Toolset, sink events.Sink) error {
	if err := validateTransform(operation.Input, operation.Output, ArtifactMedia, ArtifactMedia); err != nil {
		return err
	}
	if err := tools.EmbedMetadata(ctx, operation.Input.Path, operation.Output.Path, operation.Metadata, operation.Overwrite, sink); err != nil {
		return err
	}
	return removeOwned(operation.Input, operation.Output)
}

type ThumbnailEmbed struct {
	Input, Image, Output Artifact
	Overwrite            bool
}

func (operation ThumbnailEmbed) Name() string { return "embed-thumbnail" }
func (operation ThumbnailEmbed) Run(ctx context.Context, tools *ffmpeg.Toolset, sink events.Sink) error {
	if err := validateTransform(operation.Input, operation.Output, ArtifactMedia, ArtifactMedia); err != nil {
		return err
	}
	if err := validateArtifact(operation.Image, ArtifactThumbnail); err != nil {
		return err
	}
	if err := localRegular(operation.Image.Path); err != nil {
		return err
	}
	if err := tools.EmbedThumbnail(ctx, operation.Input.Path, operation.Image.Path, operation.Output.Path, operation.Overwrite, sink); err != nil {
		return err
	}
	return errors.Join(removeOwned(operation.Input, operation.Output), removeOwned(operation.Image, operation.Output))
}

type SubtitleEmbed struct {
	Input, Subtitle, Output Artifact
	Overwrite               bool
}

func (operation SubtitleEmbed) Name() string { return "embed-subtitle" }
func (operation SubtitleEmbed) Run(ctx context.Context, tools *ffmpeg.Toolset, sink events.Sink) error {
	if err := validateTransform(operation.Input, operation.Output, ArtifactMedia, ArtifactMedia); err != nil {
		return err
	}
	if err := validateArtifact(operation.Subtitle, ArtifactSubtitle); err != nil {
		return err
	}
	if err := localRegular(operation.Subtitle.Path); err != nil {
		return err
	}
	if err := tools.EmbedSubtitles(ctx, operation.Input.Path, operation.Subtitle.Path, operation.Output.Path, operation.Overwrite, sink); err != nil {
		return err
	}
	return errors.Join(removeOwned(operation.Input, operation.Output), removeOwned(operation.Subtitle, operation.Output))
}

type Fixup struct {
	Input, Output Artifact
	Kind          ffmpeg.Fixup
	Overwrite     bool
}

func (operation Fixup) Name() string { return "fixup" }
func (operation Fixup) Run(ctx context.Context, tools *ffmpeg.Toolset, sink events.Sink) error {
	if err := validateTransform(operation.Input, operation.Output, ArtifactMedia, ArtifactMedia); err != nil {
		return err
	}
	if err := tools.ApplyFixup(ctx, operation.Input.Path, operation.Output.Path, operation.Kind, operation.Overwrite, sink); err != nil {
		return err
	}
	return removeOwned(operation.Input, operation.Output)
}

type Concat struct {
	Inputs    []Artifact
	Output    Artifact
	Overwrite bool
}

func (operation Concat) Name() string { return "concat" }
func (operation Concat) Run(ctx context.Context, tools *ffmpeg.Toolset, sink events.Sink) error {
	if err := validateArtifact(operation.Output, ArtifactMedia); err != nil {
		return err
	}
	paths := make([]string, 0, len(operation.Inputs))
	for _, input := range operation.Inputs {
		if err := validateArtifact(input, ArtifactMedia); err != nil {
			return err
		}
		paths = append(paths, input.Path)
	}
	if err := tools.Concat(ctx, paths, operation.Output.Path, operation.Overwrite, sink); err != nil {
		return err
	}
	var cleanup error
	for _, input := range operation.Inputs {
		cleanup = errors.Join(cleanup, removeOwned(input, operation.Output))
	}
	return cleanup
}

// Move atomically moves an already-finalized artifact. Cross-device moves use
// an exclusive temporary copy followed by rename, never a partial destination.
type Move struct {
	Input, Output Artifact
	Overwrite     bool
}

func (operation Move) Name() string { return "move-file" }
func (operation Move) Run(ctx context.Context, _ *ffmpeg.Toolset, _ events.Sink) error {
	if err := validateTransform(operation.Input, operation.Output, operation.Input.Kind, operation.Output.Kind); err != nil {
		return err
	}
	return SafeMoveContext(ctx, operation.Input.Path, operation.Output.Path, operation.Overwrite)
}

func SafeMove(source, destination string, overwrite bool) error {
	return SafeMoveContext(context.Background(), source, destination, overwrite)
}

func SafeMoveContext(ctx context.Context, source, destination string, overwrite bool) error {
	if source == destination {
		return nil
	}
	if err := safePath(source); err != nil {
		return err
	}
	if err := safePath(destination); err != nil {
		return err
	}
	if err := localRegular(source); err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("%w: destination is not regular", ErrUnsafePath)
		}
		if !overwrite {
			return fmt.Errorf("%w: destination exists", ffmpeg.ErrDestinationExists)
		}
		// Windows does not provide an os.Rename replacement that preserves
		// atomicity, so refuse overwrite rather than delete a completed file.
		if runtime.GOOS == "windows" {
			return fmt.Errorf("%w: atomic overwrite unavailable on Windows", ffmpeg.ErrDestinationExists)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	temporaryFile, err := os.CreateTemp(filepath.Dir(destination), ".ytdlp-move-*"+filepath.Ext(destination))
	if err != nil {
		return err
	}
	temporary := temporaryFile.Name()
	out := temporaryFile
	copyErr := copyContext(ctx, out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(temporary)
		return errors.Join(copyErr, syncErr, closeErr)
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Remove(source)
}

func validateTransform(input, output Artifact, inputKind, outputKind ArtifactKind) error {
	if err := validateArtifact(input, inputKind); err != nil {
		return err
	}
	if err := validateArtifact(output, outputKind); err != nil {
		return err
	}
	if input.Path == output.Path {
		return fmt.Errorf("%w: source and output are the same", ErrInvalidGraph)
	}
	if err := localRegular(input.Path); err != nil {
		return err
	}
	if info, err := os.Lstat(output.Path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return fmt.Errorf("%w: output is not regular", ErrUnsafePath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func validateArtifact(artifact Artifact, expected ArtifactKind) error {
	if artifact.Kind != expected {
		return fmt.Errorf("%w: expected %s artifact", ErrInvalidGraph, expected)
	}
	return safePath(artifact.Path)
}

func safePath(path string) error {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return fmt.Errorf("%w: empty or nul path", ErrUnsafePath)
	}
	return nil
}

func localRegular(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrMissingArtifact, path)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: source is not regular", ErrUnsafePath)
	}
	return nil
}

func copyContext(ctx context.Context, destination io.Writer, source io.Reader) error {
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			if _, err := destination.Write(buffer[:count]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func removeOwned(input, output Artifact) error {
	if !input.Owned || input.Path == output.Path {
		return nil
	}
	if err := os.Remove(input.Path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove owned artifact: %w", err)
	}
	return nil
}
