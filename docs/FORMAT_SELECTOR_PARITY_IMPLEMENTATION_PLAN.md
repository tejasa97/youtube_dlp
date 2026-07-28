# Format-selection parity implementation plan

Status: PRs 1–4 merged; PRs 5–6 in progress; PRs 7–11 pending

Last updated: 2026-07-29

Behavioral baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

## Progress

| PR | Scope | Status |
| --- | --- | --- |
| 1 | Foundation, corpus, and normalization | Merged |
| 2 | Lexer and parser parity | Merged |
| 3 | Filter and Python-regex compatibility | Merged |
| 4 | Full FormatSorter | Merged |
| 5 | Evaluator, defaults, and output metadata | In progress |
| 6 | Arbitrary N-track execution | In progress ([draft PR #145](https://github.com/tejasa97/youtube_dlp/pull/145)) |
| 7 | Multi-output transaction and public result model | Pending |
| 8 | Complete per-output lifecycle | Pending |
| 9 | CLI parity and format checking | Pending |
| 10 | Pinned-baseline closure | Pending |
| 11 | Current-upstream delta | Pending |

## Objective

Achieve observable parity with Python yt-dlp's format-selection pipeline while
keeping production, builds, and Go tests completely Python-free.

Normative baseline:

- Reference checkout: `/Users/tejas/projects/yt-dlp-reference`
- Reference commit: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- Forward upstream changes are handled separately in the final delta PR.

Parity applies to normal valid inputs within documented security and resource
bounds. Deliberately abusive inputs may fail safely.

## Execution rules

Every PR in this track must:

- Start from the latest merged predecessor in an isolated branch and worktree.
- Use the Python checkout only as a read-only behavioral reference.
- Never execute Python from Go tests, builds, Docker, or production.
- Commit deterministic expected-output fixtures with provenance.
- Preserve unrelated changes and avoid extractor, SABR, and CI work.
- Verify locally rather than relying on GitHub Actions.
- Use Tejas-only commit attribution without agent or co-author trailers.
- Record remaining deviations honestly.
- Avoid expanding its scope into later PRs.

One lead reviewer should remain responsible across the sequence so architectural
decisions remain consistent.

## PR 1 — Parity foundation, corpus, and normalization

Branch: `codex/format-parity-foundation`

Implement:

- A machine-readable oracle fixture schema.
- Provenance recording the reference SHA, Python version, and derivation method.
- Defensive format normalization without mutating extractor-owned objects.
- Missing and duplicate `format_id` handling.
- Selector-control-character sanitization.
- Extension-versus-direct-ID collision behavior.
- Numeric and string field coercion required before sorting.
- Canonical normalized format data shared by selection, listing, printing, and
  InfoJSON.
- A parity-gap ledger.
- Explicit selector, regex, format-count, and planning resource budgets.

Completion gate:

- Every fixture is passing or recorded as a known gap.
- Concurrent normalization is deterministic.
- Fixture schema and provenance validation pass.
- No Python invocation exists in the verification path.

## PR 2 — Lexer and parser parity

Branch: `codex/format-selector-parser-parity`

Implement syntax only:

- A quote-, escape-, grouping-, and bracket-aware lexer.
- Exact `/`, `+`, `,`, grouping, and filter precedence.
- Direct format IDs with pinned-compatible punctuation.
- Every atom, alias, star form, and `.N`.
- Exact extension-selector recognition.
- Precise invalid-syntax spans.
- Removal of artificial `.1000` semantics while retaining documented resource
  bounds.

Do not implement filter evaluation or sorting in this PR.

Completion gate:

- The complete parser oracle corpus passes.
- All official selector examples parse correctly.
- Invalid selectors fail before extraction or network access.
- Parser fuzzing and cross-platform builds pass.

## PR 3 — Filter and Python-regex compatibility

Branch: `codex/format-selector-filter-parity`

Implement:

- Every pinned numeric and string operator.
- Negated operators.
- None-inclusive `?` behavior.
- Exact missing-field semantics.
- Complete SI and IEC numeric parsing.
- Quoted delimiters, escapes, and whitespace behavior.
- A Python-regex compatibility adapter over `regexp2`.
- Python named-group and backreference syntax translation.
- Inline flags, look-around, backreferences, and Unicode behavior.
- Strict regex input and execution limits.

Promote `regexp2` to a direct dependency.

Completion gate:

- Filter and regex oracle corpora pass.
- Regex timeouts cannot leak goroutines.
- Malformed and adversarial regex fuzzing passes.
- No normal pinned-baseline filter expression remains unsupported.

## PR 4 — Full FormatSorter

Branch: `codex/format-sorter-parity`

Implement:

- Complete default sorting order.
- Extractor `_format_sort_fields`.
- User sorting and forced precedence.
- Repeated `-S` and reset semantics.
- Codec, extension, HDR, protocol, and language rankings.
- Resolution, size, bitrate, channels, and derived fields.
- Exact and approximate filesize.
- `FIELD:LIMIT` and `FIELD~LIMIT`.
- Deprecated aliases.
- Missing- and mixed-type ordering.
- `--prefer-free-formats`.
- Stable worst-to-best ordering.

Completion gate:

- All pinned sorting cases pass.
- Extractor, default, and user precedence agrees with the oracle.
- Sorting is stable, deterministic, and race-clean.

## PR 5 — Evaluator, defaults, and output metadata

Branch: `codex/format-selector-planner-parity`

Implement:

- Correct best, worst, and combined-format behavior.
- Audio-only and video-only incomplete-format fallback.
- `all`, `mergeall`, storyboard, and pick-first ordering.
- Python multistream suppression rules.
- Audio and video multistream options.
- Removal of Go-only track deduplication where incompatible.
- Complete merged metadata and a `requested_formats` equivalent.
- `get_compatible_ext`.
- Default selector policy using injected capabilities:
  - FFmpeg availability
  - stdout output
  - live and live-from-start state
  - multistream configuration
- An injectable format-availability interface for later CLI support.

The planner must remain pure: it performs no filesystem, process, or network
probing internally.

Completion gate:

- Pinned selection cases and all four multistream combinations pass.
- Output-plan metadata agrees with the oracle.
- Planning remains deterministic and cancellation-safe.

## PR 6 — Arbitrary N-track execution

Branch: `codex/format-ntrack-execution`

Implement:

- Bounded N-track downloading and merging.
- Deterministic FFmpeg input and mapping order.
- Per-track headers and credential-isolation state.
- Compatible codec copying.
- Container selection and `--merge-output-format`.
- `mergeall`.
- Sibling cancellation on failure.
- Safe partial-workspace cleanup.
- Atomic publication.
- Accurate events and byte accounting.
- Windows-safe paths.

Completion gate:

- Single-, two-, and N-track outputs pass.
- Generated media is inspected with `ffprobe`.
- Cancellation and failure leave no published partial output.
- Race, cross-platform, and Python-free Docker checks pass.

## PR 7 — Multi-output transaction and public result model

Branch: `codex/format-multioutput-transaction`

Define and implement:

- The meaning of `Result.Filename` with multiple outputs.
- Authoritative `Result.Artifacts` ordering.
- Per-plan output-template rendering.
- Collision preflight.
- Archive timing.
- Sidecar ownership.
- Partial-failure and rollback semantics.
- Atomic multi-output publication where feasible.
- Correct per-plan selected-format metadata.
- Removal of fixed `.f<N>_<ID>` naming when templates already distinguish
  outputs.

Any deliberate improvement over Python behavior must be documented separately
from the parity claim.

Completion gate:

- Multiple outputs publish deterministically.
- Collision and rollback tests pass.
- The public result contract remains backward compatible for single outputs.

## PR 8 — Complete per-output lifecycle

Branch: `codex/format-multioutput-lifecycle`

Apply independently to every output:

- Download and merge.
- Postprocessors.
- Chapter removal.
- SponsorBlock marking and removal.
- Subtitle downloading and embedding.
- Thumbnail downloading and embedding.
- InfoJSON and sidecars.
- Interactive match-filter stages.
- Before- and after-download print stages.
- Artifact and byte accounting.
- Safe sidecar reuse without cross-output state leakage.

Remove `validateMultiOutputProduct` restrictions only after these paths are
covered.

Completion gate:

- Every postprocessor family works with at least two outputs.
- Collision, rollback, and state-isolation tests pass.
- Prints, artifacts, filenames, and byte accounting are complete per output.

## PR 9 — CLI parity and format checking

Branch: `codex/format-selector-cli-parity`

Expose:

- Audio and video multistream flags.
- Merge-output-format.
- Sort-force and sort-reset flags.
- Check-formats modes.
- Prefer-free and unplayable-format negations.
- `--all-formats`.
- Interactive `-f -`.

Interactive behavior must cover:

- Empty input selecting the default.
- Invalid syntax reprompting.
- Unavailable selection reprompting.
- EOF and cancellation categorization.
- Clean separation from JSON and progress output.

Format checking must use bounded, protocol-aware probes and must never download
complete media.

Completion gate:

- Flag, reset, and configuration precedence tests pass.
- Interactive acceptance, retry, EOF, and cancellation tests pass.
- Checking modes cache results and preserve credentials correctly.
- CLI exit codes and output channels agree with the pinned behavior.

## PR 10 — Pinned-baseline closure

Branch: `codex/format-selector-pinned-closure`

Run and record:

- All pinned format-selection and sorting cases.
- Official README examples.
- Generated atom, operator, and filter cross-products.
- All multistream combinations.
- Muxed, audio-only, video-only, storyboard, and DRM cases.
- Availability checks.
- Multi-output through every product stage.
- Malformed and adversarial input.
- Fuzzing, race, and cancellation campaigns.
- Linux, macOS, and Windows amd64 and arm64 builds.
- A Python-free Docker audit.

Replace `compat.format_selector_pilot` only when no functional deviation against
the pinned baseline remains within the documented input contract.

## PR 11 — Current-upstream delta

Branch: `codex/format-selector-upstream-delta`

Pin the then-current upstream SHA and compare it with the completed baseline.

For every difference:

- Implement it,
- Record it as intentional, or
- Create a specifically scoped follow-up.

This keeps current upstream from becoming a moving target during the primary
implementation track.

## Verification policy

Every PR must run:

- `gofmt` on changed Go files.
- `git diff --check`.
- Focused package tests.
- `go test ./...`.
- `go vet ./...`.
- `go mod tidy -diff`.
- `go run ./cmd/paritycheck`.

Race, fuzz, FFmpeg, cross-platform, and Docker verification are required when
the changed layer makes them relevant, and are all mandatory in the closure PR.
Captured logs must be scanned for parse, compile, race, panic, and script errors;
process exit status alone is not sufficient evidence.
