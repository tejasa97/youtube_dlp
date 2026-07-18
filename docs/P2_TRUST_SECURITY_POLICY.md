# Phase 2 trust and security policy

Status: Gate G2 integration policy
Scope: plugin discovery, signed packs, updater metadata, release artifacts

## Fail-closed boundary

Trust is always explicit. The product does not search the current directory,
`PATH`, download directories, or arbitrary writable locations for plugins or
packs. A missing key, unknown signer, malformed declaration, expired metadata,
permission increase, unsupported sandbox, or unavailable isolation mechanism
returns a categorized error. None of these conditions activates an unsigned,
older, Python-backed, or in-process fallback.

Production, build, test, release, plugin, and updater paths never execute
Python. A plugin or pack that declares a Python interpreter/runtime, a Python
entry point, or an interpreter trampoline is rejected before installation or
launch.

## Trusted roots and filesystem ownership

Discovery accepts only roots supplied by the embedding application or roots
selected by a platform-specific product policy. Roots are canonicalized before
use. The discovered manifest and every parent from the root to the entry point
must remain within that root and must not be a symlink, hardlink alias, device,
socket, or other non-regular object. User roots must be owned by the current
user and not group/world writable where the platform exposes reliable ownership
and mode data. A platform that cannot prove the requested property reports the
limitation rather than claiming it.

Installation and updates use private same-filesystem staging directories,
exclusive files, bounded extraction, durable writes, and atomic publication.
Existing destinations are replaced only where the platform primitive provides
the claimed atomic guarantee. Rollback retains a previously verified artifact;
it never blesses an unverified backup.

## Signing roles

Phase 2 tests use deterministic, repository-scoped Ed25519 keys that are
clearly marked non-production. The repository does not create, choose, or
store a production private key.

The production ceremony is an external deployment responsibility. Its public
trust configuration must distinguish:

- an offline root role that authorizes and revokes release/pack keys;
- pack signer roles scoped to pack identity and permission ceiling;
- release signer roles scoped to channel and platform target;
- short-lived online freshness metadata, where automatic update is enabled.

Verification covers the canonical signed metadata bytes plus the complete
artifact digest and size. Key identifiers are hashes of public keys, not
caller-controlled labels. Unknown algorithms, duplicate fields/signatures,
non-canonical encodings, ambiguous paths, and trailing archive data are
rejected. Threshold policy is configuration, with no implicit trust-on-first-
use behavior.

## Permissions and secrets

Plugin permissions are a closed, versioned set. Approval binds plugin identity,
signer, version, exact permission set, executable digest, and ABI. An update
whose permissions are not a subset of the approved set pauses for explicit
review. Downgrades require an explicit rollback authorization tied to a known
verified version.

Plugins receive a minimal allowlisted environment and structured arguments.
Credentials are not placed in argv, environment variables, ordinary metadata,
stderr, or diagnostic events. Where a future provider requires a secret, the
host passes a short-lived opaque handle through a dedicated channel and applies
scope, expiry, operation, and use-count limits. Phase 2 code must reject a
declared secret capability if that handle channel is unavailable.

## Isolation

RPC plugins run out of process with bounded messages, time, stderr, and declared
resources. Cancellation terminates the plugin process tree on platforms where
the implementation has tested support. WASM remains secondary and receives no
WASI, filesystem, network, clock, or random imports unless an individually
reviewed capability adds them. Unsupported address-space, process-count,
filesystem, or network sandbox guarantees remain explicit deviations.

Sandboxing is defense in depth; signature verification, permissions, safe
paths, bounded parsing, and crash isolation remain mandatory even when an OS
sandbox is available.

## Update freshness and rollback

Update state is scoped by product, channel, platform, and architecture. It
records the highest accepted metadata generation and artifact version. Normal
updates reject lower generations/versions, expired metadata, channel or target
mismatch, and artifact digest/size mismatch. A repository freeze is detectable
because freshness metadata expires. Clock input is injectable for deterministic
tests, and unreasonable clock state fails closed.

After publication, a bounded health check verifies the installed binary and
API/version identity without invoking a shell. Failure restores the last
verified artifact atomically. The lock and recovery journal are path-safe,
bounded, checksummed, and never interpreted as commands.

## Logging and audit

Trust decisions emit structured, secret-safe events that identify the
capability, artifact/plugin identity, signer key ID, decision, and non-secret
reason. Signatures, authorization headers, signed URLs, cookies, secret handles,
private paths containing credentials, plugin stderr, and artifact contents are
not logged. Temporary fallbacks remain governed by
`conformance/fallback_inventory.yaml`; silent and Python-backed fallback is
prohibited.
