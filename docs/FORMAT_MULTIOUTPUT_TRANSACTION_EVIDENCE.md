# Multi-output transaction evidence

Branch: `codex/format-multioutput-transaction`

Depends on merged PR 5 (planner metadata) and PR 6 (N-track execution).

## Implemented

- **Per-plan destinations:** `resolveOutputPlanDestinations` renders each
  `OutputPlan` with `planDestinationOutputInfo` / `renderFilenameBase` instead
  of one shared base plus mechanical suffixes.
- **Portable collision keys:** `portablePathKey` normalizes separators,
  cleans paths, and case-folds components for disambiguation and preflight.
  Original paths are preserved for publication and diagnostics.
- **Collision handling:** identical portable keys receive bounded `.fN_<id>`
  suffixes with iterative suffixing (up to 16 attempts); unresolved collisions
  reject before side effects.
- **Merged `correct_ext`:** `alignMergedDestinationExtension` mirrors pinned
  yt-dlp `correct_ext` for multi-output merged plans only (`len(plans) > 1`
  and `len(plan.Tracks) > 1`). Single-output filename rendering is unchanged
  (extensionless templates and custom suffixes like `%(title)s.out` stay as
  rendered). Thumbnail destination promotion runs after alignment.
- **Preflight before side effects:** `preflightMediaDestinations` runs immediately
  after plan destinations resolve and rejects portable collisions and existing
  regular files when `Overwrite` is false without mutating the filesystem.
  `acquireDestinationBackups` renames overwrite targets only immediately before
  media download starts.
- **Overwrite transaction:** existing media destinations are renamed to
  same-filesystem `.ytdlp-trx-*` backups at download time when `Overwrite=true`.
  On download failure, `rollback` restores overwritten files byte-for-byte and
  removes new artifacts. `commitDestinations` drops backups after all plan
  downloads succeed and retains destination slots when backup cleanup fails so
  rollback can still restore. Post-download embed/cut failures leave committed
  media on disk; artifact-only rollback removes sidecars/prints but not
  published media paths. Destination inspection uses `Lstat` and requires regular
  files; partial backup acquisition rolls back before returning an error.
- **Transaction rollback:** `mediaTransaction` replaces `publishedMediaTracker`;
  partial comma-output download failure rolls back newly created files and
  restores pre-existing overwritten destinations. Rollback/cleanup errors are
  joined with the primary error instead of being discarded.
- **Public result model:** `Result.Filename` is the first plan's media path;
  `Result.Artifacts` lists sidecars then media in plan order; `Result.Bytes`
  sums published media sizes after sidecar accounting.
- **Archive:** unchanged timing — record only after a fully successful
  `processMedia` (no archive on rollback failure).

## Intentional improvement over Python

Comma-selector partial download failure triggers all-or-nothing rollback of
outputs created in the current attempt (including overwrite restore). Pinned
Python leaves earlier outputs on disk.

## Out of scope (PR 8)

Per-plan postprocessors, subtitle/thumbnail embedding ownership, and lifting
`validateMultiOutputProduct` restrictions remain entry-scoped; sidecar lifecycle
per plan stays in PR 8.

## Tests

- `pkg/ytdlp/multioutput_transaction_test.go` — portable keys, merged
  `correct_ext`, single-output extensionless templates, overwrite backup
  restore, commit removes backups, restore/cleanup error surfacing, preflight
  blocks sidecars, cancellation rollback, suffix collisions
- Updated `pkg/ytdlp/format_selector_test.go` multi-output product cases
- `go test ./pkg/ytdlp/`
