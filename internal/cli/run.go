// Package cli implements the command-line boundary.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	compatconfig "github.com/ytdlp-go/ytdlp/internal/compat/config"
	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

// Version is overridden with -X for release artifacts.
var Version = "0.0.0-dev"

func Run(args []string, stdout, stderr io.Writer) int {
	return RunContext(context.Background(), args, stdout, stderr)
}

func RunContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	environment := compatconfig.RuntimeEnvironment()
	environment.HomeConfigDir = homePathFromArgs(args)
	loaded, err := compatconfig.Load(ctx, compatconfig.Request{
		Environment: environment, CommandLine: args, IncludeDefaults: true,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return 130
		}
		return 2
	}
	args = loaded.Arguments
	flags := flag.NewFlagSet("ytdlp-go", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: ytdlp-go [OPTIONS] URL")
		fmt.Fprintln(flags.Output())
		fmt.Fprintln(flags.Output(), "Experimental Python-free Go implementation informed by yt-dlp behavior.")
		fmt.Fprintln(flags.Output())
		flags.PrintDefaults()
	}

	showVersion := flags.Bool("version", false, "print the version and exit")
	output := flags.String("output", "%(title)s.%(ext)s", "output filename template")
	flags.StringVar(output, "o", *output, "alias for --output")
	outputDir := flags.String("output-dir", ".", "directory that confines output files")
	paths := &homePathFlag{target: outputDir}
	flags.Var(paths, "paths", "set a home output/config path (home:PATH)")
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
	cookiesFromBrowser := flags.String("cookies-from-browser", "", "import cookies from Firefox or a supported platform Chromium browser")
	useNetRC := flags.Bool("netrc", false, "use credentials from a native .netrc file")
	netRCLocation := flags.String("netrc-location", "", "path to .netrc or its containing directory")
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
	var formatSort, matchFilters, parseMetadata, replaceMetadata stringListFlag
	flags.Var(&formatSort, "format-sort", "format sort field (repeatable)")
	flags.Var(&formatSort, "S", "alias for --format-sort")
	preferFreeFormats := flags.Bool("prefer-free-formats", false, "prefer free containers when otherwise equivalent")
	allowUnplayable := flags.Bool("allow-unplayable-formats", false, "include DRM-marked formats in selection")
	progressTemplate := flags.String("progress-template", "", "render download events with a bounded progress template")
	flags.Var(&matchFilters, "match-filter", "metadata filter expression (repeatable OR)")
	flags.Var(&matchFilters, "match-filters", "alias for --match-filter")
	flags.BoolFunc("no-match-filter", "clear inherited metadata filters", func(string) error {
		matchFilters = nil
		return nil
	})
	flags.BoolFunc("no-match-filters", "alias for --no-match-filter", func(string) error {
		matchFilters = nil
		return nil
	})
	flags.Var(&parseMetadata, "parse-metadata", "bounded FROM:TO metadata action")
	flags.Var(&replaceMetadata, "replace-in-metadata", "bounded FIELD:REGEX:REPLACEMENT action")
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
	legacyGetting := *getURL || *getTitle || *getID || *getThumbnail || *getDescription ||
		*getDuration || *getFilename || *getFormat
	printRules = appendLegacyPrintRules(
		printRules, *getURL, *getTitle, *getID, *getThumbnail, *getDescription,
		*getDuration, *getFilename, *getFormat,
	)
	if (*dumpJSON || *dumpSingleJSON || len(printRules) > 0) && !quietSet {
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
	client := ytdlp.NewClient(clientOptions...)
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
		(*listSubtitles || *dumpJSON || *dumpSingleJSON || legacyGetting || printRulesImplySimulation(printRules))
	requestSubtitles := ytdlp.SubtitleOptions{
		WriteManual: *writeSubtitles, WriteAutomatic: *writeAutomaticSubtitles,
		Embed: *embedSubtitles, KeepFiles: *embedSubtitles && *writeSubtitles,
		ConvertFormat: subtitleConvertFormat,
		Languages:     subtitleLanguageRules(subtitleLanguages, *allSubtitles), Format: *subtitleFormat,
	}
	commentLimits, err := parseYouTubeCommentLimits(*youtubeMaxComments)
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-go: %v\n", err)
		return 2
	}
	commentLimits.Enabled = *writeComments
	commentLimits.Sort = *youtubeCommentSort
	result, err := client.Run(ctx, ytdlp.Request{
		URL: flags.Arg(0), OutputTemplate: *output, OutputDir: *outputDir, Proxy: *proxy, ImpersonationProfile: *impersonationProfile,
		CookieFile: *cookieFile, CookiesFromBrowser: *cookiesFromBrowser, UseNetRC: *useNetRC, NetRCLocation: *netRCLocation, DownloadArchive: *downloadArchive, CacheDir: *cacheDir,
		Timeout: *timeout, Overwrite: *overwrite, Simulate: requestSimulate, SkipDownload: *skipDownload, LiveFromStart: *liveFromStart,
		Format: *format, FormatSort: append([]string(nil), formatSort...),
		PreferFreeFormats: *preferFreeFormats, AllowUnplayableFormats: *allowUnplayable,
		ProgressTemplate: *progressTemplate, MatchFilters: append([]string(nil), matchFilters...),
		ParseMetadata: append([]string(nil), parseMetadata...), ReplaceMetadata: append([]string(nil), replaceMetadata...),
		Subtitles: requestSubtitles,
		RelatedFiles: ytdlp.RelatedFileOptions{
			WriteInfoJSON: *writeInfoJSON, WriteDescription: *writeDescription,
			WriteLink: *writeLink, WriteURLLink: *writeURLLink,
			WriteWeblocLink: *writeWeblocLink, WriteDesktopLink: *writeDesktopLink,
			NoPlaylist: *noPlaylistMetafiles,
		},
		PrintRules:      printRules,
		YouTubeComments: commentLimits,
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
	if len(printRules) > 0 {
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

type homePathFlag struct{ target *string }

func (value *homePathFlag) String() string {
	if value.target == nil {
		return ""
	}
	return *value.target
}

func (value *homePathFlag) Set(input string) error {
	kind, path, typed := strings.Cut(input, ":")
	if !typed {
		path = kind
	} else if kind != "home" {
		return fmt.Errorf("unsupported --paths type %q", kind)
	}
	if path == "" {
		return errors.New("home path must not be empty")
	}
	*value.target = path
	return nil
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
		kind, path, typed := strings.Cut(value, ":")
		if !typed {
			result = kind
		} else if kind == "home" {
			result = path
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
