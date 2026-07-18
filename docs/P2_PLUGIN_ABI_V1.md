# Phase 2 Plugin ABI v1

## Status and scope

The primary plugin ABI is a length-prefixed JSON RPC protocol over a native
child process's standard input and output. ABI 1.0 preserves the Phase 1 wire
version `1`; ABI 1.1 is a backwards-compatible extension encoded as `65537`
(`major << 16 | minor`). The host and plugin declare inclusive ranges and use
the newest version in their intersection. A different major never negotiates.

ABI v1 supports three independently declared capabilities:

- `extractor`: URL and bounded option input to normalized metadata;
- `postprocessor`: a host-issued input artifact handle to host-issued output
  artifact handles; and
- `provider`: a named action with bounded arguments and ordinary values.

The public author-facing types, interfaces, framing codec, versions,
permissions, artifacts, errors, and opaque secret handles are in
`pkg/pluginapi`. The host policy and transports live under `internal/plugin`.
Importing the SDK does not discover, register, execute, or grant a plugin.

WebAssembly remains a secondary extractor-only ABI for pure plugins that need
no host imports. It is not the compatibility baseline for native RPC plugins.

## Package manifest

Each package is a directory containing `plugin.json` and exactly the declared
package-relative entrypoint. A representative manifest is:

```json
{
  "schema": "ytdlp-go.plugin/v1",
  "id": "publisher.extractor",
  "name": "Publisher extractor",
  "release": "1.1.0",
  "runtime": "native",
  "entrypoint": "publisher-extractor",
  "abi": {"minimum": 1, "maximum": 65537},
  "capabilities": ["extractor"],
  "permissions": ["network"]
}
```

The decoder is bounded, strict, and rejects unknown fields, trailing JSON,
duplicate object keys at any nesting depth, invalid identities, duplicate or
unknown capabilities and permissions, cross-major ranges, absolute or nested
entrypoints, and unsupported runtimes. `python`, Python-like entrypoints, and
all interpreter/script trampolines are rejected. A native file beginning with
a shebang is rejected even if its filename looks like a binary. This includes
Python, shell, Node, Ruby, and similar launchers.

The ABI does not accept a Python runtime declaration, Python executable,
Python module, or Python build/test helper. Plugins may be authored in any
language only if their distributed native artifact is a standalone executable
that needs no interpreter at runtime, or if it is a compatible WASM module.

## Discovery and approval

Discovery is explicit and deterministic:

1. The caller supplies one or more absolute trusted roots.
2. The loader canonicalizes each root and scans only direct child package
   directories. It never consults cwd, `PATH`, `HOME`, or an environment
   variable.
3. Symlink roots, packages, manifests, and entrypoints are rejected.
4. On Unix, roots, packages, manifests, and entrypoints must be owned by the
   effective user and must not be group/world writable.
5. Manifest and entrypoint SHA-256 digests are recorded. Entrypoints over 512
   MiB and manifests over the configured bound are rejected.
6. Immediately before launch the host repeats canonical path, owner, mode,
   manifest, file-type, interpreter, and digest verification.

Secure discovery currently fails closed on Windows because portable Go
`FileMode` does not prove the owner and DACL. A signed-pack installer or a
future Windows ACL verifier must supply that proof before product discovery is
enabled there. Windows RPC process isolation and cross-builds are implemented;
this limitation is specifically trusted-root ownership validation, not the
wire protocol.

Pre-launch revalidation minimizes but cannot eliminate the pathname TOCTOU
between the final hash and the operating system's executable open. Removing
that residual race portably requires a platform launch supervisor able to
execute an already verified file handle. Product policy should keep trusted
roots non-writable for the duration of discovery and execution.

Production RPC and WASM configurations require a validated package descriptor
and an identity-bound permission approver. `UnsafeTestOnly` exists solely so
deterministic tests can run the current Go test executable without installing a
package; product code must never set it.

The approval request binds:

- plugin ID and release;
- verified signer identity, when a signed-pack loader supplies one;
- verified executable/module digest;
- negotiated ABI version; and
- the exact permission set.

The approver must grant exactly the requested valid set. Supersets, subsets,
duplicates, and unknown values fail closed. Added permissions are reported by
comparison with the prior release so an update cannot silently expand access.
Approval is a launch trust gate; the native RPC transport does not pretend that
permission names alone are an operating-system syscall sandbox. Enforcing
network/filesystem/process permissions below the process boundary is a P2-09
platform-sandbox responsibility and must fail closed when unavailable.
Signed publisher verification and pack installation are P2-09 responsibilities;
an empty signer is visible to the approver and can be rejected by product trust
policy rather than being converted into a self-asserted identity.

## RPC exchange

Every message is exactly one UTF-8 JSON object prefixed by a four-byte
big-endian size. Frames are limited to 16 MiB by a hard cap and normally to 1
MiB. Zero-length, oversized, truncated, unknown-field, trailing-value, and
duplicate-key messages are rejected before an operation is accepted.

Before creating a production child, the host negotiates against the verified
package manifest and obtains the identity-bound exact approval. The exchange
then revalidates the live manifest and negotiated ABI against that approval:

