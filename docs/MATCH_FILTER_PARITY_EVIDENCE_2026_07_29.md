# Match-filter parity evidence — 2026-07-29

Pinned reference: `yt-dlp/yt-dlp` at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

`internal/compat/matchfilter` implements the normal valid match-filter and
break-match-filter language: repeated filters are ORed, unescaped ampersands
AND conditions, comparison/unary operators have missing and incomplete-field
semantics, and numeric values accept the pinned integer/filesize/duration
coercions. Product tests cover format-dependent fields, playlist stopping,
and interactive prompt ordering.

Regex comparisons use bounded Python-compatible search semantics for ordinary
patterns, including look-around, numeric/named backreferences, and inline
flags. The implementation uses `regexp2` with a 512-byte pattern limit,
64-KiB input limit, 25-ms individual timeout, and 250-ms aggregate evaluation
budget. Timeout and budget errors are sanitized and fail closed; no input is
included in an error. Python-only syntax outside the supported `regexp2`
surface, malformed metadata, and inputs over these bounds remain deliberate
product deviations.

Evidence is self-contained in
`conformance/matchfilter-parity-2026-07-29/`, plus unit, fuzz, product, and
CLI tests. No Python runtime is part of verification.

Resolved by the parity-contract reconciliation patch:

- `docs/P2_COMPAT_LANGUAGES.md` now describes the bounded
  Python-compatible subset translated by `internal/compat/pyregex.Translate`
  and the per-source, per-translated, per-input, per-match attempts,
  per-match wall time, and aggregate wall-time budgets that re-bound it.
- `conformance/parity_manifest.yaml` `compat.languages_principal` now lists
  this evidence document and the tests
  `internal/compat/matchfilter.TestPinnedMatchFilterConformance`,
  `internal/compat/matchfilter.TestEvaluationCancellationAndBounds`, and
  the corresponding chapter-removal tests under
  `postprocess.chapter_removal`.
