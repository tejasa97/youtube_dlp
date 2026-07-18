# P3 extractor-pack upgrade evidence

## Scope

`internal/pack/upgrade` defines the Python-free signed contract for moving
extractor packs from v1.0 to the additive v1.1 schema. The production
`internal/pack/transaction` path adopts it: a signed catalog entry is bound to
the exact artifact bytes, publisher, archive signature, contract, permission
review, and existing atomic installer before any filesystem mutation. The
public `InstallPackCatalogTransaction` API exposes that transaction. Hosts
provide trust keys and upgrade policy explicitly.

## Compatibility and safety matrix

| Evidence | Automated coverage |
| --- | --- |
| v1.0 host / v1.0 pack | `TestCompatibilityMatrixOldHostNewPackAndNewHostOldPack` |
| v1.0 host / additive v1.1 pack | `TestCompatibilityMatrixOldHostNewPackAndNewHostOldPack` |
| v1.1 host / v1.0 pack | `TestCompatibilityMatrixOldHostNewPackAndNewHostOldPack` |
| v1.1 host / v1.1 pack | `TestCompatibilityMatrixOldHostNewPackAndNewHostOldPack` |
| New required host feature on old host | rejected as `ErrIncompatibleHost` |
| Unknown contract major | rejected as `ErrUnsupportedMajor`, even with a valid signature |
| Downgrade or same version | rejected as `ErrDowngrade` |
| Removal of an installed required capability | rejected as `ErrMissingCapability` |
| Permission increase | returned in `PermissionReview` and requires explicit approval |
| Python runtime or `.py` entrypoint | rejected as `ErrPythonRuntime` |
| Unknown fields, noncanonical JSON, malformed signatures/keys | categorized rejection; fuzz covered |
| Cancellation | checked before work, during negotiation, and before return |
| Resource limits | signed-record, permission, capability, annotation, and host-input bounds |
| Catalog-to-install binding | `TestPrepareBindsCatalogArtifactPublisherAndContractBeforeMutation` |
| Old install/new v1.1 activation | `TestInstallEndToEndUpgradeAndCompatibilityMatrix` |
| Rollback and revocation | `TestRollbackRevalidatesCatalogContractArtifactAndRevocation` |

Canonical signing sorts set-like fields and signs a domain-separated canonical
manifest with Ed25519. `TestCanonicalizationIsPermutationInvariant` exercises
multiple input permutations, while the two byte-exact fixtures pin v1.0 and
v1.1 output and signatures. The fixture hashes and generation provenance are
recorded in `conformance/pack/upgrade-v1.1/PROVENANCE.md`.

## Verification commands

The increment is accepted only after these commands pass:

```text
go test ./internal/pack/upgrade
go test ./internal/pack/transaction
go test -race ./internal/pack/upgrade
go test -race ./internal/pack/transaction
go vet ./internal/pack/upgrade
go test ./internal/pack/upgrade -run '^$' -fuzz '^FuzzVerifyAndNegotiate$' -fuzztime=100x -parallel=1
CGO_ENABLED=0 GOOS={linux,darwin,windows} GOARCH=amd64 go test -c ./internal/pack/upgrade
```

All production code uses the Go standard library and the existing Go pack
permission model. It has no runtime or build-time Python dependency and no
dependency on `/Users/tejas/projects/yt-dlp-reference`.
