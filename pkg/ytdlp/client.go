// Package ytdlp provides the supported Go embedding API.
package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/downloader"
	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/media/pipeline"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/dash"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

type ErrorCategory string

const (
	ErrorUnsupported  ErrorCategory = "unsupported"
	ErrorInvalidInput ErrorCategory = "invalid_input"
	ErrorNetwork      ErrorCategory = "network"
	ErrorCancelled    ErrorCategory = "cancelled"
	ErrorInternal     ErrorCategory = "internal"
)

type Error struct {
	Category ErrorCategory
	Op       string
	Err      error
}

func (err *Error) Error() string {
	if err == nil {
		return "<nil>"
	}
	if err.Op == "" {
		return fmt.Sprintf("%s: %v", err.Category, err.Err)
	}
	return fmt.Sprintf("%s: %s: %v", err.Category, err.Op, err.Err)
}

func (err *Error) Unwrap() error { return err.Err }

func IsCategory(err error, category ErrorCategory) bool {
	var target *Error
	return errors.As(err, &target) && target.Category == category
}

type Request struct {
	URL            string
	OutputTemplate string
	OutputDir      string
	Proxy          string
	Timeout        time.Duration
	Overwrite      bool
	SkipDownload   bool
}

type Result struct {
	InfoJSON   json.RawMessage
	Extractor  string
	Downloaded bool
	Filename   string
	Bytes      int64
}

