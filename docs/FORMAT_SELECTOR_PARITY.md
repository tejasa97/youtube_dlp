# Format selector parity

The authoritative selector and format-normalization baseline is
`internal/format/testdata/selector_conformance.json`. It is pinned to
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`; production tests consume
only the committed JSON and do not invoke Python or access the network.

`internal/format.TestSelectorConformanceCorpus` validates the fixture schema,
provenance, unique case IDs, safety limits, parity classification, normalized
format objects, output-plan order, source associations, headers, and errors.
Normalization-specific invariants and concurrency coverage live in
`internal/format/normalize_test.go`; the product rendering boundary is covered
by `pkg/ytdlp/format_normalization_test.go`.

## Parser contract

Selector parsing is bounded to 16 KiB and produces a source-spanned AST before
any extractor or network work begins. Error offsets are half-open byte ranges
into the original selector; leading and trailing whitespace is not discarded
before offsets are computed.

The grammar precedence, from lowest to highest, is comma-separated outputs,
slash fallbacks, plus merges, and atoms/groups with attached filters. Filter
boundaries are quote-aware; backslash escapes apply only inside quoted filter
values, matching pinned CPython string tokens. Direct IDs accept the punctuation
surface emitted by the pinned tokenizer while reserving selector operators.
NAME, NUMBER, and non-structural OP tokens are joined the same way as pinned
`_remove_unused_ops`, including whitespace-separated forms (`best video` →
`bestvideo`, `best . 2` → `best.2`) and multi-character OP tokens such as
`//` (`best//` is a direct ID). Trailing commas and trailing slashes are
accepted when they follow a selector (`best,`, `best/`, `(best/)`), while empty
comma branches (`,best`, `best,,`, `(,best)`, `(best,,)`) and dangling plus
(`best+`) remain syntax errors. Empty groups (`()`) parse and evaluate to no
match. Positive `.N` atom indexes have no artificial 1,000 limit; indexes too
large for the host integer are valid syntax and deterministically produce no
match. Alias-looking strings that fail the exact quality-atom regex fall back
to direct IDs (`best*foo`, `best.01`, `best.0`, `best.`).

