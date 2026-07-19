# Phase 2 signed updater and release foundations

Status: implemented internal foundation; product command and CI integration are
owned by the primary Phase 2 integration lane.

## Boundary and trust model

`internal/update` accepts a trust configuration and already-downloaded bytes.
It does not discover keys, choose a publisher, fetch a URL, inspect `PATH`, or
create signing authority. `internal/release` converts explicit inputs into
deterministic artifacts and metadata. Neither package invokes Python or reads
the pinned Python reference checkout.

Release envelopes use canonical JSON and threshold Ed25519 signatures. A key ID
is the lowercase SHA-256 of its public key. Trust is explicitly scoped to the
`release` role, product, channel, and platform/architecture pairs. Verification
rejects unknown signers, duplicate fields or signatures, non-canonical bytes,
wrong scopes, invalid targets, and signatures below threshold. Metadata records
a monotonically increasing generation and an RFC 3339 expiration. Selection
rejects expired or overly long-lived freshness, replay/freeze, downgrade,
wrong-channel, wrong-product, and wrong-platform results.

The deterministic private key used by tests is derived from a repository-scoped
string documented in `conformance/update/PROVENANCE.md`. It is deliberately not
a production key. Production offline-root custody, threshold participants,
release-key authorization/rotation/revocation, online freshness delegation,
and transparency publication are external operational decisions. This
repository neither chooses custodians nor stores production private material.

## Installation, recovery, and rollback

An updater manager receives a private absolute root selected by product policy.
It creates same-filesystem `staging` and `releases` directories and publishes
immutable `releases/<version>/<artifact>` trees. The active executable is a
checksummed state pointer, avoiding replacement of a running executable on
Windows. State publication uses a synced temporary file and atomic replacement;
Windows uses `MoveFileEx` with replace-existing and write-through flags.

Every state transition is protected by an owner lock. A stale timestamp alone
cannot steal a long-running live process's lock; reclaim also requires the
recorded owner PID to be dead. State and recovery journals are bounded,
canonical, SHA-256 protected, path-safe records. A remaining journal means the
new state was not durably health-acknowledged, so recovery restores the last
verified pointer and only removes a newly published artifact after its exact
size and digest are verified.

After activation, a required health checker validates the selected artifact.
The supplied command checker executes the artifact directly with a fixed empty
or locale-only environment, bounded argv/output/time, exact version identity,
and no shell. A failed check restores the prior verified state. Explicit
rollback re-verifies the retained artifact before activation and preserves the
highest accepted metadata generation, so rollback does not weaken subsequent
freeze protection.

## Reproducible releases

`internal/release` provides:

- stable no-cgo Go build plans using trim-path, disabled VCS stamping, readonly
  modules, an empty build ID, explicit platform and source epoch;
- sorted deterministic `tar.gz` and ZIP archives with fixed timestamps,
  owners/modes, portable paths, and case-fold collision rejection;
- standard sorted `SHA256SUMS` generation and verification;
- normalized license bundles plus exact SBOM-to-license coverage validation;
- stable SPDX 2.3 JSON with explicit namespace, creator, timestamp, licenses,
  download locations, and optional component digests;
- deterministic release manifests and a bounded two-build byte comparator.

`cmd/ytdlp-release` now binds those primitives to explicit cross-built Go
binaries, validates a common linked dependency graph, includes retained license
texts, and writes the complete release set without replacing existing files.
The manual alpha workflow builds each native target twice, runs it on its native
runner, then assembles and verifies the short-lived engineering artifacts. See
`docs/P2_ALPHA_RELEASE.md` for the procedure and distribution constraints.

All parsers and generators have fixed entry, component, path, metadata, and
byte limits. Diagnostics use sentinel categories and never render signed bodies,
artifact contents, URLs, private paths, signatures, keys, or health-check
output.

## Automated evidence

The updater tests cover deterministic/threshold signatures, canonical and
duplicate JSON, tampering, unknown scopes, expiration, freeze, downgrade,
platform/channel selection, hostile/reserved paths, digest/size mismatch,
concurrent writers, caller trust mutation, live and dead stale locks,
cancellation, failed health checks, explicit rollback, crash recovery, corrupt
journals, and generated Linux/macOS/Windows install scenarios. The release
tests inspect archive headers, modes, ordering and timestamps; permute inputs;
validate checksum, license, SBOM, manifest, secret-bearing URL, and hostile path
failures; and compare independent outputs byte for byte. Both decoders have
bounded fuzz targets.

Scoped verification includes unit, race, vet, fuzz, and CGO-disabled test-binary
cross-compilation for Linux amd64, macOS arm64, and Windows amd64. The primary
lane owns final command/workflow wiring, two clean full-product build comparison,
artifact install/update/rollback/run execution on each OS, and container audit.

## Explicit deviations and integration risks

- The Windows Go standard-library boundary does not prove root owner or writable
  ACLs. Product policy must select and secure a per-user root. The implementation
  still rejects symlink/reparse-point roots and non-directory components.
- Health-check cancellation terminates a Unix process group. Windows uses a
  `KILL_ON_JOB_CLOSE` Job Object and has a Windows-only descendant termination
  test. The standard `os/exec` start-then-assign sequence leaves a narrow race
  in which a hostile binary could spawn and detach a child before Job Object
  assignment; eliminating it requires a suspended-process launcher outside the
  current standard-library boundary.
- Windows has no portable directory-fsync operation. Files are synced and
  replacement requests write-through, but the package does not claim a POSIX
  directory durability primitive there.
- Artifact transport, offline-root rotation, transparency, production signing,
  updater UI/events, and automatic scheduling remain outside these internal
  packages. Missing integration must fail closed; it is not a silent fallback.
- The SPDX and license APIs consume the linked dependency inventory from Go
  build information and invoke exact coverage validation. Components without a
  reviewed license conclusion remain `NOASSERTION`; the tool never guesses or
  downloads a license. The project component is explicitly declared
  `Apache-2.0` and its checked-in license text is included in every archive.
- Archive construction is memory-oriented and bounded to the declared Phase 2
  limits. Streaming multi-gigabyte release construction is not claimed.
