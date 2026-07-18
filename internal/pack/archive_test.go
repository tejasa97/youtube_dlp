package pack

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var fixtureSeed = [ed25519.SeedSize]byte{
	0x37, 0x8e, 0xa4, 0x61, 0x90, 0x52, 0xc3, 0x77,
	0xa8, 0x4c, 0x26, 0x12, 0xfa, 0xe9, 0x42, 0x0b,
	0x88, 0x0f, 0x15, 0x43, 0xb1, 0x69, 0x4d, 0x60,
	0xe5, 0xd1, 0x31, 0x9a, 0x73, 0x6c, 0x22, 0x4f,
}

func fixtureKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(fixtureSeed[:])
}

func fixtureManifest(version string, permissions ...Permission) Manifest {
	return Manifest{
		SchemaVersion:  SchemaVersion,
		Name:           "fixture-pack",
		Version:        version,
		Runtime:        RuntimeRPC,
		Entrypoint:     "bin/fixture",
		CreatedAt:      "2026-01-01T00:00:00Z",
		ExpiresAt:      "2028-01-01T00:00:00Z",
		MinHostVersion: "1.2.0",
		Permissions:    permissions,
	}
}

func fixturePayload(body string) map[string]Payload {
	return map[string]Payload{
		"bin/fixture": {Bytes: []byte(body), Mode: 0o700},
		"README.txt":  {Bytes: []byte("synthetic fixture\n"), Mode: 0o600},
	}
}

func fixturePolicy(t *testing.T, now time.Time) VerifyPolicy {
	t.Helper()
	key := fixtureKey().Public().(ed25519.PublicKey)
	keyID, err := KeyID(key)
	if err != nil {
		t.Fatal(err)
	}
	return VerifyPolicy{Trust: map[string]ed25519.PublicKey{keyID: key}, Now: now, HostVersion: "1.4.0"}
}

