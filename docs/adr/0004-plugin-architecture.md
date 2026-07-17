# ADR 0004: Plugin architecture

Status: Accepted direction; protocol deferred to Phase 1

## Context

Go's native plugin mechanism is platform-limited and tightly coupled to compiler
and dependency versions. Python plugin compatibility would reintroduce Python
and cannot be the permanent extension model.

## Decision

The primary plugin ABI will be a versioned, out-of-process RPC protocol over
standard input/output or a local socket. WASM is the secondary sandboxed option
for portable plugins that fit its resource and networking model. In-process Go
plugins are not a supported cross-platform ABI.

The protocol will negotiate versions and capabilities, use bounded messages,
propagate cancellation, separate secrets from ordinary metadata, and require an
explicit permission declaration. Plugin failures cannot corrupt the host
operation state.

## Consequences

Plugins may be authored in Go or another language without imposing Python on the
product. Process overhead is accepted for portability and fault isolation.
Phase 1 owns RPC and WASM spikes; Phase 0 exposes no unstable plugin surface.
