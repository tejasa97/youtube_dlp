package ytdlp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/media/postprocess"
)

func (operation *operation) applyPostprocessors(ctx context.Context, outputRoot, downloadedPath string, sink events.Sink) (string, []Artifact, error) {
	if len(operation.request.Postprocessors) == 0 {
		return downloadedPath, []Artifact{{Path: downloadedPath, Kind: "media"}}, nil
	}
	if len(operation.request.Postprocessors) > 64 {
		return "", nil, fmt.Errorf("%w: more than 64 postprocessors", postprocess.ErrInvalidGraph)
	}
	current := downloadedPath
	auxiliary := make([]Artifact, 0)
	var tools *ffmpeg.Toolset
	discover := func() (*ffmpeg.Toolset, error) {
		if tools != nil {
			return tools, nil
		}
		var err error
		tools, err = ffmpeg.Discover(ffmpeg.Config{})
		return tools, err
	}
	for index, specification := range operation.request.Postprocessors {
		if countPostprocessorChoices(specification) != 1 {
			return "", nil, fmt.Errorf("%w: postprocessors[%d] must select exactly one operation", postprocess.ErrInvalidGraph, index)
		}
		var graphOperation postprocess.Operation
		needsTools := true
		switch {
		case specification.ExtractAudio != nil:
			config := specification.ExtractAudio
			destination, err := postprocessOutput(outputRoot, config.Destination, current, config.Codec)
			if err != nil {
				return "", nil, err
			}
			graphOperation = postprocess.AudioExtract{
				Input:     postprocess.Artifact{Path: current, Kind: postprocess.ArtifactMedia, Owned: true},
				Output:    postprocess.Artifact{Path: destination, Kind: postprocess.ArtifactMedia},
				Options:   ffmpeg.AudioOptions{Codec: config.Codec, Bitrate: config.Bitrate, Quality: config.Quality},
				Overwrite: operation.request.Overwrite,
			}
			current = destination
		case specification.Remux != nil:
			format := specification.Remux.Format
			if format == "" {
				format = "mkv"
			}
			destination, err := postprocessOutput(outputRoot, specification.Remux.Destination, current, format)
			if err != nil {
				return "", nil, err
			}
			toolset, err := discover()
			if err != nil {
				return "", nil, err
			}
			if err := toolset.Remux(ctx, current, destination, operation.request.Overwrite, sink); err != nil {
				return "", nil, err
			}
			if current != destination {
				if err := os.Remove(current); err != nil {
					return "", nil, err
				}
			}
			current = destination
			continue
		case specification.ConvertSubtitle != nil:
			config := specification.ConvertSubtitle
			source, err := postprocessInput(outputRoot, config.Source)
			if err != nil {
				return "", nil, err
			}
			destination, err := postprocessOutput(outputRoot, config.Destination, source, config.Format)
			if err != nil {
				return "", nil, err
			}
			graphOperation = postprocess.SubtitleConvert{Input: postprocess.Artifact{Path: source, Kind: postprocess.ArtifactSubtitle}, Output: postprocess.Artifact{Path: destination, Kind: postprocess.ArtifactSubtitle}, Options: ffmpeg.SubtitleOptions{Format: config.Format}, Overwrite: operation.request.Overwrite}
			auxiliary = append(auxiliary, Artifact{Path: destination, Kind: "subtitle"})
		case specification.ConvertThumbnail != nil:
			config := specification.ConvertThumbnail
			source, err := postprocessInput(outputRoot, config.Source)
			if err != nil {
				return "", nil, err
			}
			destination, err := postprocessOutput(outputRoot, config.Destination, source, config.Format)
			if err != nil {
				return "", nil, err
			}
			graphOperation = postprocess.ThumbnailConvert{Input: postprocess.Artifact{Path: source, Kind: postprocess.ArtifactThumbnail}, Output: postprocess.Artifact{Path: destination, Kind: postprocess.ArtifactThumbnail}, Options: ffmpeg.ImageOptions{Format: config.Format}, Overwrite: operation.request.Overwrite}
			auxiliary = append(auxiliary, Artifact{Path: destination, Kind: "thumbnail"})
		case specification.EmbedMetadata != nil:
			config := specification.EmbedMetadata
			destination, err := postprocessOutput(outputRoot, config.Destination, current, filepath.Ext(current))
			if err != nil {
				return "", nil, err
			}
			graphOperation = postprocess.MetadataEmbed{Input: postprocess.Artifact{Path: current, Kind: postprocess.ArtifactMedia, Owned: true}, Output: postprocess.Artifact{Path: destination, Kind: postprocess.ArtifactMedia}, Metadata: ffmpeg.Metadata(config.Metadata), Overwrite: operation.request.Overwrite}
			current = destination
		case specification.EmbedChapters != nil:
			config := specification.EmbedChapters
			destination, err := postprocessOutput(outputRoot, config.Destination, current, filepath.Ext(current))
			if err != nil {
				return "", nil, err
			}
			chapters := make([]ffmpeg.Chapter, len(config.Chapters))
			for index, chapter := range config.Chapters {
				chapters[index] = ffmpeg.Chapter{Start: chapter.Start, End: chapter.End, Title: chapter.Title}
			}
			graphOperation = postprocess.ChapterEmbed{Input: postprocess.Artifact{Path: current, Kind: postprocess.ArtifactMedia, Owned: true}, Output: postprocess.Artifact{Path: destination, Kind: postprocess.ArtifactMedia}, Chapters: chapters, Overwrite: operation.request.Overwrite}
			current = destination
		case specification.EmbedThumbnail != nil:
			config := specification.EmbedThumbnail
			source, err := postprocessInput(outputRoot, config.Source)
			if err != nil {
				return "", nil, err
			}
			destination, err := postprocessOutput(outputRoot, config.Destination, current, filepath.Ext(current))
			if err != nil {
				return "", nil, err
			}
			graphOperation = postprocess.ThumbnailEmbed{Input: postprocess.Artifact{Path: current, Kind: postprocess.ArtifactMedia, Owned: true}, Image: postprocess.Artifact{Path: source, Kind: postprocess.ArtifactThumbnail}, Output: postprocess.Artifact{Path: destination, Kind: postprocess.ArtifactMedia}, Overwrite: operation.request.Overwrite}
			current = destination
		case specification.EmbedSubtitle != nil:
			config := specification.EmbedSubtitle
			source, err := postprocessInput(outputRoot, config.Source)
			if err != nil {
				return "", nil, err
			}
			destination, err := postprocessOutput(outputRoot, config.Destination, current, filepath.Ext(current))
			if err != nil {
				return "", nil, err
			}
			graphOperation = postprocess.SubtitleEmbed{Input: postprocess.Artifact{Path: current, Kind: postprocess.ArtifactMedia, Owned: true}, Subtitle: postprocess.Artifact{Path: source, Kind: postprocess.ArtifactSubtitle}, Output: postprocess.Artifact{Path: destination, Kind: postprocess.ArtifactMedia}, Overwrite: operation.request.Overwrite}
			current = destination
		case specification.Fixup != nil:
			config := specification.Fixup
			destination, err := postprocessOutput(outputRoot, config.Destination, current, filepath.Ext(current))
			if err != nil {
				return "", nil, err
			}
			graphOperation = postprocess.Fixup{Input: postprocess.Artifact{Path: current, Kind: postprocess.ArtifactMedia, Owned: true}, Output: postprocess.Artifact{Path: destination, Kind: postprocess.ArtifactMedia}, Kind: ffmpeg.Fixup(config.Kind), Overwrite: operation.request.Overwrite}
			current = destination
		case specification.Concat != nil:
			config := specification.Concat
			inputs := make([]postprocess.Artifact, len(config.Sources))
			for sourceIndex, sourceName := range config.Sources {
				source, err := postprocessInput(outputRoot, sourceName)
				if err != nil {
					return "", nil, err
				}
				inputs[sourceIndex] = postprocess.Artifact{Path: source, Kind: postprocess.ArtifactMedia}
			}
			destination, err := postprocessOutput(outputRoot, config.Destination, current, filepath.Ext(current))
			if err != nil {
				return "", nil, err
			}
			graphOperation = postprocess.Concat{Inputs: inputs, Output: postprocess.Artifact{Path: destination, Kind: postprocess.ArtifactMedia}, Overwrite: operation.request.Overwrite}
			current = destination
		case specification.Move != nil:
			destination, err := postprocessOutput(outputRoot, specification.Move.Destination, current, filepath.Ext(current))
			if err != nil {
				return "", nil, err
			}
			graphOperation = postprocess.Move{Input: postprocess.Artifact{Path: current, Kind: postprocess.ArtifactMedia, Owned: true}, Output: postprocess.Artifact{Path: destination, Kind: postprocess.ArtifactMedia}, Overwrite: operation.request.Overwrite}
			current, needsTools = destination, false
		}
		var toolset *ffmpeg.Toolset
		if needsTools {
			var err error
			toolset, err = discover()
			if err != nil {
				return "", nil, err
			}
		}
		if err := (postprocess.Graph{Operations: []postprocess.Operation{graphOperation}}).Run(ctx, toolset, sink); err != nil {
			return "", nil, err
		}
	}
	artifacts := append(auxiliary, Artifact{Path: current, Kind: "media"})
	return current, artifacts, nil
}