type Event struct {
	Kind      string `json:"kind"`
	Extractor string `json:"extractor,omitempty"`
	URL       string `json:"url,omitempty"`
	Path      string `json:"path,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
	Resuming  bool   `json:"resuming,omitempty"`
	Message   string `json:"message,omitempty"`
	Fragment  int    `json:"fragment,omitempty"`
	Fragments int    `json:"fragments,omitempty"`
}

type EventHandler func(context.Context, Event) error

type Option func(*Client)

func WithEventHandler(handler EventHandler) Option {
	return func(client *Client) { client.handler = handler }
}

// Runner is the cancellable operation contract.
type Runner interface {
	Run(context.Context, Request) (Result, error)
}

// Client is stateless between operations and safe for concurrent use. A
// configured event handler must provide its own synchronization when shared.
type Client struct {
	handler EventHandler
}

func NewClient(options ...Option) *Client {
	client := &Client{}
	for _, option := range options {
		option(client)
	}
	return client
}

func (client *Client) Run(ctx context.Context, request Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	transport, err := network.New(network.Config{Proxy: request.Proxy, Timeout: request.Timeout})
	if err != nil {
		return Result{}, categorized("configure network", err)
	}
	registry := extractor.NewRegistry(extractor.NewFixture(), extractor.NewGeneric())
	selected, err := registry.Select(request.URL)
	if err != nil {
		return Result{}, categorized("select extractor", err)
	}
	if err := client.emit(ctx, Event{Kind: string(events.KindExtracting), Extractor: selected.Name(), URL: request.URL}); err != nil {
		return Result{}, &Error{Category: ErrorInternal, Op: "emit extracting event", Err: err}
	}
	info, err := selected.Extract(ctx, extractor.Request{URL: request.URL, Transport: transport})
	if err != nil {
		return Result{}, categorized(selected.Name()+" extraction", err)
	}
	if err := client.emit(ctx, Event{Kind: string(events.KindExtracted), Extractor: selected.Name(), URL: request.URL}); err != nil {
		return Result{}, &Error{Category: ErrorInternal, Op: "emit extracted event", Err: err}
	}
	encoded, err := json.Marshal(value.ObjectValue(info.Fields()))
	if err != nil {
		return Result{}, &Error{Category: ErrorInternal, Op: "encode metadata", Err: err}
	}
	result := Result{InfoJSON: encoded, Extractor: selected.Name()}
	if request.SkipDownload {
		return result, nil
	}

	selectedFormat, err := mediaformat.Best(info)
	if err != nil {
		return Result{}, categorized("select format", err)
	}
	pattern := request.OutputTemplate
	if pattern == "" {
		pattern = "%(title)s.%(ext)s"
	}
	outputDir := request.OutputDir
	if outputDir == "" {
		outputDir = "."
	}
	destination, err := outputtemplate.Resolve(outputDir, pattern, info)
	if err != nil {
		return Result{}, categorized("render output template", err)
	}

	sink := events.SinkFunc(func(ctx context.Context, event events.Event) error {
		return client.emit(ctx, Event{
			Kind: string(event.Kind), URL: event.URL, Path: event.Path, Bytes: event.Bytes,
			Total: event.Total, Attempt: event.Attempt, Resuming: event.Resuming, Message: event.Message,
			Fragment: event.Fragment, Fragments: event.Fragments,
		})
	})
	var downloadedPath string
	var downloadedBytes int64
	switch selectedFormat.Protocol {
	case "m3u8_native":
		downloaded, err := hls.NewDownloader(transport, hls.Config{}).Download(ctx, selectedFormat.URL, outputDir, destination, request.Overwrite, sink)
		if err != nil {
			return Result{}, categorized("download HLS", err)
		}
		downloadedPath, downloadedBytes = downloaded.Path, downloaded.Bytes
	case "http_dash_segments":
		downloaded, err := dash.NewDownloader(transport, dash.Config{}).Download(ctx, selectedFormat.URL, outputDir, destination, request.Overwrite, sink)
		if err != nil {
			return Result{}, categorized("download DASH", err)
		}
		if downloaded.MergeRequired {
			tools, err := ffmpeg.Discover(ffmpeg.Config{})
			if err != nil {
				return Result{}, categorized("discover ffmpeg", err)
			}
			if err := pipeline.FinalizeDASH(ctx, downloaded, destination, request.Overwrite, tools, sink); err != nil {
				return Result{}, categorized("merge DASH", err)
			}
			downloadedPath = destination
			if info, statErr := os.Stat(destination); statErr == nil {
				downloadedBytes = info.Size()
			}
		} else {
			downloadedPath = downloaded.Tracks[0].Download.Path
			downloadedBytes = downloaded.Tracks[0].Download.Bytes
		}
	default:
		downloaded, err := downloader.New(transport).Download(ctx, downloader.Job{
			URL: selectedFormat.URL, OutputRoot: outputDir, Destination: destination, Overwrite: request.Overwrite,
		}, sink)
		if err != nil {
			return Result{}, categorized("download", err)
		}
		downloadedPath, downloadedBytes = downloaded.Path, downloaded.Bytes
	}
	result.Downloaded = true
	result.Filename = downloadedPath
	result.Bytes = downloadedBytes
	return result, nil
}

func (client *Client) emit(ctx context.Context, event Event) error {
	if client.handler == nil {
		return nil
	}
	return client.handler(ctx, event)
}

func categorized(op string, err error) error {
	if err == nil {
		return nil
	}
	category := ErrorNetwork
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		category = ErrorCancelled
	case errors.Is(err, extractor.ErrUnsupported):
		category = ErrorUnsupported
	case errors.Is(err, ffmpeg.ErrFFmpegUnavailable), errors.Is(err, ffmpeg.ErrFFprobeUnavailable):
		category = ErrorUnsupported
	case errors.Is(err, outputtemplate.ErrInvalidTemplate), errors.Is(err, outputtemplate.ErrUnsafePath),
		errors.Is(err, downloader.ErrDestinationExists), errors.Is(err, downloader.ErrUnsafeDestination),
		errors.Is(err, network.ErrInvalidProxy):
		category = ErrorInvalidInput
	case errors.Is(err, mediaformat.ErrNoFormats), errors.Is(err, extractor.ErrInvalidMetadata):
		category = ErrorInternal
	}
	return &Error{Category: category, Op: op, Err: err}
}
