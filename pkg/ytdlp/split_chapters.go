package ytdlp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const maxSplitChapterDurationSeconds = 24 * 60 * 60

// splitChapters writes chapter artifacts after the final media postprocessor.
// Chapter paths are rendered beneath the dedicated chapter root from bounded
// section fields; the primary media path remains the archive-bearing result.
func (operation *operation) splitChapters(
	ctx context.Context, info value.Info, mediaPath string, sink events.Sink,
) ([]Artifact, error) {
	if !operation.request.SplitChapters {
		return nil, nil
	}
	chapters, err := canonicalEmbeddedChapters(info)
	if err != nil {
		return nil, err
	}
	if len(chapters) == 0 {
		if err := operation.client.emit(ctx, Event{
			Kind: EventMetadataWarning, Message: "chapter splitting skipped because chapter information is unavailable",
		}); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if len(chapters) > ffmpegMaxChapters() {
		return nil, fmt.Errorf("%w: too many chapters to split", ffmpeg.ErrInvalidOperation)
	}

	outputs := make([]ffmpeg.ChapterOutput, len(chapters))
	artifacts := make([]Artifact, len(chapters))
	seen := make(map[string]struct{}, len(chapters))
	for index, chapter := range chapters {
		if err := validateSplitChapterBounds(chapter); err != nil {
			return nil, fmt.Errorf("%w: chapter %d exceeds duration limit", ffmpeg.ErrInvalidOperation, index+1)
		}
		chapterInfo := value.NewInfo(info.Fields().Clone())
		chapterInfo.Set("section_number", value.Int(int64(index+1)))
		chapterInfo.Set("section_title", value.String(strings.TrimSpace(chapter.Title)))
		chapterInfo.Set("section_start", value.Float(chapter.Start.Seconds()))
		chapterInfo.Set("section_end", value.Float(chapter.End.Seconds()))
		path, err := operation.resolveOutputPath(
			operation.request.outputRoot(OutputPathChapter),
			operation.request.outputTemplate(OutputTemplateChapter), chapterInfo,
		)
		if err != nil {
			return nil, fmt.Errorf("render chapter %d path: %w", index+1, err)
		}
		path = filepath.Clean(path)
		key := portablePathKey(path)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("%w: chapter %d collides with another chapter", ffmpeg.ErrInvalidOperation, index+1)
		}
		seen[key] = struct{}{}
		if _, err := confinedPostprocessPath(operation.request.outputRoot(OutputPathHome), path); err != nil {
			return nil, fmt.Errorf("confine chapter %d destination: %w", index+1, err)
		}
		if err := operation.protectTransactionPath(ctx, path); err != nil {
			return nil, fmt.Errorf("protect chapter %d destination: %w", index+1, err)
		}
		outputs[index] = ffmpeg.ChapterOutput{
			Number: index + 1, Start: chapter.Start, End: chapter.End,
			Title: chapter.Title, Path: path,
		}
		artifacts[index] = Artifact{Path: path, Kind: "chapter"}
	}
	if err := operation.discoverAndSplitChapters(ctx, mediaPath, outputs, sink); err != nil {
		return nil, err
	}
	return artifacts, nil
}

func validateSplitChapterBounds(chapter ffmpeg.Chapter) error {
	if chapter.Start < 0 || chapter.End <= chapter.Start || chapter.End-chapter.Start > maxSplitChapterDurationSeconds*time.Second {
		return fmt.Errorf("invalid chapter duration")
	}
	return nil
}

func (operation *operation) discoverAndSplitChapters(
	ctx context.Context, mediaPath string, outputs []ffmpeg.ChapterOutput, sink events.Sink,
) error {
	tools, err := operation.discoverFFmpeg()
	if err != nil {
		return err
	}
	return tools.SplitChapters(ctx, mediaPath, outputs, operation.request.postprocessorOverwrites(), sink)
}

// ffmpegMaxChapters keeps the product-level validation aligned with the typed
// ffmpeg boundary without exposing its internal constant as public API.
func ffmpegMaxChapters() int { return 1000 }
