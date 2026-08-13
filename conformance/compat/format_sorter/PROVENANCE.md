# Format sorter parity provenance

- Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- Reference interpreter: `CPython 3.12.13`
- Recorded: 2026-07-28
- Fixture: `internal/format/testdata/format_sorter_conformance.json` (schema version 1)
- Fixture SHA-256: `0d982bc538ae74e1baec5a19196a020255b54c7603a56d0e9e74ed21613d4656`
- Tests: `internal/format.TestFormatSorterConformance`,
  `internal/format.TestFormatSorterFieldComposition`,
  `internal/format.TestFormatSorterOrderingContract`,
  `internal/format.TestFormatSorterAliasesAndLimits`,
  `internal/format.TestFormatSorterDerivedFields`
- Capture script: `conformance/compat/format_sorter/capture_oracle.py`
- Existing selector ledger: `conformance/compat/format_selector/PROVENANCE.md`
- Existing selector ledger extension: `docs/FORMAT_SELECTOR_PARITY.md`

## Upstream sources

Expectations were transcribed from the pinned checkout at
`/path/to/yt-dlp-reference`:

- `yt_dlp/utils/_utils.py:5367-5666` `FormatSorter` defaults, settings, ordered
  rankings, evaluate_params, calculate_preference, and `_fill_sorting_fields`.
- `yt_dlp/utils/_utils.py:1769-1773` `parse_bytes` byte-size grammar.
- `yt_dlp/utils/_utils.py:1311-1321` `determine_ext` URL-extension derivation.
- `yt_dlp/utils/_utils.py:3190-3205` `determine_protocol` URL-protocol
  derivation.
- `yt_dlp/utils/_utils.py:2029-2038` `int_or_none` scalar coercion (the bounded
  numeric surface used by `_fill_sorting_fields`).
- `yt_dlp/YoutubeDL.py:2996-3028` filter, coercion, sort, and ID-normalization
  ordering used by the selector preparation path.

Capture resets a deep copy of the pinned `FormatSorter.settings` for every
case because the Python implementation stores per-instance sort overrides in
that class-level mapping. Input formats are also deep-copied and tagged only
inside the capture process, preventing derived fields or prior cases from
contaminating fixture inputs and expectations.

## Canonical ownership model

`Prepared.formats` and `Prepared.Info().formats` are stable worst-to-best. The
canonical list indexes (normalized index) reflect this order. The evaluator
adapter in `Prepared.evaluationFormats` reverses the canonical list once so
the existing best-first selector implementation continues to work without
semantic changes. Source indexes (original extractor list position) are kept
alongside the canonical indexes.

`Options.Sort` carries the final ordered user sort list after CLI/config
accumulation and reset processing. Repeated `-S` flags append fields in occurrence order;
`--format-sort-reset` clears previously accumulated user fields. CLI and API
tests verify that `Options.Sort` order is preserved exactly.

`Options.PreferExtensions` is retained for Go API compatibility. It is
applied as an explicit final legacy extension tiebreaker only after the
complete pinned preference tuple compares equal. It must not alter the
pinned oracle when empty.

## Effective field-order composition

The Go sorter implements the pinned first-occurrence-wins composition. With
`Options.SortForce == false` the order is:

1. forced default fields (`hidden`, `aud_or_vid`);
2. priority default fields (`hasvid`, `ie_pref`);
3. user fields from `Options.Sort`;
4. extractor `_format_sort_fields`;
5. full pinned default order.

After alias and combined-field expansion, the first occurrence of each
canonical field wins. Later duplicates are ignored. User field order is
preserved exactly within the user-fields segment.

## Sort specification syntax

Retained and complete:

- `FIELD`
- `+FIELD`
- `FIELD:LIMIT`
- `FIELD~LIMIT`
- combined-field limits such as `ext:video_limit:audio_limit`

Meaning:

- no `+`: larger/preferred values sort later and are therefore better;
- `+`: reverse the field's normal preference;
- `:`: hard-limit preference;
- `~`: closest-to-limit preference.

Rejected with `ErrInvalidPreference`:

- empty fields;
- malformed separators;
- empty limit text;
- trailing junk;
- unknown invalid syntax;
- oversized specifications (greater than 256 UTF-8 bytes per token);
- more than the documented field limits (32 user, 32 extractor, 64
  effective expanded).

## Selector deviations left unchanged

The corpus and ledger retain plain `best` / `worst` semantics, forward `all`
ordering relative to the canonical best-first adapter, negated-operator
evaluation, none-inclusive filters, quoted escaping, field syntax, Go RE2
regexes, separate product multistream policy, interactive `-f -` scope,
multi-output post-processing constraints, and bounded parser and evaluator
limits. The evaluator uses the canonical prepared-format adapter rather than
legacy score-based ordering.

## Updating the baseline

Changes to sorter behavior must update the fixture,
`conformance/compat/format_sorter/PROVENANCE.md`, and the parity ledger in
`docs/FORMAT_SELECTOR_PARITY.md` together. Every fixture entry must execute
and be classified; skipped or environment-dependent cases are not permitted.
New expectations must be captured against an explicit upstream commit
before being committed.
