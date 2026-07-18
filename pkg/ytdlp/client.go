// Package ytdlp provides the supported Go embedding API.
package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/cookies/chromium"
	"github.com/ytdlp-go/ytdlp/internal/downloader"
	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/javascript/ejs"
	"github.com/ytdlp-go/ytdlp/internal/javascript/supervisor"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/media/pipeline"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/dash"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

type ErrorCategory string

const (
	ErrorUnsupported    ErrorCategory = "unsupported"
	ErrorAuthentication ErrorCategory = "authentication"
	ErrorInvalidInput   ErrorCategory = "invalid_input"
	ErrorNetwork        ErrorCategory = "network"
	ErrorCancelled      ErrorCategory = "cancelled"
	ErrorInternal       ErrorCategory = "internal"
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
	URL                string
	OutputTemplate     string
	OutputDir          string
	Proxy              string
	CookiesFromBrowser string
	Timeout            time.Duration
	Overwrite          bool
	SkipDownload       bool
}

type Result struct {
	InfoJSON   json.RawMessage
	Extractor  string
	Downloaded bool
	Filename   string
	Bytes      int64
	Entries    []Result
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

// WithJavaScriptHelper selects the isolated helper used for extractor
// JavaScript challenges. If unset, the client checks beside its executable and
// then PATH for ytdlp-js-helper.
func WithJavaScriptHelper(path string) Option {
	return func(client *Client) { client.javascriptHelper = path }
}

// Runner is the cancellable operation contract.
type Runner interface {
	Run(context.Context, Request) (Result, error)
}

// Client is stateless between operations and safe for concurrent use. A
// configured event handler must provide its own synchronization when shared.
type Client struct {
	handler               EventHandler
	javascriptHelper      string
	browserCookieImporter func(context.Context, chromium.Options) (chromium.Result, error)
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
	defer transport.CloseIdleConnections()
	if request.CookiesFromBrowser != "" {
		options, err := parseBrowserCookieSpec(request.CookiesFromBrowser)
		if err != nil {
			return Result{}, categorized("parse browser cookie source", err)
		}
		importer := client.browserCookieImporter
		if importer == nil {
			importer = chromium.Import
		}
		cookies, importErr := importer(ctx, options)
		if importErr != nil {
			recoverable := len(cookies.Cookies) > 0 &&
				(errors.Is(importErr, chromium.ErrDecrypt) || errors.Is(importErr, chromium.ErrKeyUnavailable))
			if !recoverable {
				return Result{}, categorized("import browser cookies", importErr)
			}
		}
		if err := transport.AddCookies(cookies.Cookies); err != nil {
			return Result{}, categorized("load browser cookies", err)
		}
		message := fmt.Sprintf("imported %d of %d browser cookies", cookies.Imported, cookies.Total)
		if cookies.Failed > 0 {
			message += fmt.Sprintf("; skipped %d", cookies.Failed)
		}
		if err := client.emit(ctx, Event{Kind: "browser_cookies", Message: message}); err != nil {
			return Result{}, &Error{Category: ErrorInternal, Op: "emit browser cookie event", Err: err}
		}
	}
	challengeSolver := &lazyYouTubeSolver{path: discoverJavaScriptHelper(client.javascriptHelper)}
	defer challengeSolver.Close()
	operation := &operation{
		client: client, request: request, transport: transport,
		registry: extractor.NewRegistry(extractor.NewYouTube(), extractor.NewVimeo(), extractor.NewFixture(), extractor.NewGeneric()),
		solver:   challengeSolver,
	}
	return operation.process(ctx, request.URL, "", nil, make(map[string]bool), 0)
}

const (
	maxPlaylistDepth   = 8
	maxPlaylistEntries = 10_000
)

type operation struct {
	client    *Client
	request   Request
	transport *network.Client
	registry  *extractor.Registry
	solver    extractor.YouTubeChallengeSolver
}

func (operation *operation) process(ctx context.Context, rawURL, extractorKey string, overlay *extractor.Entry, ancestors map[string]bool, depth int) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, categorized("process extraction", err)
	}
	if depth > maxPlaylistDepth || ancestors[rawURL] {
		return Result{}, categorized("expand playlist", extractor.ErrPlaylistLimit)
	}
	ancestors[rawURL] = true
	defer delete(ancestors, rawURL)

	selected, err := operation.registry.SelectFor(rawURL, extractorKey)
	if err != nil {
		return Result{}, categorized("select extractor", err)
	}
	if err := operation.client.emit(ctx, Event{Kind: string(events.KindExtracting), Extractor: selected.Name(), URL: rawURL}); err != nil {
		return Result{}, &Error{Category: ErrorInternal, Op: "emit extracting event", Err: err}
	}
	extracted, err := selected.Extract(ctx, extractor.Request{
		URL: rawURL, Transport: operation.transport, ChallengeSolver: operation.solver,
	})
	if err != nil {
		return Result{}, categorized(selected.Name()+" extraction", err)
	}
	if overlay != nil && overlay.Transparent {
		info := value.NewInfo(extracted.Info.Fields().Clone())
		if overlay.ID != "" {
			info.Set("id", value.String(overlay.ID))
		}
		if overlay.Title != "" {
			info.Set("title", value.String(overlay.Title))
		}
		extracted.Info = info
	}
	if err := operation.client.emit(ctx, Event{Kind: string(events.KindExtracted), Extractor: selected.Name(), URL: rawURL}); err != nil {
		return Result{}, &Error{Category: ErrorInternal, Op: "emit extracted event", Err: err}
	}
	if extracted.IsPlaylist() {
		return operation.processPlaylist(ctx, extracted, selected.Name(), ancestors, depth)
	}
	return operation.processMedia(ctx, extracted.Info, selected.Name())
}

