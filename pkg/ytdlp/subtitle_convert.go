package ytdlp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func (operation *operation) convertSelectedSubtitles(
	ctx context.Context,
	tracks []subtitleTrack,
	artifacts []Artifact,
	sink events.Sink,
) ([]subtitleTrack, []Artifact, bool, error) {
	format := operation.request.Subtitles.ConvertFormat
	if format == "" {
		return tracks, artifacts, false, nil
	}
	if format == "vtt" {
		format = "webvtt"
	}
	extension := format
	if extension == "webvtt" {
		extension = "vtt"
	}
	var tools *ffmpeg.Toolset
	converted := false
	for index := range tracks {
		source, ok := tracks[index].metadata.Lookup("filepath").StringValue()
		if !ok || source == "" || strings.EqualFold(tracks[index].extension, extension) {
			continue
		}
		info, err := os.Lstat(source)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, nil, false, fmt.Errorf("%w: subtitle source is not a regular file", ffmpeg.ErrInvalidOperation)
		}
		destination := strings.TrimSuffix(source, filepath.Ext(source)) + "." + extension
		outputRoot := operation.request.OutputDir
		if outputRoot == "" {
			outputRoot = "."
		}
		destination, err = confinedPostprocessPath(outputRoot, destination)
		if err != nil {
			return nil, nil, false, err
		}
		if tools == nil {
			tools, err = ffmpeg.Discover(ffmpeg.Config{})
			if err != nil {
				return nil, nil, false, err
			}
		}
		if err := tools.ConvertSubtitle(ctx, source, destination, ffmpeg.SubtitleOptions{Format: format}, operation.request.Overwrite, sink); err != nil {
			return nil, nil, false, err
		}
		removeErr := operation.removeLocalFile(source)
		retainedSource := removeErr != nil && !os.IsNotExist(removeErr)
		if retainedSource {
			operation.emitSubtitleCleanupWarning(ctx, source)
		}
		for artifactIndex := range artifacts {
			if artifacts[artifactIndex].Kind == "subtitle" && artifacts[artifactIndex].Path == source {
				artifacts[artifactIndex].Path = destination
			}
		}
		if retainedSource {
			artifacts = append(artifacts, Artifact{Path: source, Kind: "subtitle"})
		}
		tracks[index].extension = extension
		tracks[index].metadata.Set("filepath", value.String(destination))
		tracks[index].metadata.Set("ext", value.String(extension))
		converted = true
	}
	return tracks, artifacts, converted, nil
}

func (operation *operation) removeLocalFile(path string) error {
	if operation.removeFile != nil {
		return operation.removeFile(path)
	}
	return os.Remove(path)
}

func (operation *operation) emitSubtitleCleanupWarning(ctx context.Context, path string) {
	if operation.client == nil {
		return
	}
	// Cleanup happens only after the replacement is committed, so an observer
	// failure cannot roll back the operation and is intentionally non-vetoable.
	_ = operation.client.emit(ctx, Event{
		Kind: EventMetadataWarning, Path: path,
		Message: "could not remove a superseded subtitle sidecar; it remains in the result artifacts",
	})
}
