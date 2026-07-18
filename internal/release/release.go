// Package release builds deterministic, Python-free release artifacts and
// metadata from explicit inputs.
package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidInput    = errors.New("invalid release input")
	ErrUnsafePath      = errors.New("unsafe release path")
	ErrTooLarge        = errors.New("release resource limit exceeded")
	ErrIO              = errors.New("release I/O failure")
	ErrNotReproducible = errors.New("release output is not reproducible")
)

const (
	maxEntries     = 1024
	maxEntryBytes  = 256 << 20
	maxTotalBytes  = 1 << 30
	maxComponents  = 4096
	maxLicenseText = 4 << 20
)

type Target struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}

func (target Target) Validate() error {
	if !validToken(target.GOOS, 32, "_-") || !validToken(target.GOARCH, 32, "_-") {
		return fmt.Errorf("%w: target", ErrInvalidInput)
	}
	return nil
}

// BuildPlan is a deterministic argv/environment overlay for a no-cgo Go
// build. Callers choose the Go toolchain externally and may add cache paths;
// the plan never searches PATH or executes a command itself.
type BuildPlan struct {
	Program     string
	Arguments   []string
	Environment []string
}

type BuildOptions struct {
	Target          Target
	MainPackage     string
	Output          string
	SourceDateEpoch time.Time
	Tags            []string
}

func NewBuildPlan(options BuildOptions) (BuildPlan, error) {
	if err := options.Target.Validate(); err != nil {
		return BuildPlan{}, err
	}
	if !safeBase(options.Output) || !safePackage(options.MainPackage) || !reasonableEpoch(options.SourceDateEpoch) || len(options.Tags) > 32 {
		return BuildPlan{}, ErrInvalidInput
	}
	tags := append([]string(nil), options.Tags...)
	sort.Strings(tags)
	for index, tag := range tags {
		if !validToken(tag, 64, "_-") || index > 0 && tag == tags[index-1] {
			return BuildPlan{}, fmt.Errorf("%w: build tag", ErrInvalidInput)
		}
	}
	arguments := []string{"build", "-trimpath", "-buildvcs=false", "-mod=readonly", "-ldflags=-buildid="}
	if len(tags) > 0 {
		arguments = append(arguments, "-tags="+strings.Join(tags, ","))
	}
	arguments = append(arguments, "-o", options.Output, options.MainPackage)
	environment := []string{
		"CGO_ENABLED=0",
		"GOARCH=" + options.Target.GOARCH,
		"GOOS=" + options.Target.GOOS,
		"SOURCE_DATE_EPOCH=" + fmt.Sprint(options.SourceDateEpoch.Unix()),
	}
	return BuildPlan{Program: "go", Arguments: arguments, Environment: environment}, nil
}

type Artifact struct {
	Name   string `json:"name"`
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Schema      string     `json:"schema"`
	Version     string     `json:"version"`
	Commit      string     `json:"commit"`
	GeneratedAt string     `json:"generated_at"`
	Artifacts   []Artifact `json:"artifacts"`
}

func NewManifest(version, commit string, generatedAt time.Time, contents map[string]struct {
	Target Target
	Data   []byte
}) (Manifest, error) {
	if !validVersion(version) || !validCommit(commit) || !reasonableEpoch(generatedAt) || len(contents) == 0 || len(contents) > maxEntries {
		return Manifest{}, ErrInvalidInput
	}
	names := make([]string, 0, len(contents))
	for name := range contents {
		names = append(names, name)
	}
	sort.Strings(names)
	artifacts := make([]Artifact, 0, len(names))
	for _, name := range names {
		content := contents[name]
		if !safeBase(name) || len(content.Data) == 0 || len(content.Data) > maxEntryBytes {
			return Manifest{}, ErrInvalidInput
		}
		if err := content.Target.Validate(); err != nil {
			return Manifest{}, err
		}
		digest := sha256.Sum256(content.Data)
		artifacts = append(artifacts, Artifact{Name: name, GOOS: content.Target.GOOS, GOARCH: content.Target.GOARCH, Size: int64(len(content.Data)), SHA256: hex.EncodeToString(digest[:])})
	}
	return Manifest{Schema: "ytdlp-go-release-v1", Version: version, Commit: commit, GeneratedAt: generatedAt.UTC().Format(time.RFC3339), Artifacts: artifacts}, nil
}

