package ytdlp

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/pack"
	packcatalog "github.com/ytdlp-go/ytdlp/internal/pack/catalog"
)

func TestPublicPackCatalogVerificationAndExactResolution(t *testing.T) {
	seed := sha256.Sum256([]byte("public pack catalog deterministic test key"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID, _ := pack.KeyID(publicKey)
	catalog := packcatalog.Catalog{
		SchemaVersion: packcatalog.SchemaV1, GeneratedAt: "2026-07-19T00:00:00Z", ExpiresAt: "2026-08-19T00:00:00Z",
		Entries: []packcatalog.Entry{{
			Name: "public-fixture", Version: "1.1.0", Artifact: "packs/public-fixture.ydp",
			ArchiveSHA256: strings.Repeat("a", 64), ArchiveSize: 1024, PublisherKeyID: keyID,
		}},
	}
	encoded, err := packcatalog.Build(catalog, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyPackCatalog(context.Background(), encoded, PackCatalogTrust{
		Keys: map[string]ed25519.PublicKey{keyID: publicKey}, Now: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
	})
	if err != nil || len(verified.Entries) != 1 || verified.Entries[0].Name != "public-fixture" {
		t.Fatalf("VerifyPackCatalog() = %+v, %v", verified, err)
	}
	entry, err := verified.Resolve(context.Background(), "public-fixture", "1.1.0")
	if err != nil || entry.ArchiveSize != 1024 {
		t.Fatalf("Resolve() = %+v, %v", entry, err)
	}
	if _, err := verified.Resolve(context.Background(), "public-fixture", "1.2.0"); !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("missing category = %v", err)
	}
	encoded[len(encoded)/2] ^= 1
	if _, err := VerifyPackCatalog(context.Background(), encoded, PackCatalogTrust{Keys: map[string]ed25519.PublicKey{keyID: publicKey}, Now: time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)}); err == nil {
		t.Fatal("tampered catalog succeeded")
	}
}
