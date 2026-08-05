package youtubeump

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/events"
)

// TrackKind selects audio-only or video-only SABR reconstruction.
type TrackKind string

const (
	TrackAudio TrackKind = "audio"
	TrackVideo TrackKind = "video"
)

// Config bounds finite-VOD SABR download work.
type Config struct {
	Headers         http.Header
	UserAgent       string
	AcceptLanguage  string
	ServerURL       string
	UstreamerConfig []byte
	Format          FormatID
	TrackKind       TrackKind
	ClientInfo      ClientInfo
	VideoID         string
	VisitorData     string
	POToken         []byte
	DrcEnabled      bool
	AudioTrackID    string
	DurationSec     int64
	MaxBytes        int64
	MaxRounds       int
	Attempts        int
	RetryBaseDelay  time.Duration
	RetryMaxDelay   time.Duration
	// RetainCompletionMarker keeps destination.sabr.json after a successful
	// publish. Pair A/V sidecars set this so interrupted merge can resume;
	// standalone final downloads leave only the published media.
	RetainCompletionMarker bool
	// Reload recovers RELOAD_PLAYER_RESPONSE via bounded extraction refresh.
	Reload ReloadFunc
	// Refresh re-acquires signed SABR material for the same identity.
	Refresh RefreshFunc
	// POTokenSource optionally refreshes PO tokens mid-session (expiry skew).
	POTokenSource POTokenSource
	// MaxSabrErrorRecoveries overrides MaxSabrErrorRecoveries when > 0.
	MaxSabrErrorRecoveries int
	// MaxReloadAttempts overrides MaxReloadAttempts when > 0.
	MaxReloadAttempts int
	// MaxRefreshAttempts overrides MaxSabrRefreshAttempts when > 0.
	MaxRefreshAttempts int
}

// Result is a completed finite-VOD SABR artifact.
type Result struct {
	Path    string
	Bytes   int64
	Resumed bool
}

type Downloader struct {
	transport         IsolatedTransport
	config            Config
	policyBackoffWait func(context.Context, time.Duration) error
}

func NewDownloader(transport IsolatedTransport, config Config) *Downloader {
	config.Headers = config.Headers.Clone()
	return &Downloader{
		transport:         transport,
		config:            config,
		policyBackoffWait: sleep,
	}
}

