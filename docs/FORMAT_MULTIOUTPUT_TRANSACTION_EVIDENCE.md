# Multi-output transaction evidence

Branch: `codex/format-multioutput-transaction`

Depends on merged PR 5 (planner metadata) and PR 6 (N-track execution).

## Implemented

- **Per-plan destinations:** `resolveOutputPlanDestinations` renders each
  `OutputPlan` with `printFilenameForPlan` / `selectedPlanInfo` instead of one
  shared base plus mechanical suffixes.
- **Collision handling:** identical rendered paths receive bounded `.fN_<id>`
  suffixes; distinct `%(ext)s` templates need no suffix.
- **Preflight:** `preflightPlanDestinations` rejects duplicate destinations and
  existing files when `Overwrite` is false, before media download starts.
- **Transaction rollback:** `mediaTransaction` replaces `publishedMediaTracker`;
  partial comma-output failure rolls back newly created files and preserves
  pre-existing paths.
- **Public result model:** `Result.Filename` is the first plan's media path;
  `Result.Artifacts` lists sidecars then media in plan order; `Result.Bytes`
  sums published media sizes after sidecar accounting.
- **Archive:** unchanged timing — record only after a fully successful
  `processMedia` (no archive on rollback failure).

## Intentional improvement over Python

Comma-selector partial failure triggers all-or-nothing rollback of outputs
created in the current attempt. Pinned Python leaves earlier outputs on disk.

## Out of scope (PR 8)

Per-plan postprocessors, subtitle/thumbnail embedding, and sidecar ownership
remain entry-scoped; `validateMultiOutputProduct` restrictions are unchanged.

## Tests

- `pkg/ytdlp/multioutput_transaction_test.go`
- Updated `pkg/ytdlp/format_selector_test.go` multi-output product cases
- `go test ./pkg/ytdlp/`
