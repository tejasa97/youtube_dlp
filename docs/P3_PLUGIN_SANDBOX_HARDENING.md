# Plugin sandbox hardening

Installed plugins are hostile code even when their pack signature and ABI
identity are valid. A signature identifies the publisher; it does not confer
ambient filesystem, network, process, credential, or host-resource authority.

## Enforced boundary

- Native plugins require `NewSandboxedPluginHost`; the ordinary host constructor
  is WASM-only. Product extractor routing also rejects a native installed plugin
  without an explicit sandbox policy.
- The package is revalidated at discovery, before approval, and once again
  immediately before process creation. Any manifest, path, ownership, mode, or
  executable-digest mutation invalidates the launch.
- Native launch has an empty inherited environment except the adapter's fixed
  locale/path variables. Args, environment overrides, roots, and secret handles
  are bounded and validated; secret values never enter argv or environment.
- Stderr is bounded and an overflow is an operation failure. ABI frames,
  WASM memory, operation time, cancellation, and native process-tree cleanup
  are also bounded.
- Permissions are approved against plugin ID, release, signer, executable
  digest, ABI, and exact requested permission set. Package identity or
  permission changes require a new approval.

## Platform matrix

| Platform | Native execution | Resource/process enforcement | Status |
| --- | --- | --- | --- |
| Linux | Validated `bwrap` plan only after `AllowExternalTools=true` | `prlimit` wraps `bwrap` before exec for address space, CPU, process-count, and file-descriptor limits; process group is killed on cancellation | Supported when both adapters are available; otherwise fail closed |
| macOS | No native launch plan | No maintained Apple kernel API is currently wired for the required filesystem/network and resource contract | Fail closed. `sandbox-exec` is deliberately not claimed as a security boundary. |
| Windows | No product generic native launch: `PrepareForOS("windows", ...)` rejects it | The internal RPC lifecycle code can create a child suspended, assign `KILL_ON_JOB_CLOSE` plus supported memory/CPU/process Job limits, then resume it | This Job code is **not** a reachable product generic sandbox path. Product sandbox APIs fail closed until filesystem/network confinement is implemented. |
| WASM | In-process wazero with no WASI or host imports | Memory page cap, ABI frame cap, cancellation/timeout, no ambient FS/env/stdin/stdout imports | Supported. Pinned wazero v1.9.0 has no stable public instruction-fuel API; requesting `WASMInstructionBudget` fails closed. |

## Intentional residual limits

- Linux depends on explicitly allowed, owner-validated distribution tools. The
  host does not attempt a racy post-start `setrlimit`/`prlimit` operation.
- Windows Jobs cannot express the descriptor/output policy or filesystem and
  network namespace used by the generic sandbox contract. `PrepareForOS`
  rejects Windows, so the Job sequencing code is not evidence of reachable
  product containment.
- Wazero wall-clock cancellation is not represented as fuel. Deterministic
  instruction budgeting remains unavailable until a stable wazero API exists.
