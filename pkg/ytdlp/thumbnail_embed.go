package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/events"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

var thumbnailEmbeddingContainers = map[string]bool{
	"mp3": true, "m4a": true, "mp4": true, "m4v": true, "mov": true,
	"mka": true, "mkv": true, "flac": true, "ogg": true, "opus": true,
}

type thumbnailEmbedFunc func(context.Context, string, string, events.Sink) error

func thumbnailEmbeddingOutputExtension(request Request, selections []mediaformat.Selection, base string) string {
	if base == "" {
		base = mergedOutputExtension(selections)
	}
	if request.Thumbnails.Embed && request.MergeOutputFormat == "" &&
		len(selections) == 2 && base == "webm" && mergeableSelections(selections) {
		return "mkv"
	}
	return base
}

func thumbnailEmbeddingDestination(
	request Request, selections []mediaformat.Selection, destination string, info value.Info,
) string {
	if !hasThumbnailForEmbedding(info) {
		return destination
	}
	oldExtension := mergedOutputExtension(selections)
	newExtension := thumbnailEmbeddingOutputExtension(request, selections, oldExtension)
	if oldExtension == newExtension || destination == "-" {
		return destination
	}
	realExtension := strings.TrimPrefix(filepath.Ext(destination), ".")
	if realExtension == oldExtension || realExtension == newExtension {
		destination = strings.TrimSuffix(destination, filepath.Ext(destination))
	}
	return destination + "." + newExtension
}

func hasThumbnailForEmbedding(info value.Info) bool {
	if thumbnails, ok := info.Lookup("thumbnails").ListValue(); ok && len(thumbnails) > 0 {
		return true
	}
	thumbnail, ok := info.Lookup("thumbnail").StringValue()
	return ok && strings.TrimSpace(thumbnail) != ""
}

func (operation *operation) applyThumbnailEmbeddingOutputExtension(
	info *value.Info, selections []mediaformat.Selection,
) {
	if info == nil || len(selections) == 0 {
		return
	}
	if !hasThumbnailForEmbedding(*info) {
		return
	}
	base, _ := info.Lookup("ext").StringValue()
	if base == "" {
		base = mergedOutputExtension(selections)
	}
	extension := thumbnailEmbeddingOutputExtension(operation.request, selections, base)
	if extension != base {
		info.Set("ext", value.String(extension))
	}
}

