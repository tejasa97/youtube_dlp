package engine

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	providerapi "github.com/tejasa97/youtube_dlp/engine/provider"
	"github.com/tejasa97/youtube_dlp/internal/downloader"
	"github.com/tejasa97/youtube_dlp/internal/events"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/fragment"
	"github.com/tejasa97/youtube_dlp/internal/media/pipeline"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/protocol/dash"
	"github.com/tejasa97/youtube_dlp/internal/protocol/hds"
	"github.com/tejasa97/youtube_dlp/internal/protocol/hls"
	"github.com/tejasa97/youtube_dlp/internal/protocol/ism"
	"github.com/tejasa97/youtube_dlp/internal/protocol/youtubelive"
	"github.com/tejasa97/youtube_dlp/internal/protocol/youtubeump"
)

func planDestinationExtension(plan mediaformat.OutputPlan, overridePreferences []string) (string, error) {
	tracks := plan.Tracks
	if len(tracks) == 0 {
		return "", fmt.Errorf("%w: output plan has no tracks", ErrUnsupported)
	}
	if len(tracks) == 1 {
		return plannedOutputExtension(plan, overridePreferences), nil
	}
	if !mergeableTracks(tracks) {
		return "", fmt.Errorf("%w: output plan with %d tracks is not mergeable", ErrUnsupported, len(tracks))
	}
	return plannedOutputExtension(plan, overridePreferences), nil
}

func plannedOutputExtension(plan mediaformat.OutputPlan, overridePreferences []string) string {
	if len(overridePreferences) > 0 && len(plan.Tracks) > 1 {
		return safeExtension(mediaformat.CompatibleExtensionForSelections(plan.Tracks, overridePreferences))
	}
	if ext, ok := plan.Metadata.Lookup("ext").StringValue(); ok && ext != "" {
		return safeExtension(ext)
	}
	if len(plan.Tracks) == 1 {
		return safeExtension(plan.Tracks[0].Ext)
	}
	return safeExtension(mediaformat.CompatibleExtensionForSelections(plan.Tracks, nil))
}

func (operation *operation) downloadSelections(ctx context.Context, selections []mediaformat.Selection, outputRoot, destination string, sink events.Sink) (string, int64, error) {
	if err := operation.validateCredentialIsolatedDispatch(selections, operation.request.Downloader.External != nil); err != nil {
		return "", 0, err
	}
	if len(selections) == 1 {
		return operation.downloadSelection(ctx, selections[0], outputRoot, destination, sink)
	}
	if !mergeableTracks(selections) {
		return "", 0, fmt.Errorf("%w: selected format set is not mergeable", ErrUnsupported)
	}
	if len(selections) == 2 && sabrPairSelections(selections) {
		return operation.downloadYouTubeSABRPair(ctx, selections, outputRoot, destination, sink)
	}
	if len(selections) == 2 && selections[0].YouTubeLiveFromStart && selections[1].YouTubeLiveFromStart {
		temporaryRoot, err := os.MkdirTemp(outputRoot, ".ytdlp-formats-")
		if err != nil {
			return "", 0, fmt.Errorf("create selected-format workspace: %w", err)
		}
		defer os.RemoveAll(temporaryRoot)
		return operation.downloadYouTubeLivePair(ctx, selections, outputRoot, destination, temporaryRoot, sink)
	}
	return operation.downloadAndMergeTracks(ctx, selections, outputRoot, destination, sink)
}

func sabrPairSelections(selections []mediaformat.Selection) bool {
	if len(selections) != 2 {
		return false
	}
	for _, selection := range selections {
		if !selection.YouTubeSABR && selection.Protocol != "youtube_sabr_ump" {
			return false
		}
	}
	return true
}

func sabrTrackDestination(finalDestination string, selection mediaformat.Selection) string {
	track := selection.YouTubeSABRTrack
	switch track {
	case "audio", "video":
	default:
		track = "track"
	}
	itag := selection.YouTubeSABRItag
	if itag < 0 {
		itag = 0
	}
	return finalDestination + ".sabr." + track + "." + strconv.FormatInt(itag, 10) + "." + safeExtension(selection.Ext)
}

