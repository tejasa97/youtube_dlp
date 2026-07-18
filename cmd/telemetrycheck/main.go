// Command telemetrycheck merges privacy-safe snapshots and evaluates a local
// coverage gate. It has no network exporter and never accepts raw event data.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

const maxInputs = 256

type stringList []string

func (values *stringList) String() string { return fmt.Sprint([]string(*values)) }
func (values *stringList) Set(value string) error {
	if len(*values) >= maxInputs {
		return errors.New("too many values")
	}
	*values = append(*values, value)
	return nil
}

type report struct {
	Schema   string                  `json:"schema"`
	Inputs   int                     `json:"inputs"`
	Coverage ytdlp.TelemetryCoverage `json:"coverage"`
	Snapshot ytdlp.TelemetrySnapshot `json:"snapshot"`
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("telemetrycheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var inputs, extraExtractors stringList
	flags.Var(&inputs, "input", "snapshot path, or - for stdin (repeatable)")
	flags.Var(&extraExtractors, "extractor", "additional signed plugin extractor ID (repeatable)")
	minimum := flags.Uint("minimum-basis-points", 0, "minimum successful coverage from 0 through 10000")
	requireExact := flags.Bool("require-exact", false, "fail when overflow or saturation makes coverage inexact")
	requireZeroFallback := flags.Bool("require-zero-fallback", false, "fail when any fallback observation exists")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *minimum > 10_000 || len(inputs) == 0 {
		return 2
	}
	extractors := append(ytdlp.BuiltInExtractorIDs(), extraExtractors...)
	collector, err := ytdlp.NewTelemetryCollector(ytdlp.TelemetryConfig{Extractors: extractors})
	if err != nil {
		fmt.Fprintln(stderr, "telemetrycheck: invalid extractor configuration")
		return 2
	}
	usedStdin := false
	for _, input := range inputs {
		if err := ctx.Err(); err != nil {
			fmt.Fprintln(stderr, "telemetrycheck: canceled")
			return 130
		}
		var reader io.Reader
		var closeInput func() error
		if input == "-" {
			if usedStdin || stdin == nil {
				fmt.Fprintln(stderr, "telemetrycheck: stdin may be read once")
				return 2
			}
			usedStdin = true
			reader = stdin
		} else {
			file, openErr := os.Open(input)
			if openErr != nil {
				fmt.Fprintln(stderr, "telemetrycheck: cannot open snapshot")
				return 1
			}
			reader = file
			closeInput = file.Close
		}
		snapshot, decodeErr := ytdlp.DecodeTelemetrySnapshot(ctx, reader, 0)
		if closeInput != nil {
			_ = closeInput()
		}
		if decodeErr != nil {
			fmt.Fprintln(stderr, "telemetrycheck: invalid snapshot")
			if errors.Is(decodeErr, context.Canceled) || errors.Is(decodeErr, context.DeadlineExceeded) {
				return 130
			}
			return 1
		}
		if err := collector.Merge(ctx, snapshot); err != nil {
			fmt.Fprintln(stderr, "telemetrycheck: snapshot is outside the configured dimensions")
			return 1
		}
	}
	coverage, err := collector.Coverage()
	if err != nil {
		fmt.Fprintln(stderr, "telemetrycheck: cannot calculate coverage")
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(report{Schema: "ytdlp-go.telemetry-report/v1", Inputs: len(inputs), Coverage: coverage, Snapshot: collector.Snapshot()}); err != nil {
		fmt.Fprintln(stderr, "telemetrycheck: cannot write report")
		return 1
	}
	if *requireExact && !coverage.Exact || *requireZeroFallback && coverage.Fallback != 0 || coverage.Denominator == 0 && *minimum != 0 || coverage.BasisPoints < uint32(*minimum) {
		return 3
	}
	return 0
}
