// Command diffcheck compares normalized Go and reference snapshots.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ytdlp-go/ytdlp/internal/differential"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("diffcheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	policyPath := flags.String("policy", "", "comparison policy YAML (strict comparison when omitted)")
	jsonPath := flags.String("json", "", "write the machine-readable report to this path")
	markdownPath := flags.String("markdown", "", "write the Markdown review report to this path")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 2 {
		fmt.Fprintln(stderr, "usage: diffcheck [-policy policy.yaml] [-json report.json] [-markdown report.md] expected.json actual.json")
		return 2
	}

	expected, err := differential.LoadDocument(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "diffcheck: expected: %v\n", err)
		return 2
	}
	actual, err := differential.LoadDocument(flags.Arg(1))
	if err != nil {
		fmt.Fprintf(stderr, "diffcheck: actual: %v\n", err)
		return 2
	}
	policy := differential.DefaultPolicy()
	if *policyPath != "" {
		policy, err = differential.LoadPolicyFile(*policyPath)
		if err != nil {
			fmt.Fprintf(stderr, "diffcheck: %v\n", err)
			return 2
		}
	}
	report := differential.Compare(expected, actual, policy)
	if err := writeReport(*jsonPath, stdout, func(writer io.Writer) error {
		return differential.WriteJSON(writer, report)
	}); err != nil {
		fmt.Fprintf(stderr, "diffcheck: JSON report: %v\n", err)
		return 2
	}
	if *markdownPath != "" {
		if err := writeReport(*markdownPath, io.Discard, func(writer io.Writer) error {
			return differential.WriteMarkdown(writer, report)
		}); err != nil {
			fmt.Fprintf(stderr, "diffcheck: Markdown report: %v\n", err)
			return 2
		}
	}
	if !report.Equal {
		return 1
	}
	return 0
}

func writeReport(path string, fallback io.Writer, write func(io.Writer) error) error {
	if path == "" {
		return write(fallback)
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(file); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
