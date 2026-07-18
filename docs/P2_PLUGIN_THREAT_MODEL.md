# Phase 2 plugin-pack and sandbox threat model

This document refines the project trust policy for signed packs and hostile
out-of-process plugins. It aligns with `docs/P2_TRUST_SECURITY_POLICY.md`; that
primary-agent-owned policy remains authoritative.

## Assets and boundaries

Protected assets are trusted publisher roots, active-version state, downloaded
media, cookie/credential material, user files outside declared writable roots,
and host availability.

The pack verifier treats every archive byte, JSON field, filename, permission,
signature record, and payload as hostile until canonical verification succeeds.
A valid signature establishes publisher identity and artifact integrity; it
does not make plugin behavior safe. Runtime permissions and sandboxing remain
mandatory.

The install root is a separate trust boundary. Linux/macOS operations require a
canonical, current-user-owned `0700` root and revalidate file type, owner, mode,
link count, and identity around sensitive reads. A root/system attacker and an
attacker able to arbitrarily act as the same UID can still race ordinary
filesystem APIs; those actors are outside the guaranteed isolation boundary.
Detected aliases or mutations fail closed.

## Threats and controls

| Threat | Control | Residual risk |
| --- | --- | --- |
| Archive traversal or platform alias | ASCII portable names; case-fold/DOS/drive/trailing-dot rejection; exact declared entries | Future platforms may introduce new reserved aliases and require an updated schema |
| Compression bomb or oversized metadata | Store-only canonical ZIP and independent compressed/uncompressed/count/aggregate bounds | Configured bounds still consume bounded memory because verification returns payload bytes |
| Signature/key-ID substitution | Full public-key-derived ID, domain-separated Ed25519 signature, explicit trust map, no TOFU | Compromised trusted keys require externally distributed revocation metadata |
| Downgrade, expiration, or revoked artifact | Caller time, semantic versions, host minimum, key/manifest/version revocations | Authenticity and freshness of the revocation feed belong to updater/release policy |
| Partial/crashed activation | exclusive staging, file/directory fsync, atomic rename, signed orphan recovery, kernel crash-releasing lock | Sudden hardware loss follows filesystem durability guarantees |
| Partial/crashed removal | state-first pending-removal journal, quarantine rename, directory fsync, replay on next lock | Physical deletion may finish on the next operation after a crash |
| Symlink/hardlink replacement | no archive links; Lstat/SameFile/link-count checks; exact installed tree on rollback; removal never follows links | Same-UID racing after checks is outside the guarantee |
| Permission escalation on update/rollback/removal | sorted added/removed review; increases require explicit approval | A granted capability can still be abused by a malicious but correctly signed plugin |
| Secret disclosure | sandbox accepts only validated opaque handle IDs; no secret values in manifest, argv, environment, errors, or fixtures | A broker is not implemented in this lane; hosts must deny secret permission until an operation-scoped broker exists |
| Host resource exhaustion | bounded RPC/WASM hosts; Linux adapter can require `prlimit`; wall-clock/process-tree control remains supervisor-owned | macOS adapter cannot enforce CPU/memory/process limits and rejects requested adapter limits |

## Writable paths

Writable roots must be canonical, current-user-owned directories with no group
or world permissions. They may not overlap read-only roots or contain the
plugin executable. A writable root is persistent attacker-controlled output:
the host must allocate it per publisher/plugin (and preferably per operation),
must never scan it for executable plugins, and must validate every artifact
before moving it into a user destination. Plugin install/version directories
are always read-only to the plugin.

Temporary paths should be private and operation-scoped. Cleanup failures are
reported but cannot cause the host to activate files from a writable root.

## Secret handles

`internal/sandbox` carries only lowercase opaque identifiers such as
`cookie.main`; it has no field for a secret value and never injects handles into
argv or environment. The eventual supervisor/broker must bind each handle to a
single operation, publisher key ID, plugin version, and declared permission;
expire it on cancellation; rate-limit redemption; and return only the minimum
credential material required. Broker requests and diagnostics must log the
handle category, never its value or resolved secret.

Until that broker is integrated, `secrets` and `cookies` permissions are review
data only and must not grant access.

## Sandbox adapters

Linux plans use `bwrap` with a new session, all namespaces unshared, network
shared only when explicitly granted, a new `/proc`, `/dev`, and `/tmp`, an empty
environment rebuilt from fixed values, read-only declared binds, and distinct
writable binds. Optional address-space, CPU, process, and descriptor caps are
applied by `prlimit`; requested caps fail if it is unavailable. Dynamic runtime
and library roots must be explicitly declared, favoring static RPC plugins.

macOS plans use an explicit `sandbox-exec` deny-default profile with declared
read/write paths and network disabled by default. The adapter is deprecated by
Apple and checked at runtime. It cannot prove resource caps, so any requested
cap returns `ErrUnsupportedLimit`. A separate supervisor remains responsible
for wall-clock cancellation and process-tree termination.

Windows and other platforms return `ErrUnsupportedPlatform`; there is no
unsandboxed fallback. A future Windows adapter needs an AppContainer/restricted
token, job-object process tree, ACL-aware path broker, and automated escape and
resource-limit evidence.

Plans contain argv arrays and fixed environment entries. Integrators must use
them directly with `os/exec`, set the specified working directory/environment,
and never pass them through a shell.

## Hostile plugin response

A crash, malformed frame, timeout, cancellation refusal, resource violation,
or denied broker request terminates that operation and must not change active
pack state. The supervisor owns process-group termination and bounded stderr;
the pack manager owns immutable signed payload selection. Repeated failures may
disable a plugin in separate policy state, but must never silently fall back to
an unverified or unsandboxed executable.
