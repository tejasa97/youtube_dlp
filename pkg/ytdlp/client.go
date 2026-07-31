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
	"sync"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/archive"
	"github.com/ytdlp-go/ytdlp/internal/cache"
	"github.com/ytdlp-go/ytdlp/internal/compat/chapterremove"
	"github.com/ytdlp-go/ytdlp/internal/compat/matchfilter"
	compatmetadata "github.com/ytdlp-go/ytdlp/internal/compat/metadata"
	"github.com/ytdlp-go/ytdlp/internal/compat/progress"
	outputtemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/cookies/chromium"
	"github.com/ytdlp-go/ytdlp/internal/cookies/chromiumlinux"
	"github.com/ytdlp-go/ytdlp/internal/cookies/chromiumwindows"
	"github.com/ytdlp-go/ytdlp/internal/cookies/firefox"
	"github.com/ytdlp-go/ytdlp/internal/cookies/netscape"
	"github.com/ytdlp-go/ytdlp/internal/cookies/safari"
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
	"github.com/ytdlp-go/ytdlp/internal/protocol/hds"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/protocol/ism"
	"github.com/ytdlp-go/ytdlp/internal/protocol/youtubelive"
	"github.com/ytdlp-go/ytdlp/internal/protocol/youtubeump"
	"github.com/ytdlp-go/ytdlp/internal/value"
	"github.com/ytdlp-go/ytdlp/internal/youtubepot"
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
	URL                  string
	OutputTemplate       string
	OutputTemplates      OutputTemplates
	OutputDir            string
	OutputPaths          OutputPaths
	Proxy                string
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
	CacheDir        string
	Timeout         time.Duration
	Overwrite       bool
	// Simulate suppresses media, sidecar, archive, and postprocessor output
	// while still performing extraction. Unlike SkipDownload, it does not
	// permit related-file writes.
	Simulate            bool
	SkipDownload        bool
	Format              string
	FormatSort          []string
	FormatSortForce     bool
	PreferredExtensions []string
	PreferFreeFormats   bool
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
	// ForceKeyframesAtCuts applies to ordinary chapter, manual range, and
	// SponsorBlock cuts. It is invalid when no removal is requested.
	ForceKeyframesAtCuts bool
	Subtitles            SubtitleOptions
	Thumbnails           ThumbnailOptions
	RelatedFiles         RelatedFileOptions
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
	EmbedMetadata  bool
	EmbedChapters  *bool
	Downloader     DownloaderOptions
	Postprocessors []Postprocessor
	// PluginID explicitly selects an installed signed plugin extractor. Plugins
	// are never considered by automatic URL routing.
	PluginID string
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

type Result struct {
	InfoJSON   json.RawMessage
	Extractor  string
	Downloaded bool
	Archived   bool
	Skipped    bool
	SkipReason string
	// Stopped reports that a breaking match filter ended this result's queue.
	Stopped    bool
	StopReason string
	Filename   string
	Bytes      int64
	Entries    []Result
	Artifacts  []Artifact
	Prints     []PrintOutput
	// SuppressedFailures counts ordinary entry failures that a playlist
	// continued past. The failures remain observable even when Run returns a
	// usable partial playlist result.
	SuppressedFailures int
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
	youtubePOT            *youtubepot.Director
	youtubePOTErr         error
	transportFactory      func(network.Config) (*network.Client, error)

	solverMu     sync.Mutex
	sharedSolver *lazyYouTubeSolver
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
	if request.SponsorBlock.ChapterTitle != nil {
		chapterTitle := *request.SponsorBlock.ChapterTitle
		request.SponsorBlock.ChapterTitle = &chapterTitle
	}
	if request.EmbedChapters != nil {
		embedChapters := *request.EmbedChapters
		request.EmbedChapters = &embedChapters
	}
	request.OutputTemplates = cloneOutputTemplates(request.OutputTemplates)
	request.OutputPaths = cloneOutputPaths(request.OutputPaths)
	if err := validateRequestOptions(request); err != nil {
		return Result{}, categorized("validate request options", err)
	}
	if client.youtubePOTErr != nil {
		return Result{}, &Error{Category: ErrorInvalidInput, Op: "configure YouTube PO-token providers", Err: client.youtubePOTErr}
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		return Result{}, err
	}
	transportFactory := client.transportFactory
	if transportFactory == nil {
		transportFactory = network.New
	}
	transport, err := transportFactory(network.Config{Proxy: request.Proxy, Timeout: request.Timeout, DefaultProfile: request.ImpersonationProfile})
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
	challengeSolver := client.sharedChallengeSolver()
	_, ffmpegErr := ffmpeg.DiscoverFFmpeg(ffmpeg.Config{})
	plannerCapabilities := mediaformat.PlannerCapabilities{
		CanMergeFormats: ffmpegErr == nil,
		OutputToStdout:  request.outputTemplate(OutputTemplateDefault) == "-",
	}
	operation := &operation{
		client: client, request: request, transport: transport,
		registry: client.productRegistry(),
		solver:   challengeSolver, archive: downloadArchive, cache: operationCache,
		credentials:         credentials,
		compatibility:       compatibility,
		rootExtractor:       &rootExtractor,
		plannerCapabilities: &plannerCapabilities,
	}
	// Explicit selected/all checks take precedence over allow-unplayable. The
	// latter bypasses only Auto's DRM/needs-testing default policy.
	if shouldCheckFormats(request.CheckFormats, request.AllowUnplayableFormats) {
		checker := newFormatAvailabilityChecker(ctx, transport, request.CheckFormats)
		operation.formatAvailability = checker
		operation.formatAvailabilityChecker = checker
	}
	return operation.process(ctx, request.URL, request.PluginID, nil, make(map[string]bool), 0)
}