Direct IDs intentionally reject comment (`#`) and string/line-continuation
punctuation (`\`, `'`, `"`) that the pinned Python tokenizer comments away or
token-errors on. Comment discard can rewrite a requested ID; the Go parser fails
the selector instead. Single-character OP tokens such as `!`, `$`, `?`, and
`` ` `` are retained and joined into direct IDs. Unicode numbers outside ASCII
digits (for example `²` and Roman numerals) are accepted as in pinned NAME
tokens. Extension-atom recognition uses the exact pinned `_format_selection_exts`
set; tokens outside that set parse as direct IDs. Negated filter operators
(`!^=`, `!$=`, `!*=`, `!~=`) parse and evaluate with pinned none-inclusive and
missing-field semantics. Regex filters use a bounded Python-`re` compatibility
adapter over `regexp2` (search semantics, named groups/backrefs, look-around,
conditionals, scoped/global flags, Python Unicode classes/names/case folding,
atomic groups, and possessive quantifiers). The adapter caps source patterns at
4 KiB, translated patterns at 16 KiB, capture groups and nesting at 64, regex
predicates at 32 per selector AST, and input strings at 64 KiB. A selection plan
also has aggregate attempt, inspected-byte, wall-time, and per-match timeout
budgets. Resource exhaustion returns `ErrSelectorLimit`; it is never treated as
a non-match. The complete Unicode 15.0.0 name/alias table is embedded at build
time, so production and tests do not invoke Python or access the network.

## Normalization contract

The Go preparation stage deep-clones extractor formats, applies the existing
stable format order, then performs the pinned ID transformation before
selection:

1. Remove disallowed DRM formats and formats with missing or empty URLs.
2. Coerce scalar `format_id` values and exact `_NUMERIC_FIELDS` entries via
   `int_or_none`-compatible conversion (int64-bounded). Sorter preference fields
   are not mutated during preparation.
3. Stable-sort the surviving formats.
4. Assign missing, null, and empty IDs from the filtered post-sort index.
5. Replace each Python `re` `\s` whitespace code point (including U+001C–U+001F)
   and each `, / + [ ] ( )` with `_`.
6. Give every member of a duplicate group `-0`, `-1`, and so on.
7. Prefix `f` when an ID conflicts with a recognized extension selector and its
   own extension differs.

The resulting defensive `Info` is the single canonical view used by selection,
format tables, prints, simulated and skipped results, and `InfoJSON`.

## FormatSorter contract

Canonical formats are sorted worst-to-best using the pinned `FormatSorter`
from `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` under CPython
3.12.13. The dedicated 38-case fixture is
`internal/format/testdata/format_sorter_conformance.json` (SHA-256
`20b24919c583ac7c0885ea2ad333ddebd4ce082537dc9816470aa745a8b56d5a`),
captured with:

```sh
/Users/tejas/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3 \
  conformance/compat/format_sorter/capture_oracle.py --write
```

The effective field order follows pinned forced, priority, user, extractor,
and default composition with first occurrence winning after aliases and
combined fields are expanded. Ordered codec, HDR, protocol, and extension
rankings, limits, mixed scalar classes, free-format ordering, and observable
derived sorting fields are covered by the fixture. `Prepared.formats` and
`Prepared.Info().formats` retain canonical worst-to-best order; a fresh,
pointer-preserving reversal adapter supplies the existing best-first evaluator
without mutating canonical state.

`Options.PreferExtensions` remains a Go-only compatibility tiebreaker after
otherwise equal pinned tuples. CLI format-sort reset wiring remains deferred to
PR 9, and replacement of the evaluator/default-selector scoring path remains
deferred to PR 5.

The transformation intentionally remains one-pass, matching the pinned Python
behavior. It can therefore leave collisions such as `x,x,x-0 -> x-0,x-1,x-0`
or `mp4,fmp4 -> fmp4,fmp4`. Selection carries the original extractor-list
index, so headers and descriptive metadata remain attached to the exact source
format despite those collisions.

Go deliberately differs from Python by never mutating extractor-owned metadata
and by rejecting malformed or excessive collections with bounded sentinels.
The limits are 4,096 formats, 16 KiB per input/final ID, and 4 MiB total final
ID bytes.

## Gap ledger

| ID | Surface | Pinned behavior | Go behavior | Fixture | Status | Decision |
|---|---|---|---|---|---|---|
| `atom.plain-best-combined` | `best` / `worst` | Plain atoms require/score the pinned combined-format universe. | Historical playable-universe scoring remains. | `gap.plain-best-combined` | Known gap | Do not change selection algorithms in this PR. |
| `operator.all-order` | `all` | Emits the ordered candidate list in reverse. | Preserves forward extractor order. | `gap.all-order` | Intentional deviation | Keep deterministic Go order until the selector algorithm parity phase. |
| `filter.negated-string` | `!^=`, `!$=`, `!*=`, `!~=` | Parses and evaluates negated string operators. | Parses and evaluates with pinned missing/`?` semantics. | `filter.negated-prefix`, filter oracle | Passing parity | Closed in the filter-parity phase. |
| `filter.none-inclusive` | `?` missing-value modifier | Includes missing metadata according to the operator. | Missing/null pass only when `?` is present. | `filter.none-inclusive`, filter oracle | Passing parity | Closed in the filter-parity phase. |
| `filter.quoted-escapes` | Quoted filter values | Unescapes only `\\`, `\"`, and `\'` for non-regex values. | Matches pinned unescape rules; regex values keep capture escapes. | `filter.quoted-escape`, filter oracle | Passing parity | Closed in the filter-parity phase. |
| `filter.field-syntax` | Filter field names | Numeric keys allow Unicode `\w` plus `.`/`-`; string keys are ASCII. | Matches pinned key grammars; grammar selection is from filter text. | `filter.field-syntax`, filter oracle | Passing parity | Closed in the filter-parity phase. |
| `filter.regex-engine` | `~=` | Uses Python regular expressions (search semantics). | Bounded Python-`re` adapter over `regexp2` default mode with translation/validation and resource limits. | `filter.regex`, python regex oracle | Passing parity | Timeouts/budgets return `ErrSelectorLimit`, never silent false. |
| `sort.conversion` | Sort aliases, combined fields, rankings, and limits | Implements pinned `FormatSorter` composition, conversion, ordered rankings, limits, and derived fields. | Matches the dedicated pinned sorter corpus; `PreferExtensions` remains a documented Go-only final tiebreaker. | `format_sorter_conformance.json`, `TestFormatSorterConformance` | Passing parity | Keep evaluator algorithm replacement and CLI reset wiring in their designated later PRs. |
| `extension.exact-recognition` | Bare extension atoms | Pinned `_format_selection_exts` per `yt_dlp/utils/_utils.py`. | Parser recognizes the exact pinned media-extension set. | `parser.extension-boundary-direct-id`, extension-tagged corpus cases | Passing parity | The Go extension map is now byte-for-byte the pinned selection set; tokens outside the set parse as direct IDs. |
| `direct-id.discarded-punctuation` | Direct-format IDs containing `# \ ' "` | Pinned Python comments `#...` away or token-errors on `\ ' "`. | Parser rejects the token with a syntax error. | `parser.direct-id-discarded-punctuation` | Deliberate safety gap for `#`; parity rejection for `\ ' "` | Failing closed avoids silently selecting a different ID for comments. |
| `media.storyboard` | `mhtml` | Selects storyboard formats. | Supported and pinned in the corpus. | `extension.storyboard` | Closed | Guard against playable-universe regressions. |
| `product.multistream-policy` | Same-kind merged tracks | Product defaults constrain multiple video/audio streams. | Evaluator retains distinct tracks; unsupported downloader layouts fail later. | `gap.multistream-product` | Product unsupported | Keep evaluator and product-policy responsibilities separate. |
| `product.interactive-selector` | `-f -` | Prompts interactively per video. | `-` is not an interactive selector surface. | Provenance only | Product unsupported | Outside the library/fixture scope. |
| `product.multi-output-postprocess` | Multiple independent outputs | Upstream supports broader post-processing combinations. | Some postprocessors fail closed for multi-output plans. | Existing product tests | Product unsupported | Preserve explicit safety checks. |
| `bounds.collection` | Extractor format collections | No explicit format-count or ID-size bounds. | Returns `ErrFormatLimit`. | `limit.format-count`, `limit.id-bytes`, `limit.aggregate-id-bytes` | Deliberate safety gap | Retain deterministic resource limits. |
| `bounds.selector-evaluation` | Large selector results | Broader/unbounded behavior. | Parser/evaluator cap AST depth, filters, merge terms, products, and outputs. | `limit.output-count` and parser-limit tests | Deliberate safety gap | Retain bounded evaluation. |
| `metadata.mutation` | Processing ownership | Python mutates retained format dictionaries. | Go recursively clones formats and preserves the original `value.Info`. | Normalization and product tests | Deliberate safety gap | Nonmutation is a required API guarantee. |
| `normalization.pre-filter` | DRM and malformed URLs | Removes disallowed DRM and missing/empty-URL formats before sorting and IDs. | Matches pinned order while retaining original source indexes. | `drm.*`, `url-filter.*` | Passing parity | Guard `all`, direct IDs, and generated indexes. |
| `normalization.scalar-coercion` | `format_id` and `_NUMERIC_FIELDS` | Coerces non-string IDs and `_NUMERIC_FIELDS` before sorting; preference fields stay untouched. | Bounded scalar coercion matches pinned cases; string `preference` is retained. | `coercion.numeric-id`, `int_or_none` oracle | Passing parity | Numeric IDs are not a safety gap; preference coercion is not claimed. |
| `product.canonical-info` | Selection/listing/printing/results | All post-normalization consumers see the processed formats. | A single defensive prepared `Info` feeds selection, tables, prints, simulate/skip results, and `InfoJSON`. | `TestFormatNormalizationCanonicalAcrossProductSurfaces` | Closed | Do not independently normalize product surfaces. |
| `metadata.malformed` | Invalid collection/member/structured-ID shapes | May fail later with a runtime exception or arbitrary stringification. | Invalid collections and unsupported structured IDs return `ErrInvalidFormats`. | `malformed.*` | Deliberate safety gap | Keep bounded typed validation while coercing pinned scalar IDs. |
| `normalization.residual-collision` | Duplicate/extension rewrite | One pass can leave duplicate final IDs. | Matches the one-pass result while carrying source identity separately. | `normalize.duplicates`, `normalize.extension-conflict` | Passing parity | Preserve until the pinned baseline changes. |

## Updating the baseline

Changes to selector or normalization behavior must update the fixture,
`conformance/compat/format_selector/PROVENANCE.md`, and this ledger together.
Every fixture entry must execute and be classified; skipped or environment-
dependent cases are not permitted. New expectations must be captured against an
explicit upstream commit before being committed.
