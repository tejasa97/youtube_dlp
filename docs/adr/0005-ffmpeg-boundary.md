# ADR 0005: ffmpeg process boundary

Status: Accepted for future implementation

## Context

Feature parity requires merging, transcoding, probing, and post-processing that
ffmpeg already implements well. Reimplementing codecs in Go is outside the
project goal. ffmpeg is not Python and is an acceptable optional external tool.

## Decision

ffmpeg and ffprobe will be supervised child processes invoked directly with an
argument vector, never through a shell. Discovery order, minimum version,
capabilities, and overrides will be explicit. Process lifetime is bound to the
operation context, stderr is bounded, secrets are redacted from diagnostic
arguments, and progress is translated into structured events.

Inputs and outputs must remain under approved paths unless the caller explicitly
provides an external path. Temporary outputs use the same atomic-finalization
rules as downloads. Tests use generated license-safe media fixtures.

## Consequences

Codec parity can advance without Python or a codec rewrite. ffmpeg remains an
optional runtime dependency for capabilities that need it; direct downloads do
not require it. Phase 1 implements and tests this boundary.
