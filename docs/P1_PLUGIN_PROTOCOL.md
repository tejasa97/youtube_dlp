# Plugin protocol boundary

## Decision

The implemented plugin boundary uses a versioned out-of-process RPC protocol. WebAssembly remains a secondary option for
pure, sandbox-friendly extractors that need no ambient filesystem, network,
cookie, or secret access.

The protocol is not wired into automatic extractor discovery. Loading arbitrary
files from a search path is outside the supported boundary and would bypass the
repository's pack verification and permission controls.

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

Permissions are deny-by-default. The protocol names network, cookie, secret, and
filesystem-read permissions. Naming a permission does not grant it and does not
create a host API. A required permission must be present in the caller's grant.

Secrets must not be placed in executable arguments, environment variables,
stderr, manifests, or ordinary metadata. Neither protocol transfers secrets.

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

Captured plugin stderr is never embedded in returned errors. Remote structured
error messages redact conventional token/signature/password/key assignments
before rendering; callers must treat the explicit detail object as untrusted.

The portable RPC boundary limits time and communication resources. It does not
claim portable hard address-space or process-count isolation.

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
instruction-fuel budget, so the current boundary does not claim fuel metering.

## Discovery, signing, and updates

Automatic search-path discovery is unsupported. Signed-pack verification and
installation are separate explicit operations with the platform limitations
documented by the pack and updater contracts.

## Portability and evidence

The RPC implementation relies only on Go's portable `os/exec` and pipes. The
WASM implementation is pure Go and uses wazero. The tests use the current Go
test executable as an RPC child and construct a license-safe WASM module without
an external compiler. Cross-build evidence is recorded by building the plugin
packages and example commands for Linux, macOS, and Windows.

Known deviations are explicit:

- no automatic discovery or product registry integration;
- signing and update are separate explicit package operations;
- no secret-transfer channel;
- no portable RPC OS-level memory/process sandbox;
- no WASM instruction fuel counter; and
- no WASM host network/filesystem APIs, even when permissions are granted.