func countPostprocessorChoices(step Postprocessor) int {
	count := 0
	for _, selected := range []bool{
		step.ExtractAudio != nil, step.Remux != nil, step.ConvertSubtitle != nil,
		step.ConvertThumbnail != nil, step.EmbedMetadata != nil, step.EmbedChapters != nil,
		step.EmbedThumbnail != nil, step.EmbedSubtitle != nil, step.Fixup != nil,
		step.Concat != nil, step.Move != nil,
	} {
		if selected {
			count++
		}
	}
	return count
}

func postprocessInput(root, requested string) (string, error) {
	if requested == "" {
		return "", fmt.Errorf("%w: source is required", postprocess.ErrUnsafePath)
	}
	if filepath.IsAbs(requested) {
		if strings.ContainsRune(requested, 0) {
			return "", postprocess.ErrUnsafePath
		}
		return filepath.Clean(requested), nil
	}
	return confinedPostprocessPath(root, requested)
}

func validatePostprocessorPaths(request Request) error {
	root := request.OutputDir
	if root == "" {
		root = "."
	}
	validateDestination := func(destination string) error {
		if destination == "" {
			return nil
		}
		_, err := confinedPostprocessPath(root, destination)
		return err
	}
	validateSource := func(source string) error {
		if source == "" {
			return postprocess.ErrUnsafePath
		}
		_, err := postprocessInput(root, source)
		return err
	}
	for _, step := range request.Postprocessors {
		switch {
		case step.ExtractAudio != nil:
			if err := validateDestination(step.ExtractAudio.Destination); err != nil {
				return err
			}
		case step.Remux != nil:
			if err := validateDestination(step.Remux.Destination); err != nil {
				return err
			}
		case step.ConvertSubtitle != nil:
			if err := validateSource(step.ConvertSubtitle.Source); err != nil {
				return err
			}
			if err := validateDestination(step.ConvertSubtitle.Destination); err != nil {
				return err
			}
		case step.ConvertThumbnail != nil:
			if err := validateSource(step.ConvertThumbnail.Source); err != nil {
				return err
			}
			if err := validateDestination(step.ConvertThumbnail.Destination); err != nil {
				return err
			}
		case step.EmbedMetadata != nil:
			if err := validateDestination(step.EmbedMetadata.Destination); err != nil {
				return err
			}
		case step.EmbedChapters != nil:
			if err := validateDestination(step.EmbedChapters.Destination); err != nil {
				return err
			}
		case step.EmbedThumbnail != nil:
			if err := validateSource(step.EmbedThumbnail.Source); err != nil {
				return err
			}
			if err := validateDestination(step.EmbedThumbnail.Destination); err != nil {
				return err
			}
		case step.EmbedSubtitle != nil:
			if err := validateSource(step.EmbedSubtitle.Source); err != nil {
				return err
			}
			if err := validateDestination(step.EmbedSubtitle.Destination); err != nil {
				return err
			}
		case step.Fixup != nil:
			if err := validateDestination(step.Fixup.Destination); err != nil {
				return err
			}
		case step.Concat != nil:
			for _, source := range step.Concat.Sources {
				if err := validateSource(source); err != nil {
					return err
				}
			}
			if err := validateDestination(step.Concat.Destination); err != nil {
				return err
			}
		case step.Move != nil:
			if err := validateDestination(step.Move.Destination); err != nil {
				return err
			}
		}
	}
	return nil
}

