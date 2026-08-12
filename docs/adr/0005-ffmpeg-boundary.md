# ADR 0005: FFmpeg process boundary

Status: Accepted

## Context

Merging, transcoding, probing, and media post-processing rely on FFmpeg and
FFprobe. Reimplementing codecs in Go is outside the project scope. These tools
are external native dependencies, not a Python runtime.

## Decision

FFmpeg and FFprobe run as supervised child processes invoked directly with an
argument vector, never through a shell. Discovery, capability checks, and
caller overrides are explicit. Process lifetime is bound to the operation
context, stderr is bounded, sensitive arguments are redacted from diagnostics,
and progress is translated into structured events.

Inputs and outputs remain within approved paths unless the caller explicitly
supplies another permitted path. Temporary outputs use the same staged and
atomic-finalization rules as other media artifacts. Tests use generated,
license-safe media fixtures.

## Consequences

Media processing does not require Python or a codec implementation in this
repository. FFmpeg remains an external runtime dependency for operations that
need it; compatible direct downloads do not require it.
