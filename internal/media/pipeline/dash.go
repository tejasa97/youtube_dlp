// Package pipeline composes protocol downloads with supervised media processing.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"

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
	if !result.MergeRequired {
		if len(result.Tracks) != 1 {
			return ErrMissingDASHTracks
		}
		return nil
	}
	var videoPath, audioPath string
	for _, track := range result.Tracks {
		switch track.Representation.ContentType {
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
