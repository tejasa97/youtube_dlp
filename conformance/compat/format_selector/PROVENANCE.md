# Format-selector and normalization provenance

- Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- Fixture derivation interpreter: `CPython 3.12.13`; the filter/regex and
  Unicode-name generators require that exact version.
- Recorded: 2026-07-25; normalization evidence corrected 2026-07-28; parser
  parity evidence expanded 2026-07-28; filter/regex oracle captured 2026-07-28
- Corpus: `internal/format/testdata/selector_conformance.json` (schema version 1)
- `int_or_none` oracle: `internal/format/testdata/int_or_none_oracle.json`
- Filter oracle: `internal/format/testdata/filter_oracle.json`
- Python-regex oracle: `internal/format/testdata/python_regex_oracle.json`
- Tests: `internal/format.TestSelectorConformanceCorpus`,
  `internal/format.TestIntOrNoneOracleFixture`,
  `internal/format.TestFilterOracleFixture`,
  `internal/format.TestPythonRegexOracleFixture`,
  `internal/format.TestParserParity*`,
  `internal/format.TestFilter*`,
  `internal/format.TestPythonRegex*`,
  `pkg/ytdlp.TestFormatSelectorInvalidSyntaxFailsBeforeExtraction`
- Gap ledger: `docs/FORMAT_SELECTOR_PARITY.md`

## Fixture hashes

Computed over the committed UTF-8 file bytes:

| Artifact | SHA-256 |
|---|---|
| `internal/format/testdata/selector_conformance.json` | `5b933cf6a6380f2d71a26ed941cf5b38033548d4ea690100d03f0ac82de2e58d` |
| `internal/format/testdata/int_or_none_oracle.json` | `a3f1af159f326f2d5f7e50825f3fa18eb061291a93da8d2f4f245abf389f3418` |
| `internal/format/testdata/filter_oracle.json` | `5a3ea78f8825847adcb5798b023cff7f8b635f6c4cf1a3ed3163e6720468119a` |
| `internal/format/testdata/python_regex_oracle.json` | `7f2c2fb5016e3459f9ab7ac99ea3c6b71bef82e1cbae4a32f9d60962c0ddd51d` |
| `internal/format/unicode_names.bin` | `0a76d5792d895a7054d63c8789f0fb79790b58608cbb0bc18426a236c84cf2de` |

## Maintainer-only capture

Go tests, builds, Docker images, and production remain Python-free. Maintainers
regenerate oracles with a supported interpreter against the pinned checkout:

```bash
python3 conformance/compat/format_selector/capture_oracle.py \
  --reference /Users/tejas/projects/yt-dlp-reference \
  --commit aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8 \
  --write

python3 \
  conformance/compat/format_selector/capture_filter_oracle.py \
  --reference /path/to/pinned/yt-dlp \
  --commit aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8 \
  --write

python3 conformance/compat/format_selector/generate_unicode_names.py \
  --aliases /path/to/Unicode-15.0.0/NameAliases.txt \
  --write
```

The filter/regex and Unicode-name commands require CPython 3.12.13 with Unicode
15.0.0. The Unicode generator verifies the pinned NameAliases.txt SHA-256 before
writing the deterministic embedded artifact. Go tests and production do not
invoke either script or read the network.

## Upstream sources and exact order

The committed expectations were transcribed from the pinned checkout at
`/Users/tejas/projects/yt-dlp-reference`:

- `yt_dlp/YoutubeDL.py:2577-2651`: selector construction/evaluation, atom
  predicates, extension priority, filters, `all`, and merge behavior.
- `yt_dlp/YoutubeDL.py:2208-2270`: exact numeric/string filter grammar,
  missing-value handling, operators, quoting, and Python-regex compilation.
- `yt_dlp/utils/_utils.py:1756-1848`: `lookup_unit_table` and complete SI/IEC
  filesize units through YB/YiB, including float multiplication and
  round-half-to-even.
- CPython 3.12.13 `re` search/compile behavior supplies the regex oracle,
  including Unicode 15.0.0 names and aliases, flags, classes, lookaround,
  conditionals, backreferences, atomic groups, and possessive quantifiers.
- `yt_dlp/YoutubeDL.py:2923-3028`: format collection preparation, stable sorting,
  generated IDs, unsafe-character replacement, duplicate suffixing, and
  extension-selector conflict handling.
- `yt_dlp/YoutubeDL.py:3067-3089`: selection after normalization.
- `yt_dlp/utils/_utils.py:5114-5122`: media-extension categories used to build
  extension selector atoms.
- `test/test_YoutubeDL.py` `TestFormatSelection`: selector and error traps.

The pinned pre-selection sequence is authoritative:

1. remove disallowed DRM formats unless unplayable formats are allowed;
2. remove formats whose URL is missing or empty;
3. coerce `format_id` to a string and `_NUMERIC_FIELDS` values to numeric/null
   (`preference`, `language_preference`, `quality`, and `source_preference` are
   not sanitized here; sorter conversion is non-mutating);
4. fill sorting fields and sort the surviving formats;
5. assign missing IDs from indexes in that filtered, sorted list;
6. replace exactly the characters matched by the pinned
   `re.sub(r'[\s,/+\[\]()]', '_', ...)` rule, including U+001C–U+001F;
   NUL and other non-whitespace control characters remain unchanged;
7. suffix every member of a duplicate group with a zero-based ordinal;
8. rewrite extension-selector conflicts, then select.

The rewrite is one-pass and can leave final duplicate IDs. Original extractor
list indexes are retained separately from canonical indexes so filtering and
sorting do not lose source provenance.

## Capture and sanitization

