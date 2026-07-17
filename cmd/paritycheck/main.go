// Command paritycheck validates the capability manifest.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ytdlp-go/ytdlp/internal/conformance"
)

func main() {
	flags := flag.NewFlagSet("paritycheck", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	summary := flags.Bool("summary", false, "print a Markdown capability summary")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "usage: paritycheck [-summary] [manifest]")
		os.Exit(2)
	}
	path := "conformance/parity_manifest.yaml"
	if flags.NArg() == 1 {
		path = flags.Arg(0)
	}

	manifest, err := conformance.LoadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "paritycheck: %v\n", err)
		os.Exit(1)
	}
	if *summary {
		if err := conformance.WriteSummary(os.Stdout, manifest); err != nil {
			fmt.Fprintf(os.Stderr, "paritycheck: write summary: %v\n", err)
			os.Exit(1)
		}
		return
	}
	fmt.Printf("validated %d capabilities in %s\n", len(manifest.Capabilities), path)
}
