// Package transaction binds signed catalog resolution, artifact verification,
// the v1.x runtime contract, and atomic pack activation into one fail-closed
// product operation.
package transaction

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/ytdlp-go/ytdlp/internal/pack"
	"github.com/ytdlp-go/ytdlp/internal/pack/catalog"
	"github.com/ytdlp-go/ytdlp/internal/pack/upgrade"
)

var (
	ErrInvalidRequest = errors.New("invalid pack transaction request")
	ErrCatalog        = errors.New("pack transaction catalog failure")
	ErrArtifact       = errors.New("pack transaction artifact failure")
	ErrBinding        = errors.New("pack transaction identity binding failure")
	ErrContract       = errors.New("pack transaction contract failure")
	ErrActivation     = errors.New("pack transaction activation failure")
)

// ArtifactLoader opens the exact relative artifact path authenticated by the
// catalog. The implementation may read an offline directory or fetch from a
// transport, but must honor ctx. The transaction enforces the catalog size
// after the loader returns and before parsing the archive.
type ArtifactLoader func(ctx context.Context, artifact string, maximumSize int64) ([]byte, error)

type InstallRequest struct {
	Catalog                   []byte
	CatalogPolicy             catalog.Policy
	Name                      string
	Version                   string
	LoadArtifact              ArtifactLoader
	Contract                  []byte
	ContractPolicy            upgrade.Policy
	PackPolicy                pack.VerifyPolicy
	Root                      string
	ApprovePermissionIncrease bool
}

// Prepared is immutable transaction evidence produced before any installer
// filesystem mutation.
type Prepared struct {
	Entry    catalog.Entry
	Artifact []byte
	Pack     pack.Verified
	Contract upgrade.Result
}

type InstallReceipt struct {
	Prepared Prepared
	Install  pack.Receipt
}

// Prepare authenticates and binds every distribution layer without mutating
// the installation root. It is separated for deterministic conformance and
// fuzz testing; callers should normally use Install.
func Prepare(ctx context.Context, request InstallRequest) (Prepared, error) {
	var prepared Prepared
	if ctx == nil || request.LoadArtifact == nil || request.Root == "" {
		return prepared, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return prepared, err
	}
	verifiedCatalog, err := catalog.Verify(ctx, request.Catalog, request.CatalogPolicy)
	if err != nil {
		return prepared, errors.Join(ErrCatalog, err)
	}
	entry, err := catalog.Resolve(ctx, verifiedCatalog, request.Name, request.Version)
	if err != nil {
		return prepared, errors.Join(ErrCatalog, err)
	}
	artifact, err := request.LoadArtifact(ctx, entry.Artifact, entry.ArchiveSize)
	if err != nil {
		return prepared, errors.Join(ErrArtifact, err)
	}
	if err := ctx.Err(); err != nil {
		return prepared, err
	}
	if int64(len(artifact)) != entry.ArchiveSize {
		return prepared, errors.Join(ErrArtifact, errors.New("catalog size mismatch"))
	}
	digest := sha256.Sum256(artifact)
	if fmt.Sprintf("%x", digest) != entry.ArchiveSHA256 {
		return prepared, errors.Join(ErrArtifact, errors.New("catalog digest mismatch"))
	}
	verifiedPack, err := pack.Verify(artifact, request.PackPolicy)
	if err != nil {
		return prepared, errors.Join(ErrArtifact, err)
	}
	if verifiedPack.Manifest.Name != entry.Name || verifiedPack.Manifest.Version != entry.Version ||
		verifiedPack.Manifest.PublisherKeyID != entry.PublisherKeyID ||
		verifiedPack.ArchiveSHA256 != entry.ArchiveSHA256 || verifiedPack.ArchiveSize != entry.ArchiveSize {
		return prepared, ErrBinding
	}
	contractPolicy := request.ContractPolicy
	contractPolicy.ApprovePermissionIncrease = request.ApprovePermissionIncrease
	contractResult, err := upgrade.VerifyAndNegotiate(ctx, request.Contract, contractPolicy)
	if err != nil {
		return prepared, errors.Join(ErrContract, err)
	}
	if err := bindContract(entry, verifiedPack, contractResult); err != nil {
		return prepared, err
	}
	if err := bindCurrentPolicies(request.PackPolicy, contractPolicy); err != nil {
		return prepared, err
	}
	prepared = Prepared{
		Entry: entry, Artifact: append([]byte(nil), artifact...), Pack: verifiedPack, Contract: contractResult,
	}
	return prepared, nil
}