func WriteManifest(writer io.Writer, manifest Manifest) error {
	generatedAt, timeErr := time.Parse(time.RFC3339, manifest.GeneratedAt)
	if manifest.Schema != "ytdlp-go-release-v1" || !validVersion(manifest.Version) || !validCommit(manifest.Commit) || timeErr != nil || generatedAt.Format(time.RFC3339) != manifest.GeneratedAt || !reasonableEpoch(generatedAt) || len(manifest.Artifacts) == 0 || len(manifest.Artifacts) > maxEntries {
		return ErrInvalidInput
	}
	artifacts := append([]Artifact(nil), manifest.Artifacts...)
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Name < artifacts[j].Name })
	seen := make(map[string]struct{}, len(artifacts))
	targets := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		if !safeBase(artifact.Name) || artifact.Size <= 0 || artifact.Size > maxEntryBytes || !validDigest(artifact.SHA256) {
			return ErrInvalidInput
		}
		if err := (Target{GOOS: artifact.GOOS, GOARCH: artifact.GOARCH}).Validate(); err != nil {
			return err
		}
		folded := strings.ToLower(artifact.Name)
		if _, duplicate := seen[folded]; duplicate {
			return ErrInvalidInput
		}
		seen[folded] = struct{}{}
		target := artifact.GOOS + "/" + artifact.GOARCH
		if _, duplicate := targets[target]; duplicate {
			return ErrInvalidInput
		}
		targets[target] = struct{}{}
	}
	manifest.Artifacts = artifacts
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("%w: encode manifest", ErrIO)
	}
	if _, err := writer.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("%w: write manifest", ErrIO)
	}
	return nil
}

// CheckReproducible executes a pure builder twice and compares the complete
// bytes. Its bounded buffers prevent a broken builder from exhausting memory.
func CheckReproducible(ctx context.Context, build func(context.Context, io.Writer) error, maximum int64) (string, error) {
	if build == nil || maximum <= 0 || maximum > maxTotalBytes {
		return "", ErrInvalidInput
	}
	outputs := [2]bytes.Buffer{}
	for index := range outputs {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		limited := &limitWriter{writer: &outputs[index], remaining: maximum}
		if err := build(ctx, limited); err != nil {
			return "", err
		}
		if limited.exceeded {
			return "", ErrTooLarge
		}
	}
	if !bytes.Equal(outputs[0].Bytes(), outputs[1].Bytes()) {
		return "", ErrNotReproducible
	}
	digest := sha256.Sum256(outputs[0].Bytes())
	return hex.EncodeToString(digest[:]), nil
}

type limitWriter struct {
	writer    io.Writer
	remaining int64
	exceeded  bool
}

func (writer *limitWriter) Write(value []byte) (int, error) {
	if int64(len(value)) > writer.remaining {
		writer.exceeded = true
		return 0, ErrTooLarge
	}
	written, err := writer.writer.Write(value)
	writer.remaining -= int64(written)
	return written, err
}

func safePackage(value string) bool {
	if value == "" || len(value) > 256 || strings.IndexByte(value, 0) >= 0 || strings.ContainsAny(value, " \\;|&$`\n\r\t") {
		return false
	}
	trimmed := strings.TrimPrefix(value, "./")
	clean := filepath.ToSlash(filepath.Clean(trimmed))
	return clean == trimmed && trimmed != "." && !strings.HasPrefix(trimmed, "../") && !filepath.IsAbs(value)
}

func safeBase(value string) bool {
	return value != "" && len(value) <= 128 && value == filepath.Base(value) && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00") && !strings.HasSuffix(value, ".") && !strings.HasSuffix(value, " ") && !windowsReserved(value)
}

func windowsReserved(value string) bool {
	stem := strings.ToUpper(strings.SplitN(value, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validToken(value string, maximum int, extra string) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune(extra, character) {
			continue
		}
		return false
	}
	return true
}

func validVersion(value string) bool { return validToken(value, 64, ".-_") }

func validCommit(value string) bool {
	if len(value) < 7 || len(value) > 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func reasonableEpoch(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Nanosecond() == 0 && value.Year() >= 2020 && value.Year() <= 3000
}