func (operation *operation) processPlaylist(ctx context.Context, extracted extractor.Extraction, extractorName string, ancestors map[string]bool, depth int) (Result, error) {
	iterator := extracted.Entries.Iterator()
	children := make([]Result, 0)
	entryValues := make([]value.Value, 0)
	playlistID, _ := extracted.Info.ID()
	playlistTitle, _ := extracted.Info.Title()
	for index := 0; ; index++ {
		entry, ok, err := iterator.Next(ctx)
		if err != nil {
			return Result{}, categorized(extractorName+" playlist iteration", err)
		}
		if !ok {
			info := value.NewInfo(extracted.Info.Fields().Clone())
			info.Set("entries", value.List(entryValues...))
			encoded, err := encodeInfo(info)
			if err != nil {
				return Result{}, err
			}
			result := Result{InfoJSON: encoded, Extractor: extractorName, Entries: children}
			for _, child := range children {
				result.Bytes += child.Bytes
				result.Downloaded = result.Downloaded || child.Downloaded
			}
			return result, nil
		}
		if index >= maxPlaylistEntries {
			return Result{}, categorized(extractorName+" playlist iteration", extractor.ErrPlaylistLimit)
		}
		if entry.URL == "" {
			return Result{}, categorized(extractorName+" playlist entry", extractor.ErrInvalidPlaylist)
		}
		child, err := operation.process(ctx, entry.URL, entry.ExtractorKey, &entry, ancestors, depth+1)
		if err != nil {
			return Result{}, fmt.Errorf("playlist entry %d: %w", index+1, err)
		}
		entryValue, err := playlistEntryValue(child.InfoJSON, index+1, playlistID, playlistTitle)
		if err != nil {
			return Result{}, err
		}
		child.InfoJSON, err = entryValue.MarshalJSON()
		if err != nil {
			return Result{}, &Error{Category: ErrorInternal, Op: "encode playlist entry metadata", Err: err}
		}
		children = append(children, child)
		entryValues = append(entryValues, entryValue)
	}
}

func playlistEntryValue(encoded json.RawMessage, index int, playlistID, playlistTitle string) (value.Value, error) {
	var entry value.Value
	if err := json.Unmarshal(encoded, &entry); err != nil {
		return value.Missing(), &Error{Category: ErrorInternal, Op: "decode playlist entry metadata", Err: err}
	}
	object, ok := entry.Object()
	if !ok {
		return value.Missing(), &Error{Category: ErrorInternal, Op: "decode playlist entry metadata", Err: extractor.ErrInvalidMetadata}
	}
	object.Set("playlist_index", value.Int(int64(index)))
	if playlistID != "" {
		object.Set("playlist_id", value.String(playlistID))
	}
	if playlistTitle != "" {
		object.Set("playlist_title", value.String(playlistTitle))
	}
	return value.ObjectValue(object), nil
}

func encodeInfo(info value.Info) (json.RawMessage, error) {
	encoded, err := json.Marshal(value.ObjectValue(info.Fields()))
	if err != nil {
		return nil, &Error{Category: ErrorInternal, Op: "encode metadata", Err: err}
	}
	return encoded, nil
}

