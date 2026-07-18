package catalog

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/pack"
)

func catalogKey() ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("ytdlp-go non-production pack catalog key"))
	return ed25519.NewKeyFromSeed(seed[:])
}

func catalogFixture() Catalog {
	digest := strings.Repeat("1", 64)
	keyID, _ := pack.KeyID(catalogKey().Public().(ed25519.PublicKey))
	return Catalog{
		SchemaVersion: SchemaV1,
		GeneratedAt:   "2026-07-19T00:00:00Z",
		ExpiresAt:     "2026-08-19T00:00:00Z",
		Entries: []Entry{
			{Name: "fixture-pack", Version: "1.1.0", Artifact: "packs/fixture-pack-1.1.0.ydp", ArchiveSHA256: digest, ArchiveSize: 4096, PublisherKeyID: keyID},
			{Name: "fixture-pack", Version: "1.0.0", Artifact: "packs/fixture-pack-1.0.0.ydp", ArchiveSHA256: strings.Repeat("2", 64), ArchiveSize: 2048, PublisherKeyID: keyID},
		},
	}
}

func catalogPolicy() Policy {
	publicKey := catalogKey().Public().(ed25519.PublicKey)
	keyID, _ := pack.KeyID(publicKey)
	return Policy{Trust: map[string]ed25519.PublicKey{keyID: publicKey}, Now: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)}
}

func TestBuildVerifyIsDeterministicAndResolveIsExact(t *testing.T) {
	first, err := Build(catalogFixture(), catalogKey())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(catalogFixture(), catalogKey())
	if err != nil || string(first) != string(second) {
		t.Fatalf("deterministic build mismatch: %v", err)
	}
	verified, err := Verify(context.Background(), first, catalogPolicy())
	if err != nil {
		t.Fatal(err)
	}
	entry, err := Resolve(context.Background(), verified, "fixture-pack", "1.1.0")
	if err != nil || entry.ArchiveSize != 4096 {
		t.Fatalf("Resolve() = %+v, %v", entry, err)
	}
	if _, err := Resolve(context.Background(), verified, "fixture-pack", "1.2.0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error = %v", err)
	}
}

func TestSignedRevocationFailsClosed(t *testing.T) {
	fixture := catalogFixture()
	fixture.Revocations = []Revocation{{Name: "fixture-pack", Version: "1.1.0", ArchiveSHA256: strings.Repeat("1", 64)}}
	encoded, err := Build(fixture, catalogKey())
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(context.Background(), encoded, catalogPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), verified, "fixture-pack", "1.1.0"); !errors.Is(err, ErrRevoked) {
		t.Fatalf("Resolve() error = %v", err)
	}
	if _, err := Resolve(context.Background(), verified, "fixture-pack", "1.0.0"); err != nil {
		t.Fatalf("unrevoked entry: %v", err)
	}
	fixture = catalogFixture()
	fixture.Revocations = []Revocation{{Name: "fixture-pack", Version: "1.1.0", ArchiveSHA256: strings.Repeat("9", 64)}}
	encoded, _ = Build(fixture, catalogKey())
	verified, _ = Verify(context.Background(), encoded, catalogPolicy())
	if _, err := Resolve(context.Background(), verified, "fixture-pack", "1.1.0"); err != nil {
		t.Fatalf("non-matching digest revocation blocked entry: %v", err)
	}
}

func TestVerifyRejectsTamperTrustRevocationExpiryAndCancellation(t *testing.T) {
	encoded, err := Build(catalogFixture(), catalogKey())
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	catalog := envelope["catalog"].(map[string]any)
	catalog["expires_at"] = "2027-08-19T00:00:00Z"
	tampered, _ := json.Marshal(envelope)
	if _, err := Verify(context.Background(), tampered, catalogPolicy()); !errors.Is(err, ErrSignature) {
		t.Fatalf("tamper error = %v", err)
	}

	untrusted := catalogPolicy()
	untrusted.Trust = nil
	if _, err := Verify(context.Background(), encoded, untrusted); !errors.Is(err, ErrUntrusted) {
		t.Fatalf("trust error = %v", err)
	}
	revoked := catalogPolicy()
	for keyID := range revoked.Trust {
		revoked.RevokedKeys = map[string]bool{keyID: true}
	}
	if _, err := Verify(context.Background(), encoded, revoked); !errors.Is(err, ErrRevoked) {
		t.Fatalf("key revocation error = %v", err)
	}
	expired := catalogPolicy()
	expired.Now = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if _, err := Verify(context.Background(), encoded, expired); !errors.Is(err, ErrExpired) {
		t.Fatalf("expiry error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Verify(ctx, encoded, catalogPolicy()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	if _, err := Verify(context.Background(), append(encoded, []byte(" trailing")...), catalogPolicy()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("trailing data error = %v", err)
	}
}

func TestCatalogRejectsHostilePathsDuplicatesAndBounds(t *testing.T) {
	tests := []func(*Catalog){
		func(c *Catalog) { c.Entries[0].Artifact = "../escape.ydp" },
		func(c *Catalog) { c.Entries[0].Artifact = "packs\\escape.ydp" },
		func(c *Catalog) { c.Entries = append(c.Entries, c.Entries[0]) },
		func(c *Catalog) { c.Entries[0].ArchiveSize = 73 << 20 },
	}
	for index, mutate := range tests {
		fixture := catalogFixture()
		mutate(&fixture)
		if _, err := Build(fixture, catalogKey()); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
}

func FuzzVerify(f *testing.F) {
	encoded, _ := Build(catalogFixture(), catalogKey())
	f.Add(encoded)
	f.Add([]byte(`{"catalog":{},"signature":{}}`))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > maximumBytes+1 {
			return
		}
		_, _ = Verify(context.Background(), input, catalogPolicy())
	})
}
