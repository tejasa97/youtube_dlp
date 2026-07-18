# Phase 3 operations fixture provenance

The version-1 canary suite and repair-drill evidence were authored on
2026-07-19 for the native Go port. They are not derived from a yt-dlp response:
the pinned upstream project does not define this deployment operations schema.

All identifiers are synthetic. `target_ref` values are inert deployment-owned
names, not URLs. `secret_handle` contains only a synthetic provider/name pair;
there is no credential value. The incident uses a synthetic Git-like patch
reference and fixed millisecond timestamps. Its two-hour diagnosis and 23-hour
detection-to-verification latency deterministically evaluate to `met_24h`.

The fixtures are consumed without network access, credentials, Python, a clock,
or the read-only reference checkout. Their purpose is canonical serialization,
privacy-boundary, and 24–48-hour SLO evidence.
