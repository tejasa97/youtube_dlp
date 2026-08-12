# ADR 0006: JavaScript helper launch identity

Status: Accepted current boundary (2026-08-01)

## Context

The JavaScript helper is a native executable. A successful launch gives it the
host user's operating-system privileges before protocol framing, environment
scrubbing, resource limits, and cancellation can constrain its behavior.

## Decision

Helper discovery accepts an absolute configured path or the helper beside the
application. It does not search `PATH`. Validation rejects symlinks and unsafe
Unix parent directories, checks regular-file and executable policy, compares
the opened file with the inspected identity, and can verify an
embedding-supplied SHA-256 digest.

The current validation handle is closed before `exec.Command` resolves the
canonical pathname again. This is pre-launch validation, not handle-bound
execution. It does not prove that the validated bytes are the bytes eventually
executed against a same-user pathname or in-place replacement race.

```text
configured path / executable sibling
        |
        v
canonicalize + parent/file policy
        |
        v
open -> compare identity -> optional digest
        |
        v
close validation handle
        |
        v
pathname launch
```

An expected digest is meaningful only when supplied by a trusted embedding or
artifact record. A digest derived from the same untrusted path does not
establish origin. A code signature or publisher identity also does not by
itself bind a prior path check to the bytes selected by a later pathname launch.

## Platform boundary

- **Linux and other Unix-like systems:** no-follow, owner, mode, ancestor, and
  opened-file checks narrow pathname substitution but do not provide
  descriptor-bound execution.
- **macOS:** the current implementation does not claim that path-based static
  code validation closes concurrent modification or pathname replacement.
- **Windows:** portable Go metadata does not prove owner/DACL trust, and
  `CreateProcessW` launches by executable name rather than a verified file
  handle.

## Current evidence

Repository tests cover absolute-path policy, search-path and symlink rejection,
unsafe Unix helper and parent modes, opened-file identity, optional digest
validation, and a controlled swap during validation. No-cgo compile probes
cover Linux/amd64, macOS/arm64, and Windows/amd64.

That evidence supports the pre-launch validation claim only. The project does
not claim handle-atomic launch identity, a native-code sandbox, publisher
verification, or a protected installation root.

## Consequences

- Helper validation reduces accidental and search-path execution risk.
- Protocol bounds do not sandbox native code after launch.
- SHA-256 validation is not presented as launch-bound identity.
- Product and release documentation must use the narrower current claim.

## References

- [JavaScript helper protocol](../JAVASCRIPT_HELPER_PROTOCOL.md)
- [Helper supervisor implementation](../../internal/javascript/supervisor/supervisor.go)
- [Helper supervisor tests](../../internal/javascript/supervisor/supervisor_test.go)