func (operation *operation) processMedia(ctx context.Context, info value.Info, extractorName string) (Result, error) {
	encoded, err := encodeInfo(info)
	if err != nil {
		return Result{}, err
	}
	result := Result{InfoJSON: encoded, Extractor: extractorName}
	if operation.request.SkipDownload {
		return result, nil
	}

	selectedFormat, err := mediaformat.Best(info)
	if err != nil {
		return Result{}, categorized("select format", err)
	}
	pattern := operation.request.OutputTemplate
	if pattern == "" {
		pattern = "%(title)s.%(ext)s"
	}
	outputDir := operation.request.OutputDir
	if outputDir == "" {
		outputDir = "."
	}
	destination, err := outputtemplate.Resolve(outputDir, pattern, info)
	if err != nil {
		return Result{}, categorized("render output template", err)
	}

	sink := events.SinkFunc(func(ctx context.Context, event events.Event) error {
		return operation.client.emit(ctx, Event{
			Kind: string(event.Kind), URL: event.URL, Path: event.Path, Bytes: event.Bytes,
			Total: event.Total, Attempt: event.Attempt, Resuming: event.Resuming, Message: event.Message,
			Fragment: event.Fragment, Fragments: event.Fragments,
		})
	})
	var downloadedPath string
	var downloadedBytes int64
	switch selectedFormat.Protocol {
	case "m3u8_native":
		downloaded, err := hls.NewDownloader(operation.transport, hls.Config{}).Download(ctx, selectedFormat.URL, outputDir, destination, operation.request.Overwrite, sink)
		if err != nil {
			return Result{}, categorized("download HLS", err)
		}
		downloadedPath, downloadedBytes = downloaded.Path, downloaded.Bytes
	case "http_dash_segments":
		downloaded, err := dash.NewDownloader(operation.transport, dash.Config{}).Download(ctx, selectedFormat.URL, outputDir, destination, operation.request.Overwrite, sink)
		if err != nil {
			return Result{}, categorized("download DASH", err)
		}
		if downloaded.MergeRequired {
			tools, err := ffmpeg.Discover(ffmpeg.Config{})
			if err != nil {
				return Result{}, categorized("discover ffmpeg", err)
			}
			if err := pipeline.FinalizeDASH(ctx, downloaded, destination, operation.request.Overwrite, tools, sink); err != nil {
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
		downloaded, err := downloader.New(operation.transport).Download(ctx, downloader.Job{
			URL: selectedFormat.URL, OutputRoot: outputDir, Destination: destination, Overwrite: operation.request.Overwrite,
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
	case errors.Is(err, extractor.ErrAuthentication):
		category = ErrorAuthentication
	case errors.Is(err, chromium.ErrDatabaseNotFound), errors.Is(err, chromium.ErrInvalidDatabase), errors.Is(err, chromium.ErrSnapshot),
		errors.Is(err, chromium.ErrKeyUnavailable), errors.Is(err, chromium.ErrDecrypt):
		category = ErrorAuthentication
	case errors.Is(err, chromium.ErrUnsupportedBrowser), errors.Is(err, chromium.ErrUnsupportedPlatform):
		category = ErrorUnsupported
	case errors.Is(err, extractor.ErrUnavailable), errors.Is(err, extractor.ErrChallengeSolver),
		errors.Is(err, extractor.ErrTransportProfile), errors.Is(err, network.ErrImpersonationUnavailable):
		category = ErrorUnsupported
	case errors.Is(err, ffmpeg.ErrFFmpegUnavailable), errors.Is(err, ffmpeg.ErrFFprobeUnavailable):
		category = ErrorUnsupported
	case errors.Is(err, outputtemplate.ErrInvalidTemplate), errors.Is(err, outputtemplate.ErrUnsafePath),
		errors.Is(err, downloader.ErrDestinationExists), errors.Is(err, downloader.ErrUnsafeDestination),
		errors.Is(err, network.ErrInvalidProxy), errors.Is(err, network.ErrInvalidCookie),
		errors.Is(err, errInvalidBrowserCookieSpec):
		category = ErrorInvalidInput
	case errors.Is(err, mediaformat.ErrNoFormats), errors.Is(err, extractor.ErrInvalidMetadata),
		errors.Is(err, extractor.ErrInvalidPlaylist), errors.Is(err, extractor.ErrPlaylistLimit):
		category = ErrorInternal
	}
	return &Error{Category: category, Op: op, Err: err}
}

var errInvalidBrowserCookieSpec = errors.New("invalid browser cookie source")

func parseBrowserCookieSpec(spec string) (chromium.Options, error) {
	browserName, profile, hasProfile := strings.Cut(strings.TrimSpace(spec), ":")
	if browserName != string(chromium.Chrome) {
		return chromium.Options{}, fmt.Errorf("%w: supported value is chrome[:PROFILE]", errInvalidBrowserCookieSpec)
	}
	if hasProfile && (profile == "" || profile == "." || profile == ".." || strings.ContainsAny(profile, `:/\\\x00`)) {
		return chromium.Options{}, fmt.Errorf("%w: invalid Chrome profile", errInvalidBrowserCookieSpec)
	}
	return chromium.Options{Browser: chromium.Chrome, Profile: profile}, nil
}

type lazyYouTubeSolver struct {
	path       string
	supervisor *supervisor.Client
	solver     *ejs.Solver
}

func (solver *lazyYouTubeSolver) SolvePlayer(
	ctx context.Context,
	id string,
	player string,
	requests []ejs.ChallengeRequest,
	outputPreprocessed bool,
) (ejs.Result, error) {
	if solver.solver == nil {
		client, err := supervisor.New(supervisor.Config{Path: solver.path, MemoryBytes: ejs.SolverMemoryBytes})
		if err != nil {
			return ejs.Result{}, err
		}
		challengeSolver, err := ejs.New(client)
		if err != nil {
			_ = client.Close()
			return ejs.Result{}, err
		}
		solver.supervisor, solver.solver = client, challengeSolver
	}
	return solver.solver.SolvePlayer(ctx, id, player, requests, outputPreprocessed)
}

func (solver *lazyYouTubeSolver) Close() {
	if solver != nil && solver.supervisor != nil {
		_ = solver.supervisor.Close()
	}
}

func discoverJavaScriptHelper(configured string) string {
	if configured != "" {
		return configured
	}
	name := "ytdlp-js-helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), name)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate
		}
	}
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	return name
}