func (operation *operation) downloadYouTubeSABRPair(ctx context.Context, selections []mediaformat.Selection, outputRoot, destination string, sink events.Sink) (string, int64, error) {
	if err := operation.validateCredentialIsolatedDispatch(selections, false); err != nil {
		return "", 0, err
	}
	if sink == nil {
		sink = events.Nop()
	}
	serializedSink := &lockedEventSink{sink: sink}
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	coordinator := newYouTubeSABRRefreshCoordinator(operation)

	type outcome struct {
		index int
		path  string
		bytes int64
		err   error
	}
	outcomes := make(chan outcome, len(selections))
	for index, selection := range selections {
		go func(index int, selection mediaformat.Selection) {
			trackDestination := sabrTrackDestination(destination, selection)
			if err := validateSABRArtifactPath(outputRoot, trackDestination); err != nil {
				outcomes <- outcome{index: index, err: err}
				return
			}
			identity, err := sabrResumeIdentity(selection)
			if err != nil {
				outcomes <- outcome{index: index, err: err}
				return
			}
			if complete, size, err := sabrTrackComplete(trackDestination, identity); err != nil {
				outcomes <- outcome{index: index, err: err}
				return
			} else if complete {
				outcomes <- outcome{index: index, path: trackDestination, bytes: size}
				return
			}
			result, downloadErr := downloadYouTubeSABRSelection(childCtx, operation, selection, outputRoot, trackDestination, serializedSink, true, coordinator)
			outcomes <- outcome{index: index, path: result.Path, bytes: result.Bytes, err: downloadErr}
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
		if result.err == nil {
			paths[result.index] = result.path
			bytes += result.bytes
		}
	}
	if firstErr != nil {
		return "", 0, firstErr
	}
	video, audio := paths[0], paths[1]
	if selections[1].VCodec != "" && selections[1].VCodec != "none" {
		video, audio = paths[1], paths[0]
	}
	merge := operation.sabrMerge
	if merge == nil {
		merge = operation.mergeSABRTracks
	}
	if err := merge(ctx, video, audio, destination, operation.request.Overwrite, serializedSink); err != nil {
		return "", 0, err
	}
	cleanupSABRTrackArtifacts(paths...)
	if info, err := os.Stat(destination); err == nil {
		bytes = info.Size()
	}
	return destination, bytes, nil
}

func (operation *operation) mergeSABRTracks(ctx context.Context, video, audio, destination string, overwrite bool, sink events.Sink) error {
	tools, err := operation.discoverFFmpeg()
	if err != nil {
		return err
	}
	return tools.Merge(ctx, video, audio, destination, overwrite, sink)
}

func sabrResumeIdentity(selection mediaformat.Selection) (youtubeump.ResumeIdentity, error) {
	if err := youtubeump.ValidateResumeVideoID(selection.YouTubeSABRVideoID); err != nil {
		return youtubeump.ResumeIdentity{}, err
	}
	ustreamer, err := base64.StdEncoding.DecodeString(selection.YouTubeSABRUstreamerConfig)
	if err != nil {
		return youtubeump.ResumeIdentity{}, fmt.Errorf("%w: invalid SABR ustreamer config", ErrInvalidMetadata)
	}
	trackKind := youtubeump.TrackKind(selection.YouTubeSABRTrack)
	if trackKind != youtubeump.TrackAudio && trackKind != youtubeump.TrackVideo {
		return youtubeump.ResumeIdentity{}, fmt.Errorf("%w: unknown SABR track", ErrInvalidMetadata)
	}
	format, err := youtubeump.FormatIDFromItag(selection.YouTubeSABRItag, selection.YouTubeSABRLastModified, selection.YouTubeSABRXTags)
	if err != nil {
		return youtubeump.ResumeIdentity{}, err
	}
	clientInfo, err := youtubeump.ClientInfoFromID(selection.YouTubeSABRClientID, selection.YouTubeSABRClientVersion)
	if err != nil {
		return youtubeump.ResumeIdentity{}, err
	}
	return youtubeump.IdentityFromConfig(youtubeump.Config{
		UstreamerConfig: ustreamer,
		Format:          format,
		TrackKind:       trackKind,
		ClientInfo:      clientInfo,
		VideoID:         selection.YouTubeSABRVideoID,
		DrcEnabled:      selection.YouTubeSABRDrc,
		AudioTrackID:    selection.YouTubeSABRAudioTrackID,
		DurationSec:     selection.YouTubeSABRDurationSec,
	}), nil
}

func validateSABRArtifactPath(outputRoot, destination string) error {
	partPath, statePath := youtubeump.CheckpointPaths(destination)
	markerPath := youtubeump.CompletionMarkerPath(destination)
	for _, path := range []string{destination, partPath, statePath, markerPath} {
		if err := youtubeump.ValidateOutputPath(outputRoot, path); err != nil {
			return err
		}
	}
	return nil
}

func sabrTrackComplete(destination string, identity youtubeump.ResumeIdentity) (bool, int64, error) {
	size, ok, err := youtubeump.ValidateCompletedTrack(destination, identity)
	return ok, size, err
}

func cleanupSABRTrackArtifacts(paths ...string) {
	for _, path := range paths {
		_ = os.Remove(path)
		_ = os.Remove(path + ".part")
		_ = os.Remove(path + ".part.json")
		_ = os.Remove(youtubeump.CompletionMarkerPath(path))
	}
}

func (operation *operation) downloadSelection(ctx context.Context, selected mediaformat.Selection, outputRoot, destination string, sink events.Sink) (string, int64, error) {
	return operation.downloadSelectionWithLiveRefresh(ctx, selected, outputRoot, destination, sink, nil)
}

func (operation *operation) validateCredentialIsolatedDispatch(selections []mediaformat.Selection, external bool) error {
	if err := validateHostPolicyDispatch(operation, selections); err != nil {
		return err
	}
	for _, selected := range selections {
		if selected.CredentialIsolatedReferer != "" && !selected.CredentialIsolated {
			return fmt.Errorf("%w: scoped referer requires credential-isolated media", ErrTransportIsolation)
		}
		if !selected.CredentialIsolated {
			continue
		}
		switch {
		case external:
			return fmt.Errorf("%w: external downloader cannot enforce credential-isolated transport", ErrTransportIsolation)
		case selected.YouTubeLiveFromStart:
			return fmt.Errorf("%w: YouTube live-from-start cannot enforce credential-isolated transport", ErrTransportIsolation)
		case selected.YouTubePostLive:
			return fmt.Errorf("%w: YouTube post-live cannot enforce credential-isolated transport", ErrTransportIsolation)
		case selected.YouTubeSABR || selected.Protocol == "youtube_sabr_ump":
			return fmt.Errorf("%w: YouTube SABR cannot enforce credential-isolated transport", ErrTransportIsolation)
		}
	}
	return nil
}

func (operation *operation) downloadSelectionWithLiveRefresh(ctx context.Context, selected mediaformat.Selection, outputRoot, destination string, sink events.Sink, liveRefresh youtubelive.LiveRefreshFunc) (string, int64, error) {
	options := operation.request.Downloader
	if err := operation.validateCredentialIsolatedDispatch([]mediaformat.Selection{selected}, options.External != nil); err != nil {
		return "", 0, err
	}
	var assetValidator func(string) error
	if selected.AssetPolicy != "" {
		hooks := operation.registry.Hooks()
		if hooks.ValidateAsset == nil {
			return "", 0, ErrUnavailable
		}
		assetValidator = func(rawURL string) error {
			return hooks.ValidateAsset(providerapi.URLPolicyRequest{Policy: selected.AssetPolicy, Role: "asset", URL: rawURL})
		}
		if err := assetValidator(selected.URL); err != nil {
			return "", 0, err
		}
	}
	var hlsGroup *hls.DiscontinuityGroupID
	if selected.Protocol == "m3u8_native" {
		group, explicit, groupErr := hlsDiscontinuityGroupFromSelection(selected)
		if groupErr != nil {
			return "", 0, groupErr
		}
		if explicit {
			hlsGroup = &group
			sink = hlsDiscontinuityProgressSink{sink: sink, sequence: group.DiscontinuitySequence}
		}
	}
	if selected.YouTubeLiveFromStart {
		if options.External != nil {
			return "", 0, fmt.Errorf("%w: external downloaders cannot consume generated YouTube live fragments", ErrUnsupported)
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
			return "", 0, fmt.Errorf("%w: external downloaders cannot consume generated YouTube post-live fragments", ErrUnsupported)
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
			return "", 0, fmt.Errorf("%w: external downloaders cannot consume generated YouTube SABR streams", ErrUnsupported)
		}
		result, err := downloadYouTubeSABRSelection(ctx, operation, selected, outputRoot, destination, sink, false, nil)
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

	mediaTransport, err := operation.mediaTransport(
		selected.CredentialIsolated,
		selected.CredentialIsolatedReferer,
		selected.HostPolicy,
		selected.Protocol,
	)
	if err != nil {
		return "", 0, err
	}
	if selected.MediaPolicy != "" {
		mediaTransport = newProviderPolicyTransport(operation, selected.MediaPolicy, "media")
	}

	switch selected.Protocol {
	case "m3u8_native":
		var initialPlaylist *hls.InitialPlaylist
		if cached, ok := hlsInitialPlaylistsFromContext(ctx)[selected.URL]; ok {
			initial := hls.InitialPlaylist{URL: cached.URL, Body: append([]byte(nil), cached.Body...)}
			initialPlaylist = &initial
		}
		result, err := hls.NewDownloader(mediaTransport.(hls.Transport), hls.Config{
			Headers:             selected.Headers,
			InitialPlaylist:     initialPlaylist,
			AllowedHosts:        append([]string(nil), selected.AllowedHosts...),
			FragmentConcurrency: options.FragmentConcurrency, PerHostConcurrency: options.PerHostFragmentConcurrency,
			MaxSegments: options.MaxSegments, MaxSegmentSize: options.MaxSegmentBytes, Attempts: options.Attempts,
			RetryBaseDelay: options.RetryBaseDelay, RetryMaxDelay: options.RetryMaxDelay,
			URLValidator: assetValidator, SelectedDiscontinuityGroup: hlsGroup,
		}).Download(ctx, selected.URL, outputRoot, destination, operation.request.Overwrite, sink)
		if err != nil {
			if preservePartialDownload(err, operation.request.Filesystem.PreservePartialOnCancel) {
				return "", 0, err
			}
			if cleanupErr := cleanupHLSFragments(destination); cleanupErr != nil {
				return "", 0, errors.Join(err, cleanupErr)
			}
			var encryption *hls.EncryptionError
			if !errors.As(err, &encryption) || !encryption.FFmpegEligible {
				return "", 0, err
			}
			if selected.CredentialIsolated {
				return "", 0, fmt.Errorf("%w: HLS ffmpeg fallback cannot enforce credential-isolated transport", ErrTransportIsolation)
			}
			fallbackURL := encryption.MediaURL
			if fallbackURL == "" {
				fallbackURL = selected.URL
			}
			fallback := operation.hlsFallback
			if fallback == nil {
				tools, discoverErr := operation.discoverFFmpegOnly()
				if discoverErr != nil {
					return "", 0, errors.Join(err, discoverErr)
				}
				fallback = func(
					ctx context.Context,
					manifestURL, _, destination string,
					headers http.Header,
					overwrite bool,
					sink events.Sink,
				) (fragment.Result, error) {
					if fallbackErr := tools.DownloadHLS(ctx, manifestURL, destination, headers, overwrite, sink); fallbackErr != nil {
						return fragment.Result{}, fallbackErr
					}
					info, statErr := os.Stat(destination)
					if statErr != nil {
						return fragment.Result{}, statErr
					}
					return fragment.Result{Path: destination, Bytes: info.Size()}, nil
				}
			}
			result, err = fallback(
				ctx, fallbackURL, outputRoot, destination, selected.Headers,
				operation.request.Overwrite, sink,
			)
			if err != nil {
				return "", 0, err
			}
		}
		return result.Path, result.Bytes, nil
	case "http_dash_segments":
		dynamicPolicy := dash.DynamicMPDPolicyDefaultAllow
		if operation.request.DenyDynamicMPD {
			dynamicPolicy = dash.DynamicMPDPolicyDeny
		}
		result, err := dash.NewDownloader(mediaTransport.(dash.Transport), dash.Config{
			Headers:             selected.Headers,
			DynamicMPDPolicy:    dynamicPolicy,
			DynamicPolls:        options.LiveMaxPolls,
			PollInterval:        options.LivePollInterval,
			FragmentConcurrency: options.FragmentConcurrency, PerHostConcurrency: options.PerHostFragmentConcurrency,
			MaxSegments: options.MaxSegments, MaxSegmentSize: options.MaxSegmentBytes, Attempts: options.Attempts,
			RetryBaseDelay: options.RetryBaseDelay, RetryMaxDelay: options.RetryMaxDelay,
			URLValidator: assetValidator,
		}).Download(ctx, selected.URL, outputRoot, destination, operation.request.Overwrite, sink)
		if err != nil {
			return "", 0, err
		}
		if result.MergeRequired || result.MultiPeriod {
			tools, discoverErr := operation.discoverFFmpeg()
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
	case "f4m", "f4m_native":
		// Map selected.TBR (float64) onto the bounded int64 the HDS layer
		// expects. NaN and non-positive values fall through to "highest
		// available" via zero; out-of-int64-range bitrates are clamped to
		// MaxInt64 so the protocol can never panic on numeric conversion.
		requestedBitrateBound := boundedFloatToInt64(selected.TBR)
		// The HDS layer applies its own MaxFragmentSize and MaxOutputBytes
		// caps; we forward options.MaxSegmentBytes as the fragment ceiling
		// (already bounded by validateOptions) and derive MaxOutputBytes
		// from options.MaxBytes so that product-level size caps reach the
		// protocol. When MaxBytes is unset we leave the protocol default.
		maxOutput := options.MaxBytes
		if maxOutput <= 0 {
			maxOutput = 8 << 30 // 8 GiB intentional output cap.
		}
		result, err := hds.NewDownloader(mediaTransport.(hds.Transport), hds.Config{
			Headers:          selected.Headers,
			Attempts:         options.Attempts,
			RetryBaseDelay:   options.RetryBaseDelay,
			RetryMaxDelay:    options.RetryMaxDelay,
			MaxFragmentSize:  options.MaxSegmentBytes,
			MaxOutputBytes:   maxOutput,
			RequestedBitrate: requestedBitrateBound,
		})
		if err != nil {
			return "", 0, err
		}
		out, err := result.Download(ctx, selected.URL, outputRoot, destination, operation.request.Overwrite, sink)
		if err != nil {
			return "", 0, err
		}
		return out.Path, out.Bytes, nil
	case "ism", "ismc", "mss":
		result, err := ism.NewDownloader(mediaTransport.(ism.Transport), ism.Config{
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
		tools, discoverErr := operation.discoverFFmpeg()
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
		job := operation.directDownloadJob(selected.URL, selected.Headers, outputRoot, destination)
		job.HTTPChunkSize = selected.HTTPChunkSize
		job.ExpectedBytes = selected.Filesize
		job.ResumeIdentity = nTrackResumeIdentity(selected)
		result, err := downloader.New(mediaTransport.(network.Doer)).Download(ctx, job, sink)
		if err != nil {
			if selected.CredentialIsolated && !preservePartialDownload(err, operation.request.Filesystem.PreservePartialOnCancel) {
				if cleanupErr := cleanupCredentialIsolatedDownload(destination); cleanupErr != nil {
					return "", 0, errors.Join(err, cleanupErr)
				}
			}
			return "", 0, err
		}
		return result.Path, result.Bytes, nil
	}
}

func preservePartialDownload(err error, enabled bool) bool {
	return enabled && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

func cleanupHLSFragments(destination string) error {
	if err := os.RemoveAll(destination + ".fragments"); err != nil {
		return fmt.Errorf("remove HLS fragments: %w", err)
	}
	if err := os.Remove(destination + ".part"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove HLS partial output: %w", err)
	}
	if err := os.Remove(destination + ".part.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove credential-isolated HLS partial state: %w", err)
	}
	return nil
}

func cleanupCredentialIsolatedDownload(destination string) error {
	for _, path := range []string{destination + ".part", destination + ".part.json"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove credential-isolated partial output: %w", err)
		}
	}
	return nil
}

func youtubeTargetDuration(seconds float64) (time.Duration, error) {
	if seconds <= 0 || seconds > 3600 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return 0, fmt.Errorf("%w: invalid YouTube live target duration", ErrInvalidMetadata)
	}
	duration := time.Duration(seconds * float64(time.Second))
	if duration <= 0 {
		return 0, fmt.Errorf("%w: invalid YouTube live target duration", ErrInvalidMetadata)
	}
	return duration, nil
}

func (operation *operation) downloadYouTubeLivePair(ctx context.Context, selections []mediaformat.Selection, outputRoot, destination, temporaryRoot string, sink events.Sink) (string, int64, error) {
	if err := operation.validateCredentialIsolatedDispatch(selections, false); err != nil {
		return "", 0, err
	}
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
	tools, err := operation.discoverFFmpeg()
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
	if len(selections) == 2 && mergeableSelections(selections) {
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
	if mergeableTracks(selections) {
		return safeExtension(mediaformat.CompatibleExtensionForSelections(selections, nil))
	}
	return "mkv"
}

var extensionPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,16}$`)

func safeExtension(extension string) string {
	if !extensionPattern.MatchString(extension) {
		return "bin"
	}
	return extension
}

func downloadYouTubeSABRSelection(ctx context.Context, operation *operation, selected mediaformat.Selection, outputRoot, destination string, sink events.Sink, retainCompletionMarker bool, coordinator *youtubeSABRRefreshCoordinator) (youtubeump.Result, error) {
	if err := operation.validateCredentialIsolatedDispatch([]mediaformat.Selection{selected}, false); err != nil {
		return youtubeump.Result{}, err
	}
	ustreamer, err := base64.StdEncoding.DecodeString(selected.YouTubeSABRUstreamerConfig)
	if err != nil {
		return youtubeump.Result{}, fmt.Errorf("%w: invalid SABR ustreamer config", ErrInvalidMetadata)
	}
	trackKind := youtubeump.TrackKind(selected.YouTubeSABRTrack)
	if trackKind != youtubeump.TrackAudio && trackKind != youtubeump.TrackVideo {
		return youtubeump.Result{}, fmt.Errorf("%w: unknown SABR track", ErrInvalidMetadata)
	}
	if coordinator == nil {
		coordinator = newYouTubeSABRRefreshCoordinator(operation)
	}
	poToken, err := resolveYouTubeSABRPOTokenEpisode(ctx, operation, selected, coordinator.episode)
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
		Headers:                selected.Headers,
		UserAgent:              selected.YouTubeSABRUserAgent,
		ServerURL:              selected.YouTubeSABRServerURL,
		UstreamerConfig:        ustreamer,
		Format:                 format,
		TrackKind:              trackKind,
		DrcEnabled:             selected.YouTubeSABRDrc,
		AudioTrackID:           selected.YouTubeSABRAudioTrackID,
		VideoID:                selected.YouTubeSABRVideoID,
		VisitorData:            selected.YouTubeSABRVisitorData,
		POToken:                poToken,
		DurationSec:            selected.YouTubeSABRDurationSec,
		MaxBytes:               options.MaxBytes,
		MaxRounds:              youtubeump.MaxRounds,
		Attempts:               options.Attempts,
		RetryBaseDelay:         options.RetryBaseDelay,
		RetryMaxDelay:          options.RetryMaxDelay,
		ClientInfo:             clientInfo,
		RetainCompletionMarker: retainCompletionMarker,
		POTokenSource:          coordinator.poTokenSource(selected),
	}
	// Refresh/Reload require an extract-capable operation. When unavailable,
	// leave callbacks nil so resume may continue with caller-supplied material
	// (documented safe case). When wired, failures are fail-closed.
	if operation != nil && operation.client != nil && operation.transport != nil {
		config.Reload = coordinator.reloadFunc(selected)
		config.Refresh = coordinator.refreshFunc(selected)
	}
	return youtubeump.NewDownloader(operation.transport, config).Download(ctx, outputRoot, destination, operation.request.Overwrite, sink)
}

func resolveYouTubeSABRPOToken(ctx context.Context, operation *operation, selected mediaformat.Selection) ([]byte, error) {
	if operation == nil || operation.client == nil || operation.client.potResolver == nil {
		return nil, nil
	}
	token, ok, err := operation.client.potResolver.ResolvePolicy(ctx, providerapi.POTRequest{
		Context:     providerapi.POTContextGVS,
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

// boundedFloatToInt64 converts a non-negative finite float64 to int64,
// clamping out-of-range values and treating NaN or negative inputs as "highest
// available" by returning zero. This matches yt-dlp's f4m.py behavior where
// the absence of an exact requested bitrate falls through to the
// deterministic highest-bitrate selection. The helper cannot fail.
func boundedFloatToInt64(value float64) int64 {
	switch {
	case value != value || value <= 0: // NaN or non-positive
		return 0
	case value > float64(math.MaxInt64):
		return math.MaxInt64
	}
	return int64(value)
}
