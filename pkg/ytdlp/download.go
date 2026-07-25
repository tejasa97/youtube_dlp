package ytdlp

import (
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	"github.com/ytdlp-go/ytdlp/internal/protocol/youtubeump"
	"github.com/ytdlp-go/ytdlp/internal/youtubepot"
)

func outputPlanDestination(base string, plan mediaformat.OutputPlan, multi bool) string {
	if !multi {
		return base
	}
	extension := filepath.Ext(base)
	stem := strings.TrimSuffix(base, extension)
	return stem + ".f" + plan.PlanID() + extension
}

func removePublishedPaths(paths []string) {
	for _, path := range paths {
		_ = os.Remove(path)
	}
}

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
	if selections[0].YouTubeLiveFromStart && selections[1].YouTubeLiveFromStart {
		return operation.downloadYouTubeLivePair(ctx, selections, outputRoot, destination, temporaryRoot, sink)
	}
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
	return operation.downloadSelectionWithLiveRefresh(ctx, selected, outputRoot, destination, sink, nil)
}

func (operation *operation) downloadSelectionWithLiveRefresh(ctx context.Context, selected mediaformat.Selection, outputRoot, destination string, sink events.Sink, liveRefresh youtubelive.LiveRefreshFunc) (string, int64, error) {
	options := operation.request.Downloader
	if selected.YouTubeLiveFromStart {
		if options.External != nil {
			return "", 0, fmt.Errorf("%w: external downloaders cannot consume generated YouTube live fragments", extractor.ErrUnsupported)
		}
		targetDuration, err := youtubeTargetDuration(selected.TargetDuration)
		if err != nil {
			return "", 0, err
		}
		if liveRefresh == nil {
			if operation.youtubeLiveRefresh != nil {
				liveRefresh = operation.youtubeLiveRefresh(selected)
			} else {
				liveRefresh = newYouTubeLiveRefreshCoordinator(operation).callback(selected)
			}
		}
		result, err := youtubelive.NewLiveDownloader(operation.transport, youtubelive.LiveConfig{
			Headers: selected.Headers, TargetDuration: targetDuration,
			LiveStartTimestamp: selected.LiveStartTimestamp, Refresh: liveRefresh,
			PollInterval: options.LivePollInterval, RefreshInterval: options.LiveRefreshInterval,
			MaxPolls: options.LiveMaxPolls, MaxNoProgressPolls: options.LiveMaxNoProgressPolls,
			MaxSegments: options.MaxSegments, MaxSegmentSize: options.MaxSegmentBytes, Attempts: options.Attempts,
			RetryBaseDelay: options.RetryBaseDelay, RetryMaxDelay: options.RetryMaxDelay,
		}).Download(ctx, selected.URL, outputRoot, destination, operation.request.Overwrite, sink)
		if err != nil {
			return "", 0, err
		}
		return result.Path, result.Bytes, nil
	}
	if selected.YouTubePostLive {
		if options.External != nil {
			return "", 0, fmt.Errorf("%w: external downloaders cannot consume generated YouTube post-live fragments", extractor.ErrUnsupported)
		}
		targetDuration, err := youtubeTargetDuration(selected.TargetDuration)
		if err != nil {
			return "", 0, err
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
	if selected.YouTubeSABR || selected.Protocol == "youtube_sabr_ump" {
		if options.External != nil {
			return "", 0, fmt.Errorf("%w: external downloaders cannot consume generated YouTube SABR streams", extractor.ErrUnsupported)
		}
		result, err := downloadYouTubeSABRSelection(ctx, operation, selected, outputRoot, destination, sink)
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
			DynamicPolls:        options.LiveMaxPolls,
			PollInterval:        options.LivePollInterval,
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

func youtubeTargetDuration(seconds float64) (time.Duration, error) {
	if seconds <= 0 || seconds > 3600 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0, fmt.Errorf("%w: invalid YouTube live target duration", extractor.ErrInvalidMetadata)
	}
	duration := time.Duration(seconds * float64(time.Second))
	if duration <= 0 {
		return 0, fmt.Errorf("%w: invalid YouTube live target duration", extractor.ErrInvalidMetadata)
	}
	return duration, nil
}

func (operation *operation) downloadYouTubeLivePair(ctx context.Context, selections []mediaformat.Selection, outputRoot, destination, temporaryRoot string, sink events.Sink) (string, int64, error) {
	if sink == nil {
		sink = events.Nop()
	}
	serializedSink := &lockedEventSink{sink: sink}
	coordinator := newYouTubeLiveRefreshCoordinator(operation)
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		index int
		path  string
		bytes int64
		err   error
	}
	outcomes := make(chan outcome, len(selections))
	for index, selection := range selections {
		go func(index int, selection mediaformat.Selection) {
			track := filepath.Join(temporaryRoot, fmt.Sprintf("track-%d.%s", index, safeExtension(selection.Ext)))
			refresh := coordinator.callback(selection)
			if operation.youtubeLiveRefresh != nil {
				refresh = operation.youtubeLiveRefresh(selection)
			}
			path, count, err := operation.downloadSelectionWithLiveRefresh(
				childCtx, selection, temporaryRoot, track, serializedSink, refresh)
			outcomes <- outcome{index: index, path: path, bytes: count, err: err}
		}(index, selection)
	}
	paths := make([]string, len(selections))
	var bytes int64
	var firstErr error
	for range selections {
		result := <-outcomes
		if result.err != nil && firstErr == nil {
			firstErr = result.err
			cancel()
		}
		paths[result.index] = result.path
		bytes += result.bytes
	}
	if firstErr != nil {
		return "", 0, firstErr
	}
	video, audio := paths[0], paths[1]
	if selections[1].VCodec != "" && selections[1].VCodec != "none" {
		video, audio = paths[1], paths[0]
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		return "", 0, err
	}
	if err := tools.Merge(ctx, video, audio, destination, operation.request.Overwrite, serializedSink); err != nil {
		return "", 0, err
	}
	if info, statErr := os.Stat(destination); statErr == nil {
		bytes = info.Size()
	}
	return destination, bytes, nil
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

func downloadYouTubeSABRSelection(ctx context.Context, operation *operation, selected mediaformat.Selection, outputRoot, destination string, sink events.Sink) (youtubeump.Result, error) {
	ustreamer, err := base64.StdEncoding.DecodeString(selected.YouTubeSABRUstreamerConfig)
	if err != nil {
		return youtubeump.Result{}, fmt.Errorf("%w: invalid SABR ustreamer config", extractor.ErrInvalidMetadata)
	}
	trackKind := youtubeump.TrackKind(selected.YouTubeSABRTrack)
	if trackKind != youtubeump.TrackAudio && trackKind != youtubeump.TrackVideo {
		return youtubeump.Result{}, fmt.Errorf("%w: unknown SABR track", extractor.ErrInvalidMetadata)
	}
	poToken, err := resolveYouTubeSABRPOToken(ctx, operation, selected)
	if err != nil {
		return youtubeump.Result{}, err
	}
	format, err := youtubeump.FormatIDFromItag(selected.YouTubeSABRItag, selected.YouTubeSABRLastModified, selected.YouTubeSABRXTags)
	if err != nil {
		return youtubeump.Result{}, err
	}
	clientInfo, err := youtubeump.ClientInfoFromID(selected.YouTubeSABRClientID, selected.YouTubeSABRClientVersion)
	if err != nil {
		return youtubeump.Result{}, err
	}
	options := operation.request.Downloader
	config := youtubeump.Config{
		Headers:         selected.Headers,
		UserAgent:       selected.YouTubeSABRUserAgent,
		ServerURL:       selected.YouTubeSABRServerURL,
		UstreamerConfig: ustreamer,
		Format:          format,
		TrackKind:       trackKind,
		DrcEnabled:      selected.YouTubeSABRDrc,
		AudioTrackID:    selected.YouTubeSABRAudioTrackID,
		VisitorData:     selected.YouTubeSABRVisitorData,
		POToken:         poToken,
		DurationSec:     selected.YouTubeSABRDurationSec,
		MaxBytes:        options.MaxBytes,
		MaxRounds:       youtubeump.MaxRounds,
		Attempts:        options.Attempts,
		RetryBaseDelay:  options.RetryBaseDelay,
		RetryMaxDelay:   options.RetryMaxDelay,
		ClientInfo:      clientInfo,
	}
	return youtubeump.NewDownloader(operation.transport, config).Download(ctx, outputRoot, destination, operation.request.Overwrite, sink)
}

func resolveYouTubeSABRPOToken(ctx context.Context, operation *operation, selected mediaformat.Selection) ([]byte, error) {
	if operation == nil || operation.client == nil || operation.client.youtubePOT == nil {
		return nil, nil
	}
	token, ok, err := operation.client.youtubePOT.ResolvePolicy(ctx, youtubepot.Request{
		Context:     youtubepot.ContextGVS,
		Client:      selected.YouTubeSABRClientName,
		VisitorData: selected.YouTubeSABRVisitorData,
		VideoID:     selected.YouTubeSABRVideoID,
	}, false, true)
	if err != nil {
		return nil, err
	}
	if !ok || token == "" {
		return nil, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(token); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(token); err == nil {
		return decoded, nil
	}
	return nil, fmt.Errorf("%w: invalid SABR PO token encoding", errInvalidRequestOptions)
}
