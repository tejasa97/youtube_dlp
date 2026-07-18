# Phase 3 offline pack catalog

The v1 catalog is a bounded canonical JSON document signed with a
domain-separated Ed25519 signature. It provides exact package/version to local
artifact mappings, archive size and digest binding, publisher identity, expiry,
and signed package revocations. Trust roots and the verification time are
caller-supplied; there is no trust-on-first-use or hidden clock.

Catalog resolution is deliberately exact. It does not fetch URLs, read
credentials, execute plugins, or select a floating version. Artifact transport
and production signing custody remain deployment responsibilities. The
`internal/pack/transaction` boundary accepts an injected, cancellable artifact
loader and binds the authenticated catalog size, digest, publisher, archive,
v1.x contract, permission review, activation, rollback, and revocation checks.

Embedding clients use `VerifyPackCatalog` and the verified catalog's `Resolve`
method. The `ytdlp-pack catalog-verify` and `catalog-resolve` commands expose
the same boundary for local distribution workflows. Both require an explicit
trusted Ed25519 key and canonical verification time; resolution requires an
exact name and semantic version.

Embedding clients that install packs use `InstallPackCatalogTransaction`.
Loading the artifact is caller-owned, but a loader result cannot reach the
filesystem unless its exact bytes satisfy every signed catalog, archive, and
contract binding. Deterministic tests cover all four v1.0/v1.1 host/pack
combinations, permission increases, tampering, cancellation, concurrent
activation, rollback, and revocation.
