package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/release"
)

const apiVersion = "ytdlp-release/v1alpha1"

const expectedMainPackage = "github.com/ytdlp-go/ytdlp/cmd/ytdlp-go"

type stringsFlag []string

func (values *stringsFlag) String() string { return strings.Join(*values, ",") }
func (values *stringsFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type artifactInput struct {
	target release.Target
	data   []byte
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ytdlp-release:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 1 && args[0] == "--api-version" {
		_, err := fmt.Fprintln(os.Stdout, apiVersion)
		return err
	}
	flags := flag.NewFlagSet("ytdlp-release", flag.ContinueOnError)
	version := flags.String("version", "", "release version")
	commit := flags.String("commit", "", "lowercase hexadecimal source commit")
	epochText := flags.String("epoch", "", "canonical UTC RFC3339 source epoch")
	output := flags.String("output", "", "existing output directory")
	noticesPath := flags.String("notices", "THIRD_PARTY_NOTICES.md", "third-party notices input")
	licensesPath := flags.String("license-dir", "third_party/licenses", "third-party license directory")
	var artifacts stringsFlag
	flags.Var(&artifacts, "artifact", "GOOS/GOARCH:binary path (repeatable)")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return release.ErrInvalidInput
	}
	epoch, err := time.Parse(time.RFC3339, *epochText)
	if err != nil || epoch.UTC().Format(time.RFC3339) != *epochText || *output == "" {
		return release.ErrInvalidInput
	}
	inputs, dependencies, err := readArtifacts(artifacts)
	if err != nil {
		return err
	}
	notices, err := os.ReadFile(*noticesPath)
	if err != nil {
		return release.ErrIO
	}
	extraLicenseEntries, err := readLicenseEntries(*licensesPath)
	if err != nil {
		return err
	}
	components, licenses := dependencyRecords(dependencies, notices)
	components = append(components, release.Component{Name: "github.com/ytdlp-go/ytdlp", Version: *version, SPDXID: "SPDXRef-ytdlp-go", LicenseDeclared: "NOASSERTION", Download: "NOASSERTION"})
	licenses = append(licenses, release.License{Component: "github.com/ytdlp-go/ytdlp", Version: *version, SPDX: "NOASSERTION", Text: []byte("NOASSERTION: this repository has no project-wide distribution license.\n")})
	if err := release.ValidateLicenseCoverage(components, licenses); err != nil {
		return err
	}
	var licenseBundle, sbom bytes.Buffer
	if err := release.WriteLicenseBundle(&licenseBundle, licenses); err != nil {
		return err
	}
	namespace := "https://github.com/ytdlp-go/ytdlp/releases/" + *version + "/spdx"
	if err := release.WriteSPDX(&sbom, release.SBOMOptions{Name: "ytdlp-go " + *version, Namespace: namespace, Created: epoch, Creator: "Tool: ytdlp-release", Components: components}); err != nil {
		return err
	}
	archives := make(map[string][]byte, len(inputs))
	manifestInputs := make(map[string]release.ArtifactInput, len(inputs))
	for _, input := range inputs {
		format, suffix := release.FormatTarGzip, ".tar.gz"
		binaryName := "ytdlp-go"
		if input.target.GOOS == "windows" {
			format, suffix, binaryName = release.FormatZIP, ".zip", "ytdlp-go.exe"
		}
		name := "ytdlp-go_" + *version + "_" + input.target.GOOS + "_" + input.target.GOARCH + suffix
		entries := []release.Entry{{Name: binaryName, Data: input.data, Executable: true}, {Name: "LICENSES.txt", Data: licenseBundle.Bytes()}, {Name: "SBOM.spdx.json", Data: sbom.Bytes()}, {Name: "THIRD_PARTY_NOTICES.md", Data: notices}}
		entries = append(entries, extraLicenseEntries...)
		var archive bytes.Buffer
		if err := release.WriteArchive(ctx, &archive, format, entries, epoch); err != nil {
			return err
		}
		archives[name] = archive.Bytes()
		manifestInputs[name] = release.ArtifactInput{Target: input.target, Data: archive.Bytes()}
	}
	manifest, err := release.NewManifest(*version, *commit, epoch, manifestInputs)
	if err != nil {
		return err
	}
	var checksums, manifestJSON bytes.Buffer
	if err := release.WriteChecksums(&checksums, archives); err != nil {
		return err
	}
	if err := release.WriteManifest(&manifestJSON, manifest); err != nil {
		return err
	}
	for name, body := range archives {
		if err := atomicWrite(*output, name, body); err != nil {
			return err
		}
	}
	for name, body := range map[string][]byte{"SHA256SUMS": checksums.Bytes(), "release.json": manifestJSON.Bytes(), "SBOM.spdx.json": sbom.Bytes(), "LICENSES.txt": licenseBundle.Bytes()} {
		if err := atomicWrite(*output, name, body); err != nil {
			return err
		}
	}
	return nil
}

