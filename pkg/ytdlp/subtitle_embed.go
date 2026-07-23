package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

var subtitleEmbeddingContainers = map[string]bool{
	"mp4": true, "mov": true, "m4a": true,
	"webm": true, "mkv": true, "mka": true,
}

func (operation *operation) embedSelectedSubtitles(
	ctx context.Context,
	info *value.Info,
	mediaPath string,
	tracks []subtitleTrack,
	artifacts []Artifact,
	sink events.Sink,
) ([]Artifact, bool, error) {
	options := operation.request.Subtitles
	if !options.Embed {
		return artifacts, false, nil
	}
	container := strings.TrimPrefix(strings.ToLower(filepath.Ext(mediaPath)), ".")
	if !subtitleEmbeddingContainers[container] {
		if err := operation.client.emit(ctx, Event{
			Kind:    EventMetadataWarning,
			Message: "subtitles can only be embedded in mp4, mov, m4a, webm, mkv, or mka media",
		}); err != nil {
			return nil, false, err
		}
		return artifacts, false, nil
	}
	inputs := make([]ffmpeg.SubtitleInput, 0, len(tracks))
	for _, track := range tracks {
		path, ok := track.metadata.Lookup("filepath").StringValue()
		if !ok || path == "" || !embeddableSubtitleExtension(container, track.extension) {
			continue
		}
		name, _ := track.metadata.Lookup("name").StringValue()
		inputs = append(inputs, ffmpeg.SubtitleInput{
			Path: path, Language: track.language, Name: name, Extension: track.extension,
		})
	}
	if len(inputs) == 0 {
		if err := operation.client.emit(ctx, Event{
			Kind: EventMetadataWarning, Message: "there are no compatible subtitles to embed",
		}); err != nil {
			return nil, false, err
		}
		return artifacts, false, nil
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		return nil, false, err
	}
	if err := tools.EmbedSubtitleTracks(ctx, mediaPath, inputs, mediaPath, true, sink); err != nil {
		return nil, false, err
	}
	embeddedPaths := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		embeddedPaths[input.Path] = true
	}
	for _, track := range tracks {
		if path, ok := track.metadata.Lookup("filepath").StringValue(); ok && embeddedPaths[path] {
			track.metadata.Set("embedded", value.Bool(true))
		}
	}
	if options.KeepFiles {
		return artifacts, true, nil
	}
	removed := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		if err := operation.removeLocalFile(input.Path); err == nil || errors.Is(err, os.ErrNotExist) {
			removed[input.Path] = true
		} else {
			operation.emitSubtitleCleanupWarning(ctx, input.Path)
		}
	}
	retained := artifacts[:0]
	for _, artifact := range artifacts {
		if artifact.Kind != "subtitle" || !removed[artifact.Path] {
			retained = append(retained, artifact)
		}
	}
	return retained, true, nil
}

func embeddableSubtitleExtension(container, extension string) bool {
	extension = strings.ToLower(extension)
	switch extension {
	case "vtt":
		return true
	case "srt", "ass", "ssa":
		return container != "webm"
	default:
		return false
	}
}

func artifactBytes(artifacts []Artifact) (int64, error) {
	var total int64
	for _, artifact := range artifacts {
		info, err := os.Stat(artifact.Path)
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}
