# Format-selector and normalization provenance

- Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- Fixture derivation interpreter: `CPython 3.9.6`
- Recorded: 2026-07-25; normalization evidence corrected 2026-07-28
- Corpus: `internal/format/testdata/selector_conformance.json` (schema version 1)
- Tests: `internal/format.TestSelectorConformanceCorpus`
- Gap ledger: `docs/FORMAT_SELECTOR_PARITY.md`

## Upstream sources and exact order

Expectations were transcribed from the pinned checkout at
`/Users/tejas/projects/yt-dlp-reference`:

- `yt_dlp/YoutubeDL.py:2577-2651`: selector construction/evaluation, atoms,
  extension priority, filters, `all`, and merge behavior.
- `yt_dlp/YoutubeDL.py:2923-3028`: collection filtering, coercion, sorting, ID
  normalization, duplicate suffixing, and extension conflicts.
- `yt_dlp/YoutubeDL.py:3067-3089`: selection after normalization.
- `yt_dlp/YoutubeDL.py:600-609`: `_NUMERIC_FIELDS`.
- `yt_dlp/utils/_utils.py:2029-2038`: `int_or_none` coercion.
- `yt_dlp/utils/_utils.py:5367-5666`: `FormatSorter` conversion and ordering.
- `yt_dlp/utils/_utils.py:5114-5122`: media-extension categories.
- `test/test_YoutubeDL.py` `TestFormatSelection`: selector traps.

The pinned pre-selection sequence is authoritative:

1. remove disallowed DRM formats unless unplayable formats are allowed;
2. remove formats whose URL is missing or empty;
3. coerce `format_id` to a string and applicable numeric fields to numeric/null
   values;
4. fill sorting fields and sort the surviving formats;
5. assign missing IDs from indexes in that filtered, sorted list;
6. replace exactly the characters matched by the pinned
   `re.sub(r'[\s,/+\[\]()]', '_', ...)` rule; NUL and other non-whitespace
   control characters remain unchanged;
7. suffix every member of a duplicate group with a zero-based ordinal;
8. rewrite extension-selector conflicts, then select.

The rewrite is one-pass and can leave final duplicate IDs. Original extractor
list indexes are retained separately from canonical indexes so filtering and
sorting do not lose source provenance.

## Capture and sanitization

The corpus is a manually reviewed synthetic transcription, serialized during
fixture maintenance with CPython 3.9.6. IDs, URLs, headers, codec strings, and
metadata use `example.invalid` or non-secret placeholders. It contains no
public-site payloads, identifiers, cookies, credentials, or tokens. Production
and Go tests read committed JSON only; they never invoke Python, access the
reference checkout, or use the network.

Every case has a unique ID, feature tags, selector/options, expected canonical
formats, expected plans or error, and a parity classification. Gaps contain a
reason and explicit pinned expectation; there is no skip mechanism.

## Canonical Go ownership model

Go recursively clones the extractor `Info` and formats. The canonical clone is
shared by selector evaluation, format tables, print templates, simulated and
skipped results, related metadata output, and `InfoJSON`. Selection is evaluated
against the already-prepared view rather than normalizing a second time.
Extractor-owned metadata remains unchanged.

Filtering occurs before sorting and generated IDs. Fixtures `drm.*` and
`url-filter.*` prove that rejected formats do not consume generated indexes and
cannot be recovered through `all` or direct-ID selectors. Selection records both
its original extractor index and canonical list index, keeping metadata and
headers attached to the exact format even after filtering and residual ID
collisions.

Pinned scalar coercion covers non-string `format_id` values and numeric fields
used by preparation/sorting. Numeric IDs are parity cases, not safety gaps.
Structured values that cannot be represented by the bounded typed metadata
contract still fail predictably.

The Go boundary retains explicit resource limits:

- 4,096 extractor-supplied entries before filtering;
- 16 KiB per input or final `format_id`;
- 4 MiB aggregate final ID bytes.

`ErrInvalidFormats` and `ErrFormatLimit` are categorized as internal extractor
metadata failures. These resource bounds and defensive ownership differ
intentionally from pinned Python.

Normalization uses the selector's existing Go extension maps. Final uniqueness
is not strengthened: residual `x,x,x-0` and `mp4,fmp4` collisions remain pinned
and are tested with exact source/header association.

## Selector deviations left unchanged

This PR does not change selector algorithms. The corpus and ledger retain plain
`best`/`worst` semantics, forward `all` ordering, unsupported negated and
none-inclusive filters, limited quoted escaping and field names, Go RE2 regexes,
incomplete sort aliases/limits, separate product multistream policy, interactive
`-f -` scope, multi-output post-processing constraints, and bounded parser and
evaluator limits.

Fixture or implementation updates must keep this provenance, corpus, ledger,
and parity manifest synchronized against an explicit reference revision.