func shouldCheckFormats(mode FormatCheckMode, allowUnplayable bool) bool {
	return mode != FormatCheckNone && (mode != FormatCheckAuto || !allowUnplayable)
}

func (client *Client) productRegistry() *extractor.Registry {
	registered := []extractor.Extractor{
		// Niconico's opaque search and exact collection routes must be selected
		// before generic HTTP handling; live/history are intentionally absent.
		extractor.NewNiconicoSearch(),
		extractor.NewNiconicoSearchURL(),
		extractor.NewNiconicoTag(),
		extractor.NewNiconicoPlaylist(),
		extractor.NewNiconicoSeries(),
		extractor.NewNiconicoUser(),
		extractor.NewNiconico(),
		extractor.NewYouTubeMusicSearch(),
		extractor.NewYouTubeMusicBrowse(),
		extractor.NewYouTubeSearch(),
		extractor.NewYouTubeHashtag(),
		extractor.NewYouTubeAliasTab(),
		extractor.NewYouTubeHandleTab(),
		extractor.NewYouTubeChannelTab(),
		extractor.NewYouTube(),
		extractor.NewVimeo(),
		extractor.NewVKUserVideos(),
		extractor.NewVKWallPost(),
		extractor.NewVK(),
		extractor.NewVKAudio(),
		extractor.NewVKPlay(),
		extractor.NewVKPlayLive(),
		extractor.NewTikTok(),
		extractor.NewHytale(),
		extractor.NewCloudflareStream(),
		// Discovery adapters are registered before broad hosted-video backends.
		// The Italian specialization precedes the country-generic dplus route.
		extractor.NewDiscoveryPlusItalyShow(),
		extractor.NewDiscoveryPlusIndiaShow(),
		extractor.NewTele5(),
		extractor.NewDiscoveryNetworksDe(),
		extractor.NewHGTVDe(),
		extractor.NewDiscoveryPlusItaly(),
		extractor.NewDiscoveryPlusIndia(),
		extractor.NewDiscoveryPlus(),
		extractor.NewDPlay(),
		extractor.NewAmHistoryChannel(),
		extractor.NewAnimalPlanet(),
		extractor.NewCookingChannel(),
		extractor.NewDestinationAmerica(),
		extractor.NewDiscoveryLife(),
		extractor.NewFoodNetwork(),
		extractor.NewGoDiscovery(),
		extractor.NewHGTVUsa(),
		extractor.NewInvestigationDiscovery(),
		extractor.NewScienceChannel(),
		extractor.NewTLC(),
		extractor.NewTravelChannel(),
		extractor.NewWashingtonPost(),
		extractor.NewADN(),
		extractor.NewBostonGlobe(),
		extractor.NewGray(),
		extractor.NewClickOnDetroit(),
		extractor.NewActionNewsJax(),
		extractor.NewElComercio(),
		extractor.NewLateja(),
		extractor.NewFifthDomain(),
		extractor.NewVLNO(),
		extractor.NewFourteenNews(),
		extractor.NewGlobeAndMail(),
		extractor.NewPilotOnline(),
		extractor.NewUpperMichiganSource(),
		extractor.NewArcPublishing(),
		extractor.NewFOX9News(),
		extractor.NewFOX9(),
		extractor.NewAnvato(),
		extractor.NewNBCOlympics(),
		extractor.NewWeatherCom(),
		extractor.NewThePlatformFeed(),
		extractor.NewThePlatform(),
		extractor.NewPGATour(),
		extractor.NewNineNews(),
		extractor.NewNineNow(),
		extractor.NewNetApp(),
		extractor.NewNetAppCollection(),
		extractor.NewAMCNetworks(),
		extractor.NewCraftsy(),
		extractor.NewTVO(),
		extractor.NewTVAPlus(),
		extractor.NewTVANouvellesArticle(),
		extractor.NewTVANouvelles(),
		extractor.NewUnitedNationsWebTV(),
		extractor.NewAZMedien(),
		extractor.NewInc(),
		extractor.NewHeise(),
		extractor.NewSpiegel(),
		extractor.NewOneFootball(),
		extractor.NewACast(),
		extractor.NewACastChannel(),
		extractor.NewSimplecastEpisode(),
		extractor.NewSimplecastPodcast(),
		extractor.NewSimplecast(),
		extractor.NewMegaphone(),
		extractor.NewArt19Show(),
		extractor.NewArt19(),
		extractor.NewLibsyn(),
		extractor.NewSpreakerShow(),
		extractor.NewSpreaker(),
		extractor.NewPRXStoriesSearch(),
		extractor.NewPRXSeriesSearch(),
		extractor.NewPRXStory(),
		extractor.NewPRXSeries(),
		extractor.NewPRXAccount(),
		extractor.NewNownessPlaylist(),
		extractor.NewNownessSeries(),
		extractor.NewNowness(),
		extractor.NewDacastPlaylist(),
		extractor.NewDacast(),
		extractor.NewPanoptoPlaylist(),
		extractor.NewPanopto(),
		extractor.NewTeachingChannel(),
		extractor.NewNowCanal(),
		extractor.NewDemocracyNow(),
		extractor.NewBuzzFeed(),
		extractor.NewMediaStream(),
		extractor.NewWinSports(),
		extractor.NewABCOTVS(),
		extractor.NewABCOTVSClips(),
		extractor.NewVidsIo(),
		extractor.NewLaracasts(),
		extractor.NewLaracastsSeries(),
		extractor.NewFormula1(),
		extractor.NewEuropeanTour(),
		extractor.NewMaoriTV(),
		extractor.NewTheStar(),
		extractor.NewTheSun(),
		extractor.NewWimbledon(),
		extractor.NewUSAToday(),
		extractor.NewSkyNewsAU(),
		extractor.NewBundesliga(),
		extractor.NewBusinessInsider(),
		extractor.NewDBTV(),
		extractor.NewHollywoodReporter(),
		extractor.NewIltalehti(),
		extractor.NewLeFigaroVideoEmbed(),
		extractor.NewMirrorCoUK(),
		extractor.NewOutsideTV(),
		extractor.NewTheIntercept(),
		extractor.NewBrightcove(),
		extractor.NewKaltura(),
		extractor.NewJWPlatform(),
		extractor.NewWistia(),
		extractor.NewSproutVideo(),
		extractor.NewDailymotionPlaylist(),
		extractor.NewDailymotionSearch(),
		extractor.NewDailymotionUser(),
		extractor.NewDailymotion(),
		extractor.NewReddit(),
		extractor.NewTwitter(),
		extractor.NewBandcampWeekly(),
		extractor.NewBandcampUser(),
		extractor.NewBandcamp(),
		extractor.NewMixcloud(),
		extractor.NewRumble(),
		extractor.NewBilibiliPlayer(),
		extractor.NewBilibiliDynamic(),
		extractor.NewBiliIntlSeries(),
		extractor.NewBiliIntl(),
		extractor.NewBilibiliCollectionList(),
		extractor.NewBilibiliSeriesList(),
		extractor.NewBilibiliBangumiMedia(),
		extractor.NewBilibiliBangumiSeason(),
		extractor.NewBilibiliBangumi(),
		extractor.NewBilibiliAudioAlbum(),
		extractor.NewBilibiliAudio(),
		extractor.NewBilibiliCategory(),
		extractor.NewBilibili(),
		extractor.NewInstagram(),
		extractor.NewKick(),
		extractor.NewBBCCoUkArticle(),
		extractor.NewBBCCoUkPlaylist(),
		extractor.NewBBCCoUkIPlayerEpisodes(),
		extractor.NewBBCCoUkIPlayerGroup(),
		extractor.NewBBCIPlayer(),
		extractor.NewRaiPlayPlaylist(),
		extractor.NewRaiPlayLive(),
		extractor.NewRaiPlay(),
		extractor.NewRaiPlaySoundPlaylist(),
		extractor.NewRaiPlaySoundLive(),
		extractor.NewRaiPlaySound(),
		extractor.NewRaiNews(),
		extractor.NewRaiCultura(),
		extractor.NewRaiSudtirol(),
		extractor.NewRai(),
		extractor.NewARDMediathekCollection(),
		extractor.NewARD(),
		extractor.NewARDAudiothekPlaylist(),
		extractor.NewARDAudiothek(),
		extractor.NewRadioFranceProgramSchedule(),
		extractor.NewFranceCulture(),
		extractor.NewRadioFrancePodcast(),
		extractor.NewRadioFranceProfile(),
		extractor.NewRadioFranceLive(),
		extractor.NewRadioFrance(),
		extractor.NewNRKSkole(),
		extractor.NewNRKRadioPodkast(),
		extractor.NewNRKTVEpisode(),
		extractor.NewNRKTVEpisodes(),
		extractor.NewNRKTVDirekte(),
		extractor.NewNRKTVSeason(),
		extractor.NewNRKTVSeries(),
		extractor.NewNRKTV(),
		extractor.NewNRKPlaylist(),
		extractor.NewNRK(),
		extractor.NewNhkVodIE(),
		extractor.NewNhkVodProgramIE(),
		extractor.NewNhkForSchoolProgramListIE(),
		extractor.NewNhkForSchoolSubjectIE(),
		extractor.NewNhkForSchoolBangumiIE(),
		extractor.NewNhkRadiruLiveIE(),
		extractor.NewNhkRadioNewsPageIE(),
		extractor.NewNhkRadiruIE(),
		// Twitch adapters are registered from narrowest overlap boundary to
		// broad live-channel semantics. All seven reuse internal/extractor/twitch.go.
		extractor.NewTwitchVodIE(),
		extractor.NewTwitchCollectionIE(),
		extractor.NewTwitchVideosClipsIE(),
		extractor.NewTwitchVideosCollectionsIE(),
		extractor.NewTwitchVideosIE(),
		extractor.NewTwitchClipsIE(),
		extractor.NewTwitchStreamIE(),
		extractor.NewSoundCloudSearch(),
		extractor.NewSoundCloudEmbed(),
		extractor.NewSoundCloud(),
		extractor.NewApplePodcasts(),
		extractor.NewStreamable(),
		extractor.NewPeerTubePlaylist(),
		extractor.NewPeerTube(),
		extractor.NewInternetArchive(),
		extractor.NewBluesky(),
		extractor.NewImgur(),
		extractor.NewFlickr(),
		extractor.NewAeonCo(),
		extractor.NewTedEmbed(),
		extractor.NewTedTalk(),
		extractor.NewTedSeries(),
		extractor.NewTedPlaylist(),
		extractor.NewMicrosoftEmbed(),
		extractor.NewMicrosoftMedius(),
		extractor.NewMicrosoftLearnPlaylist(),
		extractor.NewMicrosoftLearnEpisode(),
		extractor.NewMicrosoftLearnSession(),
		extractor.NewMicrosoftBuild(),
		extractor.NewRegionSVT(),
		extractor.NewSyntheticAuth(),
	}
	for _, installed := range client.plugins {
		if installed != nil {
			registered = append(registered, &installedPluginExtractor{installed: installed, approver: client.pluginApprover})
		}
	}
	registered = append(registered,
		extractor.NewAmara(),
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
	client                           *Client
	request                          Request
	transport                        *network.Client
	registry                         *extractor.Registry
	solver                           extractor.YouTubeChallengeSolver
	archive                          *archive.Store
	cache                            *cache.Store
	credentials                      extractor.CredentialProvider
	compatibility                    compatibilityPlan
	rootExtractor                    *string
	playlistItemsRangeWarningEmitted bool
	playlistOrderingWarningsEmitted  map[string]bool
	breakMatchTriggered              bool
	breakMatchReason                 string
	removeFile                       func(string) error
	thumbnailConvert                 thumbnailConvertFunc
	thumbnailEmbed                   thumbnailEmbedFunc
	hlsFallback                      func(context.Context, string, string, string, http.Header, bool, events.Sink) (fragment.Result, error)
	youtubeLiveRefresh               func(mediaformat.Selection) youtubelive.LiveRefreshFunc
	sabrMerge                        func(ctx context.Context, video, audio, destination string, overwrite bool, sink events.Sink) error
	plannerCapabilities              *mediaformat.PlannerCapabilities
	formatAvailability               mediaformat.FormatAvailability
	formatAvailabilityChecker        *formatAvailabilityChecker
}

func (operation *operation) process(ctx context.Context, rawURL, extractorKey string, overlay *extractor.Entry, ancestors map[string]bool, depth int) (Result, error) {
	return operation.processWithTransparentParent(ctx, rawURL, extractorKey, overlay, ancestors, depth, value.Info{})
}

func (operation *operation) processWithTransparentParent(ctx context.Context, rawURL, extractorKey string, overlay *extractor.Entry, ancestors map[string]bool, depth int, transparentParent value.Info) (Result, error) {
	referer := ""
	if overlay != nil {
		referer = overlay.Referer
	}
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
		URL: rawURL, Referer: referer, Transport: operation.transport, ChallengeSolver: operation.solver, Credentials: operation.credentials,
		VideoPassword: operation.request.VideoPassword,
		YouTubePOT:    operation.client.youtubePOT, YouTubeTranslatedCaptions: operation.request.YouTubeTranslatedCaptions,
		YouTubeLiveFromStart: operation.request.LiveFromStart,
		YouTubeComments: extractor.YouTubeCommentOptions{
			Enabled:             operation.request.YouTubeComments.Enabled,
			Sort:                operation.request.YouTubeComments.Sort,
			MaxComments:         operation.request.YouTubeComments.MaxComments,
			MaxParents:          operation.request.YouTubeComments.MaxParents,
			MaxReplies:          operation.request.YouTubeComments.MaxReplies,
			MaxRepliesPerThread: operation.request.YouTubeComments.MaxRepliesPerThread,
			MaxDepth:            operation.request.YouTubeComments.MaxDepth,
		},
		SoundCloudComments: extractor.SoundCloudCommentOptions{
			Enabled:     operation.request.SoundCloudComments.Enabled,
			Sort:        operation.request.SoundCloudComments.Sort,
			MaxComments: operation.request.SoundCloudComments.MaxComments,
		},
		NHK: extractor.NHKOptions{
			RadiruArea: operation.request.NHK.RadiruArea,
		},
		NoPlaylist: operation.request.Playlist.Disabled,
	})
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

