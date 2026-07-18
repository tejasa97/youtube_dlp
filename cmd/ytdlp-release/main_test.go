package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"runtime/debug"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/release"
)

func TestDependencyRecordsAreDeterministicAndCovered(t *testing.T) {
	dependencies := []*debug.Module{{Path: "example.invalid/b", Version: "v1.2.0"}, {Path: "example.invalid/a", Version: "v1.0.0"}}
	components, licenses := dependencyRecords(dependencies, []byte("explicit NOASSERTION notice\n"))
	if err := release.ValidateLicenseCoverage(components, licenses); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("example.invalid/b"))
	if components[0].SPDXID != "SPDXRef-Module-"+hex.EncodeToString(digest[:8]) {
		t.Fatalf("component = %#v", components[0])
	}
}

func TestBuildSetting(t *testing.T) {
	settings := []debug.BuildSetting{{Key: "GOOS", Value: "linux"}}
	if got := buildSetting(settings, "GOOS"); got != "linux" {
		t.Fatalf("GOOS = %q", got)
	}
	if got := buildSetting(settings, "GOARCH"); got != "" {
		t.Fatalf("missing = %q", got)
	}
}

func TestRunRejectsMissingInputs(t *testing.T) {
	if err := run(context.Background(), nil); err == nil {
		t.Fatal("missing inputs accepted")
	}
}

func TestRunReportsAPIVersion(t *testing.T) {
	if err := run(context.Background(), []string{"--api-version"}); err != nil {
		t.Fatal(err)
	}
}

func TestProjectReleaseMetadataDeclaresApacheLicense(t *testing.T) {
	version := "v0.1.0-alpha.1"
	projectLicense := []byte("Apache License\nVersion 2.0\n")
	components, licenses := dependencyRecords(nil, []byte("notices\n"))
	components, licenses = appendProjectLicense(components, licenses, version, projectLicense)
	if err := release.ValidateLicenseCoverage(components, licenses); err != nil {
		t.Fatal(err)
	}
	if components[0].LicenseDeclared != "Apache-2.0" || licenses[0].SPDX != "Apache-2.0" {
		t.Fatalf("component = %#v, license = %#v", components[0], licenses[0])
	}
}

func TestArtifactEntriesIncludeProjectLicense(t *testing.T) {
	projectLicense := []byte("project license\n")
	entries := artifactEntries("ytdlp-go", []byte("binary"), projectLicense, []byte("bundle"), []byte("sbom"), []byte("notices"), nil)
	for _, entry := range entries {
		if entry.Name == "LICENSE" {
			if string(entry.Data) != string(projectLicense) {
				t.Fatalf("license = %q", entry.Data)
			}
			return
		}
	}
	t.Fatal("project license missing from artifact")
}

func TestReadLicenseEntriesRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir() + string(os.PathSeparator) + "LICENSE"
	if err := os.WriteFile(target, []byte("license\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, root+string(os.PathSeparator)+"dependency.LICENSE"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readLicenseEntries(root); !errors.Is(err, release.ErrUnsafePath) {
		t.Fatalf("error = %v", err)
	}
}

func TestAtomicWriteDoesNotReplaceExistingFile(t *testing.T) {
	root := t.TempDir()
	path := root + string(os.PathSeparator) + "release.json"
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(root, "release.json", []byte("replacement")); !errors.Is(err, release.ErrUnsafePath) {
		t.Fatalf("error = %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "existing" {
		t.Fatalf("body = %q, error = %v", body, err)
	}
}
