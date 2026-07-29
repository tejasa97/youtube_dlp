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

Integration follow-up for the owning integrator:

- In `docs/P2_COMPAT_LANGUAGES.md`, replace the sentence saying match-filter
  Python regular-expression semantics are unsupported with this document's
  bounded Python-compatible regex contract.
- In `conformance/parity_manifest.yaml`, update only the match-filter
  capability/deviation entry to point at this evidence and retain the stated
  resource bounds.
