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
length/count limits. The narrow deviations remain intentional: no arbitrary
Python expressions, no arbitrary template evaluation, and no unbounded regex
or user-code execution. Product/CLI wiring, public option names, and manifest
claims are owned by the integration lane.