func postprocessOutput(root, requested, current, fallbackExtension string) (string, error) {
	if requested == "" {
		extension := strings.TrimPrefix(fallbackExtension, ".")
		if extension == "" {
			return "", fmt.Errorf("%w: destination or output format is required", postprocess.ErrUnsafePath)
		}
		stem := strings.TrimSuffix(filepath.Base(current), filepath.Ext(current))
		if strings.EqualFold(strings.TrimPrefix(filepath.Ext(current), "."), extension) {
			requested = stem + ".postprocessed." + extension
		} else {
			requested = stem + "." + extension
		}
	}
	if filepath.IsAbs(requested) {
		return confinedPostprocessPath(root, requested)
	}
	return confinedPostprocessPath(root, requested)
}

func confinedPostprocessPath(root, requested string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	candidate := requested
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootAbs, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(rootAbs, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", postprocess.ErrUnsafePath
	}
	if strings.ContainsRune(candidate, 0) {
		return "", postprocess.ErrUnsafePath
	}
	current := rootAbs
	for _, component := range strings.Split(filepath.Dir(relative), string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		if info, statErr := os.Lstat(current); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", postprocess.ErrUnsafePath
		} else if statErr != nil && !os.IsNotExist(statErr) {
			return "", statErr
		}
	}
	return candidate, nil
}