// applyTransparentOverlay writes every supported producer-side metadata field
// from a transparent overlay onto an info object. The producer's ID
// overrides whatever the child supplied, producer values override every
// other field when set, and Has* flags preserve explicit zero numeric
// values across the recursion step.
func applyTransparentOverlay(info *value.Info, overlay *extractor.Entry) {
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
func overlayOntoEntry(entry *extractor.Entry, overlay *extractor.Entry) {
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

func (operation *operation) processPlaylist(ctx context.Context, extracted extractor.Extraction, extractorName string, ancestors map[string]bool, depth int) (Result, error) {
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
	playlistID, _ := extracted.Info.ID()
	playlistTitle, _ := extracted.Info.Title()
	var failures int
	var maxFailuresEmitted bool
	finish := func() (Result, error) {
		result, err := operation.finishPlaylistResult(ctx, extracted.Info, extractorName, children, entryValues)
		result.SuppressedFailures += failures
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
			entryErr := categorized(extractorName+" playlist entry", extractor.ErrInvalidPlaylist)
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
			child, _, _, terminal, err := operation.prepareMediaResult(ctx, &entryInfo, entry.ExtractorKey, true)
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
				prints, err := operation.capturePrints(ctx, PrintVideo, entryInfo, nil, nil, "")
				if err != nil {
					return Result{}, fmt.Errorf("flat playlist entry %d print: %w", selected.SourceIndex, err)
				}
				child.Prints = append(child.Prints, prints...)
				printArtifacts, printBytes, err := operation.writePrintFiles(ctx, PrintVideo, entryInfo, nil, nil, "")
				if err != nil {
					return Result{}, fmt.Errorf("flat playlist entry %d print file: %w", selected.SourceIndex, err)
				}
				addPrintFileArtifacts(&child, printArtifacts, printBytes)
			}
			children = append(children, child)
			entryValues = append(entryValues, value.ObjectValue(entryInfo.Fields()))
			continue
		}
		child, err := operation.process(ctx, entry.URL, entry.ExtractorKey, &entry, ancestors, depth+1)
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
	result := Result{
		InfoJSON: encoded, Extractor: extractorName, Entries: children,
		Stopped: operation.breakMatchTriggered, StopReason: operation.breakMatchReason,
	}
	result.Artifacts = append(result.Artifacts, thumbnailArtifacts...)
	result.Bytes += thumbnailBytes
	result.Downloaded = len(thumbnailArtifacts) > 0
	for _, child := range children {
		result.Bytes += child.Bytes
		result.Downloaded = result.Downloaded || child.Downloaded
		result.Archived = result.Archived || child.Archived
		result.SuppressedFailures += child.SuppressedFailures
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

func flatPlaylistEntryInfo(entry extractor.Entry, index int, playlistID, playlistTitle string) value.Info {
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
	if operation.playlistItemsRangeWarningEmitted || !playlistItemsOverrideRange(operation.request.Playlist) {
		return nil
	}
	operation.playlistItemsRangeWarningEmitted = true
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
	if operation.playlistOrderingWarningsEmitted == nil {
		operation.playlistOrderingWarningsEmitted = make(map[string]bool, len(warnings))
	}
	for _, warning := range warnings {
		if operation.playlistOrderingWarningsEmitted[warning] {
			continue
		}
		if err := operation.client.emit(ctx, Event{Kind: EventMetadataWarning, Message: warning}); err != nil {
			return &Error{Category: ErrorInternal, Op: "emit playlist ordering warning", Err: err}
		}
		operation.playlistOrderingWarningsEmitted[warning] = true
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
	Entry       extractor.Entry
	SourceIndex int
}

type indexedPlaylistEntryIterator interface {
	Next(context.Context) (indexedPlaylistEntry, bool, error)
}

func newPlaylistEntryIterator(source extractor.EntryIterator, options PlaylistOptions) (indexedPlaylistEntryIterator, error) {
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
	source      extractor.EntryIterator
	start       int
	end         int
	sourceIndex int
	done        bool
}

func newSelectedPlaylistIterator(source extractor.EntryIterator, options PlaylistOptions) *selectedPlaylistIterator {
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
			return indexedPlaylistEntry{}, false, extractor.ErrPlaylistLimit
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
		return value.Missing(), &Error{Category: ErrorInternal, Op: "decode playlist entry metadata", Err: extractor.ErrInvalidMetadata}
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
		return result, archive.Identity{}, compatibilityDecision{}, terminal, nil
	}
	if operation.archive == nil {
		return result, archive.Identity{}, decision, false, nil
	}
	id, hasID := info.ID()
	if !hasID || extractorName == "" {
		if incomplete {
			return result, archive.Identity{}, compatibilityDecision{}, false, nil
		}
		return Result{}, archive.Identity{}, compatibilityDecision{}, false, categorized("build archive identity", archive.ErrInvalidIdentity)
	}
	archiveIdentity, err := archive.NewIdentity(extractorName, id)
	if err != nil {
		return Result{}, archive.Identity{}, compatibilityDecision{}, false, categorized("build archive identity", err)
	}
	legacyIDs, err := oldArchiveIDs(*info)
	if err != nil {
		return Result{}, archive.Identity{}, compatibilityDecision{}, false, categorized("read legacy archive identities", err)
	}
	matched, found, err := operation.archive.Match(ctx, archiveIdentity, legacyIDs)
	if err != nil {
		return Result{}, archive.Identity{}, compatibilityDecision{}, false, categorized("match download archive", err)
	}
	if !found {
		return result, archiveIdentity, decision, false, nil
	}
	result.Archived = true
	if err := operation.client.emit(ctx, Event{
		Kind: EventArchiveMatch, Extractor: extractorName, Message: matched,
	}); err != nil {
		return Result{}, archive.Identity{}, compatibilityDecision{}, false, &Error{
			Category: ErrorInternal, Op: "emit archive event", Err: err,
		}
	}
	return result, archiveIdentity, compatibilityDecision{}, true, nil
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
	if operation.breakMatchTriggered {
		result.Stopped, result.StopReason = true, operation.breakMatchReason
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

func (operation *operation) processMedia(ctx context.Context, extracted extractor.Extraction, extractorName string) (Result, error) {
	preparedFormats, err := mediaformat.Prepare(extracted.Info, operation.compatibility.formatOptions)
	if err != nil {
		return Result{}, categorized("normalize formats", err)
	}
	info := preparedFormats.Info()
	if operation.request.Thumbnails.List {
		if _, err := selectThumbnails(&info); err != nil {
			return Result{}, categorized("normalize thumbnails", err)
		}
	}
	preProcessPrints, err := operation.capturePrints(ctx, PrintPreProcess, info, nil, nil, "")
	if err != nil {
		return Result{}, categorized("render pre-process print", err)
	}
	preProcessArtifacts, preProcessBytes, err := operation.writePrintFiles(ctx, PrintPreProcess, info, nil, nil, "")
	if err != nil {
		return Result{}, categorized("write pre-process print file", err)
	}
	result, archiveIdentity, interactiveDecision, terminal, err := operation.prepareMediaResult(ctx, &info, extractorName, false)
	if err != nil {
		return Result{}, err
	}
	result.Prints = append(result.Prints, preProcessPrints...)
	addPrintFileArtifacts(&result, preProcessArtifacts, preProcessBytes)
	if terminal {
		if !result.Skipped {
			prints, printErr := operation.capturePrints(ctx, PrintAfterFilter, info, nil, nil, "")
			if printErr != nil {
				return Result{}, categorized("render after-filter print", printErr)
			}
			result.Prints = append(result.Prints, prints...)
			printArtifacts, printBytes, printErr := operation.writePrintFiles(ctx, PrintAfterFilter, info, nil, nil, "")
			if printErr != nil {
				return Result{}, categorized("write after-filter print file", printErr)
			}
			addPrintFileArtifacts(&result, printArtifacts, printBytes)
		}
		return result, nil
	}
	prints, err := operation.capturePrints(ctx, PrintAfterFilter, info, nil, nil, "")
	if err != nil {
		return Result{}, categorized("render after-filter print", err)
	}
	result.Prints = append(result.Prints, prints...)
	printArtifacts, printBytes, err := operation.writePrintFiles(ctx, PrintAfterFilter, info, nil, nil, "")
	if err != nil {
		return Result{}, categorized("write after-filter print file", err)
	}
	addPrintFileArtifacts(&result, printArtifacts, printBytes)
	if extracted.Enrich != nil {
		if err := extracted.Enrich(ctx, &info); err != nil {
			return Result{}, categorized(extractorName+" deferred metadata", err)
		}
		result.InfoJSON, err = encodeInfo(info)
		if err != nil {
			return Result{}, err
		}
	}
	if err := operation.enrichWithSponsorBlock(ctx, extractorName, &info); err != nil {
		return Result{}, err
	}
	selectedSubtitles, requestedSubtitles, err := selectSubtitles(info, operation.request.Subtitles)
	if err != nil {
		return Result{}, categorized("select subtitles", err)
	}
	if requestedSubtitles != nil {
		info.Set("requested_subtitles", value.ObjectValue(requestedSubtitles))
	}
	result.InfoJSON, err = encodeInfo(info)
	if err != nil {
		return Result{}, err
	}
	// Metadata actions and deferred enrichment mutate the canonical Info after
	// Prepare; rebind evaluation objects so selection matches InfoJSON.
	preparedFormats = preparedFormats.SyncInfo(info)
	var selectedFormats []mediaformat.Selection
	var outputPlans []mediaformat.OutputPlan
	needsInteractiveFormat := interactiveDecision.interactive != interactiveMatchFilterNone ||
		operation.compatibility.interactiveFormat
	if (!operation.request.SkipDownload && !operation.request.Simulate) ||
		operation.hasPrintStageAtOrAfter(PrintVideo) || needsInteractiveFormat {
		outputPlans, err = operation.planPreparedFormatsContext(ctx, preparedFormats)
		if err != nil {
			return Result{}, categorized("select format", err)
		}
		if len(outputPlans) > 0 {
			selectedFormats = outputPlans[0].Tracks
		}
		if err := validateMultiOutputProduct(operation.request, len(outputPlans)); err != nil {
			return Result{}, categorized("select format", err)
		}
		if err := validateOutputPlans(outputPlans, operation.mergeOutputPreferences()); err != nil {
			return Result{}, categorized("select format", err)
		}
	}
	var singlePrintPlan *mediaformat.OutputPlan
	if len(outputPlans) == 1 && len(outputPlans[0].Tracks) > 1 {
		singlePrintPlan = &outputPlans[0]
	}
	operation.applyThumbnailEmbeddingOutputExtension(&info, selectedFormats)
	planDestinations, err := operation.resolveOutputPlanDestinations(info, outputPlans)
	if err != nil {
		return Result{}, categorized("render output template", err)
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
				return Result{}, categorized("preflight output destinations", err)
			}
		}
	}
	ctx = withMediaTransaction(ctx, mediaTx)
	if needsInteractiveFormat {
		if len(planDestinations) == 0 {
			return Result{}, categorized("select format", mediaformat.ErrNoFormats)
		}
		for index, plan := range outputPlans {
			interactiveInfo := selectedPlanInfo(info, plan)
			operation.applyThumbnailEmbeddingOutputExtension(&interactiveInfo, plan.Tracks)
			resolved, resolveErr := operation.resolveInteractiveCompatibility(
				ctx, interactiveInfo, interactiveDecision, planDestinations[index],
			)
			if resolveErr != nil {
				return rollbackTransactionResult(mediaTx, resolveErr)
			}
			terminal, finishErr := operation.finishMatchFilterDecision(ctx, &result, extractorName, resolved)
			if finishErr != nil {
				return rollbackTransactionResult(mediaTx, finishErr)
			}
			if terminal {
				return result, nil
			}
		}
	}
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

	// PR 8 routes every output plan through the per-output lifecycle
	// abstraction. The branch is positioned after
	// validatePrintRules so the existing pre-download sidecar
	// writes (PrintVideo, thumbnails, related, PrintBeforeDL,
	// subtitles, converted subtitles) are bypassed and re-driven once
	// per plan by executePlanLifecycle.
	if len(outputPlans) > 0 {
		if !operation.request.Simulate && !operation.request.SkipDownload {
			if err := mediaTx.acquireDestinationBackups(planDestinations, operation.request.Overwrite); err != nil {
				return rollbackTransactionResult(mediaTx, categorized("prepare output destinations", err))
			}
		}
		sink := operation.eventSink()
		planResults := make([]Result, len(outputPlans))
		for index, plan := range outputPlans {
			lifecycle := newOutputLifecycleForPlan(index, plan, info, planDestinations[index])
			operation.applyThumbnailEmbeddingOutputExtension(&lifecycle.Info, plan.Tracks)
			planResult, lifecycleErr := operation.executePlanLifecycle(
				ctx, mediaTx, &lifecycle, selectedSubtitles, sink,
			)
			if lifecycleErr != nil {
				if len(outputPlans) > 1 {
					return rollbackTransactionResult(mediaTx, lifecycleErr)
				}
				return Result{}, lifecycleErr
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
				return Result{}, categorized("commit output transaction", commitErr)
			}
			mediaTx.finalize()
		}
		if operation.archive != nil && !operation.request.Simulate && !operation.request.SkipDownload {
			if _, archiveErr := operation.archive.Record(ctx, archiveIdentity); archiveErr != nil {
				return Result{}, categorized("record download archive", archiveErr)
			}
		}
		return result, nil
	}

	prints, err = operation.capturePrints(ctx, PrintVideo, info, singlePrintPlan, selectedFormats, destination)
	if err != nil {
		return rollbackTransactionResult(mediaTx, categorized("render video print", err))
	}
	result.Prints = append(result.Prints, prints...)
	printArtifacts, printBytes, err = operation.writePrintFiles(ctx, PrintVideo, info, singlePrintPlan, selectedFormats, destination)
	trackTransactionArtifacts(mediaTx, printArtifacts)
	if err != nil {
		return rollbackTransactionResult(mediaTx, categorized("write video print file", err))
	}
	addPrintFileArtifacts(&result, printArtifacts, printBytes)
	if operation.request.Simulate {
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
				return Result{}, categorized("render "+string(stage)+" print", err)
			}
			result.Prints = append(result.Prints, prints...)
			printArtifacts, printBytes, err = operation.writePrintFiles(ctx, stage, info, singlePrintPlan, selectedFormats, destination)
			if err != nil {
				return Result{}, categorized("write "+string(stage)+" print file", err)
			}
			addPrintFileArtifacts(&result, printArtifacts, printBytes)
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
			return Result{}, categorized("commit output transaction", commitErr)
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
			return Result{}, categorized("commit output transaction", commitErr)
		}
		mediaTx.finalize()
	}
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
	var existing *Error
	if errors.As(err, &existing) && existing.Category != "" {
		return &Error{Category: existing.Category, Op: op, Err: err}
	}
	category := ErrorNetwork
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		category = ErrorCancelled
	case errors.Is(err, extractor.ErrUnsupported), errors.Is(err, mediaformat.ErrMultiOutput):
		category = ErrorUnsupported
	case errors.Is(err, extractor.ErrAuthentication), errors.Is(err, extractor.ErrWrongPassword), errors.Is(err, extractor.ErrTwitchSubscriberOnly):
		category = ErrorAuthentication
	case errors.Is(err, extractor.ErrVKAuthentication):
		category = ErrorAuthentication
	case errors.Is(err, extractor.ErrVKUnsafeAsset):
		category = ErrorSecurity
	case errors.Is(err, credentialnetrc.ErrUnsafeFile):
		category = ErrorSecurity
	case errors.Is(err, errUnsafePrintFile):
		category = ErrorSecurity
	case errors.Is(err, ErrFormatCheckLimit):
		category = ErrorInvalidInput
	case errors.Is(err, errUnsafeThumbnailRedirect):
		category = ErrorSecurity
	case errors.Is(err, errNiconicoMediaHost), errors.Is(err, errNiconicoMediaRedirect):
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
	case errors.Is(err, extractor.ErrUnavailable), errors.Is(err, extractor.ErrRegionRestricted), errors.Is(err, extractor.ErrChallengeSolver),
		errors.Is(err, extractor.ErrVKUnavailable), errors.Is(err, extractor.ErrVKRegionRestricted), errors.Is(err, extractor.ErrVKNotLive),
		errors.Is(err, extractor.ErrTransportProfile), errors.Is(err, extractor.ErrTransportIsolation), errors.Is(err, network.ErrImpersonationUnavailable):
		category = ErrorUnsupported
	case errors.Is(err, extractor.ErrVKRateLimited), errors.Is(err, extractor.ErrVKNetwork), errors.Is(err, extractor.ErrVKInvalidStatus), errors.Is(err, extractor.ErrVKRepeatedPage):
		category = ErrorNetwork
	case errors.Is(err, ffmpeg.ErrFFmpegUnavailable), errors.Is(err, ffmpeg.ErrFFprobeUnavailable),
		errors.Is(err, ffmpeg.ErrUnsafeHLSHeaders):
		category = ErrorUnsupported
	case errors.Is(err, downloader.ErrExternalUnavailable), errors.Is(err, hls.ErrUnsupportedEncryption),
		errors.Is(err, dash.ErrUnsupportedTimeline), errors.Is(err, dash.ErrUnsupportedAddressing),
		errors.Is(err, hds.ErrUnsupportedLive), errors.Is(err, hds.ErrUnsupportedDRM),
		errors.Is(err, hds.ErrUnsupportedEmpty):
		category = ErrorUnsupported
	case errors.Is(err, outputtemplate.ErrInvalidTemplate), errors.Is(err, outputtemplate.ErrUnsafePath),
		errors.Is(err, errInvalidRequestOptions),
		errors.Is(err, chapterremove.ErrInvalidSpecification), errors.Is(err, chapterremove.ErrLimit),
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
		errors.Is(err, errInvalidBrowserCookieSpec), errors.Is(err, netscape.ErrMalformed), errors.Is(err, netscape.ErrFile),
		errors.Is(err, netscape.ErrWrongFormat), errors.Is(err, netscape.ErrTooLarge),
		errors.Is(err, firefox.ErrUnsafePath), errors.Is(err, firefox.ErrLimit),
		errors.Is(err, safari.ErrUnsafePath), errors.Is(err, safari.ErrLimit),
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
	case errors.Is(err, mediaformat.ErrNoFormats), errors.Is(err, mediaformat.ErrInvalidFormats), errors.Is(err, mediaformat.ErrFormatLimit), errors.Is(err, extractor.ErrInvalidMetadata),
		errors.Is(err, extractor.ErrInvalidPlaylist), errors.Is(err, extractor.ErrPlaylistLimit),
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
func (client *Client) sharedChallengeSolver() *lazyYouTubeSolver {
	client.solverMu.Lock()
	defer client.solverMu.Unlock()
	if client.sharedSolver == nil {
		client.sharedSolver = &lazyYouTubeSolver{path: discoverJavaScriptHelper(client.javascriptHelper)}
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

type lazyYouTubeSolver struct {
	mu         sync.Mutex
	path       string
	supervisor *supervisor.Client
	solver     *ejs.Solver
	active     sync.WaitGroup // tracks in-flight SolvePlayer calls
	closed     bool
}

func (solver *lazyYouTubeSolver) SolvePlayer(
	ctx context.Context,
	id string,
	player string,
	requests []ejs.ChallengeRequest,
	outputPreprocessed bool,
) (ejs.Result, error) {
	solver.mu.Lock()
	if solver.closed {
		solver.mu.Unlock()
		return ejs.Result{}, errors.New("solver is closed")
	}
	if solver.solver == nil {
		scriptHash, hashErr := ejs.BundledScriptHash()
		if hashErr != nil {
			solver.mu.Unlock()
			return ejs.Result{}, hashErr
		}
		client, err := supervisor.New(supervisor.Config{
			Path: solver.path, MemoryBytes: ejs.SolverMemoryBytes,
			TrustedScriptHash: scriptHash,
		})
		if err != nil {
			solver.mu.Unlock()
			return ejs.Result{}, err
		}
		challengeSolver, err := ejs.New(client)
		if err != nil {
			_ = client.Close()
			solver.mu.Unlock()
			return ejs.Result{}, err
		}
		solver.supervisor, solver.solver = client, challengeSolver
	}
	solver.active.Add(1)
	activeSolver := solver.solver
	solver.mu.Unlock()

	defer solver.active.Done()
	return activeSolver.SolvePlayer(ctx, id, player, requests, outputPreprocessed)
}

// Close waits for active operations to complete, then shuts down the
// supervisor. It is safe to call multiple times.
func (solver *lazyYouTubeSolver) Close() {
	if solver == nil {
		return
	}
	solver.mu.Lock()
	solver.closed = true
	sup := solver.supervisor
	solver.supervisor = nil
	solver.solver = nil
	solver.mu.Unlock()

	// Wait for in-flight operations to finish before killing the helper.
	solver.active.Wait()
	if sup != nil {
		_ = sup.Close()
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