func readArtifacts(specs []string) ([]artifactInput, []*debug.Module, error) {
	if len(specs) == 0 || len(specs) > 16 {
		return nil, nil, release.ErrInvalidInput
	}
	var inputs []artifactInput
	var baseline []*debug.Module
	seen := map[string]bool{}
	for _, spec := range specs {
		targetText, path, ok := strings.Cut(spec, ":")
		parts := strings.Split(targetText, "/")
		if !ok || len(parts) != 2 || path == "" || seen[targetText] {
			return nil, nil, release.ErrInvalidInput
		}
		target := release.Target{GOOS: parts[0], GOARCH: parts[1]}
		if err := target.Validate(); err != nil {
			return nil, nil, err
		}
		seen[targetText] = true
		body, err := os.ReadFile(path)
		if err != nil || len(body) == 0 || len(body) > 512<<20 {
			return nil, nil, release.ErrIO
		}
		info, err := buildinfo.ReadFile(path)
		if err != nil {
			return nil, nil, release.ErrInvalidInput
		}
		if info.Path != expectedMainPackage || buildSetting(info.Settings, "GOOS") != target.GOOS || buildSetting(info.Settings, "GOARCH") != target.GOARCH || buildSetting(info.Settings, "CGO_ENABLED") != "0" || buildSetting(info.Settings, "-trimpath") != "true" {
			return nil, nil, release.ErrInvalidInput
		}
		for _, dependency := range info.Deps {
			if dependency.Replace != nil {
				return nil, nil, release.ErrInvalidInput
			}
		}
		deps := normalizedDependencies(info.Deps)
		if baseline == nil {
			baseline = deps
		} else if dependencyIdentity(baseline) != dependencyIdentity(deps) {
			return nil, nil, release.ErrNotReproducible
		}
		inputs = append(inputs, artifactInput{target: target, data: body})
	}
	sort.Slice(inputs, func(i, j int) bool {
		return inputs[i].target.GOOS+"/"+inputs[i].target.GOARCH < inputs[j].target.GOOS+"/"+inputs[j].target.GOARCH
	})
	return inputs, baseline, nil
}

func buildSetting(settings []debug.BuildSetting, key string) string {
	for _, setting := range settings {
		if setting.Key == key {
			return setting.Value
		}
	}
	return ""
}

func normalizedDependencies(input []*debug.Module) []*debug.Module {
	result := append([]*debug.Module(nil), input...)
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}
func dependencyIdentity(values []*debug.Module) string {
	var builder strings.Builder
	for _, value := range values {
		fmt.Fprintf(&builder, "%s@%s\n", value.Path, value.Version)
	}
	return builder.String()
}

func dependencyRecords(dependencies []*debug.Module, notice []byte) ([]release.Component, []release.License) {
	components := make([]release.Component, 0, len(dependencies))
	licenses := make([]release.License, 0, len(dependencies))
	for _, dependency := range dependencies {
		digest := sha256.Sum256([]byte(dependency.Path))
		id := "SPDXRef-Module-" + hex.EncodeToString(digest[:8])
		components = append(components, release.Component{Name: dependency.Path, Version: dependency.Version, SPDXID: id, LicenseDeclared: "NOASSERTION", Download: "NOASSERTION"})
		licenses = append(licenses, release.License{Component: dependency.Path, Version: dependency.Version, SPDX: "NOASSERTION", Text: notice})
	}
	return components, licenses
}

func readLicenseEntries(root string) ([]release.Entry, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, release.ErrUnsafePath
	}
	directory, err := os.ReadDir(root)
	if err != nil {
		return nil, release.ErrIO
	}
	entries := make([]release.Entry, 0, len(directory))
	for _, item := range directory {
		info, err := item.Info()
		if err != nil || !item.Type().IsRegular() || !info.Mode().IsRegular() || filepath.Base(item.Name()) != item.Name() {
			return nil, release.ErrUnsafePath
		}
		body, err := os.ReadFile(filepath.Join(root, item.Name()))
		if err != nil || len(body) == 0 || len(body) > 4<<20 {
			return nil, release.ErrIO
		}
		entries = append(entries, release.Entry{Name: "licenses/" + item.Name(), Data: body})
	}
	return entries, nil
}

func atomicWrite(root, name string, body []byte) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || filepath.Base(name) != name {
		return release.ErrUnsafePath
	}
	temporary, err := os.CreateTemp(root, ".release-")
	if err != nil {
		return release.ErrIO
	}
	path := temporary.Name()
	defer os.Remove(path)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return release.ErrIO
	}
	if _, err := temporary.Write(body); err != nil {
		temporary.Close()
		return release.ErrIO
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return release.ErrIO
	}
	if err := temporary.Close(); err != nil {
		return release.ErrIO
	}
	destination := filepath.Join(root, name)
	if _, err := os.Lstat(destination); err == nil || !errors.Is(err, os.ErrNotExist) {
		return release.ErrUnsafePath
	}
	if err := os.Link(path, destination); err != nil {
		return release.ErrUnsafePath
	}
	return nil
}
