package upgrade

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/pack"
)

var fixtureSeed = [ed25519.SeedSize]byte{
	0x52, 0x48, 0x9f, 0x13, 0x67, 0x11, 0xa0, 0xbc,
	0xdd, 0x2b, 0x99, 0xc1, 0x5e, 0x32, 0x08, 0x4a,
	0x71, 0xfe, 0x63, 0x0d, 0x26, 0x9b, 0x45, 0xe2,
	0x8c, 0x06, 0x33, 0xf9, 0x18, 0xad, 0x7b, 0x40,
}

func TestCanonicalSignedContractFixtures(t *testing.T) {
	for _, test := range []struct {
		name     string
		manifest Manifest
	}{
		{"v1.0", manifestV10("1.0.0")},
		{"v1.1", manifestV11("1.1.0")},
	} {
		t.Run(test.name, func(t *testing.T) {
			first, err := Sign(context.Background(), test.manifest, fixtureKey())
			if err != nil {
				t.Fatal(err)
			}
			second, err := Sign(context.Background(), reverseManifest(test.manifest), fixtureKey())
			if err != nil || !bytes.Equal(first, second) {
				t.Fatalf("canonical signing differs: %v", err)
			}
			expected, err := os.ReadFile(fixturePath("signed-" + test.name + ".json"))
			if err != nil {
				t.Fatal(err)
			}
			expected = bytes.ReplaceAll(expected, []byte("\r\n"), []byte("\n"))
			if !bytes.Equal(first, expected) {
				t.Fatalf("replace signed fixture with:\n%s", first)
			}
			result, err := VerifyAndNegotiate(context.Background(), first, Policy{Host: hostV11(), TrustedKeys: fixtureTrust(), ApprovePermissionIncrease: true})
			if err != nil || result.Manifest.Version != test.manifest.Version || len(result.ManifestSHA256) != 64 {
				t.Fatalf("verification = %#v, %v", result, err)
			}
		})
	}
}

func TestCanonicalizationIsPermutationInvariant(t *testing.T) {
	manifest := manifestV11("1.1.0")
	manifest.Permissions = []pack.Permission{pack.PermissionNetwork, pack.PermissionCookies, pack.PermissionFilesystemRead}
	baseline := signFixture(t, manifest)
	for offset := 0; offset < 24; offset++ {
		candidate := manifest
		candidate.Permissions = rotate(manifest.Permissions, offset)
		candidate.Provides = rotate(manifest.Provides, offset)
		candidate.RequiredProvides = rotate(manifest.RequiredProvides, offset+1)
		candidate.RequiresHost = rotate(manifest.RequiresHost, offset+2)
		if encoded := signFixture(t, candidate); !bytes.Equal(encoded, baseline) {
			t.Fatalf("permutation %d changed canonical bytes", offset)
		}
	}
}

func TestCompatibilityMatrixOldHostNewPackAndNewHostOldPack(t *testing.T) {
	oldPack := signFixture(t, manifestV10("1.0.0"))
	newPack := signFixture(t, manifestV11("1.1.0"))
	tests := []struct {
		name string
		pack []byte
		host Host
	}{
		{"v1.0 host v1.0 pack", oldPack, hostV10()},
		{"v1.0 host v1.1 additive pack", newPack, hostV10()},
		{"v1.1 host v1.0 pack", oldPack, hostV11()},
		{"v1.1 host v1.1 pack", newPack, hostV11()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := VerifyAndNegotiate(context.Background(), test.pack, Policy{Host: test.host, TrustedKeys: fixtureTrust(), ApprovePermissionIncrease: true})
			if err != nil || result.Manifest.Name != "fixture-extractors" {
				t.Fatalf("matrix result = %#v, %v", result, err)
			}
		})
	}
	requiresNewHost := manifestV11("1.1.0")
	requiresNewHost.RequiresHost = []string{"host.shadow-v1"}
	if _, err := VerifyAndNegotiate(context.Background(), signFixture(t, requiresNewHost), Policy{Host: hostV10(), TrustedKeys: fixtureTrust(), ApprovePermissionIncrease: true}); !errors.Is(err, ErrIncompatibleHost) {
		t.Fatalf("old host/new required capability error = %v", err)
	}
}

