# Phase 2 compatibility-language lane

This lane implements bounded, Python-free compatibility primitives for a Go
product layer to wire into its request/CLI contract.

- `internal/format`: selector alternatives and merges, direct IDs, `all`,
  filters, format preferences, DRM policy, and deterministic ordering.
- `internal/compat/template`: output templates, traversal, defaults,
  replacements, date conversion, numeric formatting, JSON conversion, and
  output-root confinement.
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

- Match filters do not implement yt-dlp's none-inclusive `?` comparison form,
  string negation forms such as `!^=`, field-specific incomplete sets, or its
  filesize/duration text coercions. Supported negation is unary `!field` and
  `!=`/`!~=`.
- Format filters do not implement every yt-dlp selector atom, codec/container
  preference alias, filesize approximation, or advanced sort field conversion.
- Templates do not implement arithmetic, object slicing, Unicode conversion
  variants, arbitrary traversal operators, Python conversions, or arbitrary
  code evaluation. Supported traversal is object keys and numeric list indexes.
- Metadata actions do not execute postprocessor code; they accept only bounded
  regular-expression interpretation and replacement.
- A selector result containing more than one video and one audio stream is
  rejected explicitly; arbitrary `all`-format archival layouts are not yet a
  product download mode.
