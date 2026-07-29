// Package cli implements the command-line boundary.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	compatconfig "github.com/ytdlp-go/ytdlp/internal/compat/config"
	"github.com/ytdlp-go/ytdlp/internal/sponsorblock"
	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

// Version is overridden with -X for release artifacts.
var Version = "0.0.0-dev"

func Run(args []string, stdout, stderr io.Writer) int {
	return RunContextIO(context.Background(), args, os.Stdin, stdout, stderr)
}

func RunContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return RunContextIO(ctx, args, os.Stdin, stdout, stderr)
}

// RunContextIO exposes the CLI input boundary for deterministic embedding and
// tests. The production command passes os.Stdin through RunContext.
func RunContextIO(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runContextIOWithDependencies(ctx, args, stdin, stdout, stderr, runDependencies{
		newRunner: func(options []ytdlp.Option) cliRunner { return ytdlp.NewClient(options...) },
	})
}

type cliRunner interface {
	Run(context.Context, ytdlp.Request) (ytdlp.Result, error)
}

type runDependencies struct {
	newRunner func([]ytdlp.Option) cliRunner
}

func runContextIOWithDependencies(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, deps runDependencies) int {
	if deps.newRunner == nil {
		return 1
	}
	environment := compatconfig.RuntimeEnvironment()
	environment.HomeConfigDir = homePathFromArgs(args)
	loaded, err := compatconfig.Load(ctx, compatconfig.Request{
		Environment: environment, CommandLine: args, IncludeDefaults: true, Stdin: stdin,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return 130
		}
		return 2
	}
	args = loaded.Arguments
	args, printFileSpecifications, err := extractPrintToFileArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
		return 2
	}
	args, err = extractReplaceMetadataArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
		return 2
	}
	flags := flag.NewFlagSet("ytdlp-go", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: ytdlp-go [OPTIONS] URL")
		fmt.Fprintln(flags.Output())
		fmt.Fprintln(flags.Output(), "Experimental Python-free Go implementation informed by yt-dlp behavior.")
		fmt.Fprintln(flags.Output())
		fmt.Fprintln(flags.Output(), "  --print-to-file [WHEN:]TEMPLATE FILE")
		fmt.Fprintln(flags.Output(), "        append a rendered template line to a confined file")
		flags.PrintDefaults()
	}

	showVersion := flags.Bool("version", false, "print the version and exit")
	var outputTemplates outputTemplateFlag
	flags.Var(&outputTemplates, "output", "output filename template, optionally TYPES:TEMPLATE (repeatable)")
	flags.Var(&outputTemplates, "o", "alias for --output")
	outputDir := flags.String("output-dir", ".", "directory that confines output files")
	paths := &outputPathFlag{home: outputDir}
	flags.Var(paths, "paths", "set an output path, optionally TYPES:PATH (repeatable)")
	flags.Var(paths, "P", "alias for --paths")
	writeInfoJSON := flags.Bool("write-info-json", false, "write video metadata to a .info.json sidecar (may contain personal information)")
	flags.BoolFunc("no-write-info-json", "disable writing metadata sidecars", func(string) error {
		*writeInfoJSON = false
		return nil
	})
	writeDescription := flags.Bool("write-description", false, "write video descriptions to .description sidecars")
	flags.BoolFunc("no-write-description", "disable writing description sidecars", func(string) error {
		*writeDescription = false
		return nil
	})
	writeLink := flags.Bool("write-link", false, "write a platform-native internet shortcut")
	writeURLLink := flags.Bool("write-url-link", false, "write a Windows .url internet shortcut")
	writeWeblocLink := flags.Bool("write-webloc-link", false, "write a macOS .webloc internet shortcut")
	writeDesktopLink := flags.Bool("write-desktop-link", false, "write a Linux .desktop internet shortcut")
	noPlaylistMetafiles := flags.Bool("no-write-playlist-metafiles", false, "omit playlist metadata sidecars")
	flags.BoolFunc("write-playlist-metafiles", "write playlist metadata sidecars (default)", func(string) error {
		*noPlaylistMetafiles = false
		return nil
	})
	var thumbnailMode thumbnailModeFlag
	flags.BoolFunc("write-thumbnail", "write the best thumbnail image", func(input string) error {
		return thumbnailMode.setBest(input)
	})
	flags.BoolFunc("write-all-thumbnails", "write every available thumbnail image", func(input string) error {
		return thumbnailMode.setAll(input)
	})
	flags.BoolFunc("no-write-thumbnail", "disable thumbnail sidecars (default)", func(input string) error {
		return thumbnailMode.clear(input)
	})
	convertThumbnails := flags.String("convert-thumbnails", "none", "convert written thumbnails using jpg/png/webp mapping rules (none disables)")
	embedThumbnail := flags.Bool("embed-thumbnail", false, "embed the best thumbnail as media cover art")
	flags.BoolFunc("no-embed-thumbnail", "disable thumbnail embedding (default)", func(string) error {
		*embedThumbnail = false
		return nil
	})
	listThumbnails := flags.Bool("list-thumbnails", false, "list available thumbnails (simulates unless --no-simulate)")
	printJSON := flags.Bool("print-json", false, "print normalized metadata JSON to stdout")
	dumpJSON := flags.Bool("dump-json", false, "quietly print one JSON object per video (simulates unless --no-simulate)")
	flags.BoolVar(dumpJSON, "j", false, "alias for --dump-json")
	dumpSingleJSON := flags.Bool("dump-single-json", false, "quietly print one JSON object for the complete URL result (simulates unless --no-simulate)")
	flags.BoolVar(dumpSingleJSON, "J", false, "alias for --dump-single-json")
	var printTemplates stringListFlag
	flags.Var(&printTemplates, "print", "print a field or [WHEN:]output template (repeatable)")
	flags.Var(&printTemplates, "O", "alias for --print")
	getURL := flags.Bool("get-url", false, "print selected media URL(s)")
	flags.BoolVar(getURL, "g", false, "alias for --get-url")
	getTitle := flags.Bool("get-title", false, "print the video title")
	flags.BoolVar(getTitle, "e", false, "alias for --get-title")
	getID := flags.Bool("get-id", false, "print the video ID")
	getThumbnail := flags.Bool("get-thumbnail", false, "print the thumbnail URL when available")
	getDescription := flags.Bool("get-description", false, "print the description when available")
	getDuration := flags.Bool("get-duration", false, "print the duration when available")
	getFilename := flags.Bool("get-filename", false, "print the prepared output filename")
	getFormat := flags.Bool("get-format", false, "print the selected format ID(s)")
	listSubtitles := flags.Bool("list-subs", false, "list available subtitles and automatic captions (simulates unless --no-simulate)")
	var simulate, simulateSet bool
	setSimulation := func(enabled bool) func(string) error {
		return func(input string) error {
			value, err := strconv.ParseBool(input)
			if err != nil {
				return err
			}
			simulate, simulateSet = enabled == value, true
			return nil
		}
	}
	flags.BoolFunc("simulate", "do not download media or write output artifacts", setSimulation(true))
	flags.BoolFunc("s", "alias for --simulate", setSimulation(true))
	flags.BoolFunc("no-simulate", "download even when a listing option is used", setSimulation(false))
	skipDownload := flags.Bool("skip-download", false, "extract metadata without downloading")
	flags.BoolVar(skipDownload, "no-download", false, "alias for --skip-download")
	liveFromStart := flags.Bool("live-from-start", false, "download supported live streams from their beginning")
	flags.BoolFunc("no-live-from-start", "download live streams from the current edge (default)", func(string) error {
		*liveFromStart = false
		return nil
	})
	proxy := flags.String("proxy", "", "HTTP/HTTPS proxy URL")
	impersonationProfile := flags.String("impersonate", "", "default explicit browser profile (for example firefox-120)")
	timeout := flags.Duration("socket-timeout", 30*time.Second, "network operation timeout")
	overwrite := flags.Bool("force-overwrites", false, "replace an existing final file")
	progressJSON := flags.Bool("progress-json", false, "write newline-delimited progress events to stderr")
	telemetryJSON := flags.Bool("telemetry-json", false, "write one privacy-safe aggregate telemetry snapshot to stdout")
	var quiet, quietSet bool
	setQuiet := func(enabled bool) func(string) error {
		return func(input string) error {
			value, err := strconv.ParseBool(input)
			if err != nil {
				return err
			}
			quiet, quietSet = enabled == value, true
			return nil
		}
	}
	flags.BoolFunc("quiet", "suppress human-readable progress", setQuiet(true))
	flags.BoolFunc("no-quiet", "show human-readable progress when metadata output would imply quiet", setQuiet(false))
	javascriptHelper := flags.String("js-helper", "", "path to the isolated JavaScript helper")
	cookieFile := flags.String("cookies", "", "load cookies from a Netscape cookies.txt file")
	cookiesFromBrowser := flags.String("cookies-from-browser", "", "import cookies from Safari, Firefox, or a supported platform Chromium browser")
	useNetRC := flags.Bool("netrc", false, "use credentials from a native .netrc file")
	netRCLocation := flags.String("netrc-location", "", "path to .netrc or its containing directory")
	videoPassword := flags.String("video-password", "", "per-video password for extractors that gate media behind a site secret; never echoed in errors, events, or metadata")
	downloadArchive := flags.String("download-archive", "", "record and skip downloaded extractor IDs")
	flags.BoolFunc("no-download-archive", "disable an inherited download archive", func(string) error {
		*downloadArchive = ""
		return nil
	})
	cacheDir := flags.String("cache-dir", "", "directory for bounded compatibility cache entries")
	flags.BoolFunc("no-cache-dir", "disable an inherited cache directory", func(string) error {
		*cacheDir = ""
		return nil
	})
	playlistStart := flags.Int("playlist-start", 1, "first one-based playlist entry to process")
	playlistEnd := flags.Int("playlist-end", 0, "last one-based playlist entry to process (0 or -1 means all)")
	playlistReverse := flags.Bool("playlist-reverse", false, "process the selected playlist entries in reverse order")
	flags.BoolFunc("no-playlist-reverse", "disable inherited reverse playlist order", func(string) error {
		*playlistReverse = false
		return nil
	})
	playlistItems := flags.String("playlist-items", "", "comma-separated playlist indexes or START:END:STEP ranges")
	flags.StringVar(playlistItems, "I", "", "alias for --playlist-items")
	flatPlaylist := flags.Bool("flat-playlist", false, "list playlist entries without recursively extracting them")
	flags.BoolFunc("no-flat-playlist", "fully extract playlist entries (default)", func(string) error {
		*flatPlaylist = false
		return nil
	})
	format := flags.String("format", "", "format selector expression")
	flags.StringVar(format, "f", "", "alias for --format")
	var formatSort formatSortFlag
	var matchFilters, breakMatchFilters stringListFlag
	var metadataActions metadataActionFlag
	flags.Var(&formatSort, "format-sort", "format sort field (repeatable)")
	flags.Var(&formatSort, "S", "alias for --format-sort")
	flags.BoolFunc("format-sort-reset", "disregard preceding format sort fields", func(input string) error {
		enabled, err := strconv.ParseBool(input)
		if err != nil {
			return err
		}
		if enabled {
			formatSort = nil
		}
		return nil
	})
	formatSortForce := false
	setFormatSortForce := func(enabled bool) func(string) error {
		return func(input string) error {
			value, err := strconv.ParseBool(input)
			if err != nil {
				return err
			}
			formatSortForce = enabled == value
			return nil
		}
	}
	flags.BoolFunc("format-sort-force", "give user format sort fields full precedence", setFormatSortForce(true))
	flags.BoolFunc("S-force", "alias for --format-sort-force", setFormatSortForce(true))
	flags.BoolFunc("no-format-sort-force", "retain mandatory format sort field precedence (default)", setFormatSortForce(false))
	allowMultipleVideoStreams := false
	allowMultipleAudioStreams := false
	setMultistream := func(target *bool, enabled bool) func(string) error {
		return func(input string) error {
			value, err := strconv.ParseBool(input)
			if err != nil {
				return err
			}
			*target = enabled == value
			return nil
		}
	}
	flags.BoolFunc("video-multistreams", "allow multiple video streams in one output", setMultistream(&allowMultipleVideoStreams, true))
	flags.BoolFunc("no-video-multistreams", "allow only one video stream in one output (default)", setMultistream(&allowMultipleVideoStreams, false))
	flags.BoolFunc("audio-multistreams", "allow multiple audio streams in one output", setMultistream(&allowMultipleAudioStreams, true))
	flags.BoolFunc("no-audio-multistreams", "allow only one audio stream in one output (default)", setMultistream(&allowMultipleAudioStreams, false))
	flags.BoolFunc("all-formats", "select every available format (alias for -f all)", func(input string) error {
		enabled, err := strconv.ParseBool(input)
		if err != nil {
			return err
		}
		if enabled {
			*format = "all"
		}
		return nil
	})
	preferFreeFormats := false
	flags.BoolFunc("prefer-free-formats", "prefer free containers when otherwise equivalent", setMultistream(&preferFreeFormats, true))
	flags.BoolFunc("no-prefer-free-formats", "do not prefer free containers (default)", setMultistream(&preferFreeFormats, false))
	allowUnplayable := false
	flags.BoolFunc("allow-unplayable-formats", "include DRM-marked formats in selection", setMultistream(&allowUnplayable, true))
	flags.BoolFunc("no-allow-unplayable-formats", "exclude DRM-marked formats (default)", setMultistream(&allowUnplayable, false))
	checkFormats := ytdlp.FormatCheckAuto
	flags.BoolFunc("check-formats", "select only bounded-probe-available formats", func(input string) error {
		enabled, err := strconv.ParseBool(input)
		if err != nil {
			return err
		}
		if enabled {
			checkFormats = ytdlp.FormatCheckSelected
		}
		return nil
	})
	flags.BoolFunc("check-all-formats", "probe every normalized format before selection", func(input string) error {
		enabled, err := strconv.ParseBool(input)
		if err != nil {
			return err
		}
		if enabled {
			checkFormats = ytdlp.FormatCheckAll
		}
		return nil
	})
	flags.BoolFunc("no-check-formats", "do not probe format availability", func(input string) error {
		enabled, err := strconv.ParseBool(input)
		if err != nil {
			return err
		}
		if enabled {
			checkFormats = ytdlp.FormatCheckNone
		}
		return nil
	})
	mergeOutputFormat := flags.String("merge-output-format", "", "preferred merge containers separated by / (for example mp4/mkv)")
	progressTemplate := flags.String("progress-template", "", "render download events with a bounded progress template")
	flags.Var(&matchFilters, "match-filter", `metadata filter expression, or "-" to prompt (repeatable OR)`)
	flags.Var(&matchFilters, "match-filters", "alias for --match-filter")
	flags.BoolFunc("no-match-filter", "clear inherited metadata filters", func(string) error {
		matchFilters = nil
		return nil
	})
	flags.BoolFunc("no-match-filters", "alias for --no-match-filter", func(string) error {
		matchFilters = nil
		return nil
	})
	flags.Var(&breakMatchFilters, "break-match-filter", `stopping metadata filter expression, or "-" to prompt (repeatable OR)`)
	flags.Var(&breakMatchFilters, "break-match-filters", "alias for --break-match-filter")
	flags.BoolFunc("no-break-match-filter", "clear inherited breaking metadata filters", func(string) error {
		breakMatchFilters = nil
		return nil
	})
	flags.BoolFunc("no-break-match-filters", "alias for --no-break-match-filter", func(string) error {
		breakMatchFilters = nil
		return nil
	})
	flags.Var(metadataParseFlag{actions: &metadataActions}, "parse-metadata", "[WHEN:]FROM:TO metadata action")
	flags.Var(metadataReplaceFlag{actions: &metadataActions}, "replace-in-metadata", "[WHEN:]FIELDS REGEX REPLACEMENT metadata action")
	retries := flags.Int("retries", 0, "direct and fragment download attempts (maximum 100)")
	retryBaseDelay := flags.Duration("retry-base-delay", 0, "deterministic initial retry delay")
	retryMaxDelay := flags.Duration("retry-max-delay", 0, "maximum retry delay")
	fragmentConcurrency := flags.Int("concurrent-fragments", 0, "parallel fragment downloads (maximum 128)")
	perHostFragments := flags.Int("per-host-fragments", 0, "parallel fragments per host (maximum 128)")
	maxSegments := flags.Int("max-segments", 0, "maximum fragments in a manifest (maximum 10000)")
	fileRetries := flags.Int("file-access-retries", 0, "file finalization retries (maximum 10)")
	throttleRestarts := flags.Int("throttle-restarts", 0, "low-speed restart count (maximum 10)")
	throttleWindow := flags.Duration("throttle-window", 0, "low-speed observation window")
	var rateLimit, maxBytes, throttleRate, maxFragmentBytes byteSizeFlag
	flags.Var(&rateLimit, "limit-rate", "maximum transfer rate in bytes/s (K, M, G suffixes supported)")
	flags.Var(&maxBytes, "max-download-bytes", "maximum direct download size")
	flags.Var(&throttleRate, "throttled-rate", "restart below this transfer rate")
	flags.Var(&maxFragmentBytes, "max-fragment-bytes", "maximum size of one fragment")
	externalDownloader := flags.String("downloader", "", "explicit shell-free external downloader executable")
	var externalArgs stringListFlag
	flags.Var(&externalArgs, "downloader-arg", "external downloader argv item (repeatable)")
	extractAudio := flags.Bool("extract-audio", false, "extract an audio-only output with ffmpeg")
	flags.BoolVar(extractAudio, "x", false, "alias for --extract-audio")
	audioFormat := flags.String("audio-format", "mp3", "audio codec/container for --extract-audio")
	audioBitrate := flags.String("audio-bitrate", "", "ffmpeg audio bitrate for --extract-audio")
	audioQuality := flags.Int("audio-quality", 0, "ffmpeg audio quality for --extract-audio")
	remuxVideo := flags.String("remux-video", "", "remux video to the selected container with ffmpeg")
	writeSubtitles := flags.Bool("write-subs", false, "write manual subtitle sidecar files")
	flags.BoolVar(writeSubtitles, "write-srt", false, "alias for --write-subs")
	flags.BoolFunc("no-write-subs", "disable writing manual subtitles", func(string) error {
		*writeSubtitles = false
		return nil
	})
	flags.BoolFunc("no-write-srt", "alias for --no-write-subs", func(string) error {
		*writeSubtitles = false
		return nil
	})
	writeAutomaticSubtitles := flags.Bool("write-auto-subs", false, "write automatic-caption sidecar files")
	flags.BoolVar(writeAutomaticSubtitles, "write-automatic-subs", false, "alias for --write-auto-subs")
	flags.BoolFunc("no-write-auto-subs", "disable writing automatic captions", func(string) error {
		*writeAutomaticSubtitles = false
		return nil
	})
	flags.BoolFunc("no-write-automatic-subs", "alias for --no-write-auto-subs", func(string) error {
		*writeAutomaticSubtitles = false
		return nil
	})
	embedSubtitles := flags.Bool("embed-subs", false, "embed selected subtitles in supported media containers")
	flags.BoolFunc("no-embed-subs", "disable subtitle embedding (default)", func(string) error {
		*embedSubtitles = false
		return nil
	})
	writeComments := flags.Bool("write-comments", false, "retrieve comments into metadata")
	flags.BoolVar(writeComments, "get-comments", false, "alias for --write-comments")
	flags.BoolFunc("no-write-comments", "disable comment retrieval", func(string) error {
		*writeComments = false
		return nil
	})
	flags.BoolFunc("no-get-comments", "alias for --no-write-comments", func(string) error {
		*writeComments = false
		return nil
	})
	youtubeMaxComments := flags.String("youtube-max-comments", "", "bounded YouTube limits TOTAL[,PARENTS[,REPLIES[,PER_THREAD[,DEPTH]]]]")
	youtubeCommentSort := flags.String("youtube-comment-sort", "new", "YouTube comment order: new or top")
	soundCloudMaxComments := flags.Int("soundcloud-max-comments", 0, "maximum SoundCloud track comments (default 100)")
	soundCloudCommentSort := flags.String("soundcloud-comment-sort", "newest", "SoundCloud comment order: newest, oldest, or track-timestamp")
	var sponsorBlockMark, sponsorBlockRemove []string
	var removeChapters stringListFlag
	var sponsorBlockForceKeyframes bool
	setSponsorBlockForceKeyframes := func(enabled bool) func(string) error {
		return func(input string) error {
			value, err := strconv.ParseBool(input)
			if err != nil {
				return err
			}
			sponsorBlockForceKeyframes = enabled == value
			return nil
		}
	}
	noSponsorBlock := false
	flags.Func("sponsorblock-mark", "SponsorBlock categories to mark as chapters (repeatable; comma-separated; all/default selects the pinned set; prefix with - to exclude)", func(value string) error {
		next, err := parseSponsorBlockMarkCategories(value, sponsorBlockMark)
		if err != nil {
			return err
		}
		sponsorBlockMark = next
		return nil
	})
	flags.Func("sponsorblock-remove", "SponsorBlock categories to remove from media (repeatable; comma-separated; all/default selects removable sets; prefix with - to exclude)", func(value string) error {
		next, err := parseSponsorBlockRemoveCategories(value, sponsorBlockRemove)
		if err != nil {
			return err
		}
		sponsorBlockRemove = next
		return nil
	})
	sponsorBlockAPI := flags.String("sponsorblock-api", "", "SponsorBlock API origin (default https://sponsor.ajay.app)")
	var sponsorBlockChapterTitle *string
	flags.Func("sponsorblock-chapter-title", "bounded output template for marked SponsorBlock chapter titles", func(input string) error {
		value := input
		sponsorBlockChapterTitle = &value
		return nil
	})
	flags.Var(&removeChapters, "remove-chapters", "remove chapters matching REGEX or manual *START-END ranges (repeatable)")
	flags.BoolFunc("no-remove-chapters", "clear inherited chapter and manual range removal", func(input string) error {
		enabled, err := strconv.ParseBool(input)
		if err != nil {
			return err
		}
		if enabled {
			removeChapters = nil
		}
		return nil
	})
	flags.BoolFunc("force-keyframes-at-cuts", "force keyframes at chapter, range, or SponsorBlock cut boundaries", setSponsorBlockForceKeyframes(true))
	flags.BoolFunc("no-force-keyframes-at-cuts", "disable forced keyframes at chapter, range, or SponsorBlock cut boundaries", setSponsorBlockForceKeyframes(false))
	// Match pinned yt-dlp: record --no-sponsorblock during parse, then clear
	// mark/remove after all options (config + CLI) are applied so order cannot
	// re-enable them. --sponsorblock-api is preserved.
	flags.BoolFunc("no-sponsorblock", "disable SponsorBlock mark and remove without clearing --sponsorblock-api", func(string) error {
		noSponsorBlock = true
		return nil
	})
	convertSubtitles := flags.String("convert-subs", "none", "convert written subtitle sidecars to srt, ass, or vtt (none disables)")
	flags.StringVar(convertSubtitles, "convert-sub", "none", "alias for --convert-subs")
	flags.StringVar(convertSubtitles, "convert-subtitles", "none", "alias for --convert-subs")
	subtitleFormat := flags.String("sub-format", "best", "subtitle format preference separated by / (for example srt/vtt/best)")
	allSubtitles := flags.Bool("all-subs", false, "select every available subtitle language (requires a subtitle write option)")
	var subtitleLanguages stringListFlag
	flags.Var(&subtitleLanguages, "sub-langs", "subtitle languages or regexes separated by commas (repeatable)")
	flags.Var(&subtitleLanguages, "srt-langs", "alias for --sub-langs")
	var configLocations stringListFlag
	flags.Var(&configLocations, "config-location", "load an additional configuration file")
	flags.Var(&configLocations, "config-locations", "alias for --config-location")
	_ = flags.Bool("ignore-config", false, "skip default configuration files")
	_ = flags.Bool("no-config", false, "alias for --ignore-config")
	_ = flags.Bool("no-config-locations", false, "clear inherited explicit configuration locations")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "ytdlp-go %s\n", Version)
		return 0
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return 2
	}
	if *telemetryJSON && (*printJSON || *dumpJSON || *dumpSingleJSON || len(printTemplates) > 0 ||
		*getURL || *getTitle || *getID || *getThumbnail || *getDescription || *getDuration || *getFilename || *getFormat) {
		fmt.Fprintln(stderr, "ytdlp-go: --telemetry-json and metadata output cannot share stdout")
		return 2
	}
	printRules, err := parsePrintRules(printTemplates)
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
		return 2
	}
	printFileRules, err := parsePrintFileRules(printFileSpecifications)
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
		return 2
	}
	printRules = append(printRules, printFileRules...)
	legacyGetting := *getURL || *getTitle || *getID || *getThumbnail || *getDescription ||
		*getDuration || *getFilename || *getFormat
	printRules = appendLegacyPrintRules(
		printRules, *getURL, *getTitle, *getID, *getThumbnail, *getDescription,
		*getDuration, *getFilename, *getFormat,
	)
	interactiveFormatRequested := *format == "-"
	interactiveFilterRequested := hasInteractiveMatchFilter(matchFilters) ||
		hasInteractiveMatchFilter(breakMatchFilters)
	suppressInteractivePrompt := *listSubtitles && !simulateSet &&
		!*dumpJSON && !*dumpSingleJSON && !legacyGetting && len(printRules) == 0
	if *progressJSON && (interactiveFormatRequested || (interactiveFilterRequested && !suppressInteractivePrompt)) {
		fmt.Fprintln(stderr, `ytdlp-go: --progress-json cannot be combined with interactive prompts`)
		return 2
	}
	requestMatchFilters := append([]string(nil), matchFilters...)
	requestBreakMatchFilters := append([]string(nil), breakMatchFilters...)
	if suppressInteractivePrompt {
		requestMatchFilters = withoutInteractiveMatchFilter(requestMatchFilters)
		requestBreakMatchFilters = withoutInteractiveMatchFilter(requestBreakMatchFilters)
	}
	if (*dumpJSON || *dumpSingleJSON || hasConsolePrintRules(printRules)) && !quietSet {
		quiet = true
	}
	subtitleConvertFormat, err := parseSubtitleConvertFormat(*convertSubtitles)
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
		return 2
	}

	handler := func(_ context.Context, event ytdlp.Event) error {
		if *progressJSON {
			return json.NewEncoder(stderr).Encode(event)
		}
		if quiet {
			return nil
		}
		switch event.Kind {
		case ytdlp.EventExtracting:
			_, _ = fmt.Fprintf(stderr, "[%s] Extracting %s\n", event.Extractor, event.URL)
		case ytdlp.EventDownloadStarting:
			_, _ = fmt.Fprintf(stderr, "[download] Destination: %s\n", event.Path)
		case ytdlp.EventDownloadProgress:
			if event.Total > 0 {
				_, _ = fmt.Fprintf(stderr, "[download] %d/%d bytes\n", event.Bytes, event.Total)
			}
		case ytdlp.EventDownloadRetry:
			_, _ = fmt.Fprintf(stderr, "[download] Retry %d: %s\n", event.Attempt, event.Message)
		case ytdlp.EventDownloadCompleted:
			_, _ = fmt.Fprintf(stderr, "[download] Completed: %s\n", event.Path)
		}
		return nil
	}
	clientOptions := []ytdlp.Option{ytdlp.WithEventHandler(handler), ytdlp.WithJavaScriptHelper(*javascriptHelper)}
	var telemetryCollector *ytdlp.TelemetryCollector
	if *telemetryJSON {
		telemetryCollector, err = ytdlp.NewTelemetryCollector(ytdlp.TelemetryConfig{Extractors: ytdlp.BuiltInExtractorIDs()})
		if err != nil {
			fmt.Fprintln(stderr, "ytdlp-go: cannot configure telemetry")
			return 2
		}
		clientOptions = append(clientOptions, ytdlp.WithTelemetryCollector(telemetryCollector))
	}
	commentLimits, err := parseYouTubeCommentLimits(*youtubeMaxComments)
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
		return 2
	}
	commentLimits.Enabled = *writeComments
	commentLimits.Sort = *youtubeCommentSort
	soundCloudComments := ytdlp.SoundCloudCommentOptions{
		Enabled: *writeComments, Sort: *soundCloudCommentSort, MaxComments: *soundCloudMaxComments,
	}
	if noSponsorBlock {
		sponsorBlockMark = nil
		sponsorBlockRemove = nil
		if len(removeChapters) == 0 {
			sponsorBlockForceKeyframes = false
		}
	}
	if sponsorBlockForceKeyframes && len(sponsorBlockRemove) == 0 && len(removeChapters) == 0 {
		fmt.Fprintln(stderr, "ytdlp-go: force-keyframes-at-cuts requires --remove-chapters or --sponsorblock-remove")
		return 2
	}
	sponsorBlockOptions, err := buildSponsorBlockOptionsWithTitle(
		sponsorBlockMark,
		sponsorBlockRemove,
		*sponsorBlockAPI,
		sponsorBlockForceKeyframes && len(sponsorBlockRemove) > 0,
		sponsorBlockChapterTitle,
	)
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
		return 2
	}
	client := deps.newRunner(clientOptions)
	downloaderOptions := ytdlp.DownloaderOptions{
		Attempts: *retries, RetryBaseDelay: *retryBaseDelay, RetryMaxDelay: *retryMaxDelay,
		RateLimit: int64(rateLimit), MaxBytes: int64(maxBytes), ThrottleRate: int64(throttleRate),
		ThrottleWindow: *throttleWindow, ThrottleRestarts: *throttleRestarts, FileAttempts: *fileRetries,
		FragmentConcurrency: *fragmentConcurrency, PerHostFragmentConcurrency: *perHostFragments,
		MaxSegments: *maxSegments, MaxSegmentBytes: int64(maxFragmentBytes),
	}
	if *externalDownloader != "" {
		downloaderOptions.External = &ytdlp.ExternalDownloader{Executable: *externalDownloader, Arguments: append([]string(nil), externalArgs...)}
	}
	postprocessors := make([]ytdlp.Postprocessor, 0, 2)
	if *extractAudio {
		postprocessors = append(postprocessors, ytdlp.Postprocessor{ExtractAudio: &ytdlp.ExtractAudioPostprocessor{Codec: *audioFormat, Bitrate: *audioBitrate, Quality: *audioQuality}})
	}
	if *remuxVideo != "" {
		postprocessors = append(postprocessors, ytdlp.Postprocessor{Remux: &ytdlp.RemuxPostprocessor{Format: *remuxVideo}})
	}
	// yt-dlp's listing flags imply simulation only when the user has not made
	// the tri-state simulation choice explicit.
	requestSimulate := simulate || !simulateSet &&
		(*listSubtitles || *listThumbnails || *dumpJSON || *dumpSingleJSON || legacyGetting || printRulesImplySimulation(printRules))
	requestSubtitles := ytdlp.SubtitleOptions{
		WriteManual: *writeSubtitles, WriteAutomatic: *writeAutomaticSubtitles,
		Embed: *embedSubtitles, KeepFiles: *embedSubtitles && *writeSubtitles,
		ConvertFormat: subtitleConvertFormat,
		Languages:     subtitleLanguageRules(subtitleLanguages, *allSubtitles), Format: *subtitleFormat,
	}
	var interactiveMatchFilter ytdlp.InteractiveMatchFilterFunc
	var interactiveFormat ytdlp.InteractiveFormatFunc
	var coordinator *interactiveStdinCoordinator
	if interactiveFilterRequested && !suppressInteractivePrompt || interactiveFormatRequested {
		coordinator = newInteractiveStdinCoordinator(stdin)
		defer coordinator.Close()
	}
	if interactiveFilterRequested && !suppressInteractivePrompt {
		interactiveMatchFilter = newInteractiveMatchFilterPromptWithCoordinator(coordinator, stderr)
	}
	if interactiveFormatRequested {
		interactiveFormat = newInteractiveFormatPrompt(coordinator, stderr)
	}
	result, err := client.Run(ctx, ytdlp.Request{
		URL: flags.Arg(0), OutputTemplates: outputTemplates.clone(), OutputDir: *outputDir, OutputPaths: paths.clone(), Proxy: *proxy, ImpersonationProfile: *impersonationProfile,
		CookieFile: *cookieFile, CookiesFromBrowser: *cookiesFromBrowser, UseNetRC: *useNetRC, NetRCLocation: *netRCLocation,
		VideoPassword:   *videoPassword,
		DownloadArchive: *downloadArchive, CacheDir: *cacheDir,
		Timeout: *timeout, Overwrite: *overwrite, Simulate: requestSimulate, SkipDownload: *skipDownload, LiveFromStart: *liveFromStart,
		Format: *format, FormatSort: append([]string(nil), formatSort...), FormatSortForce: formatSortForce,
		PreferFreeFormats: preferFreeFormats, AllowUnplayableFormats: allowUnplayable,
		AllowMultipleVideoStreams: allowMultipleVideoStreams, AllowMultipleAudioStreams: allowMultipleAudioStreams,
		CheckFormats: checkFormats, MergeOutputFormat: *mergeOutputFormat,
		ProgressTemplate: *progressTemplate, MatchFilters: requestMatchFilters,
		InteractiveMatchFilter: interactiveMatchFilter,
		InteractiveFormat:      interactiveFormat,
		BreakMatchFilters:      requestBreakMatchFilters,
		MetadataActions:        append([]ytdlp.MetadataAction(nil), metadataActions...),
		Subtitles:              requestSubtitles,
		Thumbnails: ytdlp.ThumbnailOptions{
			Write:    thumbnailMode == thumbnailModeBest || *embedThumbnail,
			WriteAll: thumbnailMode == thumbnailModeAll, List: *listThumbnails,
			Embed: *embedThumbnail, KeepFiles: *embedThumbnail && thumbnailMode != thumbnailModeNone,
			ConvertFormat: *convertThumbnails,
		},
		RelatedFiles: ytdlp.RelatedFileOptions{
			WriteInfoJSON: *writeInfoJSON, WriteDescription: *writeDescription,
			WriteLink: *writeLink, WriteURLLink: *writeURLLink,
			WriteWeblocLink: *writeWeblocLink, WriteDesktopLink: *writeDesktopLink,
			NoPlaylist: *noPlaylistMetafiles,
		},
		PrintRules:           printRules,
		YouTubeComments:      commentLimits,
		SoundCloudComments:   soundCloudComments,
		SponsorBlock:         sponsorBlockOptions,
		RemoveChapters:       append([]string(nil), removeChapters...),
		ForceKeyframesAtCuts: sponsorBlockForceKeyframes,
		Playlist: ytdlp.PlaylistOptions{
			Start: *playlistStart, End: *playlistEnd, Reverse: *playlistReverse, Items: *playlistItems, Flat: *flatPlaylist,
		},
		Downloader: downloaderOptions, Postprocessors: postprocessors,
	})
	if telemetryCollector != nil {
		if writeErr := telemetryCollector.WriteCanonical(context.Background(), stdout); writeErr != nil {
			fmt.Fprintln(stderr, "ytdlp-go: cannot write telemetry snapshot")
			return 1
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
		return exitCode(err)
	}
	if hasConsolePrintRules(printRules) {
		if err := writePrintOutputs(ctx, result, stdout); err != nil {
			fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
			return exitCode(err)
		}
	}
	if *listSubtitles {
		if err := writeSubtitleListings(ctx, result, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
			return exitCode(err)
		}
	}
	if *listThumbnails {
		if err := writeThumbnailListings(ctx, result, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
			return exitCode(err)
		}
	}
	if *dumpJSON {
		if err := writeVideoJSONLines(ctx, result, stdout); err != nil {
			fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
			return exitCode(err)
		}
	}
	if *dumpSingleJSON {
		if err := writeJSONLine(ctx, result.InfoJSON, stdout); err != nil {
			fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
			return exitCode(err)
		}
	}
	if *printJSON && !*dumpJSON && !*dumpSingleJSON {
		_, _ = stdout.Write(result.InfoJSON)
		_, _ = fmt.Fprintln(stdout)
	}
	return 0
}