func TestUpgradeRejectsDowngradeRemovedCapabilityAndReviewsPermissions(t *testing.T) {
	current := manifestV11("2.0.0")
	candidate := manifestV11("1.9.0")
	if _, err := verifyUpgrade(t, candidate, current, true); !errors.Is(err, ErrDowngrade) {
		t.Fatalf("downgrade error = %v", err)
	}
	candidate.Version = "2.1.0"
	candidate.Provides = []string{"extractor.basic"}
	candidate.RequiredProvides = []string{"extractor.basic"}
	if _, err := verifyUpgrade(t, candidate, current, true); !errors.Is(err, ErrMissingCapability) {
		t.Fatalf("removed capability error = %v", err)
	}
	candidate = manifestV11("2.1.0")
	candidate.Permissions = []pack.Permission{pack.PermissionNetwork, pack.PermissionCookies}
	result, err := verifyUpgrade(t, candidate, current, false)
	if !errors.Is(err, ErrPermissionReview) || !result.Review.Increase() || !reflect.DeepEqual(result.Review.Added, []pack.Permission{pack.PermissionCookies}) {
		t.Fatalf("permission review = %#v, %v", result.Review, err)
	}
	result, err = verifyUpgrade(t, candidate, current, true)
	if err != nil || !result.Review.Increase() {
		t.Fatalf("approved permission review = %#v, %v", result.Review, err)
	}
}

func TestRejectsUnknownMajorMalformedPythonAndUntrustedRecords(t *testing.T) {
	unknownMajor := manifestV11("1.1.0")
	unknownMajor.ContractVersion.Major = 2
	if _, err := Sign(context.Background(), unknownMajor, fixtureKey()); !errors.Is(err, ErrUnsupportedMajor) {
		t.Fatalf("unknown major sign error = %v", err)
	}
	unknownSigned := signUnchecked(t, unknownMajor)
	if _, err := VerifyAndNegotiate(context.Background(), unknownSigned, Policy{Host: hostV11(), TrustedKeys: fixtureTrust(), ApprovePermissionIncrease: true}); !errors.Is(err, ErrUnsupportedMajor) {
		t.Fatalf("unknown major verify error = %v", err)
	}
	python := manifestV11("1.1.0")
	python.Runtime, python.Entrypoint = "python", "extractors.py"
	if _, err := Sign(context.Background(), python, fixtureKey()); !errors.Is(err, ErrPythonRuntime) {
		t.Fatalf("Python runtime error = %v", err)
	}
	valid := signFixture(t, manifestV11("1.1.0"))
	malformed := bytes.Replace(valid, []byte(`"name":"fixture-extractors"`), []byte(`"unknown":true,"name":"fixture-extractors"`), 1)
	if _, err := VerifyAndNegotiate(context.Background(), malformed, Policy{Host: hostV11(), TrustedKeys: fixtureTrust(), ApprovePermissionIncrease: true}); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, err := VerifyAndNegotiate(context.Background(), valid, Policy{Host: hostV11(), TrustedKeys: map[string]ed25519.PublicKey{}, ApprovePermissionIncrease: true}); !errors.Is(err, ErrUntrustedKey) {
		t.Fatalf("untrusted key error = %v", err)
	}
	malformedKey := map[string]ed25519.PublicKey{fixtureKeyID(): {1, 2, 3}}
	if _, err := VerifyAndNegotiate(context.Background(), valid, Policy{Host: hostV11(), TrustedKeys: malformedKey, ApprovePermissionIncrease: true}); !errors.Is(err, ErrUntrustedKey) {
		t.Fatalf("malformed trusted key error = %v", err)
	}
}

func TestCancellationAndResourceBounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Sign(ctx, manifestV11("1.1.0"), fixtureKey()); !errors.Is(err, context.Canceled) {
		t.Fatalf("sign cancellation = %v", err)
	}
	if _, err := VerifyAndNegotiate(ctx, []byte("{}"), Policy{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("verify cancellation = %v", err)
	}
	if _, err := VerifyAndNegotiate(context.Background(), bytes.Repeat([]byte("x"), MaxContractSize+1), Policy{}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("record bound = %v", err)
	}
	overCapability := manifestV11("1.1.0")
	overCapability.Provides = make([]string, maxCapabilities+1)
	for index := range overCapability.Provides {
		overCapability.Provides[index] = "extractor.cap" + strings.Repeat("x", index+1)
	}
	if _, err := Sign(context.Background(), overCapability, fixtureKey()); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("capability bound = %v", err)
	}
	overHost := hostV11()
	overHost.Capabilities = make([]string, maxCapabilities+1)
	for index := range overHost.Capabilities {
		overHost.Capabilities[index] = "host.c" + strings.Repeat("x", index%32+1)
	}
	if _, err := VerifyAndNegotiate(context.Background(), signFixture(t, manifestV11("1.1.0")), Policy{Host: overHost, TrustedKeys: fixtureTrust(), ApprovePermissionIncrease: true}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("host capability bound = %v", err)
	}
}

