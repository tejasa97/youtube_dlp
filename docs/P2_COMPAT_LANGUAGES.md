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
  each selected output plan's filename in stable order, and format-dependent
  filter fields are reevaluated after selection. Entries without a
  downloadable format fail before prompting. Merged A/V fields follow the
  pinned merge policy.
  Automatic subtitle-only listing does not prompt, while explicit simulation
  and combined metadata-output modes do.
  Interactive filtering is rejected with `--progress-json` so its stderr
  stream stays valid JSON. Match-filter regex uses the bounded
  Python-compatible subset translated by `internal/compat/pyregex.Translate`
  and compiled with `regexp2`
  (`internal/compat/matchfilter/filter.go::compilePythonRegex`). Open
  quantifiers compile; safety is enforced at execution time by source,
  translated, and per-input byte limits (`maxRegexBytes=512`,
  `maxTranslatedRegexBytes=16<<10`, `maxRegexInputBytes=64<<10`), per-match
  attempts (`maxRegexAttempts=256`), aggregate inspected bytes
  (`maxRegexInspectedBytes=4<<20`), per-match wall time
  (`regexp2.MatchTimeout=regexMatchTimeout=25ms`), and aggregate wall time
  (`regexWallBudget=250ms`, enforced in
  `internal/compat/matchfilter/filter.go::searchPythonRegex`). Patterns that
  exceed the source or translated byte budget, or that fail translation,
  are rejected at parse/compile time; runtime-matched patterns are re-bounded
  by the same per-input, per-match, and aggregate budgets. The supported
  surface is the bounded Python-compatible subset translated by
  `internal/compat/pyregex`: anchors, character classes, repetition,
  alternation, lookahead and lookbehind, and bounded backreferences. Other
  Python regular expression features remain unsupported.
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
  `--no-remove-chapters` resets inherited rules. Chapter-removal regex uses
  the same `internal/compat/pyregex.Translate` adapter as match filters
  (`internal/compat/chapterremove/program.go::Parse`).
  Per-title matching is re-bounded at execution time by source and
  translated byte limits (`MaxRegexSourceBytes=4096`,
  `MaxRegexTranslatedBytes=16<<10`), per-input byte limit
  (`MaxRegexInputBytes=64<<10`), per-match attempts and aggregate inspected
  bytes (`MaxRegexAttempts=256`, `MaxRegexInspectedBytes=4<<20`),
  per-match wall time (`regexp2.MatchTimeout=RegexMatchTimeout=25ms`), and
  the chapter-remove program's own aggregate `EvaluationBudget.attempts`,
  `inspectedBytes`, and `wall` (`RegexAggregateWallBudget=250ms`,
  `internal/compat/chapterremove/program.go::MatchTitleWithBudget`).
  Product code shares one `EvaluationBudget` across an entire media item's
  chapters so the aggregate wall time applies to the whole item rather
  than per title. References:
  `internal/compat/chapterremove/program.go::Parse`,
  `internal/compat/chapterremove/program.go::MatchTitleWithBudget`,
  `internal/compat/chapterremove/program_test.go::TestMatchTitleBoundsInputAndAggregateWork`. Infinite starts and equal/inverted ranges are rejected
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
  `Result.Artifacts`, and keep `Result.Filename` on the first output. Each plan
  independently runs sidecars, downloads, postprocessors, chapter/SponsorBlock
  cuts, subtitle and thumbnail embedding, and staged prints. Deterministic
  destinations are preflighted across plans; fixed postprocessor destinations
  that cannot be plan-specific fail before download. `mergeall` and retained
  N-track plans execute through the bounded N-track pipeline.
- `ErrSelectorLimit` is returned for syntactically valid selectors that exceed
  bounded evaluation limits (`all`, `mergeall`, comma output count, merge
  track count). Product callers should use `errors.Is(err, format.ErrSelectorLimit)`
  and expect `ytdlp.ErrorInvalidInput` categorization.
