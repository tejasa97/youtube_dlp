// ytdlp-ops validates canary evidence and produces bounded local operations summaries.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ytdlp-go/ytdlp/internal/operations"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ytdlp-ops <validate-suite|validate-policy|validate-replay|summarize> [options]")
		return 2
	}
	operation, args := args[0], args[1:]
	flags := flag.NewFlagSet("ytdlp-ops "+operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	suitePath := flags.String("suite", "", "versioned canary suite JSON")
	recordsPath := flags.String("records", "", "redacted canary record-set JSON")
	incidentsPath := flags.String("incidents", "", "redacted incident-set JSON")
	policyPath := flags.String("policy", "", "bounded canary execution policy JSON")
	replayPath := flags.String("replay", "", "redacted semantic replay JSON")
	maxRecords := flags.Int("max-records", 100000, "bounded rolling record window")
	maxIncidents := flags.Int("max-incidents", 100000, "bounded rolling incident window")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || *suitePath == "" {
		return 2
	}
	suiteFile, err := os.Open(*suitePath)
	if err != nil {
		fmt.Fprintln(stderr, "ytdlp-ops: open suite failed")
		return 2
	}
	suite, err := operations.DecodeSuite(ctx, suiteFile, 0)
	_ = suiteFile.Close()
	if err != nil {
		return report(stderr, err)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	switch operation {
	case "validate-suite":
		encoded, err := operations.MarshalSuite(suite)
		if err != nil {
			return report(stderr, err)
		}
		_, err = stdout.Write(append(encoded, '\n'))
		if err != nil {
			return 1
		}
		return 0
	case "validate-policy":
		if *policyPath == "" {
			fmt.Fprintln(stderr, "ytdlp-ops: --policy is required")
			return 2
		}
		file, err := os.Open(*policyPath)
		if err != nil {
			fmt.Fprintln(stderr, "ytdlp-ops: open policy failed")
			return 2
		}
		policy, decodeErr := operations.DecodeExecutionPolicy(ctx, file, 0, suite)
		_ = file.Close()
		if decodeErr != nil {
			return report(stderr, decodeErr)
		}
		encoded, err := operations.MarshalExecutionPolicy(suite, policy)
		if err != nil {
			return report(stderr, err)
		}
		if _, err := stdout.Write(append(encoded, '\n')); err != nil {
			return 1
		}
		return 0
	case "validate-replay":
		if *replayPath == "" {
			fmt.Fprintln(stderr, "ytdlp-ops: --replay is required")
			return 2
		}
		file, err := os.Open(*replayPath)
		if err != nil {
			fmt.Fprintln(stderr, "ytdlp-ops: open replay failed")
			return 2
		}
		replay, decodeErr := operations.DecodeReplay(ctx, file, 0, suite)
		_ = file.Close()
		if decodeErr != nil {
			return report(stderr, decodeErr)
		}
		encoded, err := operations.MarshalReplay(replay, suite)
		if err != nil {
			return report(stderr, err)
		}
		if _, err := stdout.Write(append(encoded, '\n')); err != nil {
			return 1
		}
		return 0
	case "summarize":
		if *recordsPath == "" {
			fmt.Fprintln(stderr, "ytdlp-ops: --records is required")
			return 2
		}
		metrics, err := operations.NewRollingMetrics(suite, *maxRecords, *maxIncidents)
		if err != nil {
			return report(stderr, err)
		}
		recordsFile, err := os.Open(*recordsPath)
		if err != nil {
			fmt.Fprintln(stderr, "ytdlp-ops: open records failed")
			return 2
		}
		records, err := operations.DecodeRecords(ctx, recordsFile, 0, suite)
		_ = recordsFile.Close()
		if err != nil {
			return report(stderr, err)
		}
		for _, record := range records {
			if err := metrics.AddRecord(record); err != nil {
				return report(stderr, err)
			}
		}
		if *incidentsPath != "" {
			incidentsFile, err := os.Open(*incidentsPath)
			if err != nil {
				fmt.Fprintln(stderr, "ytdlp-ops: open incidents failed")
				return 2
			}
			incidents, decodeErr := operations.DecodeIncidents(ctx, incidentsFile, 0, suite)
			_ = incidentsFile.Close()
			if decodeErr != nil {
				return report(stderr, decodeErr)
			}
			for _, incident := range incidents {
				if err := metrics.AddIncident(incident); err != nil {
					return report(stderr, err)
				}
			}
		}
		if err := encoder.Encode(metrics.Snapshot()); err != nil {
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "ytdlp-ops: unknown operation %q\n", operation)
		return 2
	}
}

func report(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "ytdlp-ops: %v\n", err)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 130
	}
	return 2
}