func hasInteractiveMatchFilter(filters []string) bool {
	for _, filter := range filters {
		if filter == "-" {
			return true
		}
	}
	return false
}

func withoutInteractiveMatchFilter(filters []string) []string {
	result := filters[:0]
	for _, filter := range filters {
		if filter != "-" {
			result = append(result, filter)
		}
	}
	return result
}

func newInteractiveMatchFilterPrompt(input io.Reader, output io.Writer) ytdlp.InteractiveMatchFilterFunc {
	return newInteractiveMatchFilterPromptWithCoordinator(newInteractiveStdinCoordinator(input), output)
}

// interactiveStdinCoordinator gives the two interactive CLI features one
// bounded reader. Keeping the scanner and its lock here prevents competing
// Scanner instances from consuming each other's input.
type interactiveStdinCoordinator struct {
	mu       sync.Mutex
	scanner  *bufio.Scanner
	requests chan chan interactiveReadResult
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	terminal error
}

type interactiveReadResult struct {
	text string
	err  error
}

func newInteractiveStdinCoordinator(input io.Reader) *interactiveStdinCoordinator {
	if input == nil {
		input = strings.NewReader("")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1024), 4096)
	coordinator := &interactiveStdinCoordinator{
		scanner: scanner, requests: make(chan chan interactiveReadResult), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go coordinator.run()
	return coordinator
}

