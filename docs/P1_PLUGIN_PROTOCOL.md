# Phase 1 plugin spike

## Outcome

The Phase 1 experiment selects a versioned out-of-process RPC protocol as the
primary Phase 2 plugin architecture. WebAssembly remains a secondary option for
pure, sandbox-friendly extractors that need no ambient filesystem, network,
cookie, or secret access.

The spike is intentionally not wired into automatic extractor discovery. A
future product integration must first define trusted installation, signing,
updates, and user-visible permission approval. Loading arbitrary files from a
search path would turn a successful protocol experiment into a supply-chain
feature without an adequate security design.

## Shared model

`internal/plugin` defines protocol version 1, extractor request and response
messages, permission declarations, resource limits, and errors that callers can
classify with `errors.Is`:

- incompatible protocol version;
- permission denied;
- resource limit exceeded;
- malformed message;
- plugin crash;
- plugin timeout; and
- caller cancellation through `context.Canceled` or
  `context.DeadlineExceeded`.

Permissions are deny-by-default. The spike names network, cookie, secret, and
filesystem-read permissions. Naming a permission does not grant it and does not
create a host API. A required permission must be present in the caller's grant.

Secrets must not be placed in executable arguments, environment variables,
stderr, manifests, or ordinary metadata. Phase 2 should transfer short-lived
opaque host handles over a separate authenticated channel. Neither spike
currently transfers secrets.

## RPC protocol

The RPC transport uses stdin/stdout. Every JSON message has a four-byte
big-endian length followed by exactly one JSON object. Unknown fields,
zero-length messages, truncated messages, and messages over the configured
limit are rejected.

The exchange is:

1. Host sends `hello` with its supported versions.
2. Plugin sends `hello` with its manifest, supported versions, and required
   permissions.
3. Host selects the highest common version and checks grants.
4. Host sends one `extract` request with the selected version.
5. Plugin sends one `result`, whose request ID must match.

On cancellation the host sends a best-effort `cancel` message, closes stdin,
waits for a bounded grace period, and kills an unresponsive process. The host
also bounds input/output frames and retained stderr. A crashed, malformed, or
hostile child cannot corrupt host state because results are accepted only after
complete validation.

The portable spike bounds time and communication resources. Portable hard
address-space/process-count limits are not implemented; Phase 2 must add
platform sandbox adapters or launch plugins through a dedicated supervisor.

## WASM ABI

The WASM host uses wazero and instantiates no WASI module or host imports. A
module must export:

- `memory`;
- `ytdlp_protocol_version() -> i32`; and
- `ytdlp_extract(input_ptr i32, input_len i32) -> i64`.

The host writes UTF-8 JSON input at byte offset 32768. The extractor return
packs the output pointer in the high 32 bits and output length in the low 32
bits. The response is strict JSON and its request ID must match.

The runtime enforces a page memory maximum, message limits, context
cancellation, and a wall-clock deadline. `WithCloseOnContextDone` terminates
non-returning guest code. wazero 1.9 does not expose a stable portable
instruction-fuel budget, so the spike does not claim fuel metering. The
wall-clock deadline is the bounded substitute for Phase 1; adding deterministic
instruction accounting remains a Phase 2 experiment.

## Discovery, signing, and updates

Recommended Phase 2 packaging is a signed archive containing a sidecar manifest
and one platform executable per target for RPC, or one WASM module. Discovery
should use explicit configured directories, reject writable-by-others paths,
verify a publisher signature before execution, pin the protocol range, and show
permission changes before update. Rollback metadata and revocation are required
before any automatic updater is enabled.

## Portability and evidence

The RPC implementation relies only on Go's portable `os/exec` and pipes. The
WASM implementation is pure Go and uses wazero. The tests use the current Go
test executable as an RPC child and construct a license-safe WASM module without
an external compiler. Cross-build evidence is recorded by building the plugin
packages and example commands for Linux, macOS, and Windows.

Known deviations are explicit:

- no automatic discovery or product registry integration;
- no signing/update implementation;
- no secret-transfer channel;
- no portable RPC OS-level memory/process sandbox;
- no WASM instruction fuel counter; and
- no WASM host network/filesystem APIs, even when permissions are granted.
