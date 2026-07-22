// Package ytdlp provides the supported Go embedding API.
package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/archive"
	"github.com/ytdlp-go/ytdlp/internal/cache"
	"github.com/ytdlp-go/ytdlp/internal/compat/matchfilter"
	compatmetadata "github.com/ytdlp-go/ytdlp/internal/compat/metadata"
	"github.com/ytdlp-go/ytdlp/internal/compat/progress"
	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/cookies/chromium"
	"github.com/ytdlp-go/ytdlp/internal/cookies/chromiumlinux"
	"github.com/ytdlp-go/ytdlp/internal/cookies/chromiumwindows"
	"github.com/ytdlp-go/ytdlp/internal/cookies/firefox"
	"github.com/ytdlp-go/ytdlp/internal/cookies/netscape"
	credentialnetrc "github.com/ytdlp-go/ytdlp/internal/credentials/netrc"
	"github.com/ytdlp-go/ytdlp/internal/downloader"
	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/fragment"
	"github.com/ytdlp-go/ytdlp/internal/javascript/ejs"
	"github.com/ytdlp-go/ytdlp/internal/javascript/supervisor"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/media/pipeline"
	"github.com/ytdlp-go/ytdlp/internal/media/postprocess"
	"github.com/ytdlp-go/ytdlp/internal/network"
	packcatalog "github.com/ytdlp-go/ytdlp/internal/pack/catalog"
	"github.com/ytdlp-go/ytdlp/internal/protocol/dash"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/protocol/ism"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

type ErrorCategory string

const (
	ErrorUnsupported    ErrorCategory = "unsupported"
	ErrorAuthentication ErrorCategory = "authentication"
	ErrorInvalidInput   ErrorCategory = "invalid_input"
	ErrorNetwork        ErrorCategory = "network"
	ErrorSecurity       ErrorCategory = "security"
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
	URL                    string
	OutputTemplate         string
	OutputDir              string
	Proxy                  string
	ImpersonationProfile   string
	CookieFile             string
	CookiesFromBrowser     string
	UseNetRC               bool
	NetRCLocation          string
	DownloadArchive        string
	CacheDir               string
	Timeout                time.Duration
	Overwrite              bool
	SkipDownload           bool
	Format                 string
	FormatSort             []string
	PreferredExtensions    []string
	PreferFreeFormats      bool
	AllowUnplayableFormats bool
	ProgressTemplate       string
	MatchFilters           []string
	ParseMetadata          []string
	ReplaceMetadata        []string
	Downloader             DownloaderOptions
	Postprocessors         []Postprocessor
	// PluginID explicitly selects an installed signed plugin extractor. Plugins
	// are never considered by automatic URL routing.
	PluginID string
}

