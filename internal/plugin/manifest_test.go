package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

func TestDecodeManifestV1UpgradeFixtures(t *testing.T) {
	baseline := readManifestFixture(t, "manifest-v1.0.json")
	upgrade := readManifestFixture(t, "manifest-v1.1.json")
	if baseline.ID != upgrade.ID || ManifestRange(baseline).Maximum != ProtocolV1_0 ||
		ManifestRange(upgrade).Minimum != ProtocolV1_0 || ManifestRange(upgrade).Maximum != ProtocolV1_1 {
		t.Fatalf("baseline, upgrade = %#v, %#v", baseline, upgrade)
	}
	selected, err := NegotiateRange(ManifestRange(baseline), ManifestRange(upgrade))
	if err != nil || selected != ProtocolV1_0 {
		t.Fatalf("v1.0 host / v1.1 plugin negotiation = %d, %v", selected, err)
	}
}

func TestValidateManifestRejectsPythonAndMalformedDeclarations(t *testing.T) {
	valid := readManifestFixture(t, "manifest-v1.0.json")
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   error
	}{
		{"python runtime", func(value *Manifest) { value.Runtime = "python" }, ErrPythonRuntime},
		{"python entrypoint", func(value *Manifest) { value.Entrypoint = "plugin.py" }, ErrPythonRuntime},
		{"traversal", func(value *Manifest) { value.Entrypoint = "../plugin" }, ErrInvalidManifest},
		{"unknown capability", func(value *Manifest) { value.Capabilities = []Capability{"shell"} }, ErrInvalidManifest},
		{"duplicate permission", func(value *Manifest) { value.Permissions = append(value.Permissions, value.Permissions[0]) }, ErrInvalidManifest},
		{"major upgrade", func(value *Manifest) { value.ABIRange = VersionRange{Minimum: 2, Maximum: 2} }, ErrIncompatibleVersion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Capabilities = append([]Capability(nil), valid.Capabilities...)
			candidate.Permissions = append([]Permission(nil), valid.Permissions...)
			test.mutate(&candidate)
			if err := ValidateManifest(candidate); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDecodeManifestBoundsUnknownFieldsAndTrailingJSON(t *testing.T) {
	valid := readManifestFixture(t, "manifest-v1.0.json")
	payload, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeManifest(bytes.NewReader(payload), int64(len(payload)-1)); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("size error = %v", err)
	}
	unknown := bytes.Replace(payload, []byte(`"schema":`), []byte(`"unknown":true,"schema":`), 1)
	if _, err := DecodeManifest(bytes.NewReader(unknown), 0); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, err := DecodeManifest(bytes.NewReader(append(payload, []byte(" {}")...)), 0); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("trailing error = %v", err)
	}
	duplicate := bytes.Replace(payload, []byte(`"minimum":1`), []byte(`"minimum":1,"minimum":1`), 1)
	if _, err := DecodeManifest(bytes.NewReader(duplicate), 0); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("nested duplicate-key error = %v", err)
	}
}