// run is the sole owner of Scanner.Scan. A blocked arbitrary io.Reader cannot
// be portably interrupted, but cancellation terminally rejects future prompt
// reads so it can never race a later Scan. Close releases an idle reader; a
// blocked reader exits when its input is released/closed by its owner.
func (coordinator *interactiveStdinCoordinator) run() {
	defer close(coordinator.done)
	for {
		select {
		case <-coordinator.stop:
			return
		case response := <-coordinator.requests:
			if coordinator.scanner.Scan() {
				response <- interactiveReadResult{text: coordinator.scanner.Text()}
				continue
			}
			err := coordinator.scanner.Err()
			if err == nil {
				err = io.EOF
			}
			response <- interactiveReadResult{err: err}
			return
		}
	}
}

func (coordinator *interactiveStdinCoordinator) Close() {
	if coordinator == nil {
		return
	}
	coordinator.stopOnce.Do(func() { close(coordinator.stop) })
}

func (coordinator *interactiveStdinCoordinator) readLine(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	coordinator.mu.Lock()
	if coordinator.terminal != nil {
		err := coordinator.terminal
		coordinator.mu.Unlock()
		return "", err
	}
	response := make(chan interactiveReadResult, 1)
	coordinator.mu.Unlock()
	select {
	case coordinator.requests <- response:
	case <-ctx.Done():
		coordinator.mu.Lock()
		coordinator.terminal = ctx.Err()
		coordinator.mu.Unlock()
		return "", ctx.Err()
	}
	select {
	case <-ctx.Done():
		coordinator.mu.Lock()
		coordinator.terminal = ctx.Err()
		coordinator.mu.Unlock()
		return "", ctx.Err()
	case result := <-response:
		if result.err != nil {
			if errors.Is(result.err, io.EOF) {
				return "", context.Canceled
			}
			return "", fmt.Errorf("%w: %v", ytdlp.ErrInteractiveInput, result.err)
		}
		return result.text, nil
	}
}

