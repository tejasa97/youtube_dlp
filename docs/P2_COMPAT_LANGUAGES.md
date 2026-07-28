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
- `internal/compat/chapterremove`: repeatable ordinary chapter-title regular
  expressions and `*start-end` manual cut ranges, compiled before extraction
  and applied through the transactional media/subtitle cut pipeline.

All parsers report byte source spans where syntax is rejected and use explicit
length/count limits. The public `pkg/ytdlp` request contract and CLI now wire
these languages into format choice, metadata mutation, policy skips, output
paths, and structured progress events. Invalid language input is rejected
before extraction or download begins.

Intentional unsupported syntax is explicit rather than silently approximated:

- Match filters implement bounded boolean composition, presence checks, string
  and numeric comparisons, none-inclusive `?` comparisons, string negation,
  incomplete-field handling, yt-dlp-compatible integer, filesize, and
  duration coercion for the documented corpus, and product-level
  `--break-match-filter(s)` queue stopping. Exact `-` markers on ordinary or
  breaking filters prompt only for complete, non-archived entries; empty or
  `y` accepts, `n` skips or stops, and other input reprompts. The prompt uses
  the selected single-output format filename, and format-dependent filter
  fields are reevaluated after selection. Entries without a downloadable
  format fail before prompting. Merged A/V fields follow the pinned merge
  policy. Interactive filtering with multi-output selectors is rejected
  explicitly rather than approximated.
  Automatic subtitle-only listing does not prompt, while explicit simulation
  and combined metadata-output modes do.
  Interactive filtering is rejected with `--progress-json` so its stderr
  stream stays valid JSON. Python regular-expression semantics and unbounded
  expressions remain unsupported.
- Format filters implement the pinned numeric/string operators, none-inclusive
  semantics, SI/IEC values, quoted escapes, and bounded Python-compatible regex
  search. FormatSorter implements the pinned field composition, aliases,
  codec/container/HDR/protocol/language rankings, exact and approximate
  filesize, derived values, limits, and stable mixed-type ordering. Plain
  `best`/`worst`, `all`, defaults, and multistream suppression still use the
  pre-PR-5 evaluator behavior; product execution still rejects unsupported
  track layouts before media transfer.
- Templates implement bounded arithmetic and Unicode `U` conversions
  (including `#`/`+` flags), but do not implement object slicing, arbitrary
  traversal operators, the wider Python format mini-language, or arbitrary
  code evaluation. Supported traversal covers object keys, numeric list and
  string indexes and slices, list mapping through `:`, and object projections.
- Metadata actions do not execute postprocessor code; they accept only bounded
  regular-expression interpretation and replacement.
- Chapter removal uses search semantics, repeatable expressions, open or
  finite non-negative ranges, `inf`/`infinite`, and the pinned duration forms.
  It merges ordinary, manual, and SponsorBlock removals before one ffmpeg cut;
  `--no-remove-chapters` resets inherited rules. Expressions use Go's bounded
  RE2 syntax, so Python-only look-around and backreferences remain explicit
  unsupported syntax. Infinite starts and equal/inverted ranges are rejected
  up front; upstream accepts these degenerate forms initially even though they
  cannot produce a positive cut. Before mutation, the downloaded media is
  probed, an open final chapter is completed from its real duration, and a
  greater-than-one-second metadata mismatch fails closed.
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
  no-postprocessor path is executed. Interactive match filtering also rejects
  multi-output plans because per-output prompting is not yet represented.
  After-download print stages render only
  the first plan's selections and primary path. `mergeall` and >2-track merges
  remain explicit unsupported at download time.
- `ErrSelectorLimit` is returned for syntactically valid selectors that exceed
  bounded evaluation limits (`all`, `mergeall`, comma output count, merge
  track count). Product callers should use `errors.Is(err, format.ErrSelectorLimit)`
  and expect `ytdlp.ErrorInvalidInput` categorization.
