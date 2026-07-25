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

	"github.com/ytdlp-go/ytdlp/internal/events"
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
}

// Result is a completed finite-VOD SABR artifact.
type Result struct {
	Path  string
	Bytes int64
}

type Downloader struct {
	transport IsolatedTransport
	config    Config
}

func NewDownloader(transport IsolatedTransport, config Config) *Downloader {
	config.Headers = config.Headers.Clone()
	return &Downloader{transport: transport, config: config}
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

	output, err := openOutputTemp(destination)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrDownloadFailed, err)
	}
	defer output.closeAndRemove()

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
	if err := sink.Emit(ctx, events.Event{Kind: events.KindStarting, URL: eventURL, Path: destination}); err != nil {
		return Result{}, errors.Join(ErrEventSink, err)
	}

	var (
		requestNumber  int
		playerTimeMs   int64
		selectedFormat bool
		bufferedRanges []BufferedRange
		playbackCookie []byte
	)
	assembler := newTrackAssembler(config.Format, expectedDurationMs, output.file, maxBytes)

	for round := 0; round < maxRounds; round++ {
		if assembler.trackComplete() {
			break
		}
		if err := ctx.Err(); err != nil {
			_ = sink.Emit(context.Background(), events.Event{Kind: events.KindCancelled, URL: eventURL, Path: destination, Message: err.Error()})
			return Result{}, err
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
		}.marshal()
		if err != nil {
			return Result{}, err
		}
		roundCtrl, err := downloader.postRound(ctx, config, requestNumber, body, assembler, sink, destination, eventURL)
		if err != nil {
			if ctx.Err() != nil {
				return Result{}, ctx.Err()
			}
			return Result{}, redactError(err)
		}
		if roundCtrl.updateCookie {
			playbackCookie = bytes.Clone(roundCtrl.cookie)
		}
		advance, ranges, selected := assembler.playbackState()
		playerTimeMs = advance
		bufferedRanges = ranges
		if selected {
			selectedFormat = true
		}
		if assembler.trackComplete() {
			break
		}
		if roundCtrl.backoff > 0 {
			if err := policyBackoffWait(ctx, roundCtrl.backoff); err != nil {
				_ = sink.Emit(context.Background(), events.Event{Kind: events.KindCancelled, URL: eventURL, Path: destination, Message: err.Error()})
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
	if err := publishOutput(output.path, destination, overwrite); err != nil {
		return Result{}, fmt.Errorf("%w: publish output: %v", ErrDownloadFailed, err)
	}
	output.published = true
	total := assembler.totalWritten
	_ = sink.Emit(ctx, events.Event{Kind: events.KindCompleted, URL: eventURL, Path: destination, Bytes: total})
	return Result{Path: destination, Bytes: total}, nil
}

const maxSABRAttempts = 100

func (downloader *Downloader) postRound(ctx context.Context, config Config, requestNumber int, body []byte, assembler *trackAssembler, sink events.Sink, destination, eventURL string) (roundControl, error) {
	var zero roundControl
	attempts := config.Attempts
	if attempts <= 0 {
		attempts = 3
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		ctrl, err := downloader.postOnce(ctx, config, requestNumber, body, assembler)
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

func (downloader *Downloader) postOnce(ctx context.Context, config Config, requestNumber int, body []byte, assembler *trackAssembler) (roundControl, error) {
	var zero roundControl
	request, err := newSABRRequest(ctx, config.ServerURL, requestNumber, body, config.UserAgent, config.AcceptLanguage)
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
		return zero, retryableError{requestFailure(err, config.ServerURL)}
	}
	defer response.Body.Close()
	if isRedirectResponse(response) {
		return zero, redirectFailure(config.ServerURL)
	}
	if response.StatusCode != http.StatusOK {
		return zero, responseFailure(response.StatusCode, config.ServerURL)
	}
	if err := validateResponseContentType(response.Header.Get("Content-Type")); err != nil {
		return zero, err
	}
	return consumeStream(ctx, response.Body, assembler)
}

func consumeStream(ctx context.Context, body interface {
	Read([]byte) (int, error)
}, assembler *trackAssembler) (roundControl, error) {
	var zero roundControl
	consumer := newStreamConsumer(assembler)
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