func newInteractiveMatchFilterPromptWithCoordinator(coordinator *interactiveStdinCoordinator, output io.Writer) ytdlp.InteractiveMatchFilterFunc {
	return func(ctx context.Context, prompt ytdlp.InteractiveMatchFilterPrompt) (bool, error) {
		for {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			if _, err := fmt.Fprintf(output, "Download %s? (Y/n): ", strconv.Quote(prompt.Filename)); err != nil {
				return false, err
			}
			line, err := coordinator.readLine(ctx)
			if err != nil {
				return false, err
			}
			if accepted, valid := parseInteractiveMatchFilterResponse(line); valid {
				return accepted, nil
			}
		}
	}
}

func newInteractiveFormatPrompt(coordinator *interactiveStdinCoordinator, output io.Writer) ytdlp.InteractiveFormatFunc {
	return func(ctx context.Context, prompt ytdlp.InteractiveFormatPrompt) (string, error) {
		if prompt.Error != "" {
			if _, err := fmt.Fprintf(output, "ytdlp-go: %s\n", prompt.Error); err != nil {
				return "", err
			}
		}
		if _, err := fmt.Fprint(output, "Format selector (empty for default): "); err != nil {
			return "", err
		}
		return coordinator.readLine(ctx)
	}
}

func parseInteractiveMatchFilterResponse(input string) (accepted, valid bool) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "", "y":
		return true, true
	case "n":
		return false, true
	default:
		return false, false
	}
}