func (downloader *Downloader) Download(ctx context.Context, outputRoot, destination string, overwrite bool, sink events.Sink) (Result, error) {
	config := downloader.config
	if downloader.transport == nil {
		return Result{}, fmt.Errorf("%w: missing SABR transport", ErrMissingConfig)
	}
	if config.ServerURL == "" || len(config.UstreamerConfig) == 0 || config.Format.Itag == 0 {
		return Result{}, fmt.Errorf("%w: incomplete SABR configuration", ErrMissingConfig)
	}
	if config.TrackKind != TrackAudio && config.TrackKind != TrackVideo {
		return Result{}, fmt.Errorf("%w: unknown SABR track kind", ErrInvalidMediaState)
	}
	if config.DurationSec <= 0 {
		return Result{}, fmt.Errorf("%w: finite VOD duration is required", ErrMissingConfig)
	}
	if err := validateResumeVideoID(config.VideoID); err != nil {
		return Result{}, err
	}
	if _, err := ValidateSABRURL(config.ServerURL); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(outputRoot, 0o755); err != nil {
		return Result{}, fmt.Errorf("create output root: %w", err)
	}
	if err := validateDestination(outputRoot, destination); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return Result{}, fmt.Errorf("create output directory: %w", err)
	}
	if err := validateDestination(outputRoot, destination); err != nil {
		return Result{}, err
	}
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return Result{}, ErrUnsafeDestination
		}
		if !overwrite {
			return Result{}, ErrDestinationExists
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("inspect destination: %w", err)
	}
	if sink == nil {
		sink = events.Nop()
	}

	maxBytes := config.MaxBytes
	if maxBytes <= 0 || maxBytes > MaxMediaBytes {
		maxBytes = MaxMediaBytes
	}
	maxRounds := config.MaxRounds
	if maxRounds <= 0 || maxRounds > MaxRounds {
		maxRounds = MaxRounds
	}
	attempts := config.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	if attempts > maxSABRAttempts {
		return Result{}, ErrTooManyAttempts
	}

	expectedDurationMs, err := expectedDurationMs(config.DurationSec)
	if err != nil {
		return Result{}, err
	}
	eventURL := redactURL(config.ServerURL)
	identity := identityFromConfig(config)
	partPath, statePath := checkpointPaths(destination)
	if err := validateDestination(outputRoot, partPath); err != nil {
		return Result{}, err
	}
	if err := validateDestination(outputRoot, statePath); err != nil {
		return Result{}, err
	}
	if err := regularOrAbsent(partPath); err != nil {
		return Result{}, err
	}
	if err := regularOrAbsent(statePath); err != nil {
		return Result{}, err
	}

	checkpoint, resumed, err := loadCheckpoint(statePath, identity)
	if err != nil {
		return Result{}, err
	}
	resumeOffset := int64(0)
	if resumed {
		if verifyErr := verifyCheckpointPartBytes(partPath, checkpoint); verifyErr != nil {
			clearResumeArtifacts(partPath, statePath)
			resumed = false
			checkpoint = sabrCheckpoint{}
		} else {
			resumeOffset = checkpoint.TotalWritten
		}
	}
	if !resumed {
		clearResumeArtifacts(partPath, statePath)
		checkpoint = sabrCheckpoint{}
	}

	output, err := openResumePart(partPath, resumeOffset)
	if err != nil {
		if resumed && errors.Is(err, ErrCheckpointInvalid) {
			clearResumeArtifacts(partPath, statePath)
			resumed = false
			checkpoint = sabrCheckpoint{}
			output, err = openResumePart(partPath, 0)
		}
		if err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrDownloadFailed, err)
		}
	}
	defer output.closeAndRemove()

	if err := sink.Emit(ctx, events.Event{Kind: events.KindStarting, URL: eventURL, Path: destination, Resuming: resumed}); err != nil {
		return Result{}, errors.Join(ErrEventSink, err)
	}

	assembler := newTrackAssembler(config.Format, expectedDurationMs, output.file, maxBytes)
	if resumed {
		if restoreErr := assembler.restoreCheckpoint(checkpoint, maxBytes); restoreErr != nil {
			clearResumeArtifacts(partPath, statePath)
			if reopenErr := reopenPartFresh(output, partPath); reopenErr != nil {
				return Result{}, fmt.Errorf("%w: %v", ErrDownloadFailed, reopenErr)
			}
			assembler = newTrackAssembler(config.Format, expectedDurationMs, output.file, maxBytes)
			resumed = false
		} else if err := sink.Emit(ctx, events.Event{
			Kind: events.KindProgress, URL: eventURL, Path: destination,
			Bytes: assembler.totalWritten, Resuming: true,
		}); err != nil {
			return Result{}, errors.Join(ErrEventSink, err)
		}
	}
	assembler.onCommit = func() error {
		state, snapErr := assembler.snapshotCheckpoint(identity)
		if snapErr != nil {
			return snapErr
		}
		return saveCheckpoint(statePath, state)
	}

	var (
		requestNumber  int
		playerTimeMs   int64
		selectedFormat bool
		bufferedRanges []BufferedRange
		playbackCookie []byte
		serverURL      = config.ServerURL
		contexts       = newSabrContextState()
		redirects      = newRedirectTracker(config.ServerURL)
		sabrErrors     int
		reloads        int
		refreshes      int
	)
	maxSabrErrors := config.MaxSabrErrorRecoveries
	if maxSabrErrors <= 0 || maxSabrErrors > MaxSabrErrorRecoveries {
		maxSabrErrors = MaxSabrErrorRecoveries
	}
	maxReloads := config.MaxReloadAttempts
	if maxReloads <= 0 || maxReloads > MaxReloadAttempts {
		maxReloads = MaxReloadAttempts
	}
	maxRefreshes := config.MaxRefreshAttempts
	if maxRefreshes <= 0 || maxRefreshes > MaxSabrRefreshAttempts {
		maxRefreshes = MaxSabrRefreshAttempts
	}
	if resumed {
		playerTimeMs, bufferedRanges, selectedFormat = assembler.playbackState()
		// Resume refresh is fail-closed when a Refresh callback is configured:
		// a failed or identity-rejected refresh must not fall back to stale
		// supplied inventory. The only safe continue-with-caller-material cases
		// are (1) Refresh is nil / not wired, or (2) context cancellation/
		// deadline (returned as-is without using stale inventory for success).
		if config.Refresh != nil {
			if refreshes >= maxRefreshes {
				return Result{}, ErrRefreshBudget
			}
			material, refreshErr := config.Refresh(ctx)
			if refreshErr != nil {
				return Result{}, redactError(refreshErr)
			}
			if err := applyRefreshMaterial(&config, material, &redirects); err != nil {
				return Result{}, redactError(err)
			}
			serverURL = config.ServerURL
			refreshes++
			eventURL = redactURL(serverURL)
		}
	}

	for round := 0; round < maxRounds; round++ {
		if assembler.trackComplete() {
			break
		}
		if err := ctx.Err(); err != nil {
			_ = sink.Emit(context.Background(), events.Event{Kind: events.KindCancelled, URL: eventURL, Path: destination, Message: err.Error(), Resuming: resumed})
			return Result{}, err
		}
		if config.POTokenSource != nil {
			token, tokenErr := config.POTokenSource(ctx)
			if tokenErr != nil {
				return Result{}, redactError(tokenErr)
			}
			if token != nil {
				config.POToken = bytes.Clone(token)
			}
		}
		body, err := playbackRequest{
			Format:          config.Format,
			TrackKind:       string(config.TrackKind),
			UstreamerConfig: config.UstreamerConfig,
			ClientInfo:      config.ClientInfo,
			POToken:         config.POToken,
			PlaybackCookie:  bytes.Clone(playbackCookie),
			BufferedRanges:  bufferedRanges,
			RequestNumber:   requestNumber,
			PlayerTimeMs:    playerTimeMs,
			SelectedFormat:  selectedFormat,
			DrcEnabled:      config.DrcEnabled,
			AudioTrackID:    config.AudioTrackID,
			Contexts:        contexts.clone(),
		}.marshal()
		if err != nil {
			return Result{}, err
		}
		roundCtrl, err := downloader.postRound(ctx, config, serverURL, requestNumber, body, assembler, contexts, redirects, sink, destination, eventURL)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			var sabrErr *SabrErrorSignal
			var reloadErr *ReloadPlayerSignal
			switch {
			case errors.As(err, &sabrErr):
				sabrErrors++
				if sabrErrors > maxSabrErrors {
					return Result{}, fmt.Errorf("%w: %w", ErrSabrRecoveryBudget, redactError(err))
				}
				if sleepErr := sleep(ctx, recoveryBackoff(config, sabrErrors)); sleepErr != nil {
					return Result{}, sleepErr
				}
				// Retry same committed state; failed response did not commit.
				round--
				continue
			case errors.As(err, &reloadErr):
				reloads++
				if reloads > maxReloads {
					return Result{}, fmt.Errorf("%w: %w", ErrReloadBudget, redactError(err))
				}
				if config.Reload == nil {
					return Result{}, redactError(err)
				}
				material, reloadCallErr := config.Reload(ctx, ReloadRequest{
					VideoID:     config.VideoID,
					ClientName:  config.ClientInfo.ClientName,
					ClientVer:   config.ClientInfo.ClientVersion,
					VisitorData: config.VisitorData,
					TrackKind:   config.TrackKind,
					Format:      config.Format,
					Token:       reloadErr.ReloadToken(),
				})
				if reloadCallErr != nil {
					return Result{}, redactError(reloadCallErr)
				}
				if applyErr := applyRefreshMaterial(&config, material, &redirects); applyErr != nil {
					return Result{}, fmt.Errorf("%w: %v", ErrReloadRejected, applyErr)
				}
				serverURL = config.ServerURL
				eventURL = redactURL(serverURL)
				playbackCookie = nil
				contexts = newSabrContextState()
				requestNumber = 0
				if sleepErr := sleep(ctx, recoveryBackoff(config, reloads)); sleepErr != nil {
					return Result{}, sleepErr
				}
				round--
				continue
			default:
				return Result{}, redactError(err)
			}
		}
		if roundCtrl.updateCookie {
			playbackCookie = bytes.Clone(roundCtrl.cookie)
		}
		if roundCtrl.contexts != nil {
			contexts = roundCtrl.contexts
		}
		advance, ranges, selected := assembler.playbackState()
		playerTimeMs = advance
		bufferedRanges = ranges
		if selected {
			selectedFormat = true
		}
		if assembler.totalWritten > 0 {
			if emitErr := sink.Emit(ctx, events.Event{
				Kind: events.KindProgress, URL: eventURL, Path: destination,
				Bytes: assembler.totalWritten, Resuming: resumed,
			}); emitErr != nil {
				return Result{}, errors.Join(ErrEventSink, emitErr)
			}
		}
		if assembler.trackComplete() {
			// END_OF_TRACK is authoritative: do not POST again for redirect/backoff.
			break
		}
		if roundCtrl.hasRedirect {
			redirects.record(roundCtrl.redirectURL)
			serverURL = roundCtrl.redirectURL
			eventURL = redactURL(serverURL)
		}
		if roundCtrl.backoff > 0 {
			if err := downloader.policyBackoffWait(ctx, roundCtrl.backoff); err != nil {
				_ = sink.Emit(context.Background(), events.Event{Kind: events.KindCancelled, URL: eventURL, Path: destination, Message: err.Error(), Resuming: resumed})
				return Result{}, err
			}
		}
		requestNumber++
	}
	if !assembler.trackComplete() {
		return Result{}, ErrRoundsExhausted
	}
	if err := output.syncClose(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrDownloadFailed, err)
	}
	total := assembler.totalWritten
	if config.RetainCompletionMarker {
		// Crash-atomic sidecar completion: durable identity-bound marker first,
		// while .part + checkpoint still exist. Never delete recoverable media
		// on marker failure. Only then publish and drop the checkpoint.
		if err := writeCompletionMarker(output.path, destination, IdentityFromConfig(config), total); err != nil {
			return Result{}, err
		}
		if err := publishOutput(output.path, destination, overwrite); err != nil {
			return Result{}, fmt.Errorf("%w: publish output: %v", ErrDownloadFailed, err)
		}
		output.published = true
		removeCheckpoint(statePath)
	} else {
		if err := publishOutput(output.path, destination, overwrite); err != nil {
			return Result{}, fmt.Errorf("%w: publish output: %v", ErrDownloadFailed, err)
		}
		output.published = true
		removeCheckpoint(statePath)
		// Standalone final outputs must not leave internal markers beside media.
		removeCompletionMarker(destination)
	}
	_ = sink.Emit(ctx, events.Event{Kind: events.KindCompleted, URL: eventURL, Path: destination, Bytes: total, Total: total, Resuming: resumed})
	return Result{Path: destination, Bytes: total, Resumed: resumed}, nil
}

