package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tejasa97/ytdlp-go/internal/events"
	mediaformat "github.com/tejasa97/ytdlp-go/internal/format"
	"github.com/tejasa97/ytdlp-go/internal/media/ffmpeg"
)

func (operation *operation) mergeOutputPreferences() []string {
	if operation == nil {
		return nil
	}
	return mergeOutputFormatPreferences(operation.request.MergeOutputFormat)
}

func selectionHasVideo(selection mediaformat.Selection) bool {
	return selection.VCodec != "none"
}

func selectionHasAudio(selection mediaformat.Selection) bool {
	return selection.ACodec != "none"
}

func mergeableTracks(selections []mediaformat.Selection) bool {
	if len(selections) < 2 || len(selections) > mediaformat.MaxMergeTracks {
		return false
	}
	for _, selection := range selections {
		if !selectionHasVideo(selection) && !selectionHasAudio(selection) {
			return false
		}
	}
	return true
}

func isolatedSelection(selection mediaformat.Selection) mediaformat.Selection {
	if selection.Headers == nil {
		return selection
	}
	clone := selection
	clone.Headers = selection.Headers.Clone()
	return clone
}

func trackTemporaryPath(workspace string, index int, extension string) string {
	return filepath.Join(workspace, fmt.Sprintf("track-%03d.%s", index, safeExtension(extension)))
}

func (operation *operation) downloadAndMergeTracks(
	ctx context.Context,
	selections []mediaformat.Selection,
	outputRoot, destination string,
	sink events.Sink,
) (path string, bytes int64, returnErr error) {
	if !mergeableTracks(selections) {
		return "", 0, fmt.Errorf("%w: selected format set is not mergeable", ErrUnsupported)
	}
	if sink == nil {
		sink = events.Nop()
	}
	workspace, _, err := expectedNTrackWorkspace(outputRoot, destination, selections)
	if err != nil {
		return "", 0, err
	}
	releaseWorkspace := acquireNTrackWorkspace(workspace)
	defer releaseWorkspace()
	workspace, err = prepareNTrackWorkspace(
		outputRoot, destination, selections, operation.request.Filesystem.NoContinue,
	)
	if err != nil {
		return "", 0, err
	}
	preserveWorkspace := false
	defer func() {
		if preserveWorkspace {
			return
		}
		returnErr = errors.Join(returnErr, removeNTrackWorkspace(workspace))
	}()

	serializedSink := &lockedEventSink{sink: sink}
	paths := make([]string, len(selections))
	for index, selection := range selections {
		track := trackTemporaryPath(workspace, index, selection.Ext)
		if size, reusable, reuseErr := reusableNTrack(track); reuseErr != nil {
			return "", 0, reuseErr
		} else if reusable {
			_ = serializedSink.Emit(ctx, events.Event{
				Kind: events.KindCompleted, Path: track,
				Bytes: size, Total: size, Resuming: true,
			})
			paths[index] = track
			continue
		}
		path, _, downloadErr := operation.downloadSelection(
			ctx, isolatedSelection(selection), workspace, track, serializedSink)
		if downloadErr != nil {
			preserveWorkspace = operation.request.Filesystem.PreservePartialOnCancel &&
				!operation.request.Filesystem.NoContinue && ctx.Err() != nil
			return "", 0, downloadErr
		}
		paths[index] = path
	}

	mergeInputs := make([]ffmpeg.MergeInput, len(selections))
	for index, selection := range selections {
		mergeInputs[index] = ffmpeg.MergeInput{
			Path:     paths[index],
			HasAudio: selectionHasAudio(selection),
			HasVideo: selectionHasVideo(selection),
			Protocol: selection.Protocol,
		}
	}
	tools, err := operation.discoverFFmpeg()
	if err != nil {
		return "", 0, err
	}
	if err := tools.MergeTracks(ctx, mergeInputs, destination, operation.request.Overwrite, serializedSink); err != nil {
		preserveWorkspace = operation.request.Filesystem.PreservePartialOnCancel &&
			!operation.request.Filesystem.NoContinue && ctx.Err() != nil
		return "", 0, err
	}
	info, err := os.Stat(destination)
	if err != nil {
		return "", 0, err
	}
	return destination, info.Size(), nil
}