// Install completes a fully preflighted transaction using the existing
// staging, re-verification, durable state, and atomic activation machinery.
func Install(ctx context.Context, request InstallRequest) (InstallReceipt, error) {
	var receipt InstallReceipt
	prepared, err := Prepare(ctx, request)
	if err != nil {
		return receipt, err
	}
	installed, err := pack.Install(ctx, prepared.Artifact, request.Root, request.PackPolicy, pack.InstallOptions{
		ApprovePermissionIncrease: request.ApprovePermissionIncrease,
	})
	if err != nil {
		return InstallReceipt{Prepared: prepared, Install: installed}, errors.Join(ErrActivation, err)
	}
	return InstallReceipt{Prepared: prepared, Install: installed}, nil
}

type RollbackRequest struct {
	Catalog                   []byte
	CatalogPolicy             catalog.Policy
	Name                      string
	TargetVersion             string
	Contract                  []byte
	ContractPolicy            upgrade.Policy
	PackPolicy                pack.VerifyPolicy
	Root                      string
	ApprovePermissionIncrease bool
}

type RollbackReceipt struct {
	Entry    catalog.Entry
	Contract upgrade.Result
	Rollback pack.Receipt
}

// Rollback authenticates the target's current catalog and contract records,
// then asks the installer to reconstruct and validate the installed archive.
// Catalog, publisher, digest, size, contract, and revocations all fail before
// the active-version state is mutated.
func Rollback(ctx context.Context, request RollbackRequest) (RollbackReceipt, error) {
	var receipt RollbackReceipt
	if ctx == nil || request.Root == "" {
		return receipt, ErrInvalidRequest
	}
	verifiedCatalog, err := catalog.Verify(ctx, request.Catalog, request.CatalogPolicy)
	if err != nil {
		return receipt, errors.Join(ErrCatalog, err)
	}
	entry, err := catalog.Resolve(ctx, verifiedCatalog, request.Name, request.TargetVersion)
	if err != nil {
		return receipt, errors.Join(ErrCatalog, err)
	}
	contractPolicy := request.ContractPolicy
	// Compatibility is checked here; the installer's state-derived permission
	// delta remains authoritative for rollback approval.
	contractPolicy.Current = nil
	contractPolicy.ApprovePermissionIncrease = true
	contractResult, err := upgrade.VerifyAndNegotiate(ctx, request.Contract, contractPolicy)
	if err != nil {
		return receipt, errors.Join(ErrContract, err)
	}
	if contractResult.Manifest.Name != entry.Name || contractResult.Manifest.Version != entry.Version || contractResult.KeyID != entry.PublisherKeyID {
		return receipt, ErrBinding
	}
	rolledBack, err := pack.RollbackWithOptions(ctx, request.Root, request.Name, request.PackPolicy, pack.RollbackOptions{
		ApprovePermissionIncrease: request.ApprovePermissionIncrease,
		Validate: func(verified pack.Verified) error {
			if verified.ArchiveSHA256 != entry.ArchiveSHA256 || verified.ArchiveSize != entry.ArchiveSize {
				return ErrBinding
			}
			return bindContract(entry, verified, contractResult)
		},
	})
	if err != nil {
		return RollbackReceipt{Entry: entry, Contract: contractResult, Rollback: rolledBack}, errors.Join(ErrActivation, err)
	}
	return RollbackReceipt{Entry: entry, Contract: contractResult, Rollback: rolledBack}, nil
}

func bindContract(entry catalog.Entry, verified pack.Verified, contract upgrade.Result) error {
	manifest := contract.Manifest
	if manifest.Name != entry.Name || manifest.Version != entry.Version || contract.KeyID != entry.PublisherKeyID ||
		string(manifest.Runtime) != string(verified.Manifest.Runtime) || manifest.Entrypoint != verified.Manifest.Entrypoint ||
		!equalPermissions(manifest.Permissions, verified.Manifest.Permissions) {
		return ErrBinding
	}
	return nil
}

func bindCurrentPolicies(packPolicy pack.VerifyPolicy, contractPolicy upgrade.Policy) error {
	if contractPolicy.Current == nil {
		if packPolicy.CurrentVersion != "" {
			return ErrBinding
		}
		return nil
	}
	if packPolicy.CurrentVersion != contractPolicy.Current.Version {
		return ErrBinding
	}
	return nil
}

func equalPermissions(left, right []pack.Permission) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[pack.Permission]int, len(left))
	for _, permission := range left {
		counts[permission]++
	}
	for _, permission := range right {
		counts[permission]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}
