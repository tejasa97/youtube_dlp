package ytdlp

import (
	"context"
	"crypto/ed25519"
	"time"

	packcatalog "github.com/ytdlp-go/ytdlp/internal/pack/catalog"
)

// PackCatalogTrust supplies explicit offline trust and freshness policy. It
// never discovers keys or consults a hidden clock.
type PackCatalogTrust struct {
	Keys        map[string]ed25519.PublicKey
	RevokedKeys map[string]bool
	Now         time.Time
}

type PackCatalogEntry struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	Artifact       string `json:"artifact"`
	ArchiveSHA256  string `json:"archive_sha256"`
	PublisherKeyID string `json:"publisher_key_id"`
	ArchiveSize    int64  `json:"archive_size"`
}

type PackCatalog struct {
	SchemaVersion int                `json:"schema_version"`
	GeneratedAt   string             `json:"generated_at"`
	ExpiresAt     string             `json:"expires_at"`
	Entries       []PackCatalogEntry `json:"entries"`
	verified      packcatalog.Catalog
}

func VerifyPackCatalog(ctx context.Context, encoded []byte, trust PackCatalogTrust) (PackCatalog, error) {
	verified, err := packcatalog.Verify(ctx, encoded, packcatalog.Policy{Trust: trust.Keys, RevokedKeys: trust.RevokedKeys, Now: trust.Now})
	if err != nil {
		return PackCatalog{}, categorized("verify pack catalog", err)
	}
	result := PackCatalog{SchemaVersion: verified.SchemaVersion, GeneratedAt: verified.GeneratedAt, ExpiresAt: verified.ExpiresAt, verified: verified}
	result.Entries = make([]PackCatalogEntry, 0, len(verified.Entries))
	for _, entry := range verified.Entries {
		result.Entries = append(result.Entries, publicPackCatalogEntry(entry))
	}
	return result, nil
}

// Resolve returns an exact authenticated catalog entry. Floating version
// selection is intentionally absent.
func (catalog PackCatalog) Resolve(ctx context.Context, name, version string) (PackCatalogEntry, error) {
	entry, err := packcatalog.Resolve(ctx, catalog.verified, name, version)
	if err != nil {
		return PackCatalogEntry{}, categorized("resolve pack catalog entry", err)
	}
	return publicPackCatalogEntry(entry), nil
}

func publicPackCatalogEntry(entry packcatalog.Entry) PackCatalogEntry {
	return PackCatalogEntry{
		Name: entry.Name, Version: entry.Version, Artifact: entry.Artifact,
		ArchiveSHA256: entry.ArchiveSHA256, ArchiveSize: entry.ArchiveSize, PublisherKeyID: entry.PublisherKeyID,
	}
}
