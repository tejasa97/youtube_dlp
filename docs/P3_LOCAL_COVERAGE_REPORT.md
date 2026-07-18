# Phase 3 local coverage report

`telemetrycheck` merges opt-in privacy-safe snapshots without CI or a network
exporter. It emits one deterministic-shape JSON report containing the merged
snapshot and the full-denominator coverage calculation.

Example:

```sh
go run ./cmd/telemetrycheck \
  -input day-1.json -input day-2.json \
  -minimum-basis-points 9500 \
  -require-exact -require-zero-fallback
```

Exit status 3 means the report was valid but failed a requested gate. Overflow,
counter saturation, failures, unsupported inputs, and fallbacks stay in the
denominator. A nonzero minimum also rejects an empty denominator. Plugin
extractor IDs must be explicitly allowlisted with a repeated `-extractor`; an
unconfigured dimension is rejected instead of being silently discarded.

The command reads at most 256 bounded snapshots, reads stdin at most once,
does not contact a service, and never accepts raw URLs, metadata, headers,
credentials, or error strings. File paths and retention remain operator policy.
