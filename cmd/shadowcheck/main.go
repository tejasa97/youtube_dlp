// shadowcheck compares two bounded Phase 3 semantic observations locally.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ytdlp-go/ytdlp/internal/differential"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("shadowcheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	expectedPath := flags.String("expected", "", "reference observation JSON")
	actualPath := flags.String("actual", "", "Go observation JSON")
	failSeverity := flags.String("fail-severity", "critical", "failure threshold: none, critical, high, or any")
	maxMismatches := flags.Int("max-mismatches", differential.DefaultMaxMismatches, "maximum retained mismatches")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *expectedPath == "" || *actualPath == "" || !validThreshold(*failSeverity) {
		return 2
	}
	expected, err := loadObservation(ctx, *expectedPath)
	if err != nil {
		fmt.Fprintln(stderr, "shadowcheck: invalid expected observation")
		return errorCode(ctx)
	}
	actual, err := loadObservation(ctx, *actualPath)
	if err != nil {
		fmt.Fprintln(stderr, "shadowcheck: invalid actual observation")
		return errorCode(ctx)
	}
	policy := differential.DefaultShadowPolicy()
	policy.MaxMismatches = *maxMismatches
	report, err := differential.CompareObservations(ctx, expected, actual, policy)
	if err != nil {
		fmt.Fprintln(stderr, "shadowcheck: comparison failed")
		return errorCode(ctx)
	}
	encoded, _, err := differential.CanonicalShadowReport(ctx, report)
	if err != nil {
		fmt.Fprintln(stderr, "shadowcheck: report failed")
		return errorCode(ctx)
	}
	if _, err := stdout.Write(encoded); err != nil {
		return 1
	}
	if fails(report, *failSeverity) {
		return 3
	}
	return 0
}

func loadObservation(ctx context.Context, path string) (differential.ObservationEnvelope, error) {
	file, err := os.Open(path)
	if err != nil {
		return differential.ObservationEnvelope{}, err
	}
	defer file.Close()
	return differential.ParseObservation(ctx, file)
}

func validThreshold(value string) bool {
	return value == "none" || value == "critical" || value == "high" || value == "any"
}

func fails(report differential.ShadowReport, threshold string) bool {
	if threshold == "none" {
		return false
	}
	for _, mismatch := range report.Mismatches {
		if threshold == "any" || mismatch.Severity == differential.SeverityCritical || threshold == "high" && mismatch.Severity == differential.SeverityHigh {
			return true
		}
	}
	return report.Truncated && report.MismatchCount > report.StoredCount
}

func errorCode(ctx context.Context) int {
	if ctx != nil && ctx.Err() != nil {
		return 130
	}
	return 2
}
