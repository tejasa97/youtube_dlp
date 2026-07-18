# Telemetry conformance provenance and threat assumptions

The `snapshot_v1.json` fixture was authored for the Go port's Phase 3
privacy-safe measurement foundation on 2026-07-19. It is not derived from an
upstream yt-dlp fixture: the pinned upstream reference does not define this
local aggregation/export contract. Names are synthetic capability identifiers,
counts are arbitrary deterministic values, and the fixture contains no captured
traffic or user data.

The fixture proves canonical schema-v1 JSON ordering and all four dimensions
that may appear in an exported record: approved extractor ID, approved
capability ID, closed outcome category, and aggregate count. Timestamps and
per-event identifiers are intentionally absent.

## Threat assumptions

- URLs, query strings, paths, titles, credentials, cookies, request headers,
  raw errors, and arbitrary caller labels may contain secrets or identifying
  data and must never enter telemetry state.
- Extractor and capability identifiers are application-authored allowlist
  entries. They use a restricted 64-byte lowercase identifier grammar and are
  copied at construction time.
- Outcome is one of `success`, `error`, `fallback`, or `unsupported`; detailed
  error text and dynamic error classes are outside this package's contract.
- Cardinality and input bytes are bounded even when a snapshot is malicious.
  Dropped observations and saturated counters are represented only by aggregate
  overflow counts.
- Snapshots are not authenticated by this package. A caller that accepts them
  across a trust boundary must supply integrity/authentication separately.
- Aggregates can still reveal coarse usage patterns. Export and retention are
  therefore expected to remain explicitly opt-in and governed by product
  policy when integration is added.

No Python interpreter, network service, reference checkout, environment secret,
or nondeterministic clock is used to build or validate these fixtures.
