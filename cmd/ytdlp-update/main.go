package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintf(stdout, "ytdlp-update %s\n", ytdlp.APIVersion)
		return 0
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: ytdlp-update <apply|rollback|snapshot|active> [options]")
		return 2
	}
	operation, args := args[0], args[1:]
	flags := flag.NewFlagSet("ytdlp-update "+operation, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "private absolute update root")
	metadataPath := flags.String("metadata", "", "signed update metadata (apply)")
	artifactPath := flags.String("artifact", "", "release artifact (apply)")
	product := flags.String("product", "ytdlp-go", "signed product scope")
	channel := flags.String("channel", "stable", "release channel: stable, beta, or nightly")
	goos := flags.String("goos", runtime.GOOS, "target operating system")
	goarch := flags.String("goarch", runtime.GOARCH, "target architecture")
	nowText := flags.String("now", "", "current time as canonical UTC RFC3339")
	threshold := flags.Int("threshold", 1, "required trusted signatures")
	healthPrefix := flags.String("health-prefix", "ytdlp-go ", "exact health output prefix before the version")
	healthTimeout := flags.Duration("health-timeout", 10*time.Second, "bounded artifact health timeout")
	maxArtifact := flags.Int64("max-artifact-bytes", 512<<20, "maximum artifact size")
	var publicKeys, healthArgs stringList
	flags.Var(&publicKeys, "public-key", "trusted Ed25519 public key in hex (repeatable)")
	flags.Var(&healthArgs, "health-arg", "health-check argv item (repeatable; defaults to --version)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if *root == "" {
		fmt.Fprintln(stderr, "ytdlp-update: --root is required")
		return 2
	}
	now, err := parseNow(*nowText)
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-update: %v\n", err)
		return 2
	}
	keys, err := parseKeys(publicKeys)
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-update: %v\n", err)
		return 2
	}
	selectedChannel, err := parseChannel(*channel)
	if err != nil {
		fmt.Fprintf(stderr, "ytdlp-update: %v\n", err)
		return 2
	}
	if len(healthArgs) == 0 {
		healthArgs = []string{"--version"}
	}
	updater, err := ytdlp.OpenUpdater(ctx, *root, ytdlp.UpdateOptions{
		Trust: ytdlp.UpdateTrust{
			Keys: keys, Threshold: *threshold, Role: "release", Product: *product,
			Channels:  []ytdlp.UpdateChannel{selectedChannel},
			Platforms: []ytdlp.UpdatePlatform{{GOOS: *goos, GOARCH: *goarch}},
		},
		Product: *product, Channel: selectedChannel, GOOS: *goos, GOARCH: *goarch,
		Clock: func() time.Time { return now }, MaxArtifactSize: *maxArtifact,
		Health: ytdlp.CommandUpdateHealthChecker{
			Arguments: append([]string(nil), healthArgs...), OutputPrefix: *healthPrefix,
			Timeout: *healthTimeout, MaxOutput: 64 << 10,
		},
	})
	if err != nil {
		return report(stderr, err)
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	switch operation {
	case "apply":
		metadata, err := readSmallFile(*metadataPath, 2<<20, "metadata")
		if err != nil {
			fmt.Fprintf(stderr, "ytdlp-update: %v\n", err)
			return 2
		}
		artifact, err := os.Open(*artifactPath)
		if err != nil {
			fmt.Fprintln(stderr, "ytdlp-update: open artifact failed")
			return 2
		}
		state, applyErr := updater.Apply(ctx, metadata, artifact)
		closeErr := artifact.Close()
		if applyErr != nil {
			return report(stderr, applyErr)
		}
		if closeErr != nil {
			fmt.Fprintln(stderr, "ytdlp-update: close artifact failed")
			return 1
		}
		return encode(encoder, state)
	case "rollback":
		state, err := updater.Rollback(ctx)
		if err != nil {
			return report(stderr, err)
		}
		return encode(encoder, state)
	case "snapshot":
		state, err := updater.Snapshot(ctx)
		if err != nil {
			return report(stderr, err)
		}
		return encode(encoder, state)
	case "active":
		path, err := updater.ActivePath(ctx)
		if err != nil {
			return report(stderr, err)
		}
		return encode(encoder, struct {
			Path string `json:"path"`
		}{path})
	default:
		fmt.Fprintf(stderr, "ytdlp-update: unknown operation %q\n", operation)
		return 2
	}
}

func parseNow(input string) (time.Time, error) {
	value, err := time.Parse(time.RFC3339, input)
	if err != nil || value.UTC().Format(time.RFC3339) != input {
		return time.Time{}, errors.New("--now must be canonical UTC RFC3339")
	}
	return value, nil
}

func parseKeys(inputs []string) (map[string]ed25519.PublicKey, error) {
	if len(inputs) == 0 || len(inputs) > 32 {
		return nil, errors.New("at least one --public-key is required")
	}
	result := make(map[string]ed25519.PublicKey, len(inputs))
	for _, input := range inputs {
		decoded, err := hex.DecodeString(input)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return nil, errors.New("each --public-key must be a 32-byte Ed25519 key in hex")
		}
		key := ed25519.PublicKey(decoded)
		keyID := ytdlp.UpdateKeyID(key)
		if _, duplicate := result[keyID]; duplicate {
			return nil, errors.New("duplicate --public-key")
		}
		result[keyID] = key
	}
	return result, nil
}

func parseChannel(input string) (ytdlp.UpdateChannel, error) {
	switch input {
	case "stable":
		return ytdlp.UpdateChannelStable, nil
	case "beta":
		return ytdlp.UpdateChannelBeta, nil
	case "nightly":
		return ytdlp.UpdateChannelNightly, nil
	default:
		return "", errors.New("invalid --channel")
	}
}

func readSmallFile(path string, maximum int64, label string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("--%s is required", label)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s failed", label)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(body)) > maximum {
		return nil, fmt.Errorf("%s is unreadable or oversized", label)
	}
	return body, nil
}

func encode(encoder *json.Encoder, value any) int {
	if err := encoder.Encode(value); err != nil {
		return 1
	}
	return 0
}

func report(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "ytdlp-update: %v\n", err)
	switch {
	case ytdlp.IsCategory(err, ytdlp.ErrorCancelled):
		return 130
	case ytdlp.IsCategory(err, ytdlp.ErrorUnsupported):
		return 3
	case ytdlp.IsCategory(err, ytdlp.ErrorInvalidInput):
		return 2
	case ytdlp.IsCategory(err, ytdlp.ErrorSecurity):
		return 6
	default:
		return 1
	}
}
