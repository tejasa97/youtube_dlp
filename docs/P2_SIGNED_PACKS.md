# Phase 2 signed plugin packs

Status: signed-pack, transactional install/remove, and sandbox-adapter lane
implemented; product/CLI registry integration remains primary-agent owned.

This implementation replaces unchecked Python plugin archives with a native Go
package format. It does not load Python and it does not depend on the pinned
reference checkout at build time, test time, or runtime.

## Canonical format

A pack is a ZIP archive containing exactly these entries, in this order:

1. `manifest.json`;
2. `signature.json`; and
3. one `payload/<path>` entry for each manifest file, sorted by path.

All entries use ZIP `Store`, fixed 1980-01-01 UTC timestamps, explicit `0600` or
`0700` modes, no comments, and canonical compact JSON. Verification rebuilds
the entire archive from verified fields and requires a byte-for-byte match.
This rejects prefixes, suffixes, alternate encodings, duplicate entries,
undeclared files, compression bombs, links, and ambiguous trailing data.

The Ed25519 signature covers:

```text
"ytdlp-go/plugin-pack/v1\x00" || canonical_manifest_json
```

The manifest contains each payload's SHA-256, byte length, and mode. Publisher
key IDs are always `ed25519:` plus the full SHA-256 of the public key. A caller
cannot assign an unrelated key ID. Verification requires that exact ID and key
in an explicit trust map; there is no trust-on-first-use path.

Manifest paths use forward slashes and an ASCII-only portable component set.
They reject traversal, absolute/drive paths, backslashes, case-fold aliases,
trailing dots/spaces, Unicode normalization ambiguity, and DOS reserved names.
The ZIP format cannot represent hardlinks; every accepted entry must be a
regular file.

## Bounds and policy

- archive: 72 MiB;
- aggregate payload: 64 MiB;
- individual payload: 32 MiB;
- manifest: 256 KiB;
- signature record: 16 KiB;
- payload entries: 256;
- permissions: 32; and
- installed state history/pending removals: 64 records.

`VerifyPolicy` requires a caller-supplied time and explicit trust roots. It
rejects not-yet-valid or expired packs, revoked keys/manifests/versions,
host-version incompatibility, and downgrades. Revocation metadata is strictly
bounded and validated before archive parsing proceeds.

Permission changes return deterministic `PermissionReview` data. Any added
permission requires the caller to set an explicit approval bit. A first install
that requests permissions is also an increase and therefore requires review.

## Transactions and crash recovery

Install roots must be absolute canonical paths, owned by the current Unix user,
mode `0700`, and free of symlink aliases. A new root is created only beneath an
already-existing secure parent, avoiding ambiguous recursive creation. Files
are written exclusively into a private staged directory, individually synced,
re-read and signature-verified, then renamed into the version store.
Directories containing payload and state renames are fsynced before activation
is reported.

An advisory kernel lock serializes install, rollback, and removal. The lock
file may persist, but its lock is released by the kernel on process exit or
crash. A live owner is never displaced. A subprocess crash test proves that a
subsequent installer can acquire the released lock.

Retries remove abandoned `.stage-*` and removal-quarantine `.remove-*` entries
only while holding that lock.
An inactive version left by a crash is recovered only when its reconstructed
signed archive exactly matches the requested archive. An unknown or mismatched
orphan fails closed.

Rollback reconstructs the canonical archive from installed metadata and files,
then rechecks signature, trust, digest, mode, expiration, and revocation before
publishing state. Extra files, symlinks, hardlinks, and mode/owner changes reject
rollback.

Removal publishes state containing a bounded pending-removal journal before it
renames and unlinks payload. This is the logical commit point. The next locked
operation finishes a journal left by a crash. Removal never follows symlinks or
hardlinks, so a corrupted or revoked pack can still be safely removed. Removing
an active version may optionally activate the previous version, but that target
must pass full verification and any permission increase requires approval.

Cancellation is honored before transaction publication. Once state publication
begins, the bounded local rename/sync is completed so cancellation cannot create
an ambiguous activation.

## Error categories

The package exposes stable sentinels for invalid manifest/archive/revocation,
resource bounds, unsafe paths, untrusted publishers, signatures, revocation,
validity windows, downgrade/host incompatibility, permission review, lock,
installed-state corruption, and I/O failure. Errors never include signature
bytes, payload contents, command arguments, secret values, or raw untrusted
paths.

## Portability and known deviations

- Canonical pack build and verification are portable and no-cgo.
- Secure install, rollback, and removal currently support Linux and macOS. They
  require owner/mode/link-count facts exposed by those kernels. Other platforms
  return `ErrPlatformSecurity` before filesystem mutation rather than claiming
  ACL safety that the implementation cannot prove.
- Windows support is therefore verification-only in this increment. A future
  Windows installer needs explicit owner/ACL checks, atomic publication, and a
  crash-releasing lock with equivalent evidence.
- Production signing-key custody, trusted-root distribution, and signed
  revocation-feed delivery are intentionally not selected here. This package
  accepts keys and policy; updater/release integration owns their provenance.
- There is no implicit plugin discovery, auto-enable, or Python archive
  compatibility. Product integration must register only a verified active
  version.

## Evidence

Synthetic fixture inputs and expected archive identity live in
`conformance/packs`; `PROVENANCE.md` records the pinned upstream inspection and
the absence of copied code or credentials. Test keys are deterministic and
test-only.

The scoped lane runs formatting, unit and failure tests, race detection, vet,
`FuzzVerify`, `FuzzPayloadPath`, and `FuzzPrepareArguments`, plus no-cgo package
builds for Linux, macOS, and Windows. Python is not invoked by any path.