1. Host sends `hello` with versions and its ABI range.
2. Plugin sends `hello` with its complete manifest.
3. Host validates the live manifest, capability, and selected ABI against the
   already approved verified package descriptor.
4. Host sends one `extract`, `postprocess`, or `provide` operation with the
   negotiated version.
5. Plugin sends the corresponding result. The response ID must match.

The public `pluginapi.Codec` implements this framing and strict JSON behavior.
`cmd/ytdlp-plugin-rpc-example` demonstrates the SDK wire types without copying
an internal host package.

Ordinary requests, metadata, provider values, argv, and environment values are
checked for conventional credential fields/assignments. Secrets are represented
only by short-lived opaque `SecretHandle` values; ABI v1 deliberately exposes
no operation that returns the underlying value. Child processes inherit no
environment. A caller may explicitly set only `LANG`, `LC_ALL`, `TZ`,
`TMPDIR`, `TMP`, `TEMP`, `SYSTEMROOT`, or `WINDIR`, subject to validation.
Arguments are bounded and conventional token/password/cookie/signature values
are rejected. Plugin stderr is bounded and never inserted into a returned
error.

Remote failures have a fixed category, optional stable code, bounded message,
and retryable flag. Unknown categories and empty/oversized messages are
malformed. Conventional credentials are removed from both the rendered error
and the structured failure detail before it leaves the host boundary.

## Cancellation, crashes, and native limits

Caller cancellation and wall-clock timeout send a best-effort `cancel`, close
stdin, wait for a bounded grace period, then terminate the isolated process
tree. Unix uses a dedicated process group. Windows assigns the child to a Job
Object with `KILL_ON_JOB_CLOSE`. Unsupported process isolation fails closed.
A non-zero exit, premature EOF, malformed result, hung child, or process tree
that cannot be terminated is categorized without including child stderr.

Native RPC bounds wall-clock time, cancellation grace, message bytes, retained
stderr, argument count/size, manifest bytes, entrypoint bytes, and process-tree
lifetime. It does **not** claim a portable CPU quota, process-count quota, or
address-space limit. `Limits.MemoryLimitPages` is ignored by native RPC and is
enforced only by the WASM host. A native memory/CPU sandbox requires a future
platform supervisor; the absence is explicit rather than silently falling
back to an unsandboxed mode.

On Windows there is a narrow start-to-Job-assignment race because `os/exec`
does not expose a suspended primary-thread launch. Assignment failure kills
the direct child and fails closed. Unix process-group creation happens as part
of child creation.

## Constrained WASM ABI

The WASM host uses wazero without WASI or host imports. A module exports:

- `memory`;
- `ytdlp_protocol_version() -> i32`; and
- `ytdlp_extract(input_ptr i32, input_len i32) -> i64`.

The result packs pointer in the high 32 bits and length in the low 32 bits.
Input and output are strict extractor JSON, response IDs must match, and module
bytes are hashed against a trusted descriptor before execution. The runtime
enforces message, memory-page, and wall-clock limits and closes the module on
context completion. wazero 1.9 has no stable instruction-fuel API, so ABI v1
does not claim deterministic instruction accounting. No host network,
filesystem, cookie, secret, postprocessor, or provider imports exist.

## Compatibility policy

Within major v1:

- existing fields never change meaning;
- new fields are optional and gated by the negotiated minor;
- plugins declare the full inclusive range they actually implement;
- a v1.1 plugin with range 1.0–1.1 must operate using v1.0 when selected by a
  v1.0 host;
- unknown JSON fields are not a compatibility mechanism because strict
  decoding rejects them; and
- a breaking schema, permission semantic, or operation change requires ABI
  major v2.

The checked-in v1.0/v1.1 manifests and the RPC upgrade helper verify the
baseline-host/newer-plugin path. Package release versions are independent of
ABI versions and do not imply protocol compatibility.

## Evidence and known deviations

Deterministic evidence is under `conformance/plugin/abi-v1`; provenance is
recorded alongside it. Automated coverage includes:

- strict manifest identity/range/duplicate-key/bounds decoding and fuzzing;
- deterministic discovery, owner/mode/symlink/path/digest checks;
- Python, interpreter, shebang, mutation, and oversized-file rejection;
- v1.0-to-v1.1 negotiation;
- extractor, postprocessor, and provider exchanges;
- exact permission-change approval and identity/digest/ABI binding;
- malformed, duplicate, oversized, crashed, hung, canceled, and remote-error
  handling;
- native Unix descendant termination and Windows Job Object cross-builds;
- WASM version, import, digest, memory, timeout, cancellation, malformed result,
  and remote-error handling; and
- Linux, macOS, and Windows no-cgo example/package builds.

Known deviations remain explicit:

- secure trusted-root owner/DACL verification is unavailable and fails closed
  on Windows;
- the native pathname revalidation-to-exec window is not handle-atomic;
- native address-space, CPU, and process-count quotas need a platform
  supervisor;
- Windows has the noted start-to-Job-assignment race;
- WASM has wall-clock but no instruction-fuel accounting; and
- signer verification, deterministic signed archives, installation, rollback,
  and revocation belong to P2-09 rather than this ABI package.