func writeVideoJSONLines(ctx context.Context, result ytdlp.Result, writer io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(result.Entries) != 0 {
		for _, entry := range result.Entries {
			if err := writeVideoJSONLines(ctx, entry, writer); err != nil {
				return err
			}
		}
		return nil
	}
	if result.Skipped || result.Archived {
		return nil
	}
	var kind struct {
		Type string `json:"_type"`
	}
	if err := json.Unmarshal(result.InfoJSON, &kind); err != nil {
		return fmt.Errorf("dump JSON: invalid result metadata")
	}
	if kind.Type == "playlist" {
		return nil
	}
	return writeJSONLine(ctx, result.InfoJSON, writer)
}

func writeJSONLine(ctx context.Context, raw json.RawMessage, writer io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return fmt.Errorf("dump JSON: invalid result metadata")
	}
	if written, err := writer.Write(compact.Bytes()); err != nil {
		return fmt.Errorf("write JSON metadata: %w", err)
	} else if written != compact.Len() {
		return fmt.Errorf("write JSON metadata: %w", io.ErrShortWrite)
	}
	if written, err := io.WriteString(writer, "\n"); err != nil {
		return fmt.Errorf("write JSON metadata: %w", err)
	} else if written != 1 {
		return fmt.Errorf("write JSON metadata: %w", io.ErrShortWrite)
	}
	return nil
}

