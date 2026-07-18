package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

func testKey(label string) (ed25519.PublicKey, ed25519.PrivateKey) {
	seed := sha256.Sum256([]byte("ytdlp-go deterministic NON-PRODUCTION test key: " + label))
	private := ed25519.NewKeyFromSeed(seed[:])
	return private.Public().(ed25519.PublicKey), private
}

func testRoot(public ed25519.PublicKey) Root {
	return Root{
		Keys:      map[string]ed25519.PublicKey{KeyID(public): public},
		Threshold: 1,
		Role:      ReleaseRole,
		Product:   "ytdlp-go",
		Channels:  []Channel{ChannelStable, ChannelBeta},
		Platforms: []Platform{{GOOS: "linux", GOARCH: "amd64"}, {GOOS: "darwin", GOARCH: "arm64"}, {GOOS: "windows", GOARCH: "amd64"}},
	}
}

func testMetadata(content []byte, version string, generation uint64) Metadata {
	return Metadata{
		Spec:       MetadataSpec,
		Role:       ReleaseRole,
		Product:    "ytdlp-go",
		Generation: generation,
		Expires:    testNow.Add(24 * time.Hour).Format(time.RFC3339),
		Targets: []Target{
			{Version: version, Channel: ChannelStable, GOOS: "linux", GOARCH: "amd64", Artifact: "ytdlp-go", Size: int64(len(content)), SHA256: digestString(content)},
			{Version: version, Channel: ChannelStable, GOOS: "darwin", GOARCH: "arm64", Artifact: "ytdlp-go", Size: int64(len(content)), SHA256: digestString(content)},
			{Version: version, Channel: ChannelStable, GOOS: "windows", GOARCH: "amd64", Artifact: "ytdlp-go.exe", Size: int64(len(content)), SHA256: digestString(content)},
		},
	}
}

func digestString(content []byte) string {
	digest := sha256.Sum256(content)
	return strings.ToLower(fmtHex(digest[:]))
}

func fmtHex(value []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, item := range value {
		result[index*2] = digits[item>>4]
		result[index*2+1] = digits[item&15]
	}
	return string(result)
}

func TestSignVerifySelectDeterministic(t *testing.T) {
	public, private := testKey("release-1")
	metadata := testMetadata([]byte("release one"), "1.2.3", 7)
	first, err := Sign(metadata, map[string]ed25519.PrivateKey{KeyID(public): private})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Sign(metadata, map[string]ed25519.PrivateKey{KeyID(public): private})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("signed update envelope is not deterministic")
	}
	verified, err := Verify(first, testRoot(public))
	if err != nil {
		t.Fatal(err)
	}
	target, err := Select(verified, Selection{Product: "ytdlp-go", Channel: ChannelStable, GOOS: "linux", GOARCH: "amd64", Installed: "1.2.2", HighestGeneration: 6, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	if target.Version != "1.2.3" || target.Artifact != "ytdlp-go" {
		t.Fatalf("target = %#v", target)
	}
}

func TestUpdateConformanceFixtures(t *testing.T) {
	public, private := testKey("release-1")
	generated, err := Sign(testMetadata([]byte("portable-artifact"), "1.0.0", 1), map[string]ed25519.PrivateKey{KeyID(public): private})
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join("..", "..", "conformance", "update", "valid-envelope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, fixture) {
		t.Fatal("signed update fixture drift")
	}
	if _, err := Verify(fixture, testRoot(public)); err != nil {
		t.Fatal(err)
	}
	hostile, err := os.ReadFile(filepath.Join("..", "..", "conformance", "update", "hostile-duplicate-envelope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(hostile, testRoot(public)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("hostile fixture error = %v", err)
	}
}

func TestThresholdAndScope(t *testing.T) {
	public1, private1 := testKey("release-1")
	public2, private2 := testKey("release-2")
	metadata := testMetadata([]byte("release"), "2.0.0", 2)
	envelope, err := Sign(metadata, map[string]ed25519.PrivateKey{KeyID(public1): private1, KeyID(public2): private2})
	if err != nil {
		t.Fatal(err)
	}
	root := testRoot(public1)
	root.Keys[KeyID(public2)] = public2
	root.Threshold = 2
	if _, err := Verify(envelope, root); err != nil {
		t.Fatal(err)
	}
	root.Threshold = 3
	if _, err := Verify(envelope, root); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("invalid threshold error = %v", err)
	}
	root = testRoot(public1)
	root.Product = "another-product"
	if _, err := Verify(envelope, root); !errors.Is(err, ErrSignature) {
		t.Fatalf("product scope error = %v", err)
	}
	root = testRoot(public1)
	root.Platforms = []Platform{{GOOS: "linux", GOARCH: "amd64"}}
	if _, err := Verify(envelope, root); !errors.Is(err, ErrSignature) {
		t.Fatalf("platform scope error = %v", err)
	}
	root = testRoot(public1)
	root.Channels = append(root.Channels, root.Channels[0])
	if _, err := Verify(envelope, root); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("duplicate channel error = %v", err)
	}
	root = testRoot(public1)
	root.Platforms = append(root.Platforms, root.Platforms[0])
	if _, err := Verify(envelope, root); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("duplicate platform error = %v", err)
	}
}

