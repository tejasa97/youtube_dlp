// Package engine provides provider-neutral media orchestration and an explicit
// typed provider-composition boundary.
package engine

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
	"sync"
	"time"

	providerapi "github.com/tejasa97/youtube_dlp/engine/provider"
	"github.com/tejasa97/youtube_dlp/internal/archive"
	"github.com/tejasa97/youtube_dlp/internal/cache"
	"github.com/tejasa97/youtube_dlp/internal/compat/chapterremove"
	"github.com/tejasa97/youtube_dlp/internal/compat/matchfilter"
	compatmetadata "github.com/tejasa97/youtube_dlp/internal/compat/metadata"
	"github.com/tejasa97/youtube_dlp/internal/compat/progress"
	"github.com/tejasa97/youtube_dlp/internal/compat/sections"
	outputtemplate "github.com/tejasa97/youtube_dlp/internal/compat/template"
	"github.com/tejasa97/youtube_dlp/internal/cookies/chromium"
	"github.com/tejasa97/youtube_dlp/internal/cookies/chromiumlinux"
	"github.com/tejasa97/youtube_dlp/internal/cookies/chromiumwindows"
	"github.com/tejasa97/youtube_dlp/internal/cookies/firefox"
	"github.com/tejasa97/youtube_dlp/internal/cookies/netscape"
	"github.com/tejasa97/youtube_dlp/internal/cookies/safari"
	credentialnetrc "github.com/tejasa97/youtube_dlp/internal/credentials/netrc"
	"github.com/tejasa97/youtube_dlp/internal/downloader"
	"github.com/tejasa97/youtube_dlp/internal/events"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/fragment"
	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
	"github.com/tejasa97/youtube_dlp/internal/media/pipeline"
	"github.com/tejasa97/youtube_dlp/internal/media/postprocess"
	"github.com/tejasa97/youtube_dlp/internal/network"
	packcatalog "github.com/tejasa97/youtube_dlp/internal/pack/catalog"
	"github.com/tejasa97/youtube_dlp/internal/protocol/dash"
	"github.com/tejasa97/youtube_dlp/internal/protocol/hds"
	"github.com/tejasa97/youtube_dlp/internal/protocol/hls"
	"github.com/tejasa97/youtube_dlp/internal/protocol/ism"
	"github.com/tejasa97/youtube_dlp/internal/protocol/youtubelive"
	"github.com/tejasa97/youtube_dlp/internal/protocol/youtubeump"
	"github.com/tejasa97/youtube_dlp/internal/value"
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

// DownloadHTTPStatusError identifies a non-success HTTP response from the
// media downloader. It is distinct from HTTPStatusError, which reports
// extractor and API status codes as "HTTP status N". Desktop callers can use
// errors.As or DownloadHTTPStatusCode to detect an expired signed media URL
// without matching error text.
type DownloadHTTPStatusError = downloader.HTTPStatusError

// DownloadHTTPStatusCode reports the media-downloader HTTP status when err
// unwraps to DownloadHTTPStatusError. Extractor HTTPStatusError values do
// not match.
func DownloadHTTPStatusCode(err error) (int, bool) {
	var status *DownloadHTTPStatusError
	if !errors.As(err, &status) || status == nil {
		return 0, false
	}
	return status.Code, true
}

type Request struct {
	URL string
	// ExtractorSelection controls automatic and URL-result extractor routing.
	// Rules are applied in order using the pinned comma-separated include /
	// exclude grammar. An empty final rule list selects the default policy.
	// Installed signed plugins remain explicit-only.
	ExtractorSelection ExtractorSelectionOptions
	// ForceGenericExtractor routes automatic URL selection through the
	// registered generic extractor. Explicit playlist extractor keys and an
	// explicit PluginID remain authoritative, and generic URL safety is still
	// enforced before any transport is constructed.
	ForceGenericExtractor bool
	// DefaultSearch controls bounded routing for unqualified inputs. The zero
	// value is the pinned fixup_error behavior. Supported modes are error,
	// fixup_error, auto, auto_warning, and prefixes owned by registered opaque
	// search extractors.
	DefaultSearch   string
	OutputTemplate  string
	OutputTemplates OutputTemplates
	OutputDir       string
	OutputPaths     OutputPaths
	// UseID selects the pinned %(id)s.%(ext)s default when no explicit output
	// template is configured. Explicit typed/default templates always win.
	UseID           bool
	AutonumberStart int
	AutonumberSize  int
	// AutonumberIndex is the zero-based number of media entries processed
	// before this request. CLI callers use it to preserve numbering across
	// multiple inputs; zero is the normal default.
	AutonumberIndex int
	// LoadInfoJSON treats the named local file as an untrusted single-video
	// metadata envelope. It is intentionally separate from URL extraction.
	LoadInfoJSON   string
	RemoveCacheDir bool
	Proxy          string
	// SourceAddress binds native and browser-profile TCP dials to this local IP.
	// ForceIPv4 and ForceIPv6 are mutually exclusive at the API boundary; the
	// CLI resolves them with last-option-wins semantics before Run is called.
	SourceAddress        string
	ForceIPv4            bool
	ForceIPv6            bool
	ImpersonationProfile string
	CookieFile           string
	CookiesFromBrowser   string
	UseNetRC             bool
	NetRCLocation        string
	// VideoPassword is the optional password used by extractors that gate
	// media behind a per-video secret (for example Vimeo's password-protected
	// player). Empty is valid; non-empty values are bounded, validated, and
	// never echoed back in errors, events, or metadata.
	VideoPassword   string
	DownloadArchive string
	// ForceWriteArchive records successfully selected entries even when
	// Simulate or SkipDownload suppresses the normal archive write. It never
	// records rejected, failed, or size-aborted entries.
	ForceWriteArchive bool
	CacheDir          string
	Timeout           time.Duration
	Overwrite         bool
	// KeepVideo retains media inputs that a postprocessor successfully replaces.
	// It only affects postprocessor-owned intermediate media; download outputs,
	// sidecars, and explicit file moves retain their own lifecycle ownership.
	KeepVideo bool
	// PostOverwrites controls replacement of postprocessor destinations. A nil
	// value selects yt-dlp's default (enabled); a non-nil false value rejects an
	// existing postprocessor destination without changing final-output policy.
	PostOverwrites *bool
	// Simulate suppresses media, sidecar, archive, and postprocessor output
	// while still performing extraction. ForceWriteArchive is the explicit
	// exception for successful selected entries. Unlike SkipDownload, it does
	// not permit related-file writes.
	Simulate     bool
	SkipDownload bool
	Format       string
	// HLSSplitDiscontinuity selects the first eligible discontinuity group from
	// an already-selected HLS representation. It never creates extra outputs.
	HLSSplitDiscontinuity bool
	// HLSDiscontinuitySequences selects one or more absolute HLS
	// EXT-X-DISCONTINUITY-SEQUENCE group IDs from the already-selected native
	// HLS representation. Duplicate IDs are ignored and selected groups are
	// emitted in playlist order. One ID retains the ordinary destination; more
	// than one ID creates transactional output plans.
	HLSDiscontinuitySequences []int64
	FormatSort                []string
	FormatSortForce           bool
	PreferredExtensions       []string
	PreferFreeFormats         bool
	// MergeOutputFormat is a slash-separated container preference order used
	// when merging multiple format tracks (for example "mp4/mkv"). CLI
	// exposure is added in a later PR; execution consumes the field now.
	MergeOutputFormat      string
	AllowUnplayableFormats bool
	// AllowMultipleVideoStreams and AllowMultipleAudioStreams retain later
	// same-kind tracks in merged format plans. Both default to false.
	AllowMultipleVideoStreams bool
	AllowMultipleAudioStreams bool
	// CheckFormats controls the bounded availability probes performed before a
	// format is selected. It is deliberately separate from format.Options: the
	// format package remains pure and receives the checker through
	// format.EvaluationOptions.
	CheckFormats              FormatCheckMode
	YouTubeTranslatedCaptions bool
	LiveFromStart             bool
	YouTubeComments           YouTubeCommentOptions
	SoundCloudComments        SoundCloudCommentOptions
	SponsorBlock              SponsorBlockOptions
	NHK                       NHKOptions
	// RemoveChapters contains repeatable yt-dlp --remove-chapters
	// specifications. Values beginning with "*" are manual time ranges;
	// all other values are chapter-title regular expressions.
	RemoveChapters []string
	// DownloadSections contains repeatable yt-dlp --download-sections
	// specifications. Only the bounded forms are accepted: *START-END,
	// *START-inf, and *from-url. Unsupported values are rejected. The
	// ranges are parsed by the generic section planner and delegated to
	// ffmpeg section downloading; *from-url consumes the extractor's
	// start_time/end_time bounds.
	DownloadSections []string
	// ForceKeyframesAtCuts applies to ordinary chapter, manual range,
	// SponsorBlock cuts, and section downloads. It is invalid when no
	// removal or section download is requested.
	ForceKeyframesAtCuts bool
	Subtitles            SubtitleOptions
	Thumbnails           ThumbnailOptions
	RelatedFiles         RelatedFileOptions
	Filesystem           FilesystemOptions
	PrintRules           []PrintRule
	Playlist             PlaylistOptions
	ProgressTemplate     string
	MatchFilters         []string
	// InteractiveMatchFilter is required when MatchFilters or BreakMatchFilters
	// contains "-". It is called only for complete, non-archived entries.
	InteractiveMatchFilter InteractiveMatchFilterFunc
	// InteractiveFormat is required when Format is exactly "-". It runs after
	// extraction against canonical formats and returns one selector expression;
	// an empty response selects the normal default.
	InteractiveFormat InteractiveFormatFunc
	// BreakMatchFilters use the same OR/AND language as MatchFilters, but a
	// rejection stops playlist expansion before the rejected entry is retained.
	BreakMatchFilters []string
	// SimpleFilters mirrors yt-dlp's simple --match-title/--date/--age-limit
	// style filters. They are evaluated before the generic match filters,
	// after the download-archive check, matching _match_entry's order.
	SimpleFilters SimpleFilterOptions
	// MaxDownloads aborts the run after this many media entries pass
	// selection (including simulated downloads). Zero means unlimited. The
	// limit is scoped to this Run; batch callers carry the remaining budget
	// across inputs and set BreakPerInput to reset it per input.
	MaxDownloads int
	// BreakOnExisting stops the run when an entry is already recorded in the
	// download archive (yt-dlp --break-on-existing).
	BreakOnExisting bool
	// BreakOnReject stops the run when any filter rejects an entry
	// (yt-dlp --break-on-reject).
	BreakOnReject bool
	// BreakPerInput makes MaxDownloads and the stop conditions apply per
	// input URL rather than across the whole batch (yt-dlp --break-per-input).
	BreakPerInput bool
	// MetadataActions preserves command-line ordering between parse and replace
	// metadata operations. ParseMetadata and ReplaceMetadata remain for callers
	// of the earlier programmatic API; new callers should prefer this field.
	MetadataActions []MetadataAction
	ParseMetadata   []string
	ReplaceMetadata []string
	// EmbedMetadata writes a bounded set of canonical Info fields into the
	// final media container after conversion, subtitle embedding, and chapter
	// cuts. EmbedChapters is tri-state so nil can mirror yt-dlp's dependent
	// default: chapters are embedded when metadata or SponsorBlock marking is
	// enabled, while an explicit false disables that implication.
	EmbedMetadata bool
	EmbedChapters *bool
	// EmbedInfoJSON attaches the cleaned, bounded Info JSON to Matroska media.
	// Nil and false both disable attachment; an explicit false is retained so
	// callers can clear an inherited setting without changing EmbedMetadata.
	EmbedInfoJSON *bool
	// FixupPolicy selects the closed policy set implemented by the typed
	// ffmpeg fixup operations. The zero value disables automatic fixup; the CLI
	// supplies detect_or_warn for parity with yt-dlp's default.
	FixupPolicy string
	// SplitChapters writes one media artifact per bounded internal chapter. The
	// original media remains the primary result and is retained in the archive
	// identity exactly once.
	SplitChapters bool
	// ConcatPlaylist selects the closed playlist concatenation policy. The CLI
	// supplies multi_video; an empty API value leaves concatenation disabled.
	ConcatPlaylist string
	// Xattrs writes the bounded metadata mapping to the final media file's
	// extended attributes when the platform and filesystem support it.
	Xattrs     bool
	Downloader DownloaderOptions
	// DenyDynamicMPD maps the product's --no-allow-dynamic-mpd policy to the
	// DASH protocol boundary. The zero value intentionally allows dynamic MPDs.
	DenyDynamicMPD bool
	// ExtractorRetries bounds retries of entered extractor operations only when
	// the selected extractor explicitly implements extractor.RetrySafeExtractor.
	// Extractors without that replay-safety capability are called once. A zero
	// value disables this outer retry loop; the CLI supplies yt-dlp's default of
	// three retries explicitly.
	ExtractorRetries int
	Postprocessors   []Postprocessor
	// PluginID explicitly selects an installed signed plugin extractor. Plugins
	// are never considered by automatic URL routing.
	PluginID string
}