func writeSubtitleListings(ctx context.Context, result ytdlp.Result, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(result.Entries) != 0 {
		for _, entry := range result.Entries {
			if err := writeSubtitleListings(ctx, entry, stdout, stderr); err != nil {
				return err
			}
		}
		return nil
	}
	table, status, err := renderSubtitleListing(ctx, result.InfoJSON)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(stderr, status); err != nil {
		return fmt.Errorf("write subtitle listing status: %w", err)
	}
	if _, err := io.WriteString(stdout, table); err != nil {
		return fmt.Errorf("write subtitle listing table: %w", err)
	}
	return nil
}

type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ",") }
func (values *stringListFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

// metadataActionFlag preserves the flag stream's interleaving. Standard flag
// parsing has no nargs support, so extractReplaceMetadataArgs encodes the three
// replace arguments in a private NUL-delimited transport before this point.
type metadataActionFlag []ytdlp.MetadataAction

type metadataParseFlag struct{ actions *metadataActionFlag }

func (flag metadataParseFlag) String() string { return "" }
func (flag metadataParseFlag) Set(input string) error {
	*flag.actions = append(*flag.actions, ytdlp.MetadataAction{Kind: ytdlp.MetadataActionParse, Parse: input})
	return nil
}

type metadataReplaceFlag struct{ actions *metadataActionFlag }

func (flag metadataReplaceFlag) String() string { return "" }
func (flag metadataReplaceFlag) Set(input string) error {
	parts := strings.Split(input, "\x00")
	if len(parts) == 3 {
		*flag.actions = append(*flag.actions, ytdlp.MetadataAction{
			Kind: ytdlp.MetadataActionReplace, Fields: parts[0], Search: parts[1], Replacement: parts[2],
		})
		return nil
	}
	// The old single-token colon form remains accepted for programmatic and
	// pre-existing port CLI callers. It is deliberately not advertised.
	*flag.actions = append(*flag.actions, ytdlp.MetadataAction{Kind: ytdlp.MetadataActionReplace, Parse: input})
	return nil
}

func extractReplaceMetadataArgs(args []string) ([]string, error) {
	output := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		if args[index] == "--" {
			output = append(output, args[index:]...)
			break
		}
		if args[index] != "--replace-in-metadata" {
			output = append(output, args[index])
			continue
		}
		// Preserve the old one-token form when the next argument is plainly the
		// next option (or the URL). New three-argument invocations are otherwise
		// consumed exactly as yt-dlp documents.
		if index+1 < len(args) && isLegacyMetadataReplacement(args[index+1]) {
			output = append(output, args[index], args[index+1])
			index++
			continue
		}
		if index+3 >= len(args) {
			return nil, fmt.Errorf("--replace-in-metadata requires FIELDS REGEX REPLACEMENT")
		}
		output = append(output, "--replace-in-metadata="+strings.Join(args[index+1:index+4], "\x00"))
		index += 3
	}
	return output, nil
}

func isLegacyMetadataReplacement(input string) bool {
	separators, escaped := 0, false
	for index := range input {
		if escaped {
			escaped = false
			continue
		}
		if input[index] == '\\' {
			escaped = true
			continue
		}
		if input[index] == ':' {
			separators++
		}
	}
	return separators >= 2
}

// formatSortFlag mirrors yt-dlp's orderedSet_from_options accumulation: each
// later -S occurrence prepends its comma-separated fields. The sorter itself
// keeps the first duplicate, so this order makes the latest specification win.
type formatSortFlag []string

func (values *formatSortFlag) String() string { return strings.Join(*values, ",") }

