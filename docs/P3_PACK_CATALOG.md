# Phase 3 offline pack catalog

The v1 catalog is a bounded canonical JSON document signed with a
domain-separated Ed25519 signature. It provides exact package/version to local
artifact mappings, archive size and digest binding, publisher identity, expiry,
and signed package revocations. Trust roots and the verification time are
caller-supplied; there is no trust-on-first-use or hidden clock.

Catalog resolution is deliberately offline and exact. It does not fetch URLs,
read credentials, execute plugins, or select a floating version. Artifact
transport and production signing custody remain deployment responsibilities;
the catalog supplies authenticated discovery metadata for the existing pack
verification and installation boundary.

Embedding clients use `VerifyPackCatalog` and the verified catalog's `Resolve`
method. The `ytdlp-pack catalog-verify` and `catalog-resolve` commands expose
the same boundary for local distribution workflows. Both require an explicit
trusted Ed25519 key and canonical verification time; resolution requires an
exact name and semantic version.
