// Package cli implements the command-line boundary.
package cli

import (
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
		fmt.Fprintln(flags.Output(), "Experimental Python-free Go port of yt-dlp (Phase 2 alpha development).")
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
	printJSON := flags.Bool("print-json", false, "print normalized metadata JSON to stdout")
	skipDownload := flags.Bool("skip-download", false, "extract metadata without downloading")
	proxy := flags.String("proxy", "", "HTTP/HTTPS proxy URL")
	timeout := flags.Duration("socket-timeout", 30*time.Second, "network operation timeout")
	overwrite := flags.Bool("force-overwrites", false, "replace an existing final file")
	progressJSON := flags.Bool("progress-json", false, "write newline-delimited progress events to stderr")
	telemetryJSON := flags.Bool("telemetry-json", false, "write one privacy-safe aggregate telemetry snapshot to stdout")
	quiet := flags.Bool("quiet", false, "suppress human-readable progress")
	javascriptHelper := flags.String("js-helper", "", "path to the isolated JavaScript helper")
	cookieFile := flags.String("cookies", "", "load cookies from a Netscape cookies.txt file")
	cookiesFromBrowser := flags.String("cookies-from-browser", "", "import cookies from firefox, macOS chrome, or Linux chrome/chromium/brave")
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
	format := flags.String("format", "", "format selector expression")
	flags.StringVar(format, "f", "", "alias for --format")
	var formatSort, matchFilters, parseMetadata, replaceMetadata stringListFlag
	flags.Var(&formatSort, "format-sort", "format sort field (repeatable)")
	flags.Var(&formatSort, "S", "alias for --format-sort")
	preferFreeFormats := flags.Bool("prefer-free-formats", false, "prefer free containers when otherwise equivalent")
	allowUnplayable := flags.Bool("allow-unplayable-formats", false, "include DRM-marked formats in selection")
	progressTemplate := flags.String("progress-template", "", "render download events with a bounded progress template")
	flags.Var(&matchFilters, "match-filter", "metadata filter expression (repeatable OR)")
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
	var configLocations stringListFlag
	flags.Var(&configLocations, "config-location", "load an additional configuration file")
	flags.Var(&configLocations, "config-locations", "alias for --config-location")
	_ = flags.Bool("ignore-config", false, "skip default configuration files")
	_ = flags.Bool("no-config", false, "alias for --ignore-config")
	_ = flags.Bool("no-config-locations", false, "clear inherited explicit configuration locations")
	if err := flags.Parse(args); err != nil {
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
	if *telemetryJSON && *printJSON {
		fmt.Fprintln(stderr, "ytdlp-go: --telemetry-json and --print-json cannot share stdout")
		return 2
	}

	handler := func(_ context.Context, event ytdlp.Event) error {
		if *progressJSON {
			return json.NewEncoder(stderr).Encode(event)
		}
		if *quiet {
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
	result, err := client.Run(ctx, ytdlp.Request{
		URL: flags.Arg(0), OutputTemplate: *output, OutputDir: *outputDir, Proxy: *proxy,
		CookieFile: *cookieFile, CookiesFromBrowser: *cookiesFromBrowser, DownloadArchive: *downloadArchive, CacheDir: *cacheDir,
		Timeout: *timeout, Overwrite: *overwrite, SkipDownload: *skipDownload,
		Format: *format, FormatSort: append([]string(nil), formatSort...),
		PreferFreeFormats: *preferFreeFormats, AllowUnplayableFormats: *allowUnplayable,
		ProgressTemplate: *progressTemplate, MatchFilters: append([]string(nil), matchFilters...),
		ParseMetadata: append([]string(nil), parseMetadata...), ReplaceMetadata: append([]string(nil), replaceMetadata...),
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
	if *printJSON {
		_, _ = stdout.Write(result.InfoJSON)
		_, _ = fmt.Fprintln(stdout)
	}
	return 0
}

type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ",") }
func (values *stringListFlag) Set(value string) error {
	*values = append(*values, value)
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
