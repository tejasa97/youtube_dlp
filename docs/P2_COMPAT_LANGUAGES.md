# Phase 2 compatibility-language lane

This lane implements bounded, Python-free compatibility primitives for a Go
product layer to wire into its request/CLI contract.

- `internal/format`: bounded AST selector (`ParseSelector`, `PlanSelect`,
  `OutputPlan`), comma/slash/plus/group operators, pinned atoms and extension
  selectors, filters, preferences, DRM policy, and deterministic ordering.
  Legacy `Select`/`SelectWithOptions` remain for single-output merges but return
  `ErrMultiOutput` when a comma/`all` plan cannot be flattened.
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
- Format filters do not implement every yt-dlp selector atom, codec/container
  preference alias, filesize approximation, or advanced sort field conversion.
  Plain `best`/`worst` without a media type keep the port's historical
  quality-first playable selection for pinned compatibility fixtures; typed
  `bestvideo`/`bestaudio`/star atoms follow yt-dlp predicates.
- Templates implement bounded arithmetic and Unicode `U` conversions
  (including `#`/`+` flags), but do not implement object slicing, arbitrary
  traversal operators, the wider Python format mini-language, or arbitrary
  code evaluation. Supported traversal covers object keys, numeric list and
  string indexes and slices, list mapping through `:`, and object projections.
- Metadata actions do not execute postprocessor code; they accept only bounded
  regular-expression interpretation and replacement.
- A selector result containing more than one video and one audio stream is
  rejected explicitly when tracks cannot be merged. Merge operands are retained
  in evaluation order with only exact duplicate `(format_id, url)` pairs removed;
  unsupported multi-track plans fail before media download. Comma/`all`
  multi-output downloads use per-plan container extensions (single-track `Ext`
  or deterministic video+audio merge rules) and deterministic `.f<N>_<ID>`
  destination suffixes (stable one-based ordinals plus sanitized IDs), populate
  `Result.Artifacts`, and keep `Result.Filename` on the first output. Multi-output downloads reject any
  non-empty `Request.Postprocessors`, SponsorBlock remove, and subtitle
  embedding with `ErrMultiOutput` before media download; only the
  no-postprocessor path is executed. After-download print stages render only
  the first plan's selections and primary path. `mergeall` and >2-track merges
  remain explicit unsupported at download time.
- `ErrSelectorLimit` is returned for syntactically valid selectors that exceed
  bounded evaluation limits (`all`, `mergeall`, comma output count, merge
  track count). Product callers should use `errors.Is(err, format.ErrSelectorLimit)`
  and expect `ytdlp.ErrorInvalidInput` categorization.
