# Phase 4 SponsorBlock metadata fixture provenance

The synthetic fixtures in this directory are deterministic,
cookie-free, and contain no captured production response. They are
derived from the pinned yt-dlp reference at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
(`yt_dlp/postprocessor/sponsorblock.py` and the mark-only arrangement in
`yt_dlp/postprocessor/modify_chapters.py`). The reference is a
read-only behavioral mirror and is not executed or imported by the
Go port; the fixtures replicate its hash prefix, envelope shape,
and per-segment normalization policy so the Go implementation can
be regression-tested without contacting the public SponsorBlock
service.

The deterministic mark-only cases cover normal and synthesized backgrounds,
overlapping category order, chapter descriptions, exact boundaries,
preservation of original tiny chapters, removal of tiny fragments created by
an overlay, adjacent identical markers, validation, immutability, and fuzzed
timeline invariants. They are derived expectations rather than copied upstream
fixtures.

`sample_response.json` is a hand-authored single-group envelope for
video ID `fixture0001` with three segments: a sponsor entry, a
poi_highlight entry, and a whole-video `(0, 0)` marker that must
be discarded. `sample_collision.json` exercises the
"match only the exact videoID group" policy by including two
groups for `fixture-30` and `fixture-210`, which both have the verified
SHA-256 prefix `b200`; the
implementation must select the group whose `videoID` matches the
request. `sample_malformed.json` is a structurally hostile
envelope used to verify the bounded decoder and the categorized
error path.

The video IDs are inert synthetic names. The category and action
type identifiers match the pinned reference table exactly; the
titles and per-segment numbers are deterministic but arbitrary
test data. No cookies, tokens, real video IDs, or production
response capture are present in the fixtures. The fixtures are
consumed without network access, Python, or a clock.

The hash prefixes shown in the fixtures are recomputed by the
test from the SHA-256 of the synthetic video IDs; they are not
stored values. The conformance test in
`internal/sponsorblock` and `pkg/ytdlp` is the authoritative
exerciser; this directory only documents the inputs.
