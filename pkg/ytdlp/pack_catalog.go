package ytdlp

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/pack"
	packcatalog "github.com/ytdlp-go/ytdlp/internal/pack/catalog"
	packtransaction "github.com/ytdlp-go/ytdlp/internal/pack/transaction"
	packupgrade "github.com/ytdlp-go/ytdlp/internal/pack/upgrade"
)

type PackContractVersion = packupgrade.ContractVersion
type PackContractManifest = packupgrade.Manifest
type PackContractHost = packupgrade.Host

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

// PackCatalogInstallRequest binds an authenticated catalog entry to the exact
// artifact, signed v1.x compatibility contract, and atomic installer. No
// filesystem mutation occurs until every identity and policy check passes.
type PackCatalogInstallRequest struct {
	Catalog                   []byte
	CatalogTrust              PackCatalogTrust
	Name                      string
	Version                   string
	LoadArtifact              func(context.Context, string, int64) ([]byte, error)
	Contract                  []byte
	ContractHost              PackContractHost
	ContractKeys              map[string]ed25519.PublicKey
	CurrentContract           *PackContractManifest
	PackTrust                 PluginPackTrust
	Root                      string
	ApprovePermissionIncrease bool
}

type PackCatalogInstallReceipt struct {
	Entry  PackCatalogEntry     `json:"entry"`
	Review PackPermissionReview `json:"review"`
	State  PackState            `json:"state"`
	Path   string               `json:"path"`
}

func InstallPackCatalogTransaction(ctx context.Context, request PackCatalogInstallRequest) (PackCatalogInstallReceipt, error) {
	packPolicy := packPolicy(request.PackTrust)
	receipt, err := packtransaction.Install(ctx, packtransaction.InstallRequest{
		Catalog:       request.Catalog,
		CatalogPolicy: packcatalog.Policy{Trust: clonePublicKeys(request.CatalogTrust.Keys), RevokedKeys: cloneRevokedKeys(request.CatalogTrust.RevokedKeys), Now: request.CatalogTrust.Now},
		Name:          request.Name, Version: request.Version, LoadArtifact: request.LoadArtifact,
		Contract: request.Contract,
		ContractPolicy: packupgrade.Policy{
			Host: request.ContractHost, TrustedKeys: clonePublicKeys(request.ContractKeys),
			Current: request.CurrentContract,
		},
		PackPolicy: packPolicy, Root: request.Root,
		ApprovePermissionIncrease: request.ApprovePermissionIncrease,
	})
	if err != nil {
		return PackCatalogInstallReceipt{}, categorizePackTransaction("install catalog pack transaction", err)
	}
	return PackCatalogInstallReceipt{
		Entry: publicPackCatalogEntry(receipt.Prepared.Entry), Review: receipt.Install.Review,
		State: receipt.Install.State, Path: receipt.Install.Path,
	}, nil
}

func clonePublicKeys(input map[string]ed25519.PublicKey) map[string]ed25519.PublicKey {
	output := make(map[string]ed25519.PublicKey, len(input))
	for id, key := range input {
		output[id] = append(ed25519.PublicKey(nil), key...)
	}
	return output
}

func cloneRevokedKeys(input map[string]bool) map[string]bool {
	output := make(map[string]bool, len(input))
	for id, revoked := range input {
		output[id] = revoked
	}
	return output
}

func categorizePackTransaction(op string, err error) error {
	if err == nil {
		return nil
	}
	category := ErrorInvalidInput
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		category = ErrorCancelled
	case errors.Is(err, pack.ErrPlatformSecurity), errors.Is(err, pack.ErrIncompatibleHost), errors.Is(err, packupgrade.ErrIncompatibleHost), errors.Is(err, packupgrade.ErrUnsupportedMajor):
		category = ErrorUnsupported
	case errors.Is(err, packtransaction.ErrBinding), errors.Is(err, pack.ErrUntrustedPublisher), errors.Is(err, pack.ErrSignature),
		errors.Is(err, pack.ErrRevoked), errors.Is(err, pack.ErrExpired), errors.Is(err, pack.ErrDowngrade),
		errors.Is(err, packupgrade.ErrSignature), errors.Is(err, packupgrade.ErrUntrustedKey), errors.Is(err, packupgrade.ErrDowngrade), errors.Is(err, packupgrade.ErrPermissionReview):
		category = ErrorSecurity
	}
	return &Error{Category: category, Op: op, Err: err}
}