func (operation *operation) embedSelectedThumbnail(
	ctx context.Context,
	info *value.Info,
	mediaPath string,
	artifacts []Artifact,
	sink events.Sink,
) ([]Artifact, bool, error) {
	options := operation.request.Thumbnails
	if !options.Embed {
		return artifacts, false, nil
	}
	thumbnail, metadata := thumbnailForEmbedding(info, artifacts)
	if thumbnail == "" || metadata == nil {
		if err := operation.client.emit(ctx, Event{
			Kind: EventMetadataWarning, Message: "there are no downloaded thumbnails to embed",
		}); err != nil {
			return nil, false, err
		}
		return artifacts, false, nil
	}
	container := strings.TrimPrefix(strings.ToLower(filepath.Ext(mediaPath)), ".")
	if !thumbnailEmbeddingContainers[container] {
		return artifacts, false, fmt.Errorf("%w: unsupported thumbnail embedding container %q", ffmpeg.ErrInvalidOperation, container)
	}
	if fileInfo, err := os.Lstat(thumbnail); err != nil ||
		fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
		if err := operation.client.emit(ctx, Event{
			Kind: EventMetadataWarning, Path: thumbnail,
			Message: "thumbnail embedding skipped because the selected image is missing or unsafe",
		}); err != nil {
			return nil, false, err
		}
		return artifacts, false, nil
	}

	embedImage := thumbnail
	temporaryImage := ""
	extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(embedImage)), ".")
	if container != "mkv" && container != "mka" && extension != "jpg" && extension != "jpeg" && extension != "png" {
		destination, err := thumbnailEmbedConversionPath(
			operation.request.outputRoot(OutputPathHome), thumbnail,
		)
		if err != nil {
			return artifacts, false, err
		}
		converter := operation.thumbnailConvert
		if converter == nil {
			tools, discoverErr := operation.discoverFFmpeg()
			if discoverErr != nil {
				return artifacts, false, discoverErr
			}
			converter = func(ctx context.Context, source, destination, format string, overwrite bool, sink events.Sink) error {
				return tools.ConvertImage(ctx, source, destination, ffmpeg.ImageOptions{Format: format}, overwrite, sink)
			}
		}
		if err := converter(ctx, thumbnail, destination, "png", false, sink); err != nil {
			return artifacts, false, err
		}
		embedImage, temporaryImage = destination, destination
	}

	embed := operation.thumbnailEmbed
	if embed == nil {
		tools, err := operation.discoverFFmpeg()
		if err != nil {
			operation.removeThumbnailEmbedTemporary(ctx, temporaryImage)
			return artifacts, false, err
		}
		embed = func(ctx context.Context, media, image string, sink events.Sink) error {
			return tools.EmbedThumbnail(ctx, media, image, media, true, sink)
		}
	}
	if err := embed(ctx, mediaPath, embedImage, sink); err != nil {
		operation.removeThumbnailEmbedTemporary(ctx, temporaryImage)
		return artifacts, false, err
	}
	metadata.Set("embedded", value.Bool(true))

	retained := artifacts
	if !options.KeepFiles {
		if err := operation.removeLocalFile(thumbnail); err == nil || errors.Is(err, os.ErrNotExist) {
			retained = removeArtifact(retained, "thumbnail", thumbnail)
		} else {
			operation.emitThumbnailEmbedCleanupWarning(ctx, thumbnail)
		}
	}
	if temporaryImage != "" {
		if err := operation.removeLocalFile(temporaryImage); err != nil && !errors.Is(err, os.ErrNotExist) {
			operation.emitThumbnailEmbedCleanupWarning(ctx, temporaryImage)
			retained = append(retained, Artifact{Path: temporaryImage, Kind: "thumbnail"})
		}
	}
	return retained, true, nil
}

func thumbnailForEmbedding(info *value.Info, artifacts []Artifact) (string, *value.Object) {
	if info == nil {
		return "", nil
	}
	available := make(map[string]bool)
	for _, artifact := range artifacts {
		if artifact.Kind == "thumbnail" {
			available[artifact.Path] = true
		}
	}
	thumbnails, ok := info.Lookup("thumbnails").ListValue()
	if !ok {
		return "", nil
	}
	for index := len(thumbnails) - 1; index >= 0; index-- {
		metadata, objectOK := thumbnails[index].Object()
		if !objectOK {
			continue
		}
		path, pathOK := metadata.Lookup("filepath").StringValue()
		if pathOK && available[path] {
			return path, metadata
		}
	}
	return "", nil
}

func thumbnailEmbedConversionPath(home, source string) (string, error) {
	extension := filepath.Ext(source)
	if extension == "" {
		return "", fmt.Errorf("%w: thumbnail source has no extension", ffmpeg.ErrInvalidOperation)
	}
	return confinedPostprocessPath(home, strings.TrimSuffix(source, extension)+".embed.png")
}

func removeArtifact(artifacts []Artifact, kind, path string) []Artifact {
	retained := artifacts[:0]
	for _, artifact := range artifacts {
		if artifact.Kind != kind || artifact.Path != path {
			retained = append(retained, artifact)
		}
	}
	return retained
}

func (operation *operation) removeThumbnailEmbedTemporary(ctx context.Context, path string) {
	if path == "" {
		return
	}
	if err := operation.removeLocalFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		operation.emitThumbnailEmbedCleanupWarning(ctx, path)
	}
}

func (operation *operation) emitThumbnailEmbedCleanupWarning(ctx context.Context, path string) {
	if operation.client == nil {
		return
	}
	_ = operation.client.emit(ctx, Event{
		Kind: EventMetadataWarning, Path: path,
		Message: "could not remove a thumbnail after embedding; it remains in the result artifacts",
	})
}
