# Phase 2 compatibility-language lane

This lane implements bounded, Python-free compatibility primitives for a Go
product layer to wire into its request/CLI contract.

- `internal/format`: selector alternatives and merges, direct IDs, `all`,
  filters, format preferences, DRM policy, deterministic ordering, and the
  pinned best/worst atom slice (`b`/`w`/`bv`/`ba`/long forms, optional `*`,
  and one-based `.N` indexing).
- `internal/compat/template`: output templates, traversal, defaults,
  replacements, bounded arithmetic, date and Unicode conversion, numeric
  formatting, JSON conversion, and output-root confinement.
- `internal/compat/progress`: deterministic progress-template namespaces.
- `internal/compat/metadata`: parse-metadata and replace-in-metadata actions.
- `internal/compat/matchfilter`: declarative OR/AND matching and a distinct
  rejection decision (not an extraction error).

All parsers report byte source spans where syntax is rejected and use explicit
length/count limits. The public `pkg/ytdlp` request contract and CLI now wire
these languages into format choice, metadata mutation, policy skips, output
paths, and structured progress events. Invalid language input is rejected
before extraction or download begins.

Intentional unsupported syntax is explicit rather than silently approximated:

- Match filters implement bounded boolean composition, presence checks, string
  and numeric comparisons, none-inclusive `?` comparisons, string negation,
  incomplete-field handling, and yt-dlp-compatible integer, filesize, and
  duration coercion for the documented corpus. Interactive/break filters,
  Python regular-expression semantics, and unbounded expressions remain
  unsupported.
- Format selectors implement the checked-in best/worst atom slice above, plus
  `/` fallback, `+` merge, direct IDs, `all`, and filters. They do not
  implement groups/parentheses, comma/multi-output, `mergeall`,
  extension-name selectors, interactive filters, codec/container preference
  aliases beyond the documented preference options, filesize approximation, or
  advanced sort field conversion outside `Options`.
- Templates implement bounded arithmetic and Unicode `U` conversions
  (including `#`/`+` flags), but do not implement object slicing, arbitrary
  traversal operators, the wider Python format mini-language, or arbitrary
  code evaluation. Supported traversal covers object keys, numeric list and
  string indexes and slices, list mapping through `:`, and object projections.
- Metadata actions do not execute postprocessor code; they accept only bounded
  regular-expression interpretation and replacement.
- A selector result containing more than one video and one audio stream is
  rejected explicitly; arbitrary `all`-format archival layouts are not yet a
  product download mode.