func (request Request) postprocessorOverwrites() bool {
	return request.PostOverwrites == nil || *request.PostOverwrites
}

// MetadataActionKind identifies one MetadataParser operation.
type MetadataActionKind uint8

const (
	MetadataActionParse MetadataActionKind = iota + 1
	MetadataActionReplace
)

// MetadataAction is an ordered --parse-metadata or --replace-in-metadata
// request. Replace actions use Fields, Search, and Replacement, matching
// yt-dlp's three command-line arguments.
type MetadataAction struct {
	Kind        MetadataActionKind
	Parse       string
	Fields      string
	Search      string
	Replacement string
}

// FormatCheckMode controls when selected media URLs are availability-checked.
// The zero value preserves the historical no-probe behavior.
type FormatCheckMode uint8

const (
	// FormatCheckAuto mirrors yt-dlp's default: only formats explicitly marked
	// DRM or __needs_testing are probed. It is intentionally the zero value.
	FormatCheckAuto FormatCheckMode = iota
	FormatCheckNone
	FormatCheckSelected
	FormatCheckAll
)

// ErrFormatCheckLimit reports exhaustion of the bounded availability checking
// budget. It is exported so callers can distinguish a safety limit from an
// unavailable media candidate.
var ErrFormatCheckLimit = errors.New("format availability check limit exceeded")

// InteractiveMatchFilterPrompt describes one interactive download decision.
type InteractiveMatchFilterPrompt struct {
	ID       string
	Title    string
	Filename string
}

// InteractiveMatchFilterFunc returns true to retain a media entry and false
// to skip it. Implementations must honor context cancellation where possible.
type InteractiveMatchFilterFunc func(context.Context, InteractiveMatchFilterPrompt) (bool, error)

// InteractiveFormatPrompt describes one bounded selector retry. Error is a
// safe diagnostic (it never contains a media URL or request headers).
type InteractiveFormatPrompt struct {
	Attempt int
	Error   string
}

// InteractiveFormatFunc supplies a selector for an extracted media entry.
// Empty selects the ordinary default. Implementations must honor cancellation.
type InteractiveFormatFunc func(context.Context, InteractiveFormatPrompt) (string, error)

// ErrInteractiveInput identifies a missing or failed interactive input prompt
// boundary. Context cancellation remains discoverable through errors.Is.
var ErrInteractiveInput = errors.New("interactive input unavailable")

// StopKind classifies why a result's processing queue ended. The zero value
// means the result completed normally.
type StopKind uint8

const (
	StopNone StopKind = iota
	// StopBreakMatchFilter reports a --break-match-filter rejection, which
	// always stops regardless of --break-on-reject.
	StopBreakMatchFilter
	// StopBreakOnReject reports a filter rejection with --break-on-reject.
	StopBreakOnReject
	// StopBreakOnExisting reports an archive match with --break-on-existing.
	StopBreakOnExisting
	// StopMaxDownloads reports that the per-run MaxDownloads cap was reached.
	StopMaxDownloads
)

func (kind StopKind) String() string {
	switch kind {
	case StopBreakMatchFilter:
		return "break match filter"
	case StopBreakOnReject:
		return "break on reject"
	case StopBreakOnExisting:
		return "break on existing"
	case StopMaxDownloads:
		return "max downloads reached"
	default:
		return "none"
	}
}

