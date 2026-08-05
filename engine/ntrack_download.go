package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/tejasa97/youtube_dlp/internal/events"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
)

const maxTrackDownloadConcurrency = 4

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
) (string, int64, error) {
	if !mergeableTracks(selections) {
		return "", 0, fmt.Errorf("%w: selected format set is not mergeable", ErrUnsupported)
	}
	if sink == nil {
		sink = events.Nop()
	}
	workspace, err := os.MkdirTemp(outputRoot, ".ytdlp-formats-")
	if err != nil {
		return "", 0, fmt.Errorf("create selected-format workspace: %w", err)
	}
	defer os.RemoveAll(workspace)

	serializedSink := &lockedEventSink{sink: sink}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type outcome struct {
		index int
		path  string
		bytes int64
		err   error
	}
	outcomes := make(chan outcome, len(selections))
	sem := make(chan struct{}, maxTrackDownloadConcurrency)
	var wg sync.WaitGroup
	for index, selection := range selections {
		wg.Add(1)
		go func(index int, selection mediaformat.Selection) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-childCtx.Done():
				outcomes <- outcome{index: index, err: childCtx.Err()}
				return
			}
			track := trackTemporaryPath(workspace, index, selection.Ext)
			path, count, downloadErr := operation.downloadSelection(
				childCtx, isolatedSelection(selection), workspace, track, serializedSink)
			outcomes <- outcome{index: index, path: path, bytes: count, err: downloadErr}
		}(index, selection)
	}
	go func() {
		wg.Wait()
		close(outcomes)
	}()

	paths := make([]string, len(selections))
	var firstErr error
	for result := range outcomes {
		if result.err != nil && firstErr == nil {
			firstErr = result.err
			cancel()
		}
		if result.err == nil {
			paths[result.index] = result.path
		}
	}
	if firstErr != nil {
		return "", 0, firstErr
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
	if err := tools.MergeTracks(childCtx, mergeInputs, destination, operation.request.Overwrite, serializedSink); err != nil {
		return "", 0, err
	}
	info, err := os.Stat(destination)
	if err != nil {
		return "", 0, err
	}
	return destination, info.Size(), nil
}
