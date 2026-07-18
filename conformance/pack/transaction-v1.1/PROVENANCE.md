# Signed pack transaction v1.1 provenance

The deterministic evidence in `internal/pack/transaction/transaction_test.go`
was authored for this repository on 2026-07-19 to close P3-AUD-C03. It composes
the repository's existing canonical signed catalog, `.ydp` archive, v1.0/v1.1
upgrade contract, and atomic installer formats; it is not copied from yt-dlp.

The fixed Ed25519 seeds, catalog timestamps, artifacts, contracts, publishers,
digests, sizes, permissions, and revocations are synthetic and non-production.
The matrix covers v1.0/v1.1 hosts and packs, including an old installed contract
upgraded to v1.1. Failure evidence covers catalog and package revocation,
artifact size/digest tampering, publisher and identity substitution, permission
review, policy disagreement, cancellation, rollback validation, and concurrent
activation. No fixture, test, build step, or runtime path invokes Python or the
read-only upstream reference checkout.