func (values *formatSortFlag) Set(input string) error {
	raw := strings.Split(input, ",")
	fields := make([]string, 0, len(raw))
	for _, field := range raw {
		if field = strings.TrimSpace(field); field != "" {
			fields = append(fields, field)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	next := make(formatSortFlag, 0, len(*values)+len(fields))
	next = append(next, fields...)
	next = append(next, (*values)...)
	*values = next
	return nil
}

type thumbnailModeFlag uint8

const (
	thumbnailModeNone thumbnailModeFlag = iota
	thumbnailModeBest
	thumbnailModeAll
)

func (mode *thumbnailModeFlag) setBest(input string) error {
	enabled, err := strconv.ParseBool(input)
	if err != nil {
		return err
	}
	if !enabled {
		*mode = thumbnailModeNone
	} else if *mode == thumbnailModeNone {
		*mode = thumbnailModeBest
	}
	return nil
}

func (mode *thumbnailModeFlag) setAll(input string) error {
	enabled, err := strconv.ParseBool(input)
	if err != nil {
		return err
	}
	if enabled {
		*mode = thumbnailModeAll
	} else {
		*mode = thumbnailModeNone
	}
	return nil
}

func (mode *thumbnailModeFlag) clear(input string) error {
	enabled, err := strconv.ParseBool(input)
	if err != nil {
		return err
	}
	if enabled {
		*mode = thumbnailModeNone
	}
	return nil
}

type outputTemplateFlag struct {
	values ytdlp.OutputTemplates
}

func (output *outputTemplateFlag) String() string {
	if output == nil || len(output.values) == 0 {
		return ""
	}
	ordered := []ytdlp.OutputTemplateType{
		ytdlp.OutputTemplateDefault, ytdlp.OutputTemplateSubtitle,
		ytdlp.OutputTemplateThumbnail, ytdlp.OutputTemplateDescription, ytdlp.OutputTemplateInfoJSON,
		ytdlp.OutputTemplateLink, ytdlp.OutputTemplatePLDescription,
		ytdlp.OutputTemplatePLInfoJSON, ytdlp.OutputTemplatePLThumbnail,
	}
	parts := make([]string, 0, len(output.values))
	for _, templateType := range ordered {
		if pattern, ok := output.values[templateType]; ok {
			parts = append(parts, string(templateType)+":"+pattern)
		}
	}
	return strings.Join(parts, ",")
}

func (output *outputTemplateFlag) Set(specification string) error {
	types, pattern, err := parseOutputTemplateSpecification(specification)
	if err != nil {
		return err
	}
	if output.values == nil {
		output.values = make(ytdlp.OutputTemplates)
	}
	for _, templateType := range types {
		output.values[templateType] = pattern
	}
	return nil
}

func (output *outputTemplateFlag) clone() ytdlp.OutputTemplates {
	if output == nil || len(output.values) == 0 {
		return nil
	}
	clone := make(ytdlp.OutputTemplates, len(output.values))
	for templateType, pattern := range output.values {
		clone[templateType] = pattern
	}
	return clone
}

func parseOutputTemplateSpecification(specification string) ([]ytdlp.OutputTemplateType, string, error) {
	if specification == "" || strings.ContainsRune(specification, 0) {
		return nil, "", errors.New("output template must not be empty")
	}
	prefix, pattern, separated := strings.Cut(specification, ":")
	if !separated {
		return []ytdlp.OutputTemplateType{ytdlp.OutputTemplateDefault}, specification, nil
	}
	parts := strings.Split(prefix, ",")
	types := make([]ytdlp.OutputTemplateType, 0, len(parts))
	unimplemented := make([]string, 0)
	hasUnknown := false
	for _, part := range parts {
		templateType := ytdlp.OutputTemplateType(strings.ToLower(part))
		if !supportedCLIOutputTemplateType(templateType) {
			if recognizedUnimplementedOutputTemplateType(templateType) {
				unimplemented = append(unimplemented, part)
			} else {
				hasUnknown = true
			}
			continue
		}
		types = append(types, templateType)
	}
	if hasUnknown {
		return []ytdlp.OutputTemplateType{ytdlp.OutputTemplateDefault}, specification, nil
	}
	if len(unimplemented) > 0 {
		return nil, "", fmt.Errorf("unsupported output template type %q", unimplemented[0])
	}
	seen := make(map[ytdlp.OutputTemplateType]bool)
	for _, templateType := range types {
		if seen[templateType] {
			return nil, "", fmt.Errorf("duplicate output template type %q", templateType)
		}
		seen[templateType] = true
	}
	if pattern == "" {
		return nil, "", errors.New("typed output template must not be empty")
	}
	return types, pattern, nil
}

func recognizedUnimplementedOutputTemplateType(templateType ytdlp.OutputTemplateType) bool {
	switch templateType {
	case "chapter", "annotation", "pl_video":
		return true
	default:
		return false
	}
}

func supportedCLIOutputTemplateType(templateType ytdlp.OutputTemplateType) bool {
	switch templateType {
	case ytdlp.OutputTemplateDefault, ytdlp.OutputTemplateSubtitle,
		ytdlp.OutputTemplateThumbnail, ytdlp.OutputTemplateDescription, ytdlp.OutputTemplateInfoJSON,
		ytdlp.OutputTemplateLink, ytdlp.OutputTemplatePLDescription,
		ytdlp.OutputTemplatePLInfoJSON, ytdlp.OutputTemplatePLThumbnail:
		return true
	default:
		return false
	}
}

func splitCommaList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			if item = strings.TrimSpace(item); item != "" {
				result = append(result, item)
			}
		}
	}
	return result
}

func subtitleLanguageRules(values []string, all bool) []string {
	if all {
		return []string{"all"}
	}
	return splitCommaList(values)
}

func parseYouTubeCommentLimits(input string) (ytdlp.YouTubeCommentOptions, error) {
	var options ytdlp.YouTubeCommentOptions
	if input == "" {
		return options, nil
	}
	parts := strings.Split(input, ",")
	if len(parts) > 5 {
		return options, errors.New("youtube-max-comments accepts at most five values")
	}
	values := make([]int, 5)
	for index, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return options, errors.New("youtube-max-comments values must not be empty")
		}
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 || parsed > 10_000 || index == 4 && parsed > 8 {
			return options, fmt.Errorf("invalid youtube-max-comments value %q", part)
		}
		values[index] = parsed
	}
	options.MaxComments = values[0]
	if len(parts) > 1 {
		options.MaxParents = values[1]
	}
	if len(parts) > 2 {
		options.MaxReplies = values[2]
	}
	if len(parts) > 3 {
		options.MaxRepliesPerThread = values[3]
	}
	if len(parts) > 4 {
		options.MaxDepth = values[4]
	}
	return options, nil
}

type sponsorBlockCategoryParseConfig struct {
	flagName         string
	expandAll        func() []string
	expandDefault    func() []string
	requireRemovable bool
}

var (
	sponsorBlockMarkCategoryConfig = sponsorBlockCategoryParseConfig{
		flagName: "sponsorblock-mark",
		expandAll: func() []string {
			return sponsorBlockCategoryNames(sponsorblock.AllCategories())
		},
		expandDefault: func() []string {
			return sponsorBlockCategoryNames(sponsorblock.AllCategories())
		},
	}
	sponsorBlockRemoveCategoryConfig = sponsorBlockCategoryParseConfig{
		flagName:         "sponsorblock-remove",
		expandAll:        allRemovableCategoryStrings,
		expandDefault:    defaultRemoveCategoryStrings,
		requireRemovable: true,
	}
)

func sponsorBlockCategoryNames(categories []sponsorblock.Category) []string {
	out := make([]string, 0, len(categories))
	for _, category := range categories {
		out = append(out, string(category))
	}
	return out
}

func allRemovableCategoryStrings() []string {
	out := make([]string, 0, len(sponsorblock.AllCategories()))
	for _, category := range sponsorblock.AllCategories() {
		name := string(category)
		if sponsorblock.IsRemovableCategory(name) {
			out = append(out, name)
		}
	}
	return out
}

func defaultRemoveCategoryStrings() []string {
	out := make([]string, 0, len(sponsorblock.AllCategories()))
	for _, category := range sponsorblock.AllCategories() {
		name := string(category)
		if sponsorblock.IsRemovableCategory(name) && name != string(sponsorblock.CategoryFiller) {
			out = append(out, name)
		}
	}
	return out
}

func buildSponsorBlockOptions(mark, remove []string, apiBase string, forceKeyframes bool) (ytdlp.SponsorBlockOptions, error) {
	return buildSponsorBlockOptionsWithTitle(mark, remove, apiBase, forceKeyframes, nil)
}

func buildSponsorBlockOptionsWithTitle(mark, remove []string, apiBase string, forceKeyframes bool, chapterTitle *string) (ytdlp.SponsorBlockOptions, error) {
	options := ytdlp.SponsorBlockOptions{APIBase: strings.TrimSpace(apiBase)}
	if chapterTitle != nil {
		title := *chapterTitle
		options.ChapterTitle = &title
	}
	if options.APIBase != "" {
		if err := validateSponsorBlockAPIBase(options.APIBase); err != nil {
			return ytdlp.SponsorBlockOptions{}, err
		}
	}
	hasMark := len(mark) > 0
	hasRemove := len(remove) > 0
	if !hasMark && !hasRemove {
		if forceKeyframes {
			return ytdlp.SponsorBlockOptions{}, errors.New("force-keyframes-at-cuts requires --sponsorblock-remove")
		}
		return options, nil
	}
	if forceKeyframes && !hasRemove {
		return ytdlp.SponsorBlockOptions{}, errors.New("force-keyframes-at-cuts requires --sponsorblock-remove")
	}
	options.Enabled = true
	options.Mark = hasMark
	options.Remove = hasRemove
	options.Categories = unionSponsorBlockCategories(mark, remove)
	if hasRemove {
		options.RemoveCategories = append([]string(nil), remove...)
		options.ForceKeyframes = forceKeyframes
	}
	return options, nil
}

func unionSponsorBlockCategories(mark, remove []string) []string {
	result := append([]string(nil), mark...)
	for _, category := range remove {
		result = appendUniqueSponsorBlockCategory(result, category)
	}
	return result
}

func parseSponsorBlockMarkCategories(input string, start []string) ([]string, error) {
	return parseSponsorBlockCategories(input, start, sponsorBlockMarkCategoryConfig)
}

