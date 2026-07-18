# Phase 3 operations fixture provenance

The version-1 canary suite was authored on 2026-07-19 for the native Go port.
It is not derived from a yt-dlp response: the pinned upstream project does not
define this deployment operations schema. Canary and `target_ref` identifiers
are synthetic inert deployment-owned names, not URLs. `secret_handle` contains
only a synthetic provider/name pair; there is no credential value.

`major_site_drill_v1.json` replaces the former `deadbeef` placeholder with an
attributable Twitch/live-HLS repository regression. Commit
`60fccbd12300fa0045c188efcbe2ae65515b225f` introduced LL-HLS media-sequence
addition without overflow rejection at author timestamp
2026-07-19T02:14:57+05:30 (`1784407497000` milliseconds). Commit
`9ad13deac4a7f81fe2ece83d94a53300e926bdaa` added overflow rejection and its
regression assertion at author timestamp 2026-07-19T02:15:50+05:30
(`1784407550000` milliseconds). The patch and verification timestamp are equal
because the regression test is committed with the fix.

The incident schema requires detection and diagnosis timestamps, but Git
history does not establish a live canary observation or a separate diagnosis
event. The fixture therefore uses the introducing commit timestamp as the
earliest attributable regression boundary and the fixing commit timestamp as
the diagnosis boundary. Its computed 53-second `met_24h` status is repository
repair arithmetic, **not** evidence of a production detection-to-patch SLO or
of an executed Twitch canary. No intermediate observations are inferred.

The fixtures are consumed without network access, credentials, Python, a clock,
or the read-only reference checkout. Their purpose is canonical serialization,
privacy-boundary, and 24–48-hour SLO evidence.

`canary_policy_v1.json` and `replay_capture_v1.json` were authored on
2026-07-19 as deterministic native-Go conformance fixtures. The policy binds
the exact canonical suite hash and every suite ID to an explicit expiry, and
permits no more than two starts in a
rolling hour with a fifteen-minute minimum interval. Its timestamps are fixed
test values and do not assert a production execution occurred.

The replay capture is derived from the synthetic outcomes already exercised by
`TestExecuteRequiresOptInAndEmitsRedactedSemanticRecords`. Its `suite_sha256`
is SHA-256 of the canonical `canary_suite_v1.json` object (without trailing
whitespace). It intentionally excludes raw network requests/responses, target
references, secret handles, regions, errors, timestamps, and media metadata.
Consequently it can reproduce outcome classification and aggregation, but not
server behavior or network transport. Live/authenticated/regional execution
and atomic persistence of the returned execution ledger remain deployment
responsibilities.