const maxSABRAttempts = 100

func (downloader *Downloader) postRound(
	ctx context.Context,
	config Config,
	serverURL string,
	requestNumber int,
	body []byte,
	assembler *trackAssembler,
	contexts *sabrContextState,
	redirects *redirectTracker,
	sink events.Sink,
	destination, eventURL string,
) (roundControl, error) {
	var zero roundControl
	attempts := config.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		ctrl, err := downloader.postOnce(ctx, config, serverURL, requestNumber, body, assembler, contexts, redirects)
		if err == nil {
			return ctrl, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if !isRetryable(err) {
			return zero, err
		}
		if attempt < attempts {
			if emitErr := sink.Emit(ctx, events.Event{
				Kind: events.KindRetry, URL: eventURL, Path: destination,
				Attempt: attempt + 1, Message: redactMessage(err.Error()),
			}); emitErr != nil {
				return zero, errors.Join(ErrEventSink, emitErr)
			}
			if sleepErr := sleep(ctx, retryDelay(config, attempt)); sleepErr != nil {
				return zero, sleepErr
			}
		}
	}
	return zero, lastErr
}

func (downloader *Downloader) postOnce(
	ctx context.Context,
	config Config,
	serverURL string,
	requestNumber int,
	body []byte,
	assembler *trackAssembler,
	contexts *sabrContextState,
	redirects *redirectTracker,
) (roundControl, error) {
	var zero roundControl
	request, err := newSABRRequest(ctx, serverURL, requestNumber, body, config.UserAgent, config.AcceptLanguage)
	if err != nil {
		return zero, err
	}
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		request.Header.Del(key)
	}
	applySABRCallerHeaders(request, config.Headers)
	response, err := downloader.transport.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, retryableError{requestFailure(err, serverURL)}
	}
	defer response.Body.Close()
	if isRedirectResponse(response) {
		return zero, redirectFailure(serverURL)
	}
	if response.StatusCode != http.StatusOK {
		return zero, responseFailure(response.StatusCode, serverURL)
	}
	if err := validateResponseContentType(response.Header.Get("Content-Type")); err != nil {
		return zero, err
	}
	return consumeStreamState(ctx, response.Body, assembler, contexts, redirects)
}

func consumeStream(ctx context.Context, body interface {
	Read([]byte) (int, error)
}, assembler *trackAssembler) (roundControl, error) {
	return consumeStreamState(ctx, body, assembler, newSabrContextState(), newRedirectTracker(""))
}

func consumeStreamState(ctx context.Context, body interface {
	Read([]byte) (int, error)
}, assembler *trackAssembler, contexts *sabrContextState, redirects *redirectTracker) (roundControl, error) {
	var zero roundControl
	consumer := newStreamConsumerState(assembler, contexts, redirects)
	reader := NewReader(body, MaxRoundBytes)
	for {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		part, ok, err := reader.ReadPart()
		if err != nil {
			return zero, err
		}
		if !ok {
			return consumer.finish()
		}
		if err := consumer.consumePart(part); err != nil {
			return zero, err
		}
	}
}
