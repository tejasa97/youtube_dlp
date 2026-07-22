// Package pipeline composes protocol downloads with supervised media processing.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/protocol/dash"
)

var (
	ErrMissingDASHTracks = errors.New("DASH result lacks mergeable audio/video tracks")
	ErrMissingToolset    = errors.New("media pipeline requires an ffmpeg toolset")
)

// FinalizeDASH merges separate selected tracks and removes their temporary
// assembled files only after ffmpeg completes successfully.
func FinalizeDASH(ctx context.Context, result dash.Result, destination string, overwrite bool, tools *ffmpeg.Toolset, sink events.Sink) error {
	if result.MultiPeriod {
		return finalizeMultiPeriodDASH(ctx, result, destination, overwrite, tools, sink)
	}
	if !result.MergeRequired {
		if len(result.Tracks) != 1 {
			return ErrMissingDASHTracks
		}
		return nil
	}
	var videoPath, audioPath string
	for _, track := range result.Tracks {
		switch dashTrackKind(track.Representation) {
		case "video":
			videoPath = track.Download.Path
		case "audio":
			audioPath = track.Download.Path
		}
	}
	if videoPath == "" || audioPath == "" {
		return ErrMissingDASHTracks
	}
	if tools == nil {
		return ErrMissingToolset
	}
	if err := tools.Merge(ctx, videoPath, audioPath, destination, overwrite, sink); err != nil {
		return err
	}
	return errors.Join(os.Remove(videoPath), os.Remove(audioPath))
}

func finalizeMultiPeriodDASH(ctx context.Context, result dash.Result, destination string, overwrite bool, tools *ffmpeg.Toolset, sink events.Sink) error {
	if len(result.Tracks) == 0 {
		return ErrMissingDASHTracks
	}
	if tools == nil {
		return ErrMissingToolset
	}
	inputs := func(track dash.TrackResult) ([]string, error) {
		if len(track.PeriodDownloads) < 2 {
			return nil, ErrMissingDASHTracks
		}
		paths := make([]string, len(track.PeriodDownloads))
		for index, download := range track.PeriodDownloads {
			if download.Path == "" {
				return nil, ErrMissingDASHTracks
			}
			paths[index] = download.Path
		}
		return paths, nil
	}
	removeInputs := func() error {
		var err error
		for _, track := range result.Tracks {
			for _, download := range track.PeriodDownloads {
				err = errors.Join(err, os.Remove(download.Path))
			}
		}
		return err
	}

	if !result.MergeRequired {
		if len(result.Tracks) != 1 {
			return ErrMissingDASHTracks
		}
		paths, err := inputs(result.Tracks[0])
		if err != nil {
			return err
		}
		if err := tools.Concat(ctx, paths, destination, overwrite, sink); err != nil {
			return err
		}
		return removeInputs()
	}

	temporaryRoot, err := os.MkdirTemp(filepath.Dir(destination), ".ytdlp-dash-periods-")
	if err != nil {
		return fmt.Errorf("%w: allocate multi-period workspace: %v", ffmpeg.ErrMediaFailure, err)
	}
	defer os.RemoveAll(temporaryRoot)
	var videoPath, audioPath string
	for _, track := range result.Tracks {
		kind := dashTrackKind(track.Representation)
		if kind != "video" && kind != "audio" {
			return ErrMissingDASHTracks
		}
		paths, inputErr := inputs(track)
		if inputErr != nil {
			return inputErr
		}
		concatenated := filepath.Join(temporaryRoot, kind+".mp4")
		if err := tools.Concat(ctx, paths, concatenated, false, sink); err != nil {
			return err
		}
		if kind == "video" {
			videoPath = concatenated
		} else {
			audioPath = concatenated
		}
	}
	if videoPath == "" || audioPath == "" {
		return ErrMissingDASHTracks
	}
	if err := tools.Merge(ctx, videoPath, audioPath, destination, overwrite, sink); err != nil {
		return err
	}
	return removeInputs()
}

func dashTrackKind(representation dash.Representation) string {
	if representation.ContentType != "" {
		return representation.ContentType
	}
	return strings.SplitN(representation.MimeType, "/", 2)[0]
}

// RemuxDownload runs a container-only postprocessor and removes the source
// only after the destination has been atomically finalized. The source remains
// resumable on cancellation or media failure.
func RemuxDownload(ctx context.Context, source, destination string, overwrite bool, tools *ffmpeg.Toolset, sink events.Sink) error {
	if tools == nil {
		return ErrMissingToolset
	}
	if source == "" || destination == "" {
		return fmt.Errorf("%w: source and destination are required", ffmpeg.ErrMediaFailure)
	}
	if err := tools.Remux(ctx, source, destination, overwrite, sink); err != nil {
		return err
	}
	if source == destination {
		return nil
	}
	if err := os.Remove(source); err != nil {
		return fmt.Errorf("%w: remove remux source: %v", ffmpeg.ErrMediaFailure, err)
	}
	return nil
}
