# ADR 0004: Plugin architecture

Status: Accepted

## Context

Go's native plugin mechanism is platform-limited and tightly coupled to exact
compiler and dependency versions. Python plugin compatibility would reintroduce
Python into the product boundary.

## Decision

The native plugin ABI is a versioned, out-of-process RPC protocol. WebAssembly
is the constrained option for extensions compatible with its host capability
model. In-process Go plugins are not a supported cross-platform ABI.

The protocols negotiate versions and capabilities, bound messages and retained
diagnostics, propagate cancellation, separate secrets from ordinary metadata,
and require explicit permission declarations. Plugin results are accepted only
after complete framing and schema validation.

Automatic trust or execution of arbitrary files from a search path is not part
of the plugin boundary. Pack verification, installation, and runtime support
remain limited to the behavior documented by their current platform contracts.

## Consequences

Plugins may be implemented without imposing Python on the host. Process
isolation and protocol overhead are accepted for portability and fault
containment. Permission declarations do not themselves grant a host capability.