The selector corpus is a manually reviewed synthetic transcription. The
`int_or_none` oracle is generated by the maintainer capture command above using
CPython 3.12.13. IDs, URLs, headers, codec strings, and metadata use
`example.invalid` or non-secret placeholders. Fixtures contain no public-site
payloads, identifiers, cookies, credentials, or tokens. Production and Go tests
read committed JSON only; they never invoke Python, access the reference
checkout, or use the network.

Every selector case has a unique ID, feature tags, selector/options, expected
canonical formats, expected plans or error, and a parity classification.
Parser-error cases may additionally record half-open byte offsets into the
original, untrimmed selector. Gaps contain a reason and explicit pinned
expectation; there is no skip mechanism.

The parser corpus also transcribes the selector grammar and examples documented
in the pinned README: comma outputs bind least tightly, slash fallbacks bind
above comma, plus merges bind above slash, parentheses group expressions, and
bracket filters attach to the immediately preceding atom or group. Filter
brackets are quote-aware; backslash escapes apply only inside quoted values so
`]` inside a quoted string does not terminate the filter, while an unquoted
`\]` does not suppress `]` and remains invalid upstream syntax.

Go recursively clones the extractor `Info` and formats. The canonical clone is
shared by selector evaluation, format tables, print templates, simulated and
skipped results, related metadata output, and `InfoJSON`. For implicit
top-level formats, the prepared format object shares `Info.Fields()` identity so
post-prepare metadata mutations remain coherent with selection. After metadata
actions or deferred enrichment, product selection rebinds prepared format
objects to the current canonical `Info` without re-normalizing. Extractor-owned
metadata remains unchanged.

Pinned Python processing mutates the supplied format dictionaries. The Go port
instead recursively clones every accepted format before sorting or
normalization. A private source association carries the original extractor-list
index through selection so product rendering can merge descriptive metadata
from the exact original object without relying on final ID uniqueness.

Pinned scalar coercion covers non-string `format_id` values and
`_NUMERIC_FIELDS` used by preparation. Numeric IDs are parity cases, not safety
gaps. String `preference` values are retained rather than coerced. Structured
values that cannot be represented by the bounded typed metadata contract still
fail predictably. `int_or_none` accepts underscore-separated ASCII integers and
Unicode decimal digits within int64 typed safety bounds.

The Go boundary rejects malformed collections with `ErrInvalidFormats` and
bounds extractor-controlled work with `ErrFormatLimit`:

- 4,096 format entries;
- 16 KiB per input or final `format_id`;
- 4 MiB aggregate final ID bytes.

These errors are categorized as internal extractor-metadata failures. They are
intentional safety differences from the pinned implementation.

Normalization uses the selector's existing Go extension map rather than a
second pinned-only list. The Go extension map is now the exact pinned
`_format_selection_exts` set: `aiff`, `alac`, `flac`, `m4a`, `mka`, `mp3`,
`ogg`, `opus`, `wav` for audio; `avi`, `flv`, `mkv`, `mov`, `mp4`, `webm`,
`3gp` for video; and `mhtml` for storyboards. Tokens outside that set parse as
direct IDs; bare extensions such as `wmv`, `m4v`, `f4v`, `mpg`, `divx`, or
`3g2` therefore no longer collide with the extension map.

## Parser-parity safety decisions

The PR 2 lexer and parser introduce deliberate deviations from the pinned
Python tokenizer that fail closed rather than silently changing the requested
selector:

- Direct-format IDs that contain comment punctuation (`#`) are rejected. The
  pinned Python tokenizer treats `#...` as a comment, which can rewrite the
  requested ID (for example `id#variant` becomes `id`). The Go parser treats
  the atom as a syntax error instead. String/line-continuation punctuation
  (`\`, `'`, `"`) is also rejected for direct IDs; pinned tokenization raises
  `TokenError` for those forms.
- Single-character OP tokens `!`, `$`, `?`, and `` ` `` are retained in direct
  IDs, matching `_remove_unused_ops` joins (`id!variant` selects that ID).
- Unicode number characters outside ASCII digits (Nd/Nl/No, including `²` and
  Roman numerals) are accepted in direct IDs as pinned NAME tokens.
- Negated string filter operators (`!^=`, `!$=`, `!*=`, `!~=`) parse and
  evaluate with the pinned missing-field and none-inclusive semantics.
- Host-integer overflow for positive `.N` atom indexes is retained as valid
  syntax and evaluates to no match. The pinned implementation accepts arbitrary
  digit runs; Go records overflow deterministically instead of wrapping.

Token joining, dangling-branch acceptance (`best,`, `best/`, `()`, `best//` as
a joined direct ID), and empty-comma rejection (`best,,`, `,best`) follow the
pinned Python grammar exactly and are covered by `parser.*` corpus cases plus
`internal/format/parser_parity_test.go`.

Source-span endpoints reported by the parser are a Go-defined half-open
contract: error offsets name the exact `[start, end)` byte range of the
original, untrimmed selector that triggered the rejection. Upstream Python
raises `SyntaxError` for some of the same cases but does not expose matching
end offsets for every token shape, so the spans are recorded as Go-owned
contract guarantees rather than byte-for-byte upstream comparisons.

## Bounded selector deviations

The executing corpus and `docs/FORMAT_SELECTOR_PARITY.md` retain the remaining
selector and product differences, including:

1. plain `best`/`worst` playable-universe semantics;
2. forward `all` ordering;
3. incomplete upstream sort aliases/conversions/limit semantics;
4. separate evaluator versus product multistream policy;
5. interactive `-f -` and broader multi-output post-processing outside scope;
6. bounded selector and evaluator limits.

Fixture or implementation updates must keep this provenance, the corpus, and the
parity ledger synchronized against an explicit reference revision.