func fixtureArchive(t *testing.T, version, body string, permissions ...Permission) []byte {
	t.Helper()
	archive, err := Build(fixtureManifest(version, permissions...), fixturePayload(body), fixtureKey())
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestBuildVerifyDeterministic(t *testing.T) {
	first := fixtureArchive(t, "1.0.0", "fixture executable", PermissionNetwork)
	second := fixtureArchive(t, "1.0.0", "fixture executable", PermissionNetwork)
	if !bytes.Equal(first, second) {
		t.Fatal("identical inputs produced different archives")
	}
	verified, err := Verify(first, fixturePolicy(t, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	if verified.Manifest.Name != "fixture-pack" || verified.Manifest.Version != "1.0.0" || string(verified.Payload["bin/fixture"]) != "fixture executable" {
		t.Fatalf("unexpected verification result: %#v", verified.Manifest)
	}
	digest := sha256.Sum256(first)
	if verified.ArchiveSHA256 != fmt.Sprintf("%x", digest) || verified.ArchiveSize != int64(len(first)) {
		t.Fatalf("archive identity mismatch: %#v", verified)
	}
}

func TestConformanceFixture(t *testing.T) {
	root := filepath.Join("..", "..", "conformance", "packs")
	manifestBody, err := os.ReadFile(filepath.Join(root, "fixture_manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := decodeStrict(manifestBody, &manifest); err != nil {
		t.Fatal(err)
	}
	payload := make(map[string]Payload)
	for _, file := range []struct {
		path string
		mode uint32
	}{{"bin/fixture", 0o700}, {"README.txt", 0o600}} {
		body, err := os.ReadFile(filepath.Join(root, "payload", filepath.FromSlash(file.path)))
		if err != nil {
			t.Fatal(err)
		}
		// These attributable fixtures are canonical LF text. Git may materialize
		// CRLF in a Windows checkout, which must not change the signed archive.
		body = bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
		payload[file.path] = Payload{Bytes: body, Mode: file.mode}
	}
	archive, err := Build(manifest, payload, fixtureKey())
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(archive, fixturePolicy(t, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		ArchiveSHA256  string `json:"archive_sha256"`
		ArchiveSize    int64  `json:"archive_size"`
		PublisherKeyID string `json:"publisher_key_id"`
	}
	expectedBody, err := os.ReadFile(filepath.Join(root, "expected.json"))
	if err != nil || json.Unmarshal(expectedBody, &expected) != nil {
		t.Fatalf("read expected fixture: %v", err)
	}
	if expected.ArchiveSHA256 != verified.ArchiveSHA256 || expected.ArchiveSize != verified.ArchiveSize || expected.PublisherKeyID != verified.Manifest.PublisherKeyID {
		t.Fatalf("update expected fixture: archive_sha256=%q archive_size=%d publisher_key_id=%q", verified.ArchiveSHA256, verified.ArchiveSize, verified.Manifest.PublisherKeyID)
	}
}

func TestVerifyTrustValidityAndVersionPolicy(t *testing.T) {
	archive := fixtureArchive(t, "1.2.3", "payload", PermissionNetwork)
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		policy VerifyPolicy
		want   error
	}{
		{name: "untrusted", policy: VerifyPolicy{Now: now, HostVersion: "1.4.0"}, want: ErrUntrustedPublisher},
		{name: "not yet valid", policy: fixturePolicy(t, time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)), want: ErrNotYetValid},
		{name: "expired", policy: fixturePolicy(t, time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)), want: ErrExpired},
		{name: "old host", policy: func() VerifyPolicy { value := fixturePolicy(t, now); value.HostVersion = "1.1.9"; return value }(), want: ErrIncompatibleHost},
		{name: "downgrade", policy: func() VerifyPolicy { value := fixturePolicy(t, now); value.CurrentVersion = "2.0.0"; return value }(), want: ErrDowngrade},
		{name: "missing time", policy: VerifyPolicy{}, want: ErrInvalidManifest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Verify(archive, test.policy); !errors.Is(err, test.want) {
				t.Fatalf("Verify() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestVerifyRevocations(t *testing.T) {
	archive := fixtureArchive(t, "1.2.3", "payload")
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	verified, err := Verify(archive, fixturePolicy(t, now))
	if err != nil {
		t.Fatal(err)
	}
	tests := []Revocations{
		{KeyIDs: []string{verified.Manifest.PublisherKeyID}},
		{ManifestSHA256: []string{verified.ManifestSHA256}},
		{Packages: []PackageRevocation{{Name: "fixture-pack", Version: "1.2.3"}}},
	}
	for _, revocations := range tests {
		policy := fixturePolicy(t, now)
		policy.Revocations = revocations
		if _, err := Verify(archive, policy); !errors.Is(err, ErrRevoked) {
			t.Fatalf("Verify() error = %v, want revoked", err)
		}
	}
}

func TestVerifyRejectsInvalidRevocationMetadata(t *testing.T) {
	archive := fixtureArchive(t, "1.2.3", "payload")
	policy := fixturePolicy(t, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	policy.Revocations.KeyIDs = []string{"not-a-key-id"}
	if _, err := Verify(archive, policy); !errors.Is(err, ErrInvalidRevocations) {
		t.Fatalf("Verify() error = %v, want invalid revocations", err)
	}
	policy = fixturePolicy(t, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	policy.Revocations.Packages = []PackageRevocation{{Name: "../unsafe", Version: "1.0.0"}}
	if _, err := Verify(archive, policy); !errors.Is(err, ErrInvalidRevocations) {
		t.Fatalf("Verify() error = %v, want invalid revocations", err)
	}
}

func TestVerifyRejectsSignedMetadataPayloadMismatch(t *testing.T) {
	archive := fixtureArchive(t, "1.0.0", "payload")
	policy := fixturePolicy(t, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	verified, err := Verify(archive, policy)
	if err != nil {
		t.Fatal(err)
	}
	signatureBytes, err := json.Marshal(verified.Signature)
	if err != nil {
		t.Fatal(err)
	}
	payload := make(map[string][]byte, len(verified.Payload))
	for path, body := range verified.Payload {
		payload[path] = append([]byte(nil), body...)
	}
	payload["bin/fixture"][0] ^= 0x20
	tampered, err := encodeArchive(verified.ManifestBytes, signatureBytes, verified.Manifest.Files, payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(tampered, policy); !errors.Is(err, ErrSignature) {
		t.Fatalf("Verify() error = %v, want signature category", err)
	}
}

func TestVerifyRejectsAmbiguousOrHostileArchives(t *testing.T) {
	archive := fixtureArchive(t, "1.0.0", "payload")
	policy := fixturePolicy(t, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	tests := []struct {
		name string
		body []byte
		want error
	}{
		{name: "prefix", body: append([]byte("prefix"), archive...), want: ErrInvalidArchive},
		{name: "suffix", body: append(append([]byte(nil), archive...), []byte("suffix")...), want: ErrInvalidArchive},
		{name: "truncated", body: archive[:len(archive)-12], want: ErrInvalidArchive},
		{name: "duplicate", body: hostileZIP(t, []hostileEntry{{"manifest.json", 0o600, []byte("{}")}, {"manifest.json", 0o600, []byte("{}")}, {"signature.json", 0o600, []byte("{}")}}), want: ErrInvalidArchive},
		{name: "symlink", body: hostileZIP(t, []hostileEntry{{"manifest.json", 0o600, []byte("{}")}, {"signature.json", 0o600, []byte("{}")}, {"payload/link", uint32(os.ModeSymlink | 0o777), []byte("target")}}), want: ErrInvalidArchive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Verify(test.body, policy); !errors.Is(err, test.want) {
				t.Fatalf("Verify() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestBuildRejectsUnsafeAndOversizedInput(t *testing.T) {
	tests := []string{"../entry", "/entry", `C:\\entry`, "a/../entry", ".ytdlp-pack.manifest.json", "a//entry", "entry\x00bad", "entry ", "entry.", "café", "CON", "aux.txt", "Lpt9.bin"}
	for _, filePath := range tests {
		manifest := fixtureManifest("1.0.0")
		manifest.Entrypoint = filePath
		if _, err := Build(manifest, map[string]Payload{filePath: {Bytes: []byte("x"), Mode: 0o700}}, fixtureKey()); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Build(%q) error = %v, want unsafe path", filePath, err)
		}
	}
	manifest := fixtureManifest("1.0.0")
	payload := make(map[string]Payload, maxFiles+1)
	for index := 0; index <= maxFiles; index++ {
		payload[fmt.Sprintf("entry-%03d", index)] = Payload{Bytes: []byte("x"), Mode: 0o600}
	}
	if _, err := Build(manifest, payload, fixtureKey()); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("oversized Build() error = %v", err)
	}
	manifest = fixtureManifest("1.0.0")
	manifest.Entrypoint = "Bin/Plugin"
	if _, err := Build(manifest, map[string]Payload{"Bin/Plugin": {Bytes: []byte("one"), Mode: 0o700}, "bin/plugin": {Bytes: []byte("two"), Mode: 0o700}}, fixtureKey()); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("case-fold collision error = %v", err)
	}
}

func TestReviewPermissions(t *testing.T) {
	review := ReviewPermissions([]Permission{PermissionNetwork, PermissionCookies}, []Permission{PermissionSecrets, PermissionNetwork})
	if !review.Increase() || fmt.Sprint(review.Added) != "[secrets]" || fmt.Sprint(review.Removed) != "[cookies]" {
		t.Fatalf("unexpected review: %#v", review)
	}
}

type hostileEntry struct {
	name string
	mode uint32
	body []byte
}

func hostileZIP(t *testing.T, entries []hostileEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, item := range entries {
		header := &zip.FileHeader{Name: item.name, Method: zip.Store, Modified: deterministicZipTime}
		header.SetMode(os.FileMode(item.mode))
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(item.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func FuzzVerify(fuzz *testing.F) {
	fuzz.Add([]byte("not a zip"))
	valid, err := Build(fixtureManifest("1.0.0"), fixturePayload("payload"), fixtureKey())
	if err != nil {
		fuzz.Fatal(err)
	}
	fuzz.Add(valid)
	policy := VerifyPolicy{Now: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), HostVersion: "1.4.0"}
	fuzz.Fuzz(func(t *testing.T, archive []byte) {
		if len(archive) > 1<<20 {
			t.Skip()
		}
		_, _ = Verify(archive, policy)
	})
}

func FuzzPayloadPath(fuzz *testing.F) {
	for _, seed := range []string{"bin/plugin", "../plugin", `/root`, `C:\\plugin`, "a//b"} {
		fuzz.Add(seed)
	}
	fuzz.Fuzz(func(t *testing.T, input string) {
		_ = validatePayloadPath(input)
	})
}