type Result struct {
	InfoJSON   json.RawMessage
	Extractor  string
	Downloaded bool
	Archived   bool
	Skipped    bool
	SkipReason string
	Filename   string
	Bytes      int64
	Entries    []Result
	Artifacts  []Artifact
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
// JavaScript challenges. The path must be absolute. If unset, the client checks
// only beside its executable; PATH is never searched for native helper code.
func WithJavaScriptHelper(path string) Option {
	return func(client *Client) { client.javascriptHelper = path }
}

// WithTelemetryCollector enables bounded aggregate observations. Telemetry is
// disabled by default and never changes an operation's result or error.
func WithTelemetryCollector(collector *TelemetryCollector) Option {
	return func(client *Client) { client.telemetry = collector }
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
	linuxCookieImporter   func(context.Context, chromiumlinux.Options) (chromiumlinux.Result, error)
	windowsCookieImporter func(context.Context, chromiumwindows.Options) (chromiumwindows.Result, error)
	firefoxCookieImporter func(context.Context, firefox.Options) (firefox.Result, error)
	platform              string
	plugins               []*InstalledPlugin
	pluginApprover        PluginPermissionApprover
	telemetry             *TelemetryCollector
}

func NewClient(options ...Option) *Client {
	client := &Client{}
	for _, option := range options {
		option(client)
	}
	return client
}

func (client *Client) Run(ctx context.Context, request Request) (result Result, runErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	rootExtractor := ""
	if client.telemetry != nil {
		defer func() {
			extractorName := rootExtractor
			if extractorName == "" {
				extractorName = result.Extractor
			}
			outcome := TelemetryOutcomeSuccess
			if runErr != nil {
				outcome = TelemetryOutcomeError
				if IsCategory(runErr, ErrorUnsupported) {
					outcome = TelemetryOutcomeUnsupported
				}
			}
			client.telemetry.observe(extractorName, outcome)
		}()
	}
	if err := validateRequestOptions(request); err != nil {
		return Result{}, categorized("validate request options", err)
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		return Result{}, err
	}
	transport, err := network.New(network.Config{Proxy: request.Proxy, Timeout: request.Timeout, DefaultProfile: request.ImpersonationProfile})
	if err != nil {
		return Result{}, categorized("configure network", err)
	}
	defer transport.CloseIdleConnections()
	if request.CookiesFromBrowser != "" {
		specification, err := parseBrowserCookieSpec(request.CookiesFromBrowser)
		if err != nil {
			return Result{}, categorized("parse browser cookie source", err)
		}
		cookies, importErr := client.importBrowserCookies(ctx, specification)
		if importErr != nil {
			recoverable := len(cookies.Cookies) > 0 &&
				(errors.Is(importErr, chromium.ErrDecrypt) || errors.Is(importErr, chromium.ErrKeyUnavailable) ||
					errors.Is(importErr, chromiumlinux.ErrDecrypt) || errors.Is(importErr, chromiumlinux.ErrKeyUnavailable) ||
					errors.Is(importErr, chromiumwindows.ErrDecrypt) || errors.Is(importErr, chromiumwindows.ErrKeyUnavailable) ||
					errors.Is(importErr, chromiumwindows.ErrAppBound) || errors.Is(importErr, chromiumwindows.ErrInvalidLocalState))
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
		if err := client.emit(ctx, Event{Kind: EventBrowserCookies, Message: message}); err != nil {
			return Result{}, &Error{Category: ErrorInternal, Op: "emit browser cookie event", Err: err}
		}
	}
	if request.CookieFile != "" {
		loaded, loadErr := netscape.LoadFile(ctx, request.CookieFile, netscape.Options{})
		if loadErr != nil {
			return Result{}, categorized("load cookie file", loadErr)
		}
		cookies := make([]*http.Cookie, 0, len(loaded.Entries))
		for _, entry := range loaded.Entries {
			cookie := *entry.Cookie
			if entry.IncludeSubdomains {
				cookie.Domain = "." + strings.TrimPrefix(cookie.Domain, ".")
			} else {
				cookie.Domain = strings.TrimPrefix(cookie.Domain, ".")
			}
			cookies = append(cookies, &cookie)
		}
		if err := transport.AddCookies(cookies); err != nil {
			return Result{}, categorized("load cookie file", err)
		}
		message := fmt.Sprintf("imported %d of %d cookie-file entries", loaded.Imported, loaded.Total)
		if err := client.emit(ctx, Event{Kind: EventBrowserCookies, Message: message}); err != nil {
			return Result{}, &Error{Category: ErrorInternal, Op: "emit cookie-file event", Err: err}
		}
	}
	var credentials extractor.CredentialProvider
	if request.UseNetRC {
		credentials, err = loadNetRCCredentials(ctx, request.NetRCLocation)
		if err != nil {
			return Result{}, categorized("load netrc credentials", err)
		}
	}
	var downloadArchive *archive.Store
	if request.DownloadArchive != "" {
		downloadArchive, err = archive.Open(ctx, request.DownloadArchive, archive.Options{})
		if err != nil {
			return Result{}, categorized("open download archive", err)
		}
	}
	var operationCache *cache.Store
	if request.CacheDir != "" {
		operationCache, err = cache.Open(request.CacheDir, cache.Options{})
		if err != nil {
			return Result{}, categorized("open cache", err)
		}
	}
	challengeSolver := &lazyYouTubeSolver{path: discoverJavaScriptHelper(client.javascriptHelper)}
	defer challengeSolver.Close()
	operation := &operation{
		client: client, request: request, transport: transport,
		registry: client.productRegistry(),
		solver:   challengeSolver, archive: downloadArchive, cache: operationCache,
		credentials:   credentials,
		compatibility: compatibility,
		rootExtractor: &rootExtractor,
	}
	return operation.process(ctx, request.URL, request.PluginID, nil, make(map[string]bool), 0)
}

func (client *Client) productRegistry() *extractor.Registry {
	registered := []extractor.Extractor{
		extractor.NewYouTube(),
		extractor.NewVimeo(),
		extractor.NewTikTok(),
		extractor.NewBrightcove(),
		extractor.NewKaltura(),
		extractor.NewJWPlatform(),
		extractor.NewWistia(),
		extractor.NewSproutVideo(),
		extractor.NewDailymotion(),
		extractor.NewReddit(),
		extractor.NewTwitter(),
		extractor.NewBandcamp(),
		extractor.NewMixcloud(),
		extractor.NewRumble(),
		extractor.NewBilibili(),
		extractor.NewInstagram(),
		extractor.NewKick(),
		extractor.NewBBCIPlayer(),
		extractor.NewARD(),
		extractor.NewNRK(),
		extractor.NewTwitch(),
		extractor.NewSoundCloud(),
		extractor.NewStreamable(),
		extractor.NewPeerTube(),
		extractor.NewInternetArchive(),
		extractor.NewRegionSVT(),
		extractor.NewSyntheticAuth(),
	}
	for _, installed := range client.plugins {
		if installed != nil {
			registered = append(registered, &installedPluginExtractor{installed: installed, approver: client.pluginApprover})
		}
	}
	registered = append(registered,
		extractor.NewFixture(),
		extractor.NewGeneric(),
	)
	return extractor.NewRegistry(registered...)
}

// productRegistry retains the package-level test seam for the native-only
// product registry.
func productRegistry() *extractor.Registry { return (&Client{}).productRegistry() }

const (
	maxPlaylistDepth   = 8
	maxPlaylistEntries = 10_000
)

type operation struct {
	client        *Client
	request       Request
	transport     *network.Client
	registry      *extractor.Registry
	solver        extractor.YouTubeChallengeSolver
	archive       *archive.Store
	cache         *cache.Store
	credentials   extractor.CredentialProvider
	compatibility compatibilityPlan
	rootExtractor *string
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
	if depth == 0 && operation.rootExtractor != nil {
		*operation.rootExtractor = selected.Name()
	}
	eventURL := network.RedactRawURL(rawURL)
	if err := operation.client.emit(ctx, Event{Kind: string(events.KindExtracting), Extractor: selected.Name(), URL: eventURL}); err != nil {
		return Result{}, &Error{Category: ErrorInternal, Op: "emit extracting event", Err: err}
	}
	extracted, err := selected.Extract(ctx, extractor.Request{
		URL: rawURL, Transport: operation.transport, ChallengeSolver: operation.solver, Credentials: operation.credentials,
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
	if err := operation.client.emit(ctx, Event{Kind: string(events.KindExtracted), Extractor: selected.Name(), URL: eventURL}); err != nil {
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
				result.Archived = result.Archived || child.Archived
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
	decision, err := operation.applyCompatibility(ctx, &info)
	if err != nil {
		return Result{}, err
	}
	encoded, err := encodeInfo(info)
	if err != nil {
		return Result{}, err
	}
	result := Result{InfoJSON: encoded, Extractor: extractorName}
	if !decision.Pass {
		result.Skipped, result.SkipReason = true, decision.Reason
		if err := operation.client.emit(ctx, Event{Kind: EventMatchFilterSkipped, Extractor: extractorName, Message: decision.Reason}); err != nil {
			return Result{}, &Error{Category: ErrorInternal, Op: "emit match-filter skip", Err: err}
		}
		return result, nil
	}
	var archiveIdentity archive.Identity
	if operation.archive != nil {
		id, ok := info.ID()
		if !ok {
			return Result{}, categorized("build archive identity", archive.ErrInvalidIdentity)
		}
		archiveIdentity, err = archive.NewIdentity(extractorName, id)
		if err != nil {
			return Result{}, categorized("build archive identity", err)
		}
		legacyIDs, legacyErr := oldArchiveIDs(info)
		if legacyErr != nil {
			return Result{}, categorized("read legacy archive identities", legacyErr)
		}
		matched, found, matchErr := operation.archive.Match(ctx, archiveIdentity, legacyIDs)
		if matchErr != nil {
			return Result{}, categorized("match download archive", matchErr)
		}
		if found {
			result.Archived = true
			if err := operation.client.emit(ctx, Event{Kind: EventArchiveMatch, Extractor: extractorName, Message: matched}); err != nil {
				return Result{}, &Error{Category: ErrorInternal, Op: "emit archive event", Err: err}
			}
			return result, nil
		}
	}
	if operation.request.SkipDownload {
		return result, nil
	}

	selectedFormats, err := operation.selectFormats(info)
	if err != nil {
		return Result{}, categorized("select format", err)
	}
	pattern := operation.request.OutputTemplate
	if pattern == "" {
		pattern = "%(title)s.%(ext)s"
	}
	outputInfo := value.NewInfo(info.Fields().Clone())
	outputInfo.Set("ext", value.String(mergedOutputExtension(selectedFormats)))
	outputDir := operation.request.OutputDir
	if outputDir == "" {
		outputDir = "."
	}
	destination, err := outputtemplate.Resolve(outputDir, pattern, outputInfo)
	if err != nil {
		return Result{}, categorized("render output template", err)
	}

	sink := operation.eventSink()
	downloadedPath, downloadedBytes, err := operation.downloadSelections(ctx, selectedFormats, outputDir, destination, sink)
	if err != nil {
		return Result{}, categorized("download selected formats", err)
	}
	downloadedPath, result.Artifacts, err = operation.applyPostprocessors(ctx, outputDir, downloadedPath, sink)
	if err != nil {
		return Result{}, categorized("run postprocessors", err)
	}
	if info, statErr := os.Stat(downloadedPath); statErr == nil {
		downloadedBytes = info.Size()
	}
	result.Downloaded = true
	result.Filename = downloadedPath
	result.Bytes = downloadedBytes
	if operation.archive != nil {
		if _, err := operation.archive.Record(ctx, archiveIdentity); err != nil {
			return Result{}, categorized("record download archive", err)
		}
	}
	return result, nil
}

func oldArchiveIDs(info value.Info) ([]string, error) {
	items, ok := info.Lookup("_old_archive_ids").ListValue()
	if !ok {
		return nil, nil
	}
	if len(items) > 1024 {
		return nil, archive.ErrTooLarge
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		id, ok := item.StringValue()
		if !ok {
			return nil, archive.ErrCorrupt
		}
		result = append(result, id)
	}
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
	case errors.Is(err, credentialnetrc.ErrUnsafeFile):
		category = ErrorSecurity
	case errors.Is(err, credentialnetrc.ErrIO):
		category = ErrorAuthentication
	case errors.Is(err, chromium.ErrDatabaseNotFound), errors.Is(err, chromium.ErrInvalidDatabase), errors.Is(err, chromium.ErrSnapshot),
		errors.Is(err, chromium.ErrKeyUnavailable), errors.Is(err, chromium.ErrDecrypt),
		errors.Is(err, firefox.ErrNotFound), errors.Is(err, firefox.ErrInvalidDatabase), errors.Is(err, firefox.ErrSnapshot),
		errors.Is(err, chromiumlinux.ErrNotFound), errors.Is(err, chromiumlinux.ErrInvalidDatabase), errors.Is(err, chromiumlinux.ErrSnapshot),
		errors.Is(err, chromiumlinux.ErrKeyUnavailable), errors.Is(err, chromiumlinux.ErrDecrypt),
		errors.Is(err, chromiumwindows.ErrNotFound), errors.Is(err, chromiumwindows.ErrInvalidDatabase), errors.Is(err, chromiumwindows.ErrSnapshot),
		errors.Is(err, chromiumwindows.ErrInvalidLocalState), errors.Is(err, chromiumwindows.ErrKeyUnavailable),
		errors.Is(err, chromiumwindows.ErrAppBound), errors.Is(err, chromiumwindows.ErrDecrypt):
		category = ErrorAuthentication
	case errors.Is(err, chromium.ErrUnsupportedBrowser), errors.Is(err, chromium.ErrUnsupportedPlatform),
		errors.Is(err, chromiumlinux.ErrUnsupportedBrowser), errors.Is(err, chromiumlinux.ErrUnsupportedPlatform),
		errors.Is(err, chromiumwindows.ErrUnsupportedBrowser), errors.Is(err, chromiumwindows.ErrUnsupportedPlatform):
		category = ErrorUnsupported
	case errors.Is(err, extractor.ErrUnavailable), errors.Is(err, extractor.ErrRegionRestricted), errors.Is(err, extractor.ErrChallengeSolver),
		errors.Is(err, extractor.ErrTransportProfile), errors.Is(err, extractor.ErrTransportIsolation), errors.Is(err, network.ErrImpersonationUnavailable):
		category = ErrorUnsupported
	case errors.Is(err, ffmpeg.ErrFFmpegUnavailable), errors.Is(err, ffmpeg.ErrFFprobeUnavailable):
		category = ErrorUnsupported
	case errors.Is(err, downloader.ErrExternalUnavailable), errors.Is(err, hls.ErrUnsupportedEncryption),
		errors.Is(err, dash.ErrUnsupportedTimeline), errors.Is(err, dash.ErrUnsupportedAddressing):
		category = ErrorUnsupported
	case errors.Is(err, outputtemplate.ErrInvalidTemplate), errors.Is(err, outputtemplate.ErrUnsafePath),
		errors.Is(err, errInvalidRequestOptions),
		errors.Is(err, matchfilter.ErrInvalidFilter), errors.Is(err, compatmetadata.ErrInvalidAction),
		errors.Is(err, progress.ErrInvalidProgress), errors.Is(err, mediaformat.ErrInvalidSelector),
		errors.Is(err, mediaformat.ErrInvalidPreference), errors.Is(err, mediaformat.ErrInvalidHeaders),
		errors.Is(err, downloader.ErrDestinationExists), errors.Is(err, downloader.ErrUnsafeDestination),
		errors.Is(err, downloader.ErrTooManyAttempts), errors.Is(err, downloader.ErrInvalidLimits),
		errors.Is(err, downloader.ErrUnsafeExternalArg), errors.Is(err, downloader.ErrUnsafeExternalTool),
		errors.Is(err, downloader.ErrInvalidExternalURL),
		errors.Is(err, fragment.ErrTooManySegments), errors.Is(err, fragment.ErrTooManyAttempts),
		errors.Is(err, fragment.ErrTooMuchConcurrency), errors.Is(err, fragment.ErrSegmentTooLarge),
		errors.Is(err, fragment.ErrUnsafeDestination), errors.Is(err, ism.ErrInvalidConfig),
		errors.Is(err, ffmpeg.ErrDestinationExists),
		errors.Is(err, ffmpeg.ErrInvalidOperation), errors.Is(err, postprocess.ErrInvalidGraph),
		errors.Is(err, postprocess.ErrUnsafePath),
		errors.Is(err, network.ErrInvalidProxy), errors.Is(err, network.ErrInvalidCookie),
		errors.Is(err, errInvalidBrowserCookieSpec), errors.Is(err, netscape.ErrMalformed),
		errors.Is(err, netscape.ErrWrongFormat), errors.Is(err, netscape.ErrTooLarge),
		errors.Is(err, firefox.ErrUnsafePath), errors.Is(err, firefox.ErrLimit),
		errors.Is(err, chromiumlinux.ErrUnsafePath), errors.Is(err, chromiumlinux.ErrLimit),
		errors.Is(err, chromiumwindows.ErrUnsafePath), errors.Is(err, chromiumwindows.ErrLimit),
		errors.Is(err, credentialnetrc.ErrSyntax), errors.Is(err, credentialnetrc.ErrLimit), errors.Is(err, credentialnetrc.ErrInvalidHost),
		errors.Is(err, archive.ErrInvalidIdentity), errors.Is(err, archive.ErrCorrupt), errors.Is(err, archive.ErrTooLarge), errors.Is(err, archive.ErrUnsafePath),
		errors.Is(err, cache.ErrInvalidName), errors.Is(err, cache.ErrUnsafePath), errors.Is(err, cache.ErrTooLarge), errors.Is(err, cache.ErrCorrupt):
		category = ErrorInvalidInput
	case errors.Is(err, archive.ErrIO), errors.Is(err, archive.ErrLock), errors.Is(err, cache.ErrIO):
		category = ErrorInternal
	case errors.Is(err, packcatalog.ErrUntrusted), errors.Is(err, packcatalog.ErrSignature), errors.Is(err, packcatalog.ErrRevoked), errors.Is(err, packcatalog.ErrExpired):
		category = ErrorSecurity
	case errors.Is(err, packcatalog.ErrInvalid), errors.Is(err, packcatalog.ErrLimit), errors.Is(err, packcatalog.ErrNotFound):
		category = ErrorInvalidInput
	case errors.Is(err, mediaformat.ErrNoFormats), errors.Is(err, extractor.ErrInvalidMetadata),
		errors.Is(err, extractor.ErrInvalidPlaylist), errors.Is(err, extractor.ErrPlaylistLimit),
		errors.Is(err, downloader.ErrExternalFailed), errors.Is(err, fragment.ErrNoSegments),
		errors.Is(err, fragment.ErrInvalidEncryption), errors.Is(err, hls.ErrInvalidPlaylist),
		errors.Is(err, dash.ErrInvalidMPD), errors.Is(err, ism.ErrInvalidManifest), errors.Is(err, ism.ErrTimelineBound),
		errors.Is(err, ffmpeg.ErrMediaFailure), errors.Is(err, pipeline.ErrMissingDASHTracks),
		errors.Is(err, pipeline.ErrMissingToolset):
		category = ErrorInternal
	}
	return &Error{Category: category, Op: op, Err: err}
}

var errInvalidBrowserCookieSpec = errors.New("invalid browser cookie source")

type browserCookieSpec struct {
	browser   string
	profile   string
	container string
}

type cookieImportResult struct {
	Cookies                 []*http.Cookie
	Total, Imported, Failed int
}

func parseBrowserCookieSpec(input string) (browserCookieSpec, error) {
	base, container, hasContainer := strings.Cut(strings.TrimSpace(input), "::")
	if hasContainer && strings.Contains(container, ":") {
		return browserCookieSpec{}, fmt.Errorf("%w: invalid container", errInvalidBrowserCookieSpec)
	}
	browserName, profile, hasProfile := strings.Cut(base, ":")
	switch browserName {
	case "chrome", "chromium", "brave", "edge", "vivaldi", "opera", "firefox":
	default:
		return browserCookieSpec{}, fmt.Errorf("%w: unsupported browser", errInvalidBrowserCookieSpec)
	}
	if hasProfile && (profile == "" || profile == "." || profile == ".." || strings.ContainsAny(profile, `:/\\`+"\x00")) {
		return browserCookieSpec{}, fmt.Errorf("%w: invalid browser profile", errInvalidBrowserCookieSpec)
	}
	if hasContainer && (browserName != "firefox" || container == "" || strings.ContainsAny(container, `:/\\`+"\x00")) {
		return browserCookieSpec{}, fmt.Errorf("%w: invalid Firefox container", errInvalidBrowserCookieSpec)
	}
	return browserCookieSpec{browser: browserName, profile: profile, container: container}, nil
}

func (client *Client) importBrowserCookies(ctx context.Context, specification browserCookieSpec) (cookieImportResult, error) {
	if specification.browser == "firefox" {
		importer := client.firefoxCookieImporter
		if importer == nil {
			importer = firefox.Import
		}
		result, err := importer(ctx, firefox.Options{Profile: specification.profile, Container: specification.container})
		return cookieImportResult{Cookies: result.Cookies, Total: result.Total, Imported: result.Imported}, err
	}
	platform := client.platform
	if platform == "" {
		platform = runtime.GOOS
	}
	if platform == "darwin" && specification.browser == "chrome" {
		importer := client.browserCookieImporter
		if importer == nil {
			importer = chromium.Import
		}
		result, err := importer(ctx, chromium.Options{Browser: chromium.Chrome, Profile: specification.profile})
		return cookieImportResult{Cookies: result.Cookies, Total: result.Total, Imported: result.Imported, Failed: result.Failed}, err
	}
	if platform == "linux" {
		importer := client.linuxCookieImporter
		if importer == nil {
			importer = chromiumlinux.Import
		}
		result, err := importer(ctx, chromiumlinux.Options{Browser: chromiumlinux.Browser(specification.browser), Profile: specification.profile})
		return cookieImportResult{Cookies: result.Cookies, Total: result.Total, Imported: result.Imported, Failed: result.Failed}, err
	}
	if platform == "windows" {
		importer := client.windowsCookieImporter
		if importer == nil {
			importer = chromiumwindows.Import
		}
		result, err := importer(ctx, chromiumwindows.Options{Browser: chromiumwindows.Browser(specification.browser), Profile: specification.profile})
		return cookieImportResult{Cookies: result.Cookies, Total: result.Total, Imported: result.Imported, Failed: result.Failed}, err
	}
	return cookieImportResult{}, chromiumlinux.ErrUnsupportedPlatform
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
	return ""
}