func TestDiscoverUsesOnlyCanonicalSecureRoots(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	writePackage(t, root, "z-last", "fixture.z")
	writePackage(t, root, "a-first", "fixture.a")
	packages, err := Discover(DiscoveryConfig{TrustedRoots: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{packages[0].Manifest.ID, packages[1].Manifest.ID}; !reflect.DeepEqual(got, []string{"fixture.a", "fixture.z"}) {
		t.Fatalf("discovery order = %v", got)
	}
	if packages[0].ExecutableDigest == "" || packages[0].ManifestDigest == "" || !filepath.IsAbs(packages[0].EntrypointPath) {
		t.Fatalf("package descriptor = %#v", packages[0])
	}
	if _, err := Discover(DiscoveryConfig{TrustedRoots: []string{"."}}); !errors.Is(err, ErrUntrustedPath) {
		t.Fatalf("relative-root error = %v", err)
	}
}

func TestDiscoverRejectsWritableAndSymlinkPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX ownership mode test")
	}
	writable := t.TempDir()
	if err := os.Chmod(writable, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(DiscoveryConfig{TrustedRoots: []string{writable}}); !errors.Is(err, ErrUntrustedPath) {
		t.Fatalf("writable-root error = %v", err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writePackage(t, outside, "outside", "fixture.outside")
	if err := os.Symlink(filepath.Join(outside, "outside"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	packages, err := Discover(DiscoveryConfig{TrustedRoots: []string{root}})
	if err != nil || len(packages) != 0 {
		t.Fatalf("symlink package discovery = %#v, %v", packages, err)
	}
	packageRoot := t.TempDir()
	if err := os.Chmod(packageRoot, 0700); err != nil {
		t.Fatal(err)
	}
	writePackage(t, packageRoot, "writable", "fixture.writable")
	if err := os.Chmod(filepath.Join(packageRoot, "writable"), 0770); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(DiscoveryConfig{TrustedRoots: []string{packageRoot}}); !errors.Is(err, ErrUntrustedPath) {
		t.Fatalf("writable-package error = %v", err)
	}
}

func TestLoadPackageRejectsInterpreterTrampolineAndOversize(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure discovery fails closed before file inspection on Windows")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	writePackage(t, root, "python", "fixture.python")
	pythonEntrypoint := filepath.Join(root, "python", "fixture-plugin")
	if err := os.WriteFile(pythonEntrypoint, []byte("#!/usr/bin/env python3\nprint('fixture')\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPackage(root, filepath.Join(root, "python"), 0); !errors.Is(err, ErrPythonRuntime) {
		t.Fatalf("Python shebang error = %v", err)
	}
	writePackage(t, root, "shell", "fixture.shell")
	shellEntrypoint := filepath.Join(root, "shell", "fixture-plugin")
	if err := os.WriteFile(shellEntrypoint, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPackage(root, filepath.Join(root, "shell"), 0); !errors.Is(err, ErrUntrustedPath) {
		t.Fatalf("shell shebang error = %v", err)
	}
	writePackage(t, root, "oversize", "fixture.oversize")
	oversize := filepath.Join(root, "oversize", "fixture-plugin")
	if err := os.Truncate(oversize, maximumEntrypointBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPackage(root, filepath.Join(root, "oversize"), 0); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("oversize entrypoint error = %v", err)
	}
}

func TestRevalidatePackageDetectsMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure discovery unavailable")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	writePackage(t, root, "mutable", "fixture.mutable")
	loaded, err := LoadPackage(root, filepath.Join(root, "mutable"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loaded.EntrypointPath, []byte("changed executable"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := RevalidatePackage(loaded); !errors.Is(err, ErrUntrustedPath) {
		t.Fatalf("mutation error = %v", err)
	}
}

func FuzzDecodeManifest(f *testing.F) {
	payload, err := os.ReadFile("../../conformance/plugin/abi-v1/manifest-v1.0.json")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(payload)
	f.Add([]byte(`{"runtime":"python"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeManifest(bytes.NewReader(data), 64<<10)
	})
}

func readManifestFixture(t *testing.T, name string) Manifest {
	t.Helper()
	file, err := os.Open(filepath.Join("../../conformance/plugin/abi-v1", name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	manifest, err := DecodeManifest(file, 0)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writePackage(t *testing.T, root, directory, id string) {
	t.Helper()
	path := filepath.Join(root, directory)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	entrypoint := filepath.Join(path, "fixture-plugin")
	if err := os.WriteFile(entrypoint, []byte("fixture executable"), 0700); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Schema: ManifestSchema, ID: id, Name: id, Release: "1.0.0", Runtime: pluginapi.RuntimeNative,
		Entrypoint: "fixture-plugin", ABIRange: VersionRange{Minimum: ProtocolV1_0, Maximum: ProtocolV1_0},
		Capabilities: []Capability{CapabilityExtractor},
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "plugin.json"), payload, 0600); err != nil {
		t.Fatal(err)
	}
}