type Result struct {
	InfoJSON   json.RawMessage
	Extractor  string
	Downloaded bool
	Archived   bool
	Skipped    bool
	SkipReason string
	// Stopped reports that a stopping condition (break match filter,
	// --break-on-reject, --break-on-existing, or --max-downloads) ended this
	// result's queue. StopKind classifies the condition.
	Stopped    bool
	StopKind   StopKind
	StopReason string
	// Downloads counts media entries in this result that passed selection
	// (archive and filters) and entered the download pipeline, including
	// simulated downloads. Playlist results aggregate their children.
	Downloads int
	Filename  string
	Bytes     int64
	Entries   []Result
	Artifacts []Artifact
	Prints    []PrintOutput
	// SuppressedFailures counts ordinary entry failures that a playlist
	// continued past. The failures remain observable even when Run returns a
	// usable partial playlist result.
	SuppressedFailures int
	// AutonumberCount reports how many media entries consumed an autonumber
	// slot, allowing callers processing multiple requests to carry state.
	AutonumberCount int
	Session         SessionOutcome
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

func WithPOTResolver(resolver providerapi.POTResolver, configurationError error) Option {
	return func(client *Client) {
		client.potResolver = resolver
		client.potResolverErr = configurationError
	}
}

// withTransportFactory is an unexported package test seam. Production clients
// always use network.New; same-package deterministic product tests may replace
// only construction of the normal network client without bypassing Client.Run.
func withTransportFactory(factory func(network.Config) (*network.Client, error)) Option {
	return func(client *Client) { client.transportFactory = factory }
}

// Runner is the cancellable operation contract.
type Runner interface {
	Run(context.Context, Request) (Result, error)
}

// Client is safe for concurrent use. The shared EJS solver and its bounded
// preprocessed-player cache persist across Run calls so that separate
// downloads sharing the same YouTube player script skip redundant parsing.
// A configured event handler must provide its own synchronization when shared.
type Client struct {
	composition           Composition
	handler               EventHandler
	javascriptHelper      string
	browserCookieImporter func(context.Context, chromium.Options) (chromium.Result, error)
	linuxCookieImporter   func(context.Context, chromiumlinux.Options) (chromiumlinux.Result, error)
	windowsCookieImporter func(context.Context, chromiumwindows.Options) (chromiumwindows.Result, error)
	firefoxCookieImporter func(context.Context, firefox.Options) (firefox.Result, error)
	safariCookieImporter  func(context.Context, safari.Options) (safari.Result, error)
	platform              string
	plugins               []*InstalledPlugin
	pluginApprover        PluginPermissionApprover
	telemetry             *TelemetryCollector
	potResolver           providerapi.POTResolver
	potResolverErr        error
	transportFactory      func(network.Config) (*network.Client, error)

	solverMu     sync.Mutex
	sharedSolver *lazyChallengeSolver
}

func NewClient(composition Composition, options ...Option) *Client {
	client := &Client{composition: composition}
	for _, option := range options {
		if option != nil {
			option(client)
		}
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
	if request.SponsorBlock.ChapterTitle != nil {
		chapterTitle := *request.SponsorBlock.ChapterTitle
		request.SponsorBlock.ChapterTitle = &chapterTitle
	}
	if request.EmbedChapters != nil {
		embedChapters := *request.EmbedChapters
		request.EmbedChapters = &embedChapters
	}
	if request.EmbedInfoJSON != nil {
		embedInfoJSON := *request.EmbedInfoJSON
		request.EmbedInfoJSON = &embedInfoJSON
	}
	request.OutputTemplates = cloneOutputTemplates(request.OutputTemplates)
	request.OutputPaths = cloneOutputPaths(request.OutputPaths)
	if resumeOptionsPresent(request.Filesystem.Resume) {
		request.Filesystem.Resume = cloneResumeOptions(request.Filesystem.Resume)
	}
	if err := validateRequestOptions(request); err != nil {
		return Result{}, categorized("validate request options", err)
	}
	if request.RemoveCacheDir {
		if err := cache.RemoveRoot(ctx, request.CacheDir); err != nil {
			return Result{}, categorized("remove cache directory", err)
		}
		return Result{}, nil
	}
	if client.potResolverErr != nil {
		return Result{}, &Error{Category: ErrorInvalidInput, Op: "configure provider token resolver", Err: client.potResolverErr}
	}
	runtime, err := client.composition.newRuntime(request, client.compositionState(request).Client)
	if err != nil {
		return Result{}, &Error{Category: ErrorInternal, Op: "compose provider registry", Err: err}
	}
	if err := runtime.ConfigureSelection(request.ExtractorSelection.Rules); err != nil {
		return Result{}, categorized("compile extractor selection", err)
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		return Result{}, err
	}
	routed := routedInput{URL: request.URL}
	if request.LoadInfoJSON == "" && !request.RemoveCacheDir {
		routed, err = routeRequestInput(ctx, runtime, request)
		if err != nil {
			return Result{}, categorized("route input", err)
		}
		request.URL = routed.URL
	}
	transportFactory := client.transportFactory
	if transportFactory == nil {
		transportFactory = network.New
	}
	transport, err := transportFactory(network.Config{
		Proxy: request.Proxy, Timeout: request.Timeout, DefaultProfile: request.ImpersonationProfile,
		SourceAddress: request.SourceAddress, ForceIPv4: request.ForceIPv4, ForceIPv6: request.ForceIPv6,
	})
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
	var credentials CredentialProvider
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
	challengeSolver := client.sharedChallengeSolver()
	plannerCapabilities := plannerCapabilitiesFor(request)
	operation := &operation{
		client: client, request: request, transport: transport,
		registry: runtime, routingSearchQuery: routed.SearchQuery,
		solver: challengeSolver, archive: downloadArchive, cache: operationCache,
		credentials:         credentials,
		compatibility:       compatibility,
		rootExtractor:       &rootExtractor,
		plannerCapabilities: &plannerCapabilities,
		autonumberNext:      request.AutonumberIndex,
	}
	if routed.Warning != "" {
		if err := client.emit(ctx, Event{Kind: string(events.KindMetadataWarning), Message: routed.Warning}); err != nil {
			return Result{}, &Error{Category: ErrorInternal, Op: "emit routing warning", Err: err}
		}
	}
	var loadedInfo value.Info
	if request.LoadInfoJSON != "" {
		info, loadErr := loadInfoJSON(ctx, request.LoadInfoJSON)
		if loadErr != nil {
			return Result{}, categorized("load info JSON", loadErr)
		}
		loadedInfo = info
		rootExtractor = "loaded-info-json"
		result, runErr = operation.processMedia(ctx, Media(info), "loaded-info-json")
	} else {
		// Explicit selected/all checks take precedence over allow-unplayable. The
		// latter bypasses only Auto's DRM/needs-testing default policy.
		if shouldCheckFormats(request.CheckFormats, request.AllowUnplayableFormats) {
			checker := newFormatAvailabilityChecker(ctx, transport, request.CheckFormats)
			operation.formatAvailability = checker
			operation.formatAvailabilityChecker = checker
		}
		result, runErr = operation.process(ctx, request.URL, request.PluginID, nil, make(map[string]bool), 0)
	}
	if runErr != nil && request.LoadInfoJSON != "" && shouldFallbackLoadedInfo(loadedInfo, result, runErr) {
		webpageURL, _ := loadedInfo.Lookup("webpage_url").StringValue()
		if err := client.emit(ctx, Event{Kind: EventMetadataWarning, Message: "loaded info direct URL failed; retrying its webpage URL"}); err != nil {
			return result, &Error{Category: ErrorInternal, Op: "emit loaded-info fallback warning", Err: err}
		}
		result, runErr = operation.process(ctx, webpageURL, "", nil, make(map[string]bool), 0)
	}
	if accepted := operation.autonumberCount(); result.AutonumberCount < accepted {
		result.AutonumberCount = accepted
	}
	if runErr != nil {
		// Selected attempts are part of yt-dlp's shared _num_downloads budget
		// even when their download/post-processing path returns an error. Some
		// nested error paths cannot return a partial playlist Result, so carry
		// the operation-wide accounting and stop state alongside the original
		// categorized error.
		if result.Downloads < operation.downloadCount() {
			result.Downloads = operation.downloadCount()
		}
		stopped, stopKind, stopReason := operation.stopState()
		if stopped && !result.Stopped {
			result.Stopped = true
			result.StopKind = stopKind
			result.StopReason = stopReason
		}
	}
	return result, runErr
}

func shouldFallbackLoadedInfo(info value.Info, result Result, runErr error) bool {
	if runErr == nil || result.Downloaded || len(result.Artifacts) > 0 ||
		(!IsCategory(runErr, ErrorNetwork) && !IsCategory(runErr, ErrorInternal) && !IsCategory(runErr, ErrorUnsupported)) {
		return false
	}
	directURL, directOK := info.Lookup("url").StringValue()
	webpageURL, webpageOK := info.Lookup("webpage_url").StringValue()
	return directOK && directURL != "" && webpageOK && webpageURL != "" && webpageURL != directURL
}

func shouldCheckFormats(mode FormatCheckMode, allowUnplayable bool) bool {
	return mode != FormatCheckNone && (mode != FormatCheckAuto || !allowUnplayable)
}

const (
	maxPlaylistDepth   = 8
	maxPlaylistEntries = 10_000
)

type operation struct {
	client                           *Client
	request                          Request
	transport                        *network.Client
	registry                         providerapi.Runtime[compositionState]
	routingSearchQuery               string
	solver                           providerapi.ChallengeSolver
	archive                          *archive.Store
	cache                            *cache.Store
	credentials                      CredentialProvider
	compatibility                    compatibilityPlan
	rootExtractor                    *string
	playlistItemsRangeWarningEmitted bool
	playlistOrderingWarningsEmitted  map[string]bool
	// stopTriggered is the operation-wide stopping state. Once set, every
	// subsequent result reports Stopped with stopKind/stopReason.
	stopTriggered bool
	stopKind      StopKind
	stopReason    string
	// downloads counts media entries that passed selection and entered the
	// download pipeline during this Run, mirroring yt-dlp's _num_downloads.
	downloads                 int
	removeFile                func(string) error
	thumbnailConvert          thumbnailConvertFunc
	thumbnailEmbed            thumbnailEmbedFunc
	hlsFallback               func(context.Context, string, string, string, http.Header, bool, events.Sink) (fragment.Result, error)
	youtubeLiveRefresh        func(mediaformat.Selection) youtubelive.LiveRefreshFunc
	youtubeDirectExtract      func(context.Context, string) (Extraction, error)
	sabrMerge                 func(ctx context.Context, video, audio, destination string, overwrite bool, sink events.Sink) error
	plannerCapabilities       *mediaformat.PlannerCapabilities
	formatAvailability        mediaformat.FormatAvailability
	formatAvailabilityChecker *formatAvailabilityChecker
	extractorRetryWait        extractorRetryWaitFunc
	autonumberNext            int
	autonumberMu              sync.Mutex
	stateMu                   sync.Mutex
}

func (operation *operation) setStop(kind StopKind, reason string) {
	operation.stateMu.Lock()
	defer operation.stateMu.Unlock()
	operation.setStopLocked(kind, reason)
}

func (operation *operation) setStopLocked(kind StopKind, reason string) {
	operation.stopTriggered = true
	operation.stopKind = kind
	operation.stopReason = reason
}

// admitDownload reserves one selected media attempt, or atomically rejects a
// later attempt once the operation-wide maximum has been reserved.
func (operation *operation) admitDownload() bool {
	operation.stateMu.Lock()
	defer operation.stateMu.Unlock()
	if operation.request.MaxDownloads > 0 && operation.downloads >= operation.request.MaxDownloads {
		operation.setStopLocked(StopMaxDownloads, "max downloads reached")
		return false
	}
	operation.downloads++
	return true
}

func (operation *operation) downloadCount() int {
	operation.stateMu.Lock()
	defer operation.stateMu.Unlock()
	return operation.downloads
}

func (operation *operation) finishAdmittedDownload() (bool, StopKind, string) {
	operation.stateMu.Lock()
	defer operation.stateMu.Unlock()
	if operation.request.MaxDownloads > 0 && operation.downloads >= operation.request.MaxDownloads {
		operation.setStopLocked(StopMaxDownloads, "max downloads reached")
		return true, operation.stopKind, operation.stopReason
	}
	return false, StopNone, ""
}

func (operation *operation) stopState() (bool, StopKind, string) {
	operation.stateMu.Lock()
	defer operation.stateMu.Unlock()
	return operation.stopTriggered, operation.stopKind, operation.stopReason
}

func (operation *operation) setRootExtractor(name string) {
	operation.stateMu.Lock()
	defer operation.stateMu.Unlock()
	if operation.rootExtractor != nil {
		*operation.rootExtractor = name
	}
}

func (operation *operation) process(ctx context.Context, rawURL, extractorKey string, overlay *Entry, ancestors map[string]bool, depth int) (Result, error) {
	return operation.processWithTransparentParent(ctx, rawURL, extractorKey, overlay, ancestors, depth, value.Info{})
}

// providerRequest is the product-to-provider adaptation boundary. It retains
// the legacy request shape while assigning neutral and provider-specific state
// to typed adapters that focused compositions can consume later.
func (operation *operation) providerRequest(rawURL, referer, searchQueryOverride string) providerapi.Operation {
	return providerapi.Operation{
		Request: providerapi.Request{
			URL: rawURL, SearchQueryOverride: searchQueryOverride, Referer: referer,
			Transport: operation.transport, Credentials: operation.credentials,
			VideoPassword: operation.request.VideoPassword, NoPlaylist: operation.request.Playlist.Disabled,
		},
		ChallengeSolver: newObservingChallengeSolver(operation.solver, operation.client),
		POTResolver:     operation.client.potResolver,
	}
}

func (operation *operation) newPOTEpisodeResolver() providerapi.POTEpisodeResolver {
	if operation == nil || operation.client == nil || operation.client.potResolver == nil {
		return nil
	}
	return operation.client.potResolver.NewEpisodeResolver()
}

func (operation *operation) extractProviderSource(ctx context.Context, rawURL string) (Extraction, error) {
	if operation == nil || operation.registry == nil {
		return Extraction{}, ErrUnsupported
	}
	selected, err := operation.registry.Select(rawURL)
	if err != nil {
		return Extraction{}, err
	}
	result, err := selected.Extract(ctx, operation.providerRequest(rawURL, "", ""), operation.client.compositionState(operation.request))
	return result, operation.classifyProviderError(err)
}

func (operation *operation) processWithTransparentParent(ctx context.Context, rawURL, extractorKey string, overlay *Entry, ancestors map[string]bool, depth int, transparentParent value.Info) (Result, error) {
	referer := ""
	if overlay != nil {
		referer = overlay.Referer
	}
	if err := ctx.Err(); err != nil {
		return Result{}, categorized("process extraction", err)
	}
	if depth > maxPlaylistDepth || ancestors[rawURL] {
		return Result{}, categorized("expand playlist", ErrPlaylistLimit)
	}
	ancestors[rawURL] = true
	defer delete(ancestors, rawURL)

	selected, err := operation.selectExtractor(rawURL, extractorKey)
	if err != nil {
		return Result{}, categorized("select extractor", err)
	}
	if depth == 0 {
		operation.setRootExtractor(selected.Name())
	}
	eventURL := network.RedactRawURL(rawURL)
	if err := operation.client.emit(ctx, Event{Kind: string(events.KindExtracting), Extractor: selected.Name(), URL: eventURL}); err != nil {
		return Result{}, &Error{Category: ErrorInternal, Op: "emit extracting event", Err: err}
	}
	searchQueryOverride := ""
	if depth == 0 {
		searchQueryOverride = operation.routingSearchQuery
	}
	extracted, err := operation.extractWithRetry(ctx, selected, operation.providerRequest(rawURL, referer, searchQueryOverride), eventURL)
	if err != nil {
		return Result{}, categorized(selected.Name()+" extraction", err)
	}
	if overlay != nil && overlay.Transparent {
		info := value.NewInfo(extracted.Info.Fields().Clone())
		childID, _ := info.Lookup("id").StringValue()
		if transparentParent.Fields().Len() > 0 {
			info.Fields().Merge(transparentParent.Fields(), true)
			if childID != "" {
				info.Set("id", value.String(childID))
			}
		}
		applyTransparentOverlay(&info, overlay)
		extracted.Info = info
	}
	if err := operation.client.emit(ctx, Event{Kind: string(events.KindExtracted), Extractor: selected.Name(), URL: eventURL}); err != nil {
		return Result{}, &Error{Category: ErrorInternal, Op: "emit extracted event", Err: err}
	}
	if extracted.IsURL() {
		entry := *extracted.Redirect
		if overlay != nil && overlay.Transparent {
			overlayOntoEntry(&entry, overlay)
			entry.Transparent = true
		}
		nextParent := transparentParent
		if extracted.Info.Fields().Len() > 0 {
			nextParent = extracted.Info
		}
		return operation.processWithTransparentParent(ctx, entry.URL, entry.ExtractorKey, &entry, ancestors, depth+1, nextParent)
	}
	if extracted.IsPlaylist() {
		return operation.processPlaylist(ctx, extracted, selected.Name(), ancestors, depth)
	}
	return operation.processMedia(ctx, extracted, selected.Name())
}

// selectExtractor preserves explicit URL-result keys while applying the
// request-level generic forcing policy only to automatic routing. The generic
// candidate is selected from the same registry used by normal product runs.
func (operation *operation) selectExtractor(rawURL, extractorKey string) (providerapi.Selected[compositionState], error) {
	if extractorKey == "" && operation.request.ForceGenericExtractor && operation.request.PluginID == "" {
		return operation.registry.SelectFor(rawURL, "generic")
	}
	return operation.registry.SelectFor(rawURL, extractorKey)
}

// applyTransparentOverlay writes every supported producer-side metadata field
// from a transparent overlay onto an info object. The producer's ID
// overrides whatever the child supplied, producer values override every
// other field when set, and Has* flags preserve explicit zero numeric
// values across the recursion step.
func applyTransparentOverlay(info *value.Info, overlay *Entry) {
	if overlay == nil || info == nil {
		return
	}
	if overlay.ID != "" {
		info.Set("id", value.String(overlay.ID))
	}
	if overlay.Title != "" {
		info.Set("title", value.String(overlay.Title))
	}
	if overlay.Thumbnail != "" {
		info.Set("thumbnail", value.String(overlay.Thumbnail))
	}
	if overlay.HasDuration {
		info.Set("duration", value.Float(overlay.Duration))
	}
	if overlay.HasTimestamp {
		info.Set("timestamp", value.Int(overlay.Timestamp))
	}
	if overlay.Availability != "" {
		info.Set("availability", value.String(overlay.Availability))
	}
	if overlay.HasViewCount {
		info.Set("view_count", value.Int(overlay.ViewCount))
	}
	if overlay.Language != "" {
		info.Set("language", value.String(overlay.Language))
	}
	if overlay.SeriesID != "" {
		info.Set("series_id", value.String(overlay.SeriesID))
	}
	if overlay.Series != "" {
		info.Set("series", value.String(overlay.Series))
	}
}

// overlayOntoEntry transfers a transparent overlay's metadata onto an Entry
// that will be the next URL result. Routing fields (URL, ExtractorKey,
// Referer) are intentionally never overwritten.
func overlayOntoEntry(entry *Entry, overlay *Entry) {
	if entry == nil || overlay == nil {
		return
	}
	if overlay.ID != "" {
		entry.ID = overlay.ID
	}
	if overlay.Title != "" {
		entry.Title = overlay.Title
	}
	if overlay.Thumbnail != "" {
		entry.Thumbnail = overlay.Thumbnail
	}
	if overlay.HasDuration {
		entry.Duration = overlay.Duration
		entry.HasDuration = true
	}
	if overlay.HasTimestamp {
		entry.Timestamp = overlay.Timestamp
		entry.HasTimestamp = true
	}
	if overlay.Availability != "" {
		entry.Availability = overlay.Availability
	}
	if overlay.HasViewCount {
		entry.ViewCount = overlay.ViewCount
		entry.HasViewCount = true
	}
	if overlay.Language != "" {
		entry.Language = overlay.Language
	}
	if overlay.SeriesID != "" {
		entry.SeriesID = overlay.SeriesID
	}
	if overlay.Series != "" {
		entry.Series = overlay.Series
	}
}

func (operation *operation) processPlaylist(ctx context.Context, extracted Extraction, extractorName string, ancestors map[string]bool, depth int) (Result, error) {
	if err := operation.validatePrintRules(ctx, extracted.Info, nil, nil, "", true); err != nil {
		return Result{}, categorized("validate playlist print", err)
	}
	if err := operation.emitPlaylistItemsRangeWarning(ctx); err != nil {
		return Result{}, err
	}
	iterator, err := newPlaylistEntryIterator(extracted.Entries.Iterator(), operation.request.Playlist)
	if err != nil {
		return Result{}, categorized(extractorName+" playlist selection", fmt.Errorf("%w: %w", errInvalidRequestOptions, err))
	}
	options, orderingWarnings := normalizedPlaylistExecutionOptions(operation.request.Playlist)
	if err := operation.emitPlaylistOrderingWarnings(ctx, orderingWarnings); err != nil {
		return Result{}, err
	}
	var materialized []indexedPlaylistEntry
	switch {
	case options.Lazy:
		// Lazy mode never pre-materializes; Reverse and Random are ignored.
	case !options.Lazy && options.Reverse:
		for {
			entry, ok, iterErr := iterator.Next(ctx)
			if iterErr != nil {
				return Result{}, categorized(extractorName+" playlist iteration", iterErr)
			}
			if !ok {
				break
			}
			materialized = append(materialized, entry)
		}
		reversePlaylistEntries(materialized)
	case !options.Lazy && options.Random:
		for {
			entry, ok, iterErr := iterator.Next(ctx)
			if iterErr != nil {
				return Result{}, categorized(extractorName+" playlist iteration", iterErr)
			}
			if !ok {
				break
			}
			materialized = append(materialized, entry)
		}
		shufflePlaylistEntries(materialized, options.RandomSource)
	}
	children := make([]Result, 0)
	entryValues := make([]value.Value, 0)
	autonumberBefore := operation.autonumberPosition()
	playlistID, _ := extracted.Info.ID()
	playlistTitle, _ := extracted.Info.Title()
	var failures int
	var failedDownloads int
	var maxFailuresEmitted bool
	var suppressedPrints []PrintOutput
	var suppressedArtifacts []Artifact
	var suppressedBytes int64
	finish := func() (Result, error) {
		result, err := operation.finishPlaylistResult(ctx, extracted.Info, extractorName, children, entryValues)
		if err != nil {
			return result, err
		}
		concatArtifact, concatErr := operation.concatPlaylist(ctx, extracted.Info, children, len(children)+failures)
		if concatErr != nil {
			return Result{}, categorized("concat playlist", concatErr)
		}
		if concatArtifact.Path != "" {
			info, statErr := os.Stat(concatArtifact.Path)
			if statErr != nil {
				return Result{}, categorized("account concat playlist", statErr)
			}
			result.Artifacts = append(result.Artifacts, concatArtifact)
			result.Bytes += info.Size()
			result.Downloaded = true
			result.Filename = concatArtifact.Path
		}
		result.SuppressedFailures += failures
		result.Downloads += failedDownloads
		if consumed := operation.autonumberCountSince(autonumberBefore); result.AutonumberCount < consumed {
			result.AutonumberCount = consumed
		}
		result.Prints = append(suppressedPrints, result.Prints...)
		result.Artifacts = append(suppressedArtifacts, result.Artifacts...)
		result.Bytes += suppressedBytes
		return result, err
	}
	for outputIndex := 0; ; outputIndex++ {
		var selected indexedPlaylistEntry
		var ok bool
		if materialized != nil {
			if outputIndex < len(materialized) {
				selected, ok = materialized[outputIndex], true
			}
		} else {
			var err error
			selected, ok, err = iterator.Next(ctx)
			if err != nil {
				return Result{}, categorized(extractorName+" playlist iteration", err)
			}
		}
		if !ok {
			return finish()
		}
		entry := selected.Entry
		if entry.URL == "" {
			entryErr := categorized(extractorName+" playlist entry", ErrInvalidPlaylist)
			handled, stop, handlerErr := operation.handlePlaylistEntryError(ctx, extractorName, playlistTitle, selected.SourceIndex, entryErr, &failures, &maxFailuresEmitted)
			if handlerErr != nil {
				return Result{}, handlerErr
			}
			if handled {
				if stop {
					return finish()
				}
				continue
			}
			return Result{}, entryErr
		}
		if options.Flat {
			entryInfo := flatPlaylistEntryInfo(entry, selected.SourceIndex, playlistID, playlistTitle)
			child, archiveIdentity, _, terminal, err := operation.prepareMediaResult(ctx, &entryInfo, entry.ExtractorKey, true)
			if err != nil {
				handled, stop, handlerErr := operation.handlePlaylistEntryError(ctx, extractorName, playlistTitle, selected.SourceIndex, err, &failures, &maxFailuresEmitted)
				if handlerErr != nil {
					return Result{}, handlerErr
				}
				if handled {
					if stop {
						return finish()
					}
					continue
				}
				return Result{}, fmt.Errorf("flat playlist entry %d: %w", selected.SourceIndex, err)
			}
			if child.Stopped {
				return finish()
			}
			if !terminal {
				if err := operation.recordForcedArchive(ctx, archiveIdentity); err != nil {
					handled, stop, handlerErr := operation.handlePlaylistEntryError(ctx, extractorName, playlistTitle, selected.SourceIndex, err, &failures, &maxFailuresEmitted)
					if handlerErr != nil {
						return Result{}, handlerErr
					}
					if handled {
						if stop {
							return finish()
						}
						continue
					}
					return Result{}, fmt.Errorf("flat playlist entry %d archive: %w", selected.SourceIndex, err)
				}
				operation.addAutonumber(&entryInfo)
				child.AutonumberCount = 1
				child.InfoJSON, err = encodeInfo(entryInfo)
				if err != nil {
					return Result{}, err
				}
			}
			if !child.Skipped {
				printInfo := entryInfo
				if terminal {
					printInfo = operation.provisionalAutonumberInfo(printInfo)
				}
				prints, printErr := operation.capturePrints(ctx, PrintVideo, printInfo, nil, nil, "")
				if printErr != nil {
					return Result{}, fmt.Errorf("flat playlist entry %d print: %w", selected.SourceIndex, printErr)
				}
				child.Prints = append(child.Prints, prints...)
				printArtifacts, printBytes, printErr := operation.writePrintFiles(ctx, PrintVideo, printInfo, nil, nil, "")
				if printErr != nil {
					return Result{}, fmt.Errorf("flat playlist entry %d print file: %w", selected.SourceIndex, printErr)
				}
				addPrintFileArtifacts(&child, printArtifacts, printBytes)
			}
			children = append(children, child)
			entryValues = append(entryValues, value.ObjectValue(entryInfo.Fields()))
			continue
		}
		downloadsBefore := operation.downloadCount()
		child, err := operation.process(ctx, entry.URL, entry.ExtractorKey, &entry, ancestors, depth+1)
		if err != nil {
			suppressedPrints = append(suppressedPrints, child.Prints...)
			suppressedArtifacts = append(suppressedArtifacts, child.Artifacts...)
			suppressedBytes += child.Bytes
			failedDownloads += operation.downloadCount() - downloadsBefore
			handled, stop, handlerErr := operation.handlePlaylistEntryError(ctx, extractorName, playlistTitle, selected.SourceIndex, err, &failures, &maxFailuresEmitted)
			if handlerErr != nil {
				return Result{}, handlerErr
			}
			if handled {
				if stop {
					return finish()
				}
				continue
			}
			return Result{}, fmt.Errorf("playlist entry %d: %w", selected.SourceIndex, err)
		}
		if child.Stopped {
			if !child.Skipped {
				entryValue, err := playlistEntryValue(child.InfoJSON, selected.SourceIndex, playlistID, playlistTitle)
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
			return finish()
		}
		entryValue, err := playlistEntryValue(child.InfoJSON, selected.SourceIndex, playlistID, playlistTitle)
		if err != nil {
			return Result{}, err
		}
		child.InfoJSON, err = entryValue.MarshalJSON()
		if err != nil {
			return Result{}, &Error{Category: ErrorInternal, Op: "encode playlist entry metadata", Err: err}
		}
		children = append(children, child)
		// processMedia consumes one autonumber slot for every accepted child.
		entryValues = append(entryValues, entryValue)
	}
}

func (operation *operation) finishPlaylistResult(
	ctx context.Context,
	playlistInfo value.Info,
	extractorName string,
	children []Result,
	entryValues []value.Value,
) (Result, error) {
	info := value.NewInfo(playlistInfo.Fields().Clone())
	info.Set("entries", value.List(entryValues...))
	if operation.request.Thumbnails.List {
		if _, err := selectThumbnails(&info); err != nil {
			return Result{}, categorized("normalize playlist thumbnails", err)
		}
	}
	var thumbnailArtifacts []Artifact
	var thumbnailBytes int64
	if !operation.request.Simulate && !operation.request.RelatedFiles.NoPlaylist {
		var err error
		thumbnailArtifacts, thumbnailBytes, err = operation.writeThumbnails(ctx, &info, true)
		if err != nil {
			return Result{}, categorized("write playlist thumbnails", err)
		}
	}
	encoded, err := encodeInfo(info)
	if err != nil {
		return Result{}, err
	}
	result := Result{InfoJSON: encoded, Extractor: extractorName, Entries: children}
	result.Stopped, result.StopKind, result.StopReason = operation.stopState()
	result.Artifacts = append(result.Artifacts, thumbnailArtifacts...)
	result.Bytes += thumbnailBytes
	result.Downloaded = len(thumbnailArtifacts) > 0
	for _, child := range children {
		result.Bytes += child.Bytes
		result.Downloaded = result.Downloaded || child.Downloaded
		result.Archived = result.Archived || child.Archived
		result.Downloads += child.Downloads
		result.SuppressedFailures += child.SuppressedFailures
		result.AutonumberCount += child.AutonumberCount
	}
	if !operation.request.Simulate && !operation.request.RelatedFiles.NoPlaylist {
		artifacts, artifactBytes, err := operation.writeRelatedFiles(ctx, info, true)
		if err != nil {
			return Result{}, categorized("write playlist related files", err)
		}
		result.Artifacts = append(result.Artifacts, artifacts...)
		result.Bytes += artifactBytes
		result.Downloaded = result.Downloaded || len(artifacts) > 0
	}
	printArtifacts, printBytes, err := operation.writePrintFiles(ctx, PrintPlaylist, info, nil, nil, "")
	if err != nil {
		return Result{}, categorized("write playlist print file", err)
	}
	addPrintFileArtifacts(&result, printArtifacts, printBytes)
	prints, err := operation.capturePrints(ctx, PrintPlaylist, info, nil, nil, "")
	if err != nil {
		return Result{}, categorized("render playlist print", err)
	}
	result.Prints = append(result.Prints, prints...)
	return result, nil
}

func flatPlaylistEntryInfo(entry Entry, index int, playlistID, playlistTitle string) value.Info {
	object := entry.Object()
	addPlaylistEntryFields(object, index, playlistID, playlistTitle)
	return value.NewInfo(object)
}

func addPlaylistEntryFields(object *value.Object, index int, playlistID, playlistTitle string) {
	object.Set("playlist_index", value.Int(int64(index)))
	if playlistID != "" {
		object.Set("playlist_id", value.String(playlistID))
	}
	if playlistTitle != "" {
		object.Set("playlist_title", value.String(playlistTitle))
	}
}

func (operation *operation) emitPlaylistItemsRangeWarning(ctx context.Context) error {
	if !playlistItemsOverrideRange(operation.request.Playlist) {
		return nil
	}
	operation.stateMu.Lock()
	if operation.playlistItemsRangeWarningEmitted {
		operation.stateMu.Unlock()
		return nil
	}
	operation.playlistItemsRangeWarningEmitted = true
	operation.stateMu.Unlock()
	if err := operation.client.emit(ctx, Event{
		Kind: EventMetadataWarning, Message: "playlist items override playlist start and end",
	}); err != nil {
		return &Error{Category: ErrorInternal, Op: "emit playlist selection warning", Err: err}
	}
	return nil
}

func (operation *operation) emitPlaylistOrderingWarnings(ctx context.Context, warnings []string) error {
	if len(warnings) == 0 {
		return nil
	}
	for _, warning := range warnings {
		operation.stateMu.Lock()
		if operation.playlistOrderingWarningsEmitted == nil {
			operation.playlistOrderingWarningsEmitted = make(map[string]bool, len(warnings))
		}
		if operation.playlistOrderingWarningsEmitted[warning] {
			operation.stateMu.Unlock()
			continue
		}
		operation.playlistOrderingWarningsEmitted[warning] = true
		operation.stateMu.Unlock()
		if err := operation.client.emit(ctx, Event{Kind: EventMetadataWarning, Message: warning}); err != nil {
			return &Error{Category: ErrorInternal, Op: "emit playlist ordering warning", Err: err}
		}
	}
	return nil
}

// handlePlaylistEntryError decides whether the supplied per-entry error is
// recorded and skipped (handled=true), stops the playlist when MaxFailures
// is reached (stop=true), or must be propagated by the caller (handled=false).
// A non-nil handlerErr always surfaces to the caller; it represents an
// event-sink failure rather than the entry error itself.
func (operation *operation) handlePlaylistEntryError(
	ctx context.Context,
	extractorName, playlistTitle string,
	sourceIndex int,
	err error,
	failures *int,
	maxFailuresEmitted *bool,
) (handled, stop bool, handlerErr error) {
	if isPlaylistErrorNonOverridable(err) {
		return false, false, nil
	}
	if operation.request.Playlist.ErrorPolicy == PlaylistErrorAbort {
		return false, false, nil
	}
	*failures++
	if emitErr := emitPlaylistEntryError(ctx, operation.client, extractorName, sourceIndex, err); emitErr != nil {
		return true, false, emitErr
	}
	if max := operation.request.Playlist.MaxFailures; max > 0 && *failures >= max {
		if !*maxFailuresEmitted {
			if emitErr := emitPlaylistMaxFailuresReached(ctx, operation.client, extractorName, playlistTitle, *failures); emitErr != nil {
				return true, false, emitErr
			}
			*maxFailuresEmitted = true
		}
		return true, true, nil
	}
	return true, false, nil
}

type indexedPlaylistEntry struct {
	Entry       Entry
	SourceIndex int
}

type indexedPlaylistEntryIterator interface {
	Next(context.Context) (indexedPlaylistEntry, bool, error)
}

func newPlaylistEntryIterator(source EntryIterator, options PlaylistOptions) (indexedPlaylistEntryIterator, error) {
	if options.Items == "" {
		return newSelectedPlaylistIterator(source, options), nil
	}
	specs, err := parsePlaylistItems(options.Items)
	if err != nil {
		return nil, err
	}
	return &playlistItemsIterator{source: source, specs: specs}, nil
}

type selectedPlaylistIterator struct {
	source      EntryIterator
	start       int
	end         int
	sourceIndex int
	done        bool
}

func newSelectedPlaylistIterator(source EntryIterator, options PlaylistOptions) *selectedPlaylistIterator {
	start, end := normalizedPlaylistRange(options)
	return &selectedPlaylistIterator{source: source, start: start, end: end}
}

func (iterator *selectedPlaylistIterator) Next(ctx context.Context) (indexedPlaylistEntry, bool, error) {
	if err := ctx.Err(); err != nil {
		iterator.done = true
		return indexedPlaylistEntry{}, false, err
	}
	if iterator.done {
		return indexedPlaylistEntry{}, false, nil
	}
	for {
		if iterator.end != 0 && iterator.sourceIndex >= iterator.end {
			iterator.done = true
			return indexedPlaylistEntry{}, false, nil
		}
		entry, ok, err := iterator.source.Next(ctx)
		if err != nil {
			iterator.done = true
			return indexedPlaylistEntry{}, false, err
		}
		if !ok {
			iterator.done = true
			return indexedPlaylistEntry{}, false, nil
		}
		iterator.sourceIndex++
		if iterator.sourceIndex > maxPlaylistEntries {
			iterator.done = true
			return indexedPlaylistEntry{}, false, ErrPlaylistLimit
		}
		if iterator.sourceIndex < iterator.start {
			continue
		}
		return indexedPlaylistEntry{Entry: entry, SourceIndex: iterator.sourceIndex}, true, nil
	}
}

func playlistEntryValue(encoded json.RawMessage, index int, playlistID, playlistTitle string) (value.Value, error) {
	var entry value.Value
	if err := json.Unmarshal(encoded, &entry); err != nil {
		return value.Missing(), &Error{Category: ErrorInternal, Op: "decode playlist entry metadata", Err: err}
	}
	object, ok := entry.Object()
	if !ok {
		return value.Missing(), &Error{Category: ErrorInternal, Op: "decode playlist entry metadata", Err: ErrInvalidMetadata}
	}
	addPlaylistEntryFields(object, index, playlistID, playlistTitle)
	return value.ObjectValue(object), nil
}

func encodeInfo(info value.Info) (json.RawMessage, error) {
	encoded, err := json.Marshal(value.ObjectValue(info.Fields()))
	if err != nil {
		return nil, &Error{Category: ErrorInternal, Op: "encode metadata", Err: err}
	}
	return encoded, nil
}

func (operation *operation) prepareMediaResult(
	ctx context.Context,
	info *value.Info,
	extractorName string,
	incomplete bool,
) (Result, archive.Identity, compatibilityDecision, bool, error) {
	var archiveIdentity archive.Identity
	if operation.archive != nil {
		id, hasID := info.ID()
		archiveExtractor := archiveExtractorKey(*info, extractorName)
		if hasID && archiveExtractor != "" {
			identity, err := archive.NewIdentity(archiveExtractor, id)
			if err != nil {
				return Result{}, archive.Identity{}, compatibilityDecision{}, false, categorized("build archive identity", err)
			}
			legacyIDs, err := oldArchiveIDs(*info)
			if err != nil {
				return Result{}, archive.Identity{}, compatibilityDecision{}, false, categorized("read legacy archive identities", err)
			}
			matched, found, err := operation.archive.Match(ctx, identity, legacyIDs)
			if err != nil {
				return Result{}, archive.Identity{}, compatibilityDecision{}, false, categorized("match download archive", err)
			}
			archiveIdentity = identity
			if found {
				// Archive matches take precedence over every filter check,
				// mirroring _match_entry. --break-on-existing stops the run;
				// otherwise the archived entry is skipped without a download.
				encoded, err := encodeInfo(*info)
				if err != nil {
					return Result{}, archive.Identity{}, compatibilityDecision{}, false, err
				}
				result := Result{InfoJSON: encoded, Extractor: extractorName, Archived: true}
				if err := operation.client.emit(ctx, Event{
					Kind: EventArchiveMatch, Extractor: extractorName, Message: matched,
				}); err != nil {
					return Result{}, archive.Identity{}, compatibilityDecision{}, false, &Error{
						Category: ErrorInternal, Op: "emit archive event", Err: err,
					}
				}
				terminal := true
				if operation.request.BreakOnExisting {
					reason := archiveRejectionReason(*info)
					operation.setStop(StopBreakOnExisting, reason)
					_, result.StopKind, result.StopReason = operation.stopState()
					result.Stopped = true
				}
				return result, archiveIdentity, compatibilityDecision{}, terminal, nil
			}
		}
	}
	decision, err := operation.applyCompatibility(ctx, info, incomplete)
	if err != nil {
		return Result{}, archive.Identity{}, compatibilityDecision{}, false, err
	}
	encoded, err := encodeInfo(*info)
	if err != nil {
		return Result{}, archive.Identity{}, compatibilityDecision{}, false, err
	}
	result := Result{InfoJSON: encoded, Extractor: extractorName}
	if !decision.Pass {
		terminal, err := operation.finishMatchFilterDecision(ctx, &result, extractorName, decision.Decision)
		if err != nil {
			return Result{}, archive.Identity{}, compatibilityDecision{}, false, err
		}
		return result, archiveIdentity, decision, terminal, nil
	}
	if operation.archive != nil {
		_, hasID := info.ID()
		if !hasID || extractorName == "" {
			if incomplete {
				return result, archive.Identity{}, compatibilityDecision{}, false, nil
			}
			return Result{}, archive.Identity{}, compatibilityDecision{}, false, categorized("build archive identity", archive.ErrInvalidIdentity)
		}
	}
	if err := operation.applyMetadataActions(ctx, info); err != nil {
		return Result{}, archive.Identity{}, compatibilityDecision{}, false, err
	}
	result.InfoJSON, err = encodeInfo(*info)
	if err != nil {
		return Result{}, archive.Identity{}, compatibilityDecision{}, false, err
	}
	return result, archiveIdentity, decision, false, nil
}

func archiveExtractorKey(info value.Info, fallback string) string {
	for _, field := range []string{"extractor_key", "ie_key"} {
		if key, ok := info.Lookup(field).StringValue(); ok && key != "" {
			return key
		}
	}
	if fallback == "loaded-info-json" {
		return ""
	}
	return fallback
}

// archiveRejectionReason mirrors the reference rejection message
// "<id>: <title> has already been recorded in the archive".
func archiveRejectionReason(info value.Info) string {
	reason := ""
	if id, ok := info.ID(); ok && id != "" {
		reason += id + ": "
	}
	if title, ok := info.Title(); ok && title != "" {
		reason += title + " "
	}
	return reason + "has already been recorded in the archive"
}

func (operation *operation) finishMatchFilterDecision(
	ctx context.Context,
	result *Result,
	extractorName string,
	decision matchfilter.Decision,
) (bool, error) {
	if decision.Pass {
		return false, nil
	}
	result.Skipped, result.SkipReason = true, decision.Reason
	if stopped, stopKind, stopReason := operation.stopState(); stopped {
		result.Stopped, result.StopKind, result.StopReason = true, stopKind, stopReason
	} else if operation.request.BreakOnReject {
		// --break-on-reject stops on any rejection, including simple
		// filters and ordinary --match-filters (reference break_on_reject).
		operation.setStop(StopBreakOnReject, decision.Reason)
		_, result.StopKind, result.StopReason = operation.stopState()
		result.Stopped = true
	}
	if err := operation.client.emit(ctx, Event{
		Kind: EventMatchFilterSkipped, Extractor: extractorName, Message: decision.Reason,
	}); err != nil {
		return false, &Error{
			Category: ErrorInternal, Op: "emit match-filter skip", Err: err,
		}
	}
	return true, nil
}

func (operation *operation) processMedia(ctx context.Context, extracted Extraction, extractorName string) (result Result, _ error) {
	ctx = withHLSInitialPlaylistCache(ctx)
	// counted tracks whether this entry passed selection and entered the
	// download pipeline. Admission reserves the operation-wide download slot
	// atomically; the defer applies the reference _num_downloads semantics by
	// stopping only after an admitted Nth entry finishes.
	counted := false
	defer func() {
		if counted {
			result.Downloads = 1
			if !result.Stopped {
				if reached, stopKind, stopReason := operation.finishAdmittedDownload(); reached {
					result.StopKind, result.StopReason = stopKind, stopReason
					result.Stopped = true
				}
			}
		}
	}()
	preparedFormats, err := mediaformat.Prepare(extracted.Info, operation.compatibility.formatOptions)
	if err != nil {
		return result, categorized("normalize formats", err)
	}
	info := preparedFormats.Info()
	if operation.request.Thumbnails.List {
		if _, err := selectThumbnails(&info); err != nil {
			return result, categorized("normalize thumbnails", err)
		}
	}
	preProcessInfo := operation.provisionalAutonumberInfo(info)
	preProcessPrints, err := operation.capturePrints(ctx, PrintPreProcess, preProcessInfo, nil, nil, "")
	if err != nil {
		return result, categorized("render pre-process print", err)
	}
	preliminary := Result{Prints: append([]PrintOutput(nil), preProcessPrints...)}
	preparedResult, archiveIdentity, interactiveDecision, terminal, err := operation.prepareMediaResult(ctx, &info, extractorName, false)
	if err != nil {
		return preliminary, err
	}
	result = preparedResult
	result.Prints = append(result.Prints, preProcessPrints...)
	if terminal {
		preProcessArtifacts, preProcessBytes, printErr := operation.writePrintFiles(ctx, PrintPreProcess, preProcessInfo, nil, nil, "")
		if printErr != nil {
			return result, categorized("write pre-process print file", printErr)
		}
		addPrintFileArtifacts(&result, preProcessArtifacts, preProcessBytes)
		if !result.Skipped {
			afterFilterInfo := operation.provisionalAutonumberInfo(info)
			prints, printErr := operation.capturePrints(ctx, PrintAfterFilter, afterFilterInfo, nil, nil, "")
			if printErr != nil {
				return result, categorized("render after-filter print", printErr)
			}
			result.Prints = append(result.Prints, prints...)
			printArtifacts, printBytes, printErr := operation.writePrintFiles(ctx, PrintAfterFilter, afterFilterInfo, nil, nil, "")
			if printErr != nil {
				return result, categorized("write after-filter print file", printErr)
			}
			addPrintFileArtifacts(&result, printArtifacts, printBytes)
		}
		return result, nil
	}
	// The reference increments _num_downloads for every entry that passes
	// selection, before the download attempt; an interactive match prompt is
	// the last selection step, so rejected prompts must not consume budget.
	needsInteractiveFormat := interactiveDecision.interactive != interactiveMatchFilterNone ||
		operation.compatibility.interactiveFormat
	if extracted.Enrich != nil {
		if err := extracted.Enrich(ctx, &info); err != nil {
			return result, categorized(extractorName+" deferred metadata", err)
		}
		result.InfoJSON, err = encodeInfo(info)
		if err != nil {
			return result, err
		}
	}
	if err := operation.enrichWithSponsorBlock(ctx, extractorName, &info); err != nil {
		return result, err
	}
	afterFilterInfo := operation.provisionalAutonumberInfo(info)
	afterFilterPrints, err := operation.capturePrints(ctx, PrintAfterFilter, afterFilterInfo, nil, nil, "")
	if err != nil {
		return result, categorized("render after-filter print", err)
	}
	result.Prints = append(result.Prints, afterFilterPrints...)
	selectedSubtitles, requestedSubtitles, err := selectSubtitles(info, operation.request.Subtitles)
	if err != nil {
		return result, categorized("select subtitles", err)
	}
	if requestedSubtitles != nil {
		info.Set("requested_subtitles", value.ObjectValue(requestedSubtitles))
	}
	result.InfoJSON, err = encodeInfo(info)
	if err != nil {
		return result, err
	}
	// Metadata actions and deferred enrichment mutate the canonical Info after
	// Prepare; rebind evaluation objects so selection matches InfoJSON.
	preparedFormats = preparedFormats.SyncInfo(info)
	var selectedFormats []mediaformat.Selection
	var outputPlans []mediaformat.OutputPlan
	if (!operation.request.SkipDownload && !operation.request.Simulate) ||
		operation.hasPrintStageAtOrAfter(PrintVideo) || needsInteractiveFormat {
		outputPlans, err = operation.planPreparedFormatsContext(ctx, preparedFormats)
		if err != nil {
			return result, categorized("select format", err)
		}
		annotateYouTubeDirectPlans(extractorName, info, outputPlans)
		if operation.request.HLSSplitDiscontinuity || len(operation.request.HLSDiscontinuitySequences) > 0 {
			outputPlans, err = operation.selectHLSDiscontinuityPlans(ctx, outputPlans)
			if err != nil {
				return result, categorized("select HLS discontinuity group", err)
			}
		}
		if len(outputPlans) > 0 {
			selectedFormats = outputPlans[0].Tracks
		}
		if err := validateMultiOutputProduct(operation.request, len(outputPlans)); err != nil {
			return result, categorized("select format", err)
		}
		if err := validateOutputPlans(outputPlans, operation.mergeOutputPreferences()); err != nil {
			return result, categorized("select format", err)
		}
	}
	var singlePrintPlan *mediaformat.OutputPlan
	if len(outputPlans) == 1 && len(outputPlans[0].Tracks) > 1 {
		singlePrintPlan = &outputPlans[0]
	}
	operation.applyThumbnailEmbeddingOutputExtension(&info, selectedFormats)
	if needsInteractiveFormat {
		provisionalDestinations, resolveErr := operation.resolveOutputPlanDestinations(info, outputPlans)
		if resolveErr != nil {
			return result, categorized("render output template", resolveErr)
		}
		if len(provisionalDestinations) == 0 {
			return result, categorized("select format", mediaformat.ErrNoFormats)
		}
		for index, plan := range outputPlans {
			interactiveInfo := selectedPlanInfo(info, plan)
			operation.applyThumbnailEmbeddingOutputExtension(&interactiveInfo, plan.Tracks)
			resolved, resolveErr := operation.resolveInteractiveCompatibility(
				ctx, interactiveInfo, interactiveDecision, provisionalDestinations[index],
			)
			if resolveErr != nil {
				return result, resolveErr
			}
			terminal, finishErr := operation.finishMatchFilterDecision(ctx, &result, extractorName, resolved)
			if finishErr != nil {
				return result, finishErr
			}
			if terminal {
				return result, nil
			}
		}
	}
	if !operation.admitDownload() {
		result.Stopped = true
		result.StopKind = StopMaxDownloads
		result.StopReason = "max downloads reached"
		return result, nil
	}
	operation.addAutonumber(&info)
	result.AutonumberCount = 1
	counted = true
	result.InfoJSON, err = encodeInfo(info)
	if err != nil {
		return result, err
	}
	planDestinations, err := operation.resolveOutputPlanDestinations(info, outputPlans)
	if err != nil {
		return result, categorized("render output template", err)
	}
	if sessionRequestEnabled(operation) {
		var sessionResult Result
		var runErr error
		if len(outputPlans) == 1 && len(outputPlans[0].Tracks) == 1 {
			if err := validateDirectSessionOutput(operation.request, outputPlans, selectedSubtitles); err != nil {
				return result, categorized("validate session output", err)
			}
			sessionRun, sessionErr := operation.newDirectSession(info, extractorName, outputPlans[0].Tracks[0], planDestinations[0])
			if sessionErr != nil {
				return result, categorized("open direct resume session", sessionErr)
			}
			sessionResult, runErr = sessionRun.run(ctx, operation.eventSink())
		} else {
			if err := validateMultiTrackSessionOutput(operation.request, outputPlans, selectedSubtitles); err != nil {
				return result, categorized("validate session output", err)
			}
			sessionRun, sessionErr := newMultiTrackSession(operation, info, extractorName, outputPlans[0].Tracks, planDestinations[0])
			if sessionErr != nil {
				return result, categorized("open multi-track resume session", sessionErr)
			}
			sessionResult, runErr = sessionRun.run(ctx, operation.eventSink())
		}
		if runErr == nil && sessionResult.Downloaded {
			if archiveErr := operation.recordArchive(ctx, archiveIdentity); archiveErr != nil {
				return sessionResult, archiveErr
			}
			sessionResult.Archived = operation.archive != nil
		}
		return sessionResult, runErr
	}
	var destination string
	if len(planDestinations) > 0 {
		destination = planDestinations[0]
	}
	var mediaTx *mediaTransaction
	if len(outputPlans) > 0 {
		mediaTx = newMediaTransaction()
		if !operation.request.Simulate {
			if err := operation.preflightOutputLifecycles(info, outputPlans, planDestinations, selectedSubtitles); err != nil {
				return result, categorized("preflight output destinations", err)
			}
		}
	}
	ctx = withMediaTransaction(ctx, mediaTx)
	preProcessArtifacts, preProcessBytes, err := operation.writePrintFiles(ctx, PrintPreProcess, preProcessInfo, nil, nil, "")
	if err != nil {
		return rollbackTransactionResult(mediaTx, categorized("write pre-process print file", err))
	}
	addPrintFileArtifacts(&result, preProcessArtifacts, preProcessBytes)
	afterFilterArtifacts, afterFilterBytes, err := operation.writePrintFiles(ctx, PrintAfterFilter, afterFilterInfo, nil, nil, "")
	if err != nil {
		return rollbackTransactionResult(mediaTx, categorized("write after-filter print file", err))
	}
	addPrintFileArtifacts(&result, afterFilterArtifacts, afterFilterBytes)
	if len(outputPlans) == 0 {
		if err := operation.validatePrintRules(ctx, info, singlePrintPlan, selectedFormats, destination, false); err != nil {
			return rollbackTransactionResult(mediaTx, categorized("validate print rules", err))
		}
	} else {
		for index, plan := range outputPlans {
			planInfo := selectedPlanInfo(info, plan)
			var printPlan *mediaformat.OutputPlan
			if len(plan.Tracks) > 1 {
				printPlan = &plan
			}
			if err := operation.validatePrintRules(ctx, planInfo, printPlan, plan.Tracks, planDestinations[index], false); err != nil {
				return rollbackTransactionResult(mediaTx, categorized("validate print rules", err))
			}
		}
	}

	// The current lifecycle routes every output plan through the per-output lifecycle
	// abstraction. The branch is positioned after
	// validatePrintRules so the existing pre-download sidecar
	// writes (PrintVideo, thumbnails, related, PrintBeforeDL,
	// subtitles, converted subtitles) are bypassed and re-driven once
	// per plan by executePlanLifecycle.
	if len(outputPlans) > 0 {
		sink := operation.eventSink()
		lifecycles, sectionErr := operation.buildSectionLifecycles(info, outputPlans, planDestinations)
		if sectionErr != nil {
			return rollbackTransactionResult(mediaTx, categorized("expand download sections", sectionErr))
		}
		if !operation.request.Simulate && !operation.request.SkipDownload {
			backupDestinations := make([]string, len(lifecycles))
			for index := range lifecycles {
				backupDestinations[index] = lifecycles[index].Destination
			}
			if err := mediaTx.acquireDestinationBackups(backupDestinations, operation.request.Overwrite); err != nil {
				return rollbackTransactionResult(mediaTx, categorized("prepare output destinations", err))
			}
		}
		planResults := make([]Result, len(lifecycles))
		for index, lifecycle := range lifecycles {
			operation.applyThumbnailEmbeddingOutputExtension(&lifecycle.Info, lifecycle.Plan.Tracks)
			planResult, lifecycleErr := operation.executePlanLifecycle(
				ctx, mediaTx, &lifecycle, selectedSubtitles, sink,
			)
			if lifecycleErr != nil {
				var sizeAbort *downloader.FileSizeAbortError
				if errors.As(lifecycleErr, &sizeAbort) {
					// yt-dlp's direct downloader aborts media outside the
					// min/max filesize bounds with a diagnostic; the entry is
					// skipped instead of failing the run.
					_ = operation.client.emit(ctx, Event{Kind: EventDownloadCancelled, Message: sizeAbort.Message})
					result.Skipped = true
					result.SkipReason = sizeAbort.Message
					return result, nil
				}
				// Roll back the shared transaction whenever multiple lifecycles are
				// in flight. A single output plan may expand into many section
				// lifecycles, so deciding rollback by len(outputPlans) would leave
				// earlier section outputs published on a later lifecycle failure.
				if len(lifecycles) > 1 {
					return rollbackTransactionResult(mediaTx, lifecycleErr)
				}
				return result, lifecycleErr
			}
			planResults[index] = planResult
		}

		var mediaArtifacts []Artifact
		for index := range planResults {
			planResult := planResults[index]
			result.Downloaded = result.Downloaded || planResult.Downloaded
			result.Prints = append(result.Prints, planResult.Prints...)
			if index == 0 {
				result.Filename = planResult.Filename
				if len(planResult.InfoJSON) > 0 {
					result.InfoJSON = planResult.InfoJSON
				}
			}
			for _, artifact := range planResult.Artifacts {
				if artifact.Kind == "media" {
					mediaArtifacts = appendUniqueArtifact(mediaArtifacts, artifact)
					continue
				}
				result.Artifacts = appendUniqueArtifact(result.Artifacts, artifact)
			}
		}
		for _, artifact := range mediaArtifacts {
			result.Artifacts = appendUniqueArtifact(result.Artifacts, artifact)
		}
		if len(result.Artifacts) > 0 {
			result.Bytes, err = artifactBytes(result.Artifacts)
			if err != nil {
				return rollbackTransactionResult(mediaTx, categorized("account output artifacts", err))
			}
		}
		if !operation.request.Simulate {
			if commitErr := mediaTx.commit(); commitErr != nil {
				return result, categorized("commit output transaction", commitErr)
			}
			mediaTx.finalize()
		}
		if !result.Skipped && (operation.request.ForceWriteArchive || (!operation.request.Simulate && !operation.request.SkipDownload)) {
			if err := operation.recordArchive(ctx, archiveIdentity); err != nil {
				return result, err
			}
		}
		return result, nil
	}

	prints, err := operation.capturePrints(ctx, PrintVideo, info, singlePrintPlan, selectedFormats, destination)
	if err != nil {
		return rollbackTransactionResult(mediaTx, categorized("render video print", err))
	}
	result.Prints = append(result.Prints, prints...)
	printArtifacts, printBytes, err := operation.writePrintFiles(ctx, PrintVideo, info, singlePrintPlan, selectedFormats, destination)
	trackTransactionArtifacts(mediaTx, printArtifacts)
	if err != nil {
		return rollbackTransactionResult(mediaTx, categorized("write video print file", err))
	}
	addPrintFileArtifacts(&result, printArtifacts, printBytes)
	if operation.request.Simulate {
		if err := operation.recordForcedArchive(ctx, archiveIdentity); err != nil {
			return result, err
		}
		return result, nil
	}
	thumbnailArtifacts, thumbnailBytes, err := operation.writeThumbnails(ctx, &info, false)
	trackTransactionArtifacts(mediaTx, thumbnailArtifacts)
	if err != nil {
		return rollbackTransactionResult(mediaTx, categorized("write thumbnails", err))
	}
	result.Artifacts = append(result.Artifacts, thumbnailArtifacts...)
	result.Bytes += thumbnailBytes
	relatedArtifacts, relatedBytes, err := operation.writeRelatedFiles(ctx, info, false)
	trackTransactionArtifacts(mediaTx, relatedArtifacts)
	if err != nil {
		return rollbackTransactionResult(mediaTx, categorized("write related files", err))
	}
	result.Artifacts = append(result.Artifacts, relatedArtifacts...)
	result.Bytes += relatedBytes
	prints, err = operation.capturePrints(ctx, PrintBeforeDL, info, singlePrintPlan, selectedFormats, destination)
	if err != nil {
		return rollbackTransactionResult(mediaTx, categorized("render before-download print", err))
	}
	result.Prints = append(result.Prints, prints...)
	printArtifacts, printBytes, err = operation.writePrintFiles(ctx, PrintBeforeDL, info, singlePrintPlan, selectedFormats, destination)
	trackTransactionArtifacts(mediaTx, printArtifacts)
	if err != nil {
		return rollbackTransactionResult(mediaTx, categorized("write before-download print file", err))
	}
	addPrintFileArtifacts(&result, printArtifacts, printBytes)
	subtitleArtifacts, subtitleBytes, err := operation.downloadSubtitles(ctx, info, selectedSubtitles, operation.eventSink())
	trackTransactionArtifacts(mediaTx, subtitleArtifacts)
	if err != nil {
		return rollbackTransactionResult(mediaTx, categorized("download subtitles", err))
	}
	result.Artifacts = append(result.Artifacts, subtitleArtifacts...)
	result.Bytes += subtitleBytes
	var convertedSubtitles bool
	selectedSubtitles, result.Artifacts, convertedSubtitles, err = operation.convertSelectedSubtitles(
		ctx, selectedSubtitles, result.Artifacts, operation.eventSink(),
	)
	if err != nil {
		return rollbackTransactionResult(mediaTx, categorized("convert subtitles", err))
	}
	if convertedSubtitles {
		result.Bytes, err = artifactBytes(result.Artifacts)
		if err != nil {
			return rollbackTransactionResult(mediaTx, categorized("account converted subtitle artifacts", err))
		}
	}
	if len(result.Artifacts) > 0 {
		result.Downloaded = true
	}
	result.InfoJSON, err = encodeInfo(info)
	if err != nil {
		return rollbackTransactionResult(mediaTx, err)
	}
	if operation.request.SkipDownload {
		for _, stage := range []PrintStage{PrintPostProcess, PrintAfterMove, PrintAfterVideo} {
			prints, err = operation.capturePrints(ctx, stage, info, singlePrintPlan, selectedFormats, destination)
			if err != nil {
				return result, categorized("render "+string(stage)+" print", err)
			}
			result.Prints = append(result.Prints, prints...)
			printArtifacts, printBytes, err = operation.writePrintFiles(ctx, stage, info, singlePrintPlan, selectedFormats, destination)
			if err != nil {
				return result, categorized("write "+string(stage)+" print file", err)
			}
			addPrintFileArtifacts(&result, printArtifacts, printBytes)
		}
		if err := operation.recordForcedArchive(ctx, archiveIdentity); err != nil {
			return result, err
		}
		return result, nil
	}

	outputDir := operation.request.outputRoot(OutputPathHome)

	if mediaTx != nil {
		if err := mediaTx.acquireDestinationBackups(planDestinations, operation.request.Overwrite); err != nil {
			return rollbackTransactionResult(mediaTx, categorized("prepare output destinations", err))
		}
	}

	sink := operation.eventSink()
	multiOutput := len(outputPlans) > 1
	var downloadedPath string
	mediaArtifactStart := len(result.Artifacts)
	for planIndex, plan := range outputPlans {
		planDestination := planDestinations[planIndex]
		path, _, downloadErr := operation.downloadSelections(ctx, plan.Tracks, outputDir, planDestination, sink)
		if downloadErr != nil {
			return rollbackMediaResult(mediaTx, categorized("download selected formats", downloadErr))
		}
		if mediaTx != nil {
			mediaTx.markPublished(path)
		}
		if multiOutput {
			result.Artifacts = append(result.Artifacts, Artifact{Path: path, Kind: "media"})
			if planIndex == 0 {
				downloadedPath = path
			}
			continue
		}
		var mediaArtifacts []Artifact
		path, mediaArtifacts, downloadErr = operation.applyPostprocessors(ctx, outputDir, path, sink)
		if downloadErr != nil {
			return rollbackMediaResult(mediaTx, categorized("run postprocessors", downloadErr))
		}
		if mediaTx != nil {
			mediaTx.markPublished(path)
		}
		trackTransactionArtifacts(mediaTx, mediaArtifacts)
		result.Artifacts = append(result.Artifacts, mediaArtifacts...)
		if planIndex == 0 {
			downloadedPath = path
		}
	}
	if mediaTx != nil {
		if commitErr := mediaTx.commitDestinations(); commitErr != nil {
			return result, categorized("commit output transaction", commitErr)
		}
	}
	var embeddedSubtitles bool
	if !multiOutput {
		result.Artifacts, embeddedSubtitles, err = operation.embedSelectedSubtitles(
			ctx, &info, downloadedPath, selectedSubtitles, result.Artifacts, sink,
		)
		if err != nil {
			return rollbackArtifactResult(mediaTx, categorized("embed subtitles", err))
		}
	}
	var cutApplied bool
	if !multiOutput {
		downloadedPath, result.Artifacts, cutApplied, err = operation.applyChapterCuts(ctx, &info, downloadedPath, result.Artifacts, sink)
		if err != nil {
			return rollbackArtifactResult(mediaTx, err)
		}
		result.InfoJSON, err = encodeInfo(info)
		if err != nil {
			return rollbackArtifactResult(mediaTx, err)
		}
	}
	var embeddedMetadata bool
	if !multiOutput {
		embeddedMetadata, err = operation.applyAutomaticMetadataEmbedding(ctx, info, downloadedPath, sink)
		if err != nil {
			return rollbackArtifactResult(mediaTx, categorized("embed metadata", err))
		}
	}
	var embeddedThumbnail bool
	if !multiOutput {
		result.Artifacts, embeddedThumbnail, err = operation.embedSelectedThumbnail(
			ctx, &info, downloadedPath, result.Artifacts, sink,
		)
		if err != nil {
			return rollbackArtifactResult(mediaTx, categorized("embed thumbnail", err))
		}
	}
	result.Downloaded = true
	result.Filename = downloadedPath
	if err := operation.applyOutputMtime(downloadedPath, info); err != nil {
		return rollbackArtifactResult(mediaTx, categorized("set output mtime", err))
	}
	if cutApplied || embeddedSubtitles || embeddedMetadata || embeddedThumbnail {
		result.Bytes, err = artifactBytes(result.Artifacts)
		if err != nil {
			return rollbackArtifactResult(mediaTx, categorized("account post-cut artifacts", err))
		}
		result.InfoJSON, err = encodeInfo(info)
		if err != nil {
			return rollbackArtifactResult(mediaTx, err)
		}
	} else {
		mediaBytes, err := mediaArtifactBytes(result.Artifacts[mediaArtifactStart:])
		if err != nil {
			return rollbackArtifactResult(mediaTx, categorized("account media artifacts", err))
		}
		result.Bytes += mediaBytes
	}
	for _, stage := range []PrintStage{PrintPostProcess, PrintAfterMove, PrintAfterVideo} {
		prints, err = operation.capturePrints(ctx, stage, info, singlePrintPlan, selectedFormats, downloadedPath)
		if err != nil {
			return rollbackArtifactResult(mediaTx, categorized("render "+string(stage)+" print", err))
		}
		result.Prints = append(result.Prints, prints...)
		printArtifacts, printBytes, err = operation.writePrintFiles(ctx, stage, info, singlePrintPlan, selectedFormats, downloadedPath)
		trackTransactionArtifacts(mediaTx, printArtifacts)
		if err != nil {
			return rollbackArtifactResult(mediaTx, categorized("write "+string(stage)+" print file", err))
		}
		addPrintFileArtifacts(&result, printArtifacts, printBytes)
	}
	if mediaTx != nil {
		if commitErr := mediaTx.commitArtifacts(); commitErr != nil {
			return result, categorized("commit output transaction", commitErr)
		}
		mediaTx.finalize()
	}
	if err := operation.recordArchive(ctx, archiveIdentity); err != nil {
		return result, err
	}
	return result, nil
}

func (operation *operation) recordForcedArchive(ctx context.Context, identity archive.Identity) error {
	if !operation.request.ForceWriteArchive {
		return nil
	}
	return operation.recordArchive(ctx, identity)
}

func (operation *operation) recordArchive(ctx context.Context, identity archive.Identity) error {
	if operation.archive == nil {
		return nil
	}
	_, err := operation.archive.Record(ctx, identity)
	if err != nil {
		return categorized("record download archive", err)
	}
	return nil
}

func (operation *operation) addAutonumber(info *value.Info) {
	if info == nil {
		return
	}
	operation.autonumberMu.Lock()
	defer operation.autonumberMu.Unlock()
	acceptedCount := operation.autonumberNext + 1
	info.Set("autonumber", value.Int(int64(normalizedAutonumberStart(operation.request.AutonumberStart)-1+acceptedCount)))
	operation.autonumberNext = acceptedCount
}

func (operation *operation) provisionalAutonumberInfo(info value.Info) value.Info {
	operation.autonumberMu.Lock()
	defer operation.autonumberMu.Unlock()
	provisional := value.NewInfo(info.Fields().Clone())
	provisional.Set("autonumber", value.Int(int64(normalizedAutonumberStart(operation.request.AutonumberStart)-1+operation.autonumberNext)))
	return provisional
}

func (operation *operation) autonumberCount() int {
	operation.autonumberMu.Lock()
	defer operation.autonumberMu.Unlock()
	count := operation.autonumberNext - operation.request.AutonumberIndex
	if count < 0 {
		return 0
	}
	return count
}

func (operation *operation) autonumberPosition() int {
	operation.autonumberMu.Lock()
	defer operation.autonumberMu.Unlock()
	return operation.autonumberNext
}

func (operation *operation) autonumberCountSince(previous int) int {
	current := operation.autonumberPosition()
	if current < previous {
		return 0
	}
	return current - previous
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
	var existing *Error
	if errors.As(err, &existing) && existing.Category != "" {
		return &Error{Category: existing.Category, Op: op, Err: err}
	}
	category := ErrorNetwork
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		category = ErrorCancelled
	case errors.Is(err, ErrInvalidRouting):
		category = ErrorInvalidInput
	case errors.Is(err, ErrUnsupportedRouting):
		category = ErrorUnsupported
	case errors.Is(err, ErrUnsupported), errors.Is(err, mediaformat.ErrMultiOutput):
		category = ErrorUnsupported
	case errors.Is(err, ErrHLSDiscontinuitySelection),
		errors.Is(err, ErrHLSDiscontinuityGroupMissing),
		errors.Is(err, ErrHLSDiscontinuityPlaylistEmpty),
		errors.Is(err, ErrHLSDiscontinuityGroupAdOnly),
		errors.Is(err, ErrHLSDiscontinuityPlaylistMalformed):
		category = ErrorInvalidInput
	case errors.Is(err, ErrHLSDiscontinuityHostPolicy):
		category = ErrorSecurity
	case errors.Is(err, ErrAuthentication), errors.Is(err, ErrWrongPassword):
		category = ErrorAuthentication
	case errors.Is(err, credentialnetrc.ErrUnsafeFile):
		category = ErrorSecurity
	case errors.Is(err, errUnsafePrintFile):
		category = ErrorSecurity
	case errors.Is(err, ErrFormatCheckLimit):
		category = ErrorInvalidInput
	case errors.Is(err, errUnsafeThumbnailRedirect):
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
		errors.Is(err, chromiumwindows.ErrAppBound), errors.Is(err, chromiumwindows.ErrDecrypt),
		errors.Is(err, safari.ErrNotFound), errors.Is(err, safari.ErrInvalidDatabase):
		category = ErrorAuthentication
	case errors.Is(err, chromium.ErrUnsupportedBrowser), errors.Is(err, chromium.ErrUnsupportedPlatform),
		errors.Is(err, chromiumlinux.ErrUnsupportedBrowser), errors.Is(err, chromiumlinux.ErrUnsupportedPlatform),
		errors.Is(err, chromiumwindows.ErrUnsupportedBrowser), errors.Is(err, chromiumwindows.ErrUnsupportedPlatform),
		errors.Is(err, safari.ErrUnsupportedPlatform):
		category = ErrorUnsupported
	case errors.Is(err, ErrUnavailable), errors.Is(err, ErrRegionRestricted), errors.Is(err, ErrChallengeSolver),
		errors.Is(err, ErrTransportProfile), errors.Is(err, ErrTransportIsolation), errors.Is(err, network.ErrImpersonationUnavailable):
		category = ErrorUnsupported
	case errors.Is(err, ffmpeg.ErrFFmpegUnavailable), errors.Is(err, ffmpeg.ErrFFprobeUnavailable),
		errors.Is(err, ffmpeg.ErrUnsafeHLSHeaders), errors.Is(err, ErrXattrsUnsupported):
		category = ErrorUnsupported
	case errors.Is(err, downloader.ErrExternalUnavailable), errors.Is(err, hls.ErrUnsupportedEncryption),
		errors.Is(err, dash.ErrUnsupportedTimeline), errors.Is(err, dash.ErrUnsupportedAddressing),
		errors.Is(err, dash.ErrDynamicMPDUnsupported),
		errors.Is(err, hds.ErrUnsupportedLive), errors.Is(err, hds.ErrUnsupportedDRM),
		errors.Is(err, hds.ErrUnsupportedEmpty):
		category = ErrorUnsupported
	case errors.Is(err, outputtemplate.ErrInvalidTemplate), errors.Is(err, outputtemplate.ErrUnsafePath),
		errors.Is(err, errInvalidRequestOptions),
		errors.Is(err, chapterremove.ErrInvalidSpecification), errors.Is(err, chapterremove.ErrLimit),
		errors.Is(err, sections.ErrInvalidSpecification), errors.Is(err, sections.ErrLimit),
		errors.Is(err, ErrInteractiveInput),
		errors.Is(err, matchfilter.ErrInvalidFilter), errors.Is(err, matchfilter.ErrEvaluation),
		errors.Is(err, matchfilter.ErrEvaluationLimit), errors.Is(err, compatmetadata.ErrInvalidAction),
		errors.Is(err, progress.ErrInvalidProgress), errors.Is(err, mediaformat.ErrInvalidSelector),
		errors.Is(err, mediaformat.ErrNoMatch), errors.Is(err, mediaformat.ErrFilterEvaluation),
		errors.Is(err, mediaformat.ErrInvalidPreference), errors.Is(err, mediaformat.ErrInvalidHeaders),
		errors.Is(err, mediaformat.ErrSelectorLimit),
		errors.Is(err, downloader.ErrDestinationExists), errors.Is(err, downloader.ErrUnsafeDestination),
		errors.Is(err, downloader.ErrTooManyAttempts), errors.Is(err, downloader.ErrInvalidLimits),
		errors.Is(err, downloader.ErrUnsafeExternalArg), errors.Is(err, downloader.ErrUnsafeExternalTool),
		errors.Is(err, downloader.ErrInvalidExternalURL),
		errors.Is(err, dash.ErrInvalidDynamicMPDPolicy),
		errors.Is(err, fragment.ErrTooManySegments), errors.Is(err, fragment.ErrTooManyAttempts),
		errors.Is(err, fragment.ErrTooMuchConcurrency), errors.Is(err, fragment.ErrSegmentTooLarge),
		errors.Is(err, fragment.ErrUnsafeDestination), errors.Is(err, ism.ErrInvalidConfig),
		errors.Is(err, hds.ErrInvalidManifest), errors.Is(err, hds.ErrInvalidBootstrap),
		errors.Is(err, hds.ErrInvalidMedia), errors.Is(err, hds.ErrInvalidConfig),
		errors.Is(err, hds.ErrFragmentTooLarge), errors.Is(err, hds.ErrTooManySegments),
		errors.Is(err, hds.ErrTooManyFragments), errors.Is(err, hds.ErrUnsafeDestination),
		errors.Is(err, youtubelive.ErrInvalidBaseURL), errors.Is(err, youtubelive.ErrInvalidConfig),
		errors.Is(err, youtubelive.ErrUnsafeOutput), errors.Is(err, youtubelive.ErrOutputExists),
		errors.Is(err, youtubelive.ErrLiveInvalidConfig),
		errors.Is(err, ffmpeg.ErrDestinationExists),
		errors.Is(err, ffmpeg.ErrInvalidOperation), errors.Is(err, postprocess.ErrInvalidGraph),
		errors.Is(err, postprocess.ErrUnsafePath),
		errors.Is(err, network.ErrInvalidProxy), errors.Is(err, network.ErrInvalidCookie),
		errors.Is(err, network.ErrInvalidSourceAddress), errors.Is(err, network.ErrConflictingAddressPolicy),
		errors.Is(err, network.ErrNetworkPolicyUnavailable),
		errors.Is(err, errInvalidBrowserCookieSpec), errors.Is(err, netscape.ErrMalformed), errors.Is(err, netscape.ErrFile),
		errors.Is(err, netscape.ErrWrongFormat), errors.Is(err, netscape.ErrTooLarge),
		errors.Is(err, firefox.ErrUnsafePath), errors.Is(err, firefox.ErrLimit),
		errors.Is(err, safari.ErrUnsafePath), errors.Is(err, safari.ErrLimit),
		errors.Is(err, chromiumlinux.ErrUnsafePath), errors.Is(err, chromiumlinux.ErrLimit),
		errors.Is(err, chromiumwindows.ErrUnsafePath), errors.Is(err, chromiumwindows.ErrLimit),
		errors.Is(err, credentialnetrc.ErrSyntax), errors.Is(err, credentialnetrc.ErrLimit), errors.Is(err, credentialnetrc.ErrInvalidHost),
		errors.Is(err, archive.ErrInvalidIdentity), errors.Is(err, archive.ErrCorrupt), errors.Is(err, archive.ErrTooLarge), errors.Is(err, archive.ErrUnsafePath),
		errors.Is(err, cache.ErrInvalidName), errors.Is(err, cache.ErrUnsafePath), errors.Is(err, cache.ErrTooLarge), errors.Is(err, cache.ErrCorrupt), errors.Is(err, ErrInvalidInfoJSON):
		category = ErrorInvalidInput
	case errors.Is(err, archive.ErrIO), errors.Is(err, archive.ErrLock), errors.Is(err, cache.ErrIO):
		category = ErrorInternal
	case errors.Is(err, packcatalog.ErrUntrusted), errors.Is(err, packcatalog.ErrSignature), errors.Is(err, packcatalog.ErrRevoked), errors.Is(err, packcatalog.ErrExpired):
		category = ErrorSecurity
	case errors.Is(err, packcatalog.ErrInvalid), errors.Is(err, packcatalog.ErrLimit), errors.Is(err, packcatalog.ErrNotFound):
		category = ErrorInvalidInput
	case errors.Is(err, mediaformat.ErrNoFormats), errors.Is(err, mediaformat.ErrInvalidFormats), errors.Is(err, mediaformat.ErrFormatLimit), errors.Is(err, ErrInvalidMetadata),
		errors.Is(err, ErrInvalidPlaylist), errors.Is(err, ErrPlaylistLimit),
		errors.Is(err, downloader.ErrExternalFailed), errors.Is(err, fragment.ErrNoSegments),
		errors.Is(err, fragment.ErrInvalidEncryption), errors.Is(err, hls.ErrInvalidPlaylist),
		errors.Is(err, dash.ErrInvalidMPD), errors.Is(err, ism.ErrInvalidManifest), errors.Is(err, ism.ErrTimelineBound),
		errors.Is(err, ffmpeg.ErrMediaFailure), errors.Is(err, pipeline.ErrMissingDASHTracks),
		errors.Is(err, pipeline.ErrMissingToolset), errors.Is(err, youtubelive.ErrHeadSequence),
		errors.Is(err, youtubelive.ErrNoSegments), errors.Is(err, youtubelive.ErrInvalidWindow),
		errors.Is(err, youtubelive.ErrDownloadFailed), errors.Is(err, youtubelive.ErrEventSink),
		errors.Is(err, youtubelive.ErrLiveHeadSequence), errors.Is(err, youtubelive.ErrLiveNoProgress),
		errors.Is(err, youtubelive.ErrLivePollLimit):
		category = ErrorInternal
	case errors.Is(err, youtubeump.ErrMissingConfig), errors.Is(err, youtubeump.ErrUnsupportedURL),
		errors.Is(err, youtubeump.ErrUnsafeDestination), errors.Is(err, youtubeump.ErrDestinationExists),
		errors.Is(err, youtubeump.ErrTooManyAttempts), errors.Is(err, youtubeump.ErrCheckpointInvalid):
		category = ErrorInvalidInput
	case errors.Is(err, youtubeump.ErrUnsupportedDirective), errors.Is(err, youtubeump.ErrLiveUnsupported),
		errors.Is(err, youtubeump.ErrResumeUnsupported), errors.Is(err, youtubeump.ErrSabrRecoveryBudget),
		errors.Is(err, youtubeump.ErrReloadBudget), errors.Is(err, youtubeump.ErrRefreshBudget),
		errors.Is(err, youtubeump.ErrReloadRejected), errors.Is(err, youtubeump.ErrRefreshRejected):
		category = ErrorUnsupported
	case errors.Is(err, youtubeump.ErrRedirect), errors.Is(err, youtubeump.ErrResponseTooLarge),
		errors.Is(err, youtubeump.ErrInvalidMediaState), errors.Is(err, youtubeump.ErrInvalidProtobuf),
		errors.Is(err, youtubeump.ErrMalformedFraming), errors.Is(err, youtubeump.ErrTruncatedStream),
		errors.Is(err, youtubeump.ErrNonCanonicalVarint), errors.Is(err, youtubeump.ErrVarintOverflow),
		errors.Is(err, youtubeump.ErrInvalidContentType), errors.Is(err, youtubeump.ErrOversizedPart),
		errors.Is(err, youtubeump.ErrTooManyParts), errors.Is(err, youtubeump.ErrTooManyActiveHeaders),
		errors.Is(err, youtubeump.ErrDownloadFailed), errors.Is(err, youtubeump.ErrRoundsExhausted),
		errors.Is(err, youtubeump.ErrSabrError), errors.Is(err, youtubeump.ErrReloadPlayerResponse),
		errors.Is(err, youtubeump.ErrUnsafeRedirect), errors.Is(err, youtubeump.ErrRedirectLoop),
		errors.Is(err, youtubeump.ErrRedirectBudget), errors.Is(err, youtubeump.ErrInvalidContextState),
		errors.Is(err, youtubeump.ErrExcessivePolicyBackoff):
		category = ErrorNetwork
	case errors.Is(err, youtubeump.ErrEventSink):
		category = ErrorInternal
	}
	return &Error{Category: category, Op: op, Err: err}
}

// Categorize applies the engine's stable public error taxonomy. Compatibility
// facades use it for APIs whose implementation remains outside orchestration.
func Categorize(op string, err error) error { return categorized(op, err) }

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
	case "chrome", "chromium", "brave", "edge", "vivaldi", "opera", "firefox", "safari":
	default:
		return browserCookieSpec{}, fmt.Errorf("%w: unsupported browser", errInvalidBrowserCookieSpec)
	}
	if browserName == "safari" {
		if hasContainer || (hasProfile && (profile == "" || strings.ContainsRune(profile, 0) ||
			(!filepath.IsAbs(profile) && !strings.HasPrefix(profile, "~/")))) {
			return browserCookieSpec{}, fmt.Errorf("%w: invalid Safari database path", errInvalidBrowserCookieSpec)
		}
		return browserCookieSpec{browser: browserName, profile: profile}, nil
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
	if specification.browser == "safari" {
		importer := client.safariCookieImporter
		if importer == nil {
			importer = safari.Import
		}
		result, err := importer(ctx, safari.Options{DatabasePath: specification.profile})
		return cookieImportResult{
			Cookies: result.Cookies, Total: result.Total, Imported: result.Imported, Failed: result.Failed,
		}, err
	}
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

// sharedChallengeSolver returns the client-level EJS solver, creating it on
// first use. The solver and its bounded cache persist across Run calls.
func (client *Client) sharedChallengeSolver() *lazyChallengeSolver {
	client.solverMu.Lock()
	defer client.solverMu.Unlock()
	if client.sharedSolver == nil {
		client.sharedSolver = &lazyChallengeSolver{
			path: discoverJavaScriptHelper(client.javascriptHelper), factory: client.composition.hooks.ChallengeSolverFactory,
		}
	}
	return client.sharedSolver
}

// Close releases the shared EJS solver and its supervisor child process.
// It is safe to call multiple times. After Close, subsequent Run calls will
// lazily recreate the solver if a JavaScript helper is available.
func (client *Client) Close() {
	client.solverMu.Lock()
	defer client.solverMu.Unlock()
	if client.sharedSolver != nil {
		client.sharedSolver.Close()
		client.sharedSolver = nil
	}
}

type lazyChallengeSolver struct {
	mu      sync.Mutex
	path    string
	factory ChallengeSolverFactory
	solver  providerapi.ChallengeSolver
	closer  interface{ Close() error }
	active  sync.WaitGroup
	closed  bool
}

// Available reports whether this lazy solver has enough configuration to start
// a helper. Providers use it only to gate fallbacks that necessarily require a
// JavaScript runtime; normal challenge solving still returns the authoritative
// startup error from SolvePlayer.
func (solver *lazyChallengeSolver) Available() bool {
	if solver == nil {
		return false
	}
	solver.mu.Lock()
	defer solver.mu.Unlock()
	return !solver.closed && solver.factory != nil && solver.path != ""
}

func (solver *lazyChallengeSolver) SolvePlayer(
	ctx context.Context,
	id string,
	player string,
	requests []providerapi.ChallengeRequest,
	outputPreprocessed bool,
) (providerapi.ChallengeResult, error) {
	solver.mu.Lock()
	if solver.closed {
		solver.mu.Unlock()
		return providerapi.ChallengeResult{}, unavailableChallengeFailure(errors.New("solver is closed"))
	}
	if solver.solver == nil {
		if solver.factory == nil {
			solver.mu.Unlock()
			return providerapi.ChallengeResult{}, unavailableChallengeFailure(providerapi.ErrChallengeSolver)
		}
		challengeSolver, closer, err := solver.factory(solver.path)
		if err != nil {
			solver.mu.Unlock()
			// The event exposes only the closed unavailable category, while the
			// returned chain preserves the authoritative startup/security cause.
			return providerapi.ChallengeResult{}, unavailableChallengeFailure(err)
		}
		solver.solver, solver.closer = challengeSolver, closer
	}
	solver.active.Add(1)
	activeSolver := solver.solver
	solver.mu.Unlock()

	defer solver.active.Done()
	return activeSolver.SolvePlayer(ctx, id, player, requests, outputPreprocessed)
}

// Close waits for active operations to complete, then shuts down the
// supervisor. It is safe to call multiple times.
func (solver *lazyChallengeSolver) Close() {
	if solver == nil {
		return
	}
	solver.mu.Lock()
	solver.closed = true
	closer := solver.closer
	solver.closer = nil
	solver.solver = nil
	solver.mu.Unlock()

	// Wait for in-flight operations to finish before killing the helper.
	solver.active.Wait()
	if closer != nil {
		_ = closer.Close()
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