func parseSponsorBlockRemoveCategories(input string, start []string) ([]string, error) {
	return parseSponsorBlockCategories(input, start, sponsorBlockRemoveCategoryConfig)
}

// parseSponsorBlockCategories accumulates a comma-separated SponsorBlock
// category grammar onto start, matching yt-dlp's orderedSet_from_options:
// repeated flags accumulate, all/default expand per flag semantics, and a
// leading "-" excludes a category or alias (for example all,-preview).
// Exclusions may leave an empty set (disabling that flag's effect); only an
// explicitly empty flag value or malformed empty comma tokens are rejected.
func parseSponsorBlockCategories(input string, start []string, config sponsorBlockCategoryParseConfig) ([]string, error) {
	emptyErr := errors.New(config.flagName + " requires at least one category")
	if strings.TrimSpace(input) == "" {
		return nil, emptyErr
	}
	result := append([]string(nil), start...)
	for _, raw := range strings.Split(input, ",") {
		token := strings.ToLower(strings.TrimSpace(raw))
		if token == "" {
			return nil, emptyErr
		}
		exclude := false
		if strings.HasPrefix(token, "-") {
			exclude = true
			token = strings.TrimSpace(token[1:])
			if token == "" {
				return nil, emptyErr
			}
		}
		var values []string
		switch token {
		case "all":
			values = append([]string(nil), config.expandAll()...)
		case "default":
			values = append([]string(nil), config.expandDefault()...)
		default:
			if !sponsorblock.IsValidCategory(token) {
				return nil, fmt.Errorf("unknown SponsorBlock category %q", token)
			}
			if config.requireRemovable && !sponsorblock.IsRemovableCategory(token) {
				return nil, fmt.Errorf("%s category %q not removable", config.flagName, token)
			}
			values = []string{token}
		}
		if exclude {
			for _, category := range values {
				result = removeSponsorBlockCategory(result, category)
			}
			continue
		}
		for _, category := range values {
			result = appendUniqueSponsorBlockCategory(result, category)
		}
	}
	if len(result) > sponsorblock.MaxCategories {
		return nil, errors.New("too many SponsorBlock categories")
	}
	return result, nil
}

func appendUniqueSponsorBlockCategory(categories []string, category string) []string {
	for _, existing := range categories {
		if existing == category {
			return categories
		}
	}
	return append(categories, category)
}

func removeSponsorBlockCategory(categories []string, category string) []string {
	filtered := make([]string, 0, len(categories))
	for _, existing := range categories {
		if existing == category {
			continue
		}
		filtered = append(filtered, existing)
	}
	return filtered
}

func validateSponsorBlockAPIBase(raw string) error {
	if len(raw) > 4096 {
		return errors.New("SponsorBlock API base too long")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("SponsorBlock API base invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("SponsorBlock API base scheme")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("SponsorBlock API base host")
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") {
		return errors.New("SponsorBlock API base path")
	}
	return nil
}

type byteSizeFlag int64

func (value *byteSizeFlag) String() string { return strconv.FormatInt(int64(*value), 10) }
func (value *byteSizeFlag) Set(input string) error {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return errors.New("byte size must not be empty")
	}
	multiplier := int64(1)
	switch suffix := strings.ToUpper(trimmed[len(trimmed)-1:]); suffix {
	case "K":
		multiplier, trimmed = 1024, trimmed[:len(trimmed)-1]
	case "M":
		multiplier, trimmed = 1024*1024, trimmed[:len(trimmed)-1]
	case "G":
		multiplier, trimmed = 1024*1024*1024, trimmed[:len(trimmed)-1]
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || parsed < 0 || (parsed > 0 && parsed > (1<<63-1)/multiplier) {
		return fmt.Errorf("invalid byte size %q", input)
	}
	*value = byteSizeFlag(parsed * multiplier)
	return nil
}

type outputPathFlag struct {
	home   *string
	values ytdlp.OutputPaths
}

func (value *outputPathFlag) String() string {
	if value == nil || value.home == nil {
		return ""
	}
	return *value.home
}

func (value *outputPathFlag) Set(input string) error {
	types, path, err := parseOutputPathSpecification(input)
	if err != nil {
		return err
	}
	path = strings.TrimSpace(path)
	for _, pathType := range types {
		if pathType == ytdlp.OutputPathHome {
			if path == "" {
				path = "."
			}
			*value.home = path
			continue
		}
		if value.values == nil {
			value.values = make(ytdlp.OutputPaths)
		}
		if path == "" || filepath.Clean(path) == "." {
			delete(value.values, pathType)
		} else {
			value.values[pathType] = path
		}
	}
	return nil
}

func (value *outputPathFlag) clone() ytdlp.OutputPaths {
	if value == nil || len(value.values) == 0 {
		return nil
	}
	result := make(ytdlp.OutputPaths, len(value.values))
	for pathType, path := range value.values {
		result[pathType] = path
	}
	return result
}

func parseOutputPathSpecification(input string) ([]ytdlp.OutputPathType, string, error) {
	if input == "" || strings.ContainsRune(input, 0) {
		return nil, "", errors.New("output path must not be empty")
	}
	prefix, path, separated := strings.Cut(input, ":")
	if !separated {
		return []ytdlp.OutputPathType{ytdlp.OutputPathHome}, input, nil
	}
	parts := strings.Split(prefix, ",")
	types := make([]ytdlp.OutputPathType, 0, len(parts))
	hasUnknown := false
	var unimplemented string
	for _, part := range parts {
		pathType := ytdlp.OutputPathType(strings.ToLower(part))
		if supportedCLIOutputPathType(pathType) {
			types = append(types, pathType)
		} else if recognizedUnimplementedOutputPathType(pathType) {
			if unimplemented == "" {
				unimplemented = part
			}
		} else {
			hasUnknown = true
		}
	}
	if hasUnknown {
		return []ytdlp.OutputPathType{ytdlp.OutputPathHome}, input, nil
	}
	if unimplemented != "" {
		return nil, "", fmt.Errorf("unsupported output path type %q", unimplemented)
	}
	return types, path, nil
}

func supportedCLIOutputPathType(pathType ytdlp.OutputPathType) bool {
	switch pathType {
	case ytdlp.OutputPathHome, ytdlp.OutputPathSubtitle, ytdlp.OutputPathThumbnail,
		ytdlp.OutputPathDescription, ytdlp.OutputPathInfoJSON, ytdlp.OutputPathLink,
		ytdlp.OutputPathPLDescription, ytdlp.OutputPathPLInfoJSON, ytdlp.OutputPathPLThumbnail:
		return true
	default:
		return false
	}
}

func recognizedUnimplementedOutputPathType(pathType ytdlp.OutputPathType) bool {
	switch pathType {
	case "temp", "chapter", "annotation", "pl_video":
		return true
	default:
		return false
	}
}

func homePathFromArgs(args []string) string {
	var result string
	for index := 0; index < len(args); index++ {
		argument := args[index]
		var value string
		switch {
		case argument == "-P" || argument == "--paths":
			if index+1 >= len(args) {
				continue
			}
			index++
			value = args[index]
		case strings.HasPrefix(argument, "--paths="):
			value = strings.TrimPrefix(argument, "--paths=")
		default:
			continue
		}
		types, path, err := parseOutputPathSpecification(value)
		if err != nil {
			continue
		}
		for _, pathType := range types {
			if pathType == ytdlp.OutputPathHome {
				result = path
				break
			}
		}
	}
	return result
}

func exitCode(err error) int {
	switch {
	case ytdlp.IsCategory(err, ytdlp.ErrorInvalidInput):
		return 2
	case ytdlp.IsCategory(err, ytdlp.ErrorUnsupported):
		return 3
	case ytdlp.IsCategory(err, ytdlp.ErrorAuthentication):
		return 5
	case ytdlp.IsCategory(err, ytdlp.ErrorNetwork):
		return 4
	case ytdlp.IsCategory(err, ytdlp.ErrorSecurity):
		return 6
	case ytdlp.IsCategory(err, ytdlp.ErrorCancelled), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return 130
	default:
		return 1
	}
}
