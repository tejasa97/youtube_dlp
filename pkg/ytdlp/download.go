package ytdlp

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/downloader"
	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/media/pipeline"
	"github.com/ytdlp-go/ytdlp/internal/protocol/dash"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/protocol/ism"
	"github.com/ytdlp-go/ytdlp/internal/protocol/youtubelive"
)

func (operation *operation) downloadSelections(ctx context.Context, selections []mediaformat.Selection, outputRoot, destination string, sink events.Sink) (string, int64, error) {
	if len(selections) == 1 {
		return operation.downloadSelection(ctx, selections[0], outputRoot, destination, sink)
	}
	if len(selections) != 2 || !mergeableSelections(selections) {
		return "", 0, fmt.Errorf("%w: selected format set is not a video/audio merge", extractor.ErrUnsupported)
	}
	temporaryRoot, err := os.MkdirTemp(outputRoot, ".ytdlp-formats-")
	if err != nil {
		return "", 0, fmt.Errorf("create selected-format workspace: %w", err)
	}
	defer os.RemoveAll(temporaryRoot)

	paths := make([]string, len(selections))
	var bytes int64
	for index, selection := range selections {
		track := filepath.Join(temporaryRoot, fmt.Sprintf("track-%d.%s", index, safeExtension(selection.Ext)))
		path, count, downloadErr := operation.downloadSelection(ctx, selection, temporaryRoot, track, sink)
		if downloadErr != nil {
			return "", 0, downloadErr
		}
		paths[index], bytes = path, bytes+count
	}
	video, audio := paths[0], paths[1]
	if selections[1].VCodec != "" && selections[1].VCodec != "none" {
		video, audio = paths[1], paths[0]
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		return "", 0, err
	}
	if err := tools.Merge(ctx, video, audio, destination, operation.request.Overwrite, sink); err != nil {
		return "", 0, err
	}
	if info, err := os.Stat(destination); err == nil {
		bytes = info.Size()
	}
	return destination, bytes, nil
}

