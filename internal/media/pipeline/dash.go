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

var ErrMissingDASHTracks = errors.New("DASH result lacks mergeable audio/video tracks")

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
		return fmt.Errorf("%w: ffmpeg toolset is nil", ErrMissingDASHTracks)
	}
	if err := tools.Merge(ctx, videoPath, audioPath, destination, overwrite, sink); err != nil {
		return err
	}
	return errors.Join(os.Remove(videoPath), os.Remove(audioPath))
}
