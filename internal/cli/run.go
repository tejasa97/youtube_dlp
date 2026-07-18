// Package cli implements the command-line boundary.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

const Version = "0.0.0-dev"

func Run(args []string, stdout, stderr io.Writer) int {
	return RunContext(context.Background(), args, stdout, stderr)
}

func RunContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
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
	outputDir := flags.String("output-dir", ".", "directory that confines output files")
	printJSON := flags.Bool("print-json", false, "print normalized metadata JSON to stdout")
	skipDownload := flags.Bool("skip-download", false, "extract metadata without downloading")
	proxy := flags.String("proxy", "", "HTTP/HTTPS proxy URL")
	timeout := flags.Duration("socket-timeout", 30*time.Second, "network operation timeout")
	overwrite := flags.Bool("force-overwrites", false, "replace an existing final file")
	progressJSON := flags.Bool("progress-json", false, "write newline-delimited progress events to stderr")
	quiet := flags.Bool("quiet", false, "suppress human-readable progress")
	javascriptHelper := flags.String("js-helper", "", "path to the isolated JavaScript helper")
	cookiesFromBrowser := flags.String("cookies-from-browser", "", "import cookies from chrome[:PROFILE] (macOS pilot)")
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
	client := ytdlp.NewClient(ytdlp.WithEventHandler(handler), ytdlp.WithJavaScriptHelper(*javascriptHelper))
	result, err := client.Run(ctx, ytdlp.Request{
		URL: flags.Arg(0), OutputTemplate: *output, OutputDir: *outputDir, Proxy: *proxy,
		CookiesFromBrowser: *cookiesFromBrowser, Timeout: *timeout, Overwrite: *overwrite, SkipDownload: *skipDownload,
	})
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
	case ytdlp.IsCategory(err, ytdlp.ErrorCancelled), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return 130
	default:
		return 1
	}
}
