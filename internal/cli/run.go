// Package cli implements the command-line boundary.
package cli

import (
	"flag"
	"fmt"
	"io"
)

const Version = "0.0.0-dev"

// Run executes the CLI and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ytdlp-go", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: ytdlp-go [OPTIONS] URL")
		fmt.Fprintln(flags.Output())
		fmt.Fprintln(flags.Output(), "Experimental Python-free Go port of yt-dlp.")
		fmt.Fprintln(flags.Output())
		flags.PrintDefaults()
	}

	showVersion := flags.Bool("version", false, "print the version and exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "ytdlp-go %s\n", Version)
		return 0
	}
	if flags.NArg() == 0 {
		flags.Usage()
		return 2
	}
	if flags.NArg() > 1 {
		fmt.Fprintln(stderr, "ytdlp-go: this foundation build accepts exactly one URL")
		return 2
	}

	fmt.Fprintln(stderr, "ytdlp-go: extraction and downloading are not implemented in this foundation build")
	return 1
}
