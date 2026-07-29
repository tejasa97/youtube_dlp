# Pinned metadata-transformation parity evidence

## Scope and reference

This record covers normal valid `--parse-metadata` and
`--replace-in-metadata` behavior from the read-only `yt-dlp/yt-dlp` checkout
at `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. The normative implementation is
`yt_dlp/postprocessor/metadataparser.py`; option shape and ordering are from
`yt_dlp/options.py` and `yt_dlp/__init__.py`.

The executable source-derived fixture is
`internal/compat/metadata/testdata/pinned_cases.json`; its adjacent provenance
states that no Python oracle capture was performed. It records the pinned
maintainer capture interpreter, `CPython 3.12.13`, for any future refreshed
observation. Go builds, tests, and runtime never invoke Python or read the
reference checkout.

## Covered behavior

* `[WHEN:]FROM:TO` parsing uses the first unescaped colon, field-or-template
  input, greedy output captures, Unicode capture keys, and source-local errors.
* `--replace-in-metadata` accepts the upstream three-shell-argument form
  `[WHEN:]FIELDS REGEX REPLACEMENT`; comma-separated fields expand in order.
  The port's legacy single-token colon form remains accepted but is not
  advertised.
* The CLI preserves interleaving of parse and replacement operations. Actions
  mutate the canonical ordered metadata envelope immediately, so a later
  action sees earlier changes; overwriting an existing key preserves its order.
* Missing and null fields are nonfatal "does not have" diagnostics; other
  non-string replacement sources report their stable value kind. Empty
  replacements intentionally retain the field with an empty string, matching
  `re.sub`.
* Default `pre_process` actions execute before match and break filters, format
  selection, filenames, sidecars, prints, playlist retention, and output
  lifecycle work. This is the product's sole supported MetadataParser stage.

## Safety bounds and intentional limits

Metadata patterns use the shared `internal/compat/pyregex` translator and
`regexp2`; this package does not copy the translator. It supports look-around,
numeric and named pattern backreferences, normal capture substitution (`\\1`,
`\\g<name>`), escaping, Unicode text, and literal `$` behavior. Metadata owns
separate ceilings: an 8 KiB source pattern, 16 KiB translated pattern, 64 KiB
input, 1,024 match attempts, 4 MiB inspected bytes, 25 ms per match, and a
250 ms aggregate wall budget. Timeout failures are categorized without
including the input text.

Other upstream `WHEN` values (`after_filter`, `video`, `before_dl`,
`post_process`, `after_move`, `after_video`, and `playlist`) are rejected with
`ErrUnsupportedStage`, rather than being run at the wrong lifecycle point.
They require product lifecycle ownership beyond this bounded track.

All actions, source text, rendered input, replacement source, and replacement
output have deterministic ceilings. `ApplyContext` checks cancellation between
actions. Diagnostics contain only action fields and type names, never rendered
metadata values, URLs, cookies, or request secrets.

## Evidence anchors

* `internal/compat/metadata/metadata_test.go` — fixture consumption, grammar,
  ordering, Unicode, missing/null/type diagnostics, errors, cancellation, and
  resource limits.
* `internal/cli/run_test.go:TestRunMetadataThreeArgumentGrammarAndOrdering` —
  CLI argument shape, action ordering, and pre-filter visibility.
* `pkg/ytdlp/compatibility.go` — default pre-process execution before both
  filters and downstream selection/output work.

The metadata adapter consumes only the shared translator's public `Translate`
function. No template or match-filter internals are changed by this track.