func (operation *operation) downloadSelection(ctx context.Context, selected mediaformat.Selection, outputRoot, destination string, sink events.Sink) (string, int64, error) {
	options := operation.request.Downloader
	if selected.YouTubePostLive {
		if options.External != nil {
			return "", 0, fmt.Errorf("%w: external downloaders cannot consume generated YouTube post-live fragments", extractor.ErrUnsupported)
		}
		if selected.TargetDuration <= 0 || selected.TargetDuration > 3600 ||
			math.IsNaN(selected.TargetDuration) || math.IsInf(selected.TargetDuration, 0) {
			return "", 0, fmt.Errorf("%w: invalid YouTube post-live target duration", extractor.ErrInvalidMetadata)
		}
		targetDuration := time.Duration(selected.TargetDuration * float64(time.Second))
		if targetDuration <= 0 {
			return "", 0, fmt.Errorf("%w: invalid YouTube post-live target duration", extractor.ErrInvalidMetadata)
		}
		result, err := youtubelive.NewDownloader(operation.transport, youtubelive.Config{
			Headers: selected.Headers, TargetDuration: targetDuration,
			LiveStartTimestamp:  selected.LiveStartTimestamp,
			FragmentConcurrency: options.FragmentConcurrency, PerHostConcurrency: options.PerHostFragmentConcurrency,
			MaxSegments: options.MaxSegments, MaxSegmentSize: options.MaxSegmentBytes, Attempts: options.Attempts,
			RetryBaseDelay: options.RetryBaseDelay, RetryMaxDelay: options.RetryMaxDelay,
		}).Download(ctx, selected.URL, outputRoot, destination, operation.request.Overwrite, sink)
		if err != nil {
			return "", 0, err
		}
		return result.Path, result.Bytes, nil
	}
	if options.External != nil {
		result, err := downloader.NewExternalAdapter(nil).Download(ctx, downloader.ExternalRequest{
			Executable: options.External.Executable, Arguments: append([]string(nil), options.External.Arguments...),
			URL: selected.URL, OutputRoot: outputRoot, Destination: destination,
		})
		if err != nil {
			return "", 0, err
		}
		info, err := os.Stat(result.Path)
		if err != nil {
			return "", 0, err
		}
		return result.Path, info.Size(), nil
	}

	switch selected.Protocol {
	case "m3u8_native":
		result, err := hls.NewDownloader(operation.transport, hls.Config{
			Headers:             selected.Headers,
			FragmentConcurrency: options.FragmentConcurrency, PerHostConcurrency: options.PerHostFragmentConcurrency,
			MaxSegments: options.MaxSegments, MaxSegmentSize: options.MaxSegmentBytes, Attempts: options.Attempts,
			RetryBaseDelay: options.RetryBaseDelay, RetryMaxDelay: options.RetryMaxDelay,
		}).Download(ctx, selected.URL, outputRoot, destination, operation.request.Overwrite, sink)
		if err != nil {
			return "", 0, err
		}
		return result.Path, result.Bytes, nil
	case "http_dash_segments":
		result, err := dash.NewDownloader(operation.transport, dash.Config{
			Headers:             selected.Headers,
			FragmentConcurrency: options.FragmentConcurrency, PerHostConcurrency: options.PerHostFragmentConcurrency,
			MaxSegments: options.MaxSegments, MaxSegmentSize: options.MaxSegmentBytes, Attempts: options.Attempts,
			RetryBaseDelay: options.RetryBaseDelay, RetryMaxDelay: options.RetryMaxDelay,
		}).Download(ctx, selected.URL, outputRoot, destination, operation.request.Overwrite, sink)
		if err != nil {
			return "", 0, err
		}
		if result.MergeRequired || result.MultiPeriod {
			tools, discoverErr := ffmpeg.Discover(ffmpeg.Config{})
			if discoverErr != nil {
				return "", 0, discoverErr
			}
			if err := pipeline.FinalizeDASH(ctx, result, destination, operation.request.Overwrite, tools, sink); err != nil {
				return "", 0, err
			}
			info, err := os.Stat(destination)
			if err != nil {
				return "", 0, err
			}
			return destination, info.Size(), nil
		}
		return result.Tracks[0].Download.Path, result.Tracks[0].Download.Bytes, nil
	case "ism", "ismc", "mss":
		result, err := ism.NewDownloader(operation.transport, ism.Config{
			Headers:             selected.Headers,
			FragmentConcurrency: options.FragmentConcurrency,
			PerHostConcurrency:  options.PerHostFragmentConcurrency,
			MaxSegments:         options.MaxSegments,
			MaxSegmentSize:      options.MaxSegmentBytes,
			Attempts:            options.Attempts,
			RetryBaseDelay:      options.RetryBaseDelay,
			RetryMaxDelay:       options.RetryMaxDelay,
		}).Download(ctx, selected.URL, outputRoot, destination, operation.request.Overwrite, sink)
		if err != nil {
			return "", 0, err
		}
		if !result.MergeRequired {
			return result.Tracks[0].Download.Path, result.Tracks[0].Download.Bytes, nil
		}
		var video, audio string
		for _, track := range result.Tracks {
			switch track.Stream.Type {
			case "video":
				video = track.Download.Path
			case "audio":
				audio = track.Download.Path
			}
		}
		if video == "" || audio == "" {
			return "", 0, pipeline.ErrMissingDASHTracks
		}
		tools, discoverErr := ffmpeg.Discover(ffmpeg.Config{})
		if discoverErr != nil {
			return "", 0, discoverErr
		}
		if err := tools.Merge(ctx, video, audio, destination, operation.request.Overwrite, sink); err != nil {
			return "", 0, err
		}
		_ = os.Remove(video)
		_ = os.Remove(audio)
		info, err := os.Stat(destination)
		if err != nil {
			return "", 0, err
		}
		return destination, info.Size(), nil
	default:
		result, err := downloader.New(operation.transport).Download(ctx, downloader.Job{
			URL: selected.URL, Headers: selected.Headers, OutputRoot: outputRoot, Destination: destination,
			Overwrite: operation.request.Overwrite, Attempts: options.Attempts,
			RetryBaseDelay: options.RetryBaseDelay, RetryMaxDelay: options.RetryMaxDelay,
			RateLimit: options.RateLimit, MaxBytes: options.MaxBytes,
			ThrottleRate: options.ThrottleRate, ThrottleWindow: options.ThrottleWindow,
			ThrottleRestarts: options.ThrottleRestarts, FileAttempts: options.FileAttempts,
		}, sink)
		if err != nil {
			return "", 0, err
		}
		return result.Path, result.Bytes, nil
	}
}

func mergeableSelections(selections []mediaformat.Selection) bool {
	video, audio := 0, 0
	for _, selection := range selections {
		if selection.VCodec != "" && selection.VCodec != "none" {
			video++
		}
		if selection.ACodec != "" && selection.ACodec != "none" {
			audio++
		}
	}
	return video == 1 && audio == 1
}

func mergedOutputExtension(selections []mediaformat.Selection) string {
	if len(selections) == 1 {
		return safeExtension(selections[0].Ext)
	}
	if len(selections) != 2 || !mergeableSelections(selections) {
		return "mkv"
	}
	video, audio := selections[0], selections[1]
	if audio.VCodec != "" && audio.VCodec != "none" {
		video, audio = audio, video
	}
	switch {
	case video.Ext == "webm" && audio.Ext == "webm":
		return "webm"
	case video.Ext == "mp4" && (audio.Ext == "m4a" || audio.Ext == "mp4"):
		return "mp4"
	default:
		return "mkv"
	}
}

var extensionPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,16}$`)

func safeExtension(extension string) string {
	if !extensionPattern.MatchString(extension) {
		return "bin"
	}
	return extension
}