func TestVerifyRejectsTamperDuplicateAndNonCanonical(t *testing.T) {
	public, private := testKey("release-1")
	envelope, err := Sign(testMetadata([]byte("release"), "1.0.0", 1), map[string]ed25519.PrivateKey{KeyID(public): private})
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(envelope, []byte(`"generation":1`), []byte(`"generation":2`), 1)
	if _, err := Verify(tampered, testRoot(public)); !errors.Is(err, ErrSignature) {
		t.Fatalf("tamper error = %v", err)
	}
	duplicate := bytes.Replace(envelope, []byte(`{"signed":`), []byte(`{"signed":null,"signed":`), 1)
	if _, err := Verify(duplicate, testRoot(public)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("duplicate error = %v", err)
	}
	var decoded Envelope
	if err := json.Unmarshal(envelope, &decoded); err != nil {
		t.Fatal(err)
	}
	raw := bytes.Replace(decoded.Signed, []byte(`"spec":"`), []byte(`"spec": "`), 1)
	signature := base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, raw))
	noncanonical := []byte(`{"signed":` + string(raw) + `,"signatures":[{"keyid":"` + KeyID(public) + `","sig":"` + signature + `"}]}`)
	if _, err := Verify(noncanonical, testRoot(public)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("noncanonical error = %v", err)
	}
}

func TestPortableArtifactNames(t *testing.T) {
	for _, value := range []string{"CON", "con.exe", "NUL.txt", "COM1", "lpt9.bin", "name.", "name ", `..\\escape`} {
		if safeArtifact(value, "windows") {
			t.Errorf("safeArtifact(%q, windows) = true", value)
		}
	}
	for _, value := range []string{"ytdlp-go.exe", "release_1-alpha.bin"} {
		if !safeArtifact(value, "windows") {
			t.Errorf("safeArtifact(%q, windows) = false", value)
		}
	}
}

func TestSelectProtections(t *testing.T) {
	metadata := testMetadata([]byte("release"), "1.2.3", 10)
	base := Selection{Product: "ytdlp-go", Channel: ChannelStable, GOOS: "linux", GOARCH: "amd64", Installed: "1.2.2", HighestGeneration: 9, Now: testNow}
	tests := []struct {
		name     string
		mutate   func(*Metadata, *Selection)
		expected error
	}{
		{name: "expired", mutate: func(metadata *Metadata, selection *Selection) { selection.Now = testNow.Add(48 * time.Hour) }, expected: ErrExpired},
		{name: "freeze", mutate: func(metadata *Metadata, selection *Selection) { selection.HighestGeneration = 10 }, expected: ErrFreeze},
		{name: "downgrade", mutate: func(metadata *Metadata, selection *Selection) { selection.Installed = "2.0.0" }, expected: ErrDowngrade},
		{name: "channel", mutate: func(metadata *Metadata, selection *Selection) { selection.Channel = ChannelBeta }, expected: ErrWrongChannel},
		{name: "platform", mutate: func(metadata *Metadata, selection *Selection) { selection.GOARCH = "arm64" }, expected: ErrWrongPlatform},
		{name: "product", mutate: func(metadata *Metadata, selection *Selection) { selection.Product = "other" }, expected: ErrInvalidMetadata},
		{name: "future clock", mutate: func(metadata *Metadata, selection *Selection) {
			selection.Now = time.Date(3100, 1, 1, 0, 0, 0, 0, time.UTC)
		}, expected: ErrInvalidMetadata},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := metadata
			candidate.Targets = append([]Target(nil), metadata.Targets...)
			selection := base
			test.mutate(&candidate, &selection)
			if _, err := Select(candidate, selection); !errors.Is(err, test.expected) {
				t.Fatalf("error = %v, want %v", err, test.expected)
			}
		})
	}
}

func TestVersionOrdering(t *testing.T) {
	tests := []struct {
		left, right string
		sign        int
	}{
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.2", "1.0.0-rc.10", -1},
		{"2.0.0", "10.0.0", -1},
		{"1.1.0", "1.0.9", 1},
	}
	for _, test := range tests {
		value := compareVersion(test.left, test.right)
		if value < 0 && test.sign >= 0 || value == 0 && test.sign != 0 || value > 0 && test.sign <= 0 {
			t.Errorf("compareVersion(%q, %q) = %d", test.left, test.right, value)
		}
	}
}

func FuzzVerifyEnvelope(f *testing.F) {
	public, private := testKey("release-1")
	valid, _ := Sign(testMetadata([]byte("release"), "1.0.0", 1), map[string]ed25519.PrivateKey{KeyID(public): private})
	f.Add(valid)
	f.Add([]byte(`{"signed":{},"signatures":[]}`))
	f.Fuzz(func(t *testing.T, encoded []byte) {
		_, _ = Verify(encoded, testRoot(public))
	})
}

func FuzzSelectInstalledVersion(f *testing.F) {
	metadata := testMetadata([]byte("release"), "1.0.0", 2)
	f.Add("1.0.0")
	f.Add("x")
	f.Add("")
	f.Fuzz(func(t *testing.T, installed string) {
		_, _ = Select(metadata, Selection{Product: "ytdlp-go", Channel: ChannelStable, GOOS: "linux", GOARCH: "amd64", Installed: installed, HighestGeneration: 1, Now: testNow})
	})
}