func FuzzVerifyAndNegotiate(f *testing.F) {
	valid, err := Sign(context.Background(), manifestV11("1.1.0"), fixtureKey())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(`{"manifest":null}`))
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = VerifyAndNegotiate(context.Background(), input, Policy{Host: hostV11(), TrustedKeys: fixtureTrust(), ApprovePermissionIncrease: true})
	})
}

func fixtureKey() ed25519.PrivateKey { return ed25519.NewKeyFromSeed(fixtureSeed[:]) }

func fixtureTrust() map[string]ed25519.PublicKey {
	public := fixtureKey().Public().(ed25519.PublicKey)
	return map[string]ed25519.PublicKey{keyID(public): public}
}

func fixtureKeyID() string { return keyID(fixtureKey().Public().(ed25519.PublicKey)) }

func manifestV10(version string) Manifest {
	return Manifest{
		ContractVersion: ContractVersion{Major: 1, Minor: 0}, Name: "fixture-extractors", Version: version,
		Runtime: RuntimeWASM, Entrypoint: "extractors.wasm", Permissions: []pack.Permission{pack.PermissionNetwork},
		Provides: []string{"extractor.basic"},
	}
}

func manifestV11(version string) Manifest {
	return Manifest{
		ContractVersion: ContractVersion{Major: 1, Minor: 1}, Name: "fixture-extractors", Version: version,
		Runtime: RuntimeWASM, Entrypoint: "extractors.wasm", Permissions: []pack.Permission{pack.PermissionNetwork},
		Provides: []string{"playlist.entries", "extractor.basic"}, RequiredProvides: []string{"extractor.basic", "playlist.entries"},
		RequiresHost: []string{"host.extract-v1"}, Annotations: map[string]string{"channel": "stable"},
	}
}

func hostV10() Host {
	return Host{ContractVersion: ContractVersion{Major: 1, Minor: 0}, Capabilities: []string{"host.extract-v1"}, RequiredPackCapabilities: []string{"extractor.basic"}}
}

func hostV11() Host {
	return Host{ContractVersion: ContractVersion{Major: 1, Minor: 1}, Capabilities: []string{"host.extract-v1", "host.shadow-v1"}, RequiredPackCapabilities: []string{"extractor.basic"}}
}

func signFixture(t *testing.T, manifest Manifest) []byte {
	t.Helper()
	encoded, err := Sign(context.Background(), manifest, fixtureKey())
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func verifyUpgrade(t *testing.T, candidate, current Manifest, approve bool) (Result, error) {
	t.Helper()
	return VerifyAndNegotiate(context.Background(), signFixture(t, candidate), Policy{Host: hostV11(), TrustedKeys: fixtureTrust(), Current: &current, ApprovePermissionIncrease: approve})
}

func reverseManifest(input Manifest) Manifest {
	result := input
	result.Permissions = reverse(result.Permissions)
	result.Provides = reverse(result.Provides)
	result.RequiresHost = reverse(result.RequiresHost)
	result.RequiredProvides = reverse(result.RequiredProvides)
	return result
}

func reverse[T any](input []T) []T {
	result := append([]T(nil), input...)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func rotate[T any](input []T, offset int) []T {
	result := slices.Clone(input)
	if len(result) == 0 {
		return result
	}
	offset %= len(result)
	return append(result[offset:], result[:offset]...)
}

func signUnchecked(t *testing.T, manifest Manifest) []byte {
	t.Helper()
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	key := fixtureKey()
	public := key.Public().(ed25519.PublicKey)
	signature := ed25519.Sign(key, append([]byte(signatureDomain), manifestBytes...))
	record := signedRecord{Manifest: manifestBytes, Signature: Signature{Algorithm: "Ed25519", KeyID: keyID(public), Value: base64.StdEncoding.EncodeToString(signature)}}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func fixturePath(name string) string {
	return filepath.Join("..", "..", "..", "conformance", "pack", "upgrade-v1.1", name)
}
