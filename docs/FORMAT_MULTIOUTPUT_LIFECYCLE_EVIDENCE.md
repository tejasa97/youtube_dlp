# Multi-output lifecycle evidence

Branch: `codex/format-multioutput-lifecycle`

Depends on PR 7 (`codex/format-multioutput-transaction`, commit `fc177e4`,
unmerged) which provides the transaction abstraction, per-plan
destinations, preflight collision checks, and the public result contract
that this PR consumes.

## Status

This document tracks PR 8 progress. Each section maps to a phase in the
implementation handoff.

- [Phase 1 — Lifecycle matrix](#phase-1--lifecycle-matrix)
- [Phase 2 — Per-output lifecycle abstraction](#phase-2--per-output-lifecycle-abstraction)
- [Phase 3 — Single-output routed through the abstraction](#phase-3--single-output-routed-through-the-abstraction)
- [Phase 4 — Plan-specific metadata](#phase-4--plan-specific-metadata)
- [Phase 5 — Complete preflight](#phase-5--complete-preflight)
- [Phase 6 — Interactive match-filter stages](#phase-6--interactive-match-filter-stages)
- [Phase 7 — Before-download print stages](#phase-7--before-download-print-stages)
- [Phase 8 — Download and N-track merge](#phase-8--download-and-n-track-merge)
- [Phase 9 — Postprocessors per output](#phase-9--postprocessors-per-output)
- [Phase 10 — Chapters and SponsorBlock](#phase-10--chapters-and-sponsorblock)
- [Phase 11 — Subtitle lifecycle](#phase-11--subtitle-lifecycle)
- [Phase 12 — Thumbnail lifecycle](#phase-12--thumbnail-lifecycle)
- [Phase 13 — InfoJSON and related sidecars](#phase-13--infojson-and-related-sidecars)
- [Phase 14 — After-download print stages](#phase-14--after-download-print-stages)
- [Phase 15 — Events, artifacts, and byte accounting](#phase-15--events-artifacts-and-byte-accounting)
- [Phase 16 — Archive and transaction completion](#phase-16--archive-and-transaction-completion)
- [Phase 17 — Multi-output restrictions](#phase-17--multi-output-restrictions)

PR 7 contracts consumed by this PR:

- `resolveOutputPlanDestinations(info, plans)` returns per-plan
  destinations rendered from each plan's template metadata.
- `preflightPlanDestinations(destinations)` rejects duplicate or
  pre-existing destinations before media download starts.
- `mediaTransaction.recordCreated(path)` registers published files.
- `mediaTransaction.rollback()` removes newly created files;
  pre-existing paths are never touched.
- `Result.Filename` is the first plan's media path; `Result.Artifacts`
  lists sidecars then media in plan order; `Result.Bytes` sums
  published media sizes.

## Architecture

```
processMedia
   |
   +-- build output lifecycle plans
   |
   +-- PR 7 complete preflight
   |
   +-- interactive decisions
   |
   +-- for each lifecycle in stable order:
   |       executeOutputLifecycle
   |           - pre-download prints
   |           - sidecars (thumbnails, infojson, related files)
   |           - download/merge
   |           - postprocessors
   |           - chapters / SponsorBlock
   |           - subtitle / thumbnail embedding
   |           - InfoJSON updates
   |           - after-download prints
   |           - artifact / byte result
   |
   +-- PR 7 transaction commit
   |
   +-- archive record
   |
   +-- construct public Result
```

The per-output lifecycle is internal. The public `Result` continues to
follow the PR 7 contract.

## Phase 1 — Lifecycle matrix

The table below records the lifecycle of every operation that runs after
the planner returns `[]OutputPlan`. Each row records the operation, where
it lives, what mutable state it currently consumes, whether it is run
for multi-output today, and how PR 8 will route it.

| Stage | Operation | File | Metadata input | Mutations | Selections input | Paths | Artifacts | Per-output today? | First-plan only today? | Warning vs fatal | Events | Byte accounting | PR 8 plan |
|-------|-----------|------|----------------|-----------|------------------|-------|-----------|-------------------|------------------------|------------------|--------|-----------------|-----------|
| pre-process print | `capturePrints(PrintPreProcess)` | `print.go` | canonical `info` | none | nil | n/a | n/a | entry-scoped | n/a | fatal | none | none | run once before lifecycle |
| after-filter print | `capturePrints(PrintAfterFilter)` | `print.go` | canonical `info` | none | nil | n/a | n/a | entry-scoped | n/a | fatal | none | none | run once before lifecycle |
| interactive match filter | `resolveInteractiveCompatibility` | `compatibility.go` | canonical `info` and per-plan clone | per-plan info (not original) | selected format(s) | per-plan destination | n/a | rejected via `validateMultiOutputProduct` | uses first plan | callback error aborts | none | none | one prompt per plan in stable order; callback serial; per-plan info/destination |
| pre-download prints | `capturePrints(PrintVideo, PrintBeforeDL)` + `writePrintFiles` | `print.go` | `selectedPlanInfo` + `addPrintFields` | none | first-plan tracks | first-plan destination | print file artifacts (when `FileTemplate`) | yes (entry) | yes (single template) | fatal | none | file bytes | one invocation per plan with plan metadata |
| thumbnails (list/write) | `writeThumbnails` | `thumbnails.go` | canonical `info` (entry-scoped) | updates `info.thumbnails`, `info.filename`/`info.filepath` | n/a | templates from info | thumbnail artifacts + bytes | entry-scoped only | n/a | warning (no match) | download events per thumbnail | yes | clone metadata per plan, render templates per plan |
| related files | `writeRelatedFiles` | `related_files.go` | canonical `info` (entry-scoped) | none | n/a | templates from info | sidecar artifacts + bytes | entry-scoped only | n/a | warning (templates only) | none | yes | clone metadata per plan |
| subtitle download | `downloadSubtitles` | `subtitles.go` | canonical `info` | updates `info` (subtitles field) | requested track list | subtitle templates | subtitle artifacts + bytes | entry-scoped only | n/a | warning | download events | yes | clone metadata per plan; per-plan ownership |
| subtitle convert | `convertSelectedSubtitles` | `subtitle_convert.go` | subtitle track list | updates track list | selected tracks | converted paths | subtitle artifacts + bytes | entry-scoped only | n/a | warning | none | yes | clone metadata per plan; per-plan ownership |
| download / merge | `downloadSelections` (uses merged `ntrack_download` from PR 6) | `download.go`, `ntrack_download.go` | `info` (track URLs/headers) | updates `info` (filepath, ext) | plan tracks | plan destination | media artifact (post-merge) | yes | yes | fatal | `DownloadStart/Complete/Progress` per track | none directly | run per plan; track concurrency owned by PR 6; transaction records final path |
| postprocessors | `applyPostprocessors` | `postprocess.go` | n/a | may set `info.ext`, `info.filepath` | n/a | derived from current path | postprocessor artifacts | entry-scoped | uses first plan only (when `multiOutput` skip postprocessors) | fatal | per-step events | yes (recomputed after) | run per plan; clone `info`; preflight explicit destinations |
| chapter removal / SponsorBlock cuts | `applyChapterCuts` | `sponsorblock_cut.go` | `info` (chapters, SponsorBlock, `_ranges`, duration) | updates `info._ranges`, `info.duration`, may rewrite chapters | n/a | cut output path | media artifact replacement | yes (entry) | uses first plan only | warning (open ranges), fatal (FFmpeg) | per-cut events | yes | clone `info` per plan; resolve open ranges per plan duration; clone chapter slice |
| subtitle embedding | `embedSelectedSubtitles` | `subtitle_embed.go` | `info` (requested subtitles) | updates `info` (post-embed state) | selected tracks | embed into media path | media artifact replacement | yes (entry) | uses first plan only | fatal (FFmpeg) | per-embed events | yes | clone per plan; per-plan ownership |
| thumbnail embedding | `embedSelectedThumbnail` | `thumbnail_embed.go` | `info.thumbnails`, current media path | updates `info.ext`, `info.filepath` | thumbnail list | embed into media path | media artifact replacement | yes (entry) | uses first plan only | warning (unsupported container) | per-embed events | yes | clone per plan; container compatibility per plan |
| InfoJSON | `encodeInfo` | `client.go` | `info` | re-encodes after every stage | n/a | n/a | none directly | re-encoded after cut/embed/subtitle-embed | uses first plan | fatal | none | n/a | encode per plan |
| postprocess/after-move/after-video prints | `capturePrints(PrintPostProcess, PrintAfterMove, PrintAfterVideo)` | `print.go` | `selectedPlanInfo` (or original if not present) + `addPrintFields` | none | first-plan tracks | first-plan path | print file artifacts (when `FileTemplate`) | yes (entry) | yes (single template) | fatal | none | file bytes | one invocation per plan after plan's `FinalPath` resolves |
| archive record | `archive.Record` | `client.go` | archiveIdentity (id, ext, ...) | n/a | n/a | n/a | n/a | once per entry | yes | fatal | none | n/a | single call after every plan commits |

### Rejected multi-output behaviour to be removed

Current `validateMultiOutputProduct` rejects multi-output downloads
when any of the following are set. Each rejection will be removed only
after a two-output lifecycle test exists.

| Rejection | Test that will replace it |
|-----------|---------------------------|
| postprocessors with multi-output selectors | `TestMultiOutputProductRunsPostprocessorsPerOutput` |
| SponsorBlock remove with multi-output selectors | `TestMultiOutputProductSponsorBlockCutPerOutput` |
| chapter removal with multi-output selectors | `TestMultiOutputProductChapterRemovalPerOutput` |
| subtitle embedding with multi-output selectors | `TestMultiOutputProductSubtitleEmbedPerOutput` |
| thumbnail embedding with multi-output selectors | `TestMultiOutputProductThumbnailEmbedPerOutput` |
| interactive match filtering with multi-output selectors | `TestMultiOutputProductInteractiveDecisionPerOutput` |

## Phase 2 — Per-output lifecycle abstraction

`pkg/ytdlp/output_lifecycle.go` defines `outputLifecycle` and
`executeOutputLifecycle`. The struct is internal; the public result
model remains the one defined by PR 7.

The struct holds:

- `Index int` — the plan's stable index in the planner result.
- `Plan format.OutputPlan` — immutable reference to the planner-owned
  plan.
- `Info value.Info` — a defensive per-output clone produced by
  `selectedPlanInfo` (available from PR 5).
- `Destination string` — the per-plan path resolved by PR 7's
  `resolveOutputPlanDestinations`. The destination is both the
  lifecycle's commit point and the path the executor writes to;
  PR 7 does not separate staging from publication.
- `MediaPath string` — current media file (advances through
  postprocessor outputs and cuts).
- `FinalPath string` — the path reported as `Result.Filename` when
  `Index == 0`.
- `Sidecars []Artifact` — per-output pre-download sidecars (print
  files; later phases add thumbnails, related files, subtitles).
- `MediaArtifacts []Artifact` — per-output media artifacts.
- `Prints []PrintOutput` — per-output prints.
- `Bytes int64` — per-output published byte total.
- `Downloaded bool` — set after the executor returns.

The lifecycle is built by
`newOutputLifecycleForPlan(index, plan, info, destination)`.

`executeOutputLifecycle(ctx, *mediaTransaction, lifecycle, sink)` runs
the complete lifecycle for one plan. For callers that need to
interleave entry-scoped post-process stages between download and
after-prints (the historical single-output order: download,
postprocessors, chapter cuts, embeds, after-prints), the helper is
split:

- `executeOutputLifecyclePhases(ctx, transaction, lifecycle, sink)`
  runs PrintVideo, PrintBeforeDL, and the download.
- `runLifecycleAfterPrints(ctx, transaction, lifecycle)` runs
  PrintPostProcess, PrintAfterMove, and PrintAfterVideo.

`aggregateLifecycles([]outputLifecycle)` produces the public Result
payload following PR 7's authoritative artifact ordering: sidecars of
plan 1, sidecars of plan 2, ..., media of plan 1, media of plan 2, ....
`Result.Filename` is the first plan's `FinalPath`.

Errors are wrapped via `wrapLifecycleError(op, err)` which produces
`fmt.Errorf("%s: %w: %w", op, errLifecycleInternal, err)`. The double
`%w` preserves both the lifecycle sentinel and the underlying cause,
so `errors.Is(err, errLifecycleInternal)` and
`errors.Is(err, downloader.ErrDestinationExists)` both succeed.
`categorized` keeps working unchanged.

Print file artifacts are registered with the PR 7 transaction via
`transaction.recordCreated` so `transaction.rollback()` covers
partially-written print files. A focused test
(`TestExecuteOutputLifecycleRegistersPrintArtifactsInTransaction`)
proves both the media and the print file are removed on rollback.

## Phase 3 — Single-output routed through the abstraction

The single-output product path will be routed through the abstraction
in a follow-up commit. The current PR introduces the helpers and the
end-to-end regression baselines that the Phase 3 refactor must satisfy
without regression:

- `TestSingleOutputLifecycleMatchesLegacyContract` runs the lifecycle
  directly and asserts `Result.Filename`, `Result.Bytes`, and
  `Result.Artifacts` match the historical single-output contract for
  the simplest case (no postprocessors, no cuts, no embeds, no
  sidecars beyond the media).
- `TestSingleOutputLifecycleAggregatesMatchClientRun` runs
  `client.Run` end-to-end on a single-output selection and asserts
  the public Result fields (`Downloaded`, `Filename`, `Bytes`) are
  populated. It then constructs the lifecycle from the same plan and
  destination to confirm the per-output state matches what the
  product path produced.

The actual `processMedia` refactor (replacing the existing
per-plan download loop and print stages with calls to
`executeOutputLifecyclePhases` and `runLifecycleAfterPrints`,
aggregating the lifecycle into `Result.Artifacts` in PR 7 order)
lands in the Phase 3 commit once the entry-scoped sidecars
(thumbnails, related files, subtitles) have their own per-output
ownership model from Phases 11-13.

## Phase 4 — Plan-specific metadata

For each plan:

1. Clone the canonical media-entry metadata via `selectedPlanInfo`.
2. Overlay planner-owned `OutputPlan.Metadata`.
3. Preserve the plan's `requested_formats` ordering.
4. Set fields describing only this output (`format`, `format_id`,
   `ext`, `protocol`, `vcodec`/`acodec`, resolution and bitrate fields,
   `requested_formats`, `filename`, `filepath`, filesize fields).
5. Render templates and prints against the plan-specific metadata.
6. Update metadata after every transformation: merge-container
   selection, extract-audio, remux, cuts, embedding, move, final
   extension and path.
7. Never mutate the extractor-owned info, the canonical prepared
   formats, `OutputPlan.Metadata`, or another lifecycle's metadata.

`TestMultiOutputLifecycleMetadataIsolation` mutates output 1's metadata
and asserts that output 2 and the original `info` remain unchanged.

## Phase 5 — Complete preflight

`preflightPlanDestinations` is extended to cover every destination PR 8
may create:

- media outputs;
- postprocessor outputs (extract-audio, remux, move);
- chapter/SponsorBlock outputs;
- subtitle sidecars and converted subtitles;
- thumbnails and converted thumbnails;
- InfoJSON;
- descriptions;
- URL/webloc/desktop sidecars;
- print-to-file destinations.

Each plan renders its destinations using that plan's metadata. PR 7's
collision rules apply (case-fold, Windows-safe). The preflight fails
before any network or filesystem side effect. PR 8 does not invent a
new collision policy.

## Phase 6 — Interactive match-filter stages

For multi-output with an interactive match filter:

- Process prompts in stable plan order.
- Prompt metadata and filename correspond to the current plan.
- Callbacks are never invoked concurrently.
- Collision preflight and required interactive decisions complete
  before media publication.
- Callback errors abort the transaction.
- Context cancellation remains discoverable through `errors.Is`.
- Rejection follows PR 7's selected policy (omit only the rejected
  output, or reject the complete media entry). PR 8 does not introduce
  a new policy.

The current multi-output interactive guard is removed only after the
tests in this phase pass.

## Phase 7 — Before-download print stages

`PrintVideo` and `PrintBeforeDL` (plus their print-to-file equivalents)
run once per plan with the plan's selected formats, metadata, and
destination. `Result.Prints` aggregates in deterministic plan order.
Console versus file outputs remain distinct.

## Phase 8 — Download and N-track merge

For each plan:

1. Invoke the PR 6 executor with the plan's track list and PR 7's
   staging destination.
2. Preserve the plan's track order; do not duplicate N-track
   concurrency, FFmpeg map construction, track workspace cleanup,
   container compatibility, or merge-output-format behaviour.
3. Use PR 7's staging destination; no publication outside PR 7's
   commit mechanism.
4. Preserve per-track URL/header/credential isolation.
5. Register produced media with the transaction
   (`transaction.recordCreated`).
6. Report plan-correct event paths.
7. Stop later output execution after failure and roll back
   transaction-owned work according to PR 7.

Specialised SABR and live paths remain unchanged.

## Phase 9 — Postprocessors per output

Every `Postprocessor` variant runs independently for each lifecycle:

- ExtractAudio
- Remux
- ConvertSubtitle
- ConvertThumbnail
- EmbedMetadata
- EmbedChapters
- EmbedThumbnail
- EmbedSubtitle
- Fixup
- Concat
- Move

Each chain starts with the current lifecycle's media, updates only that
lifecycle's path and metadata, and participates in the PR 7
transaction. Explicit destinations are included in the preflight.
Superseded intermediates are removed only through ownership-aware
cleanup. No postprocessor receives another output's artifact. A failure
in output 2 triggers PR 7 rollback of output 1. Pre-existing
source/destination files are preserved.

`TestMultiOutputProductRunsPostprocessorsPerOutput` exercises each
postprocessor variant in a two-output product test using FFprobe where
stream or container behaviour matters.

## Phase 10 — Chapters and SponsorBlock

SponsorBlock chapter marking, SponsorBlock removal, ordinary
chapter-title removal, manual time ranges, merged removal ranges, and
force-keyframe-at-cuts behaviour run independently for each lifecycle.

Each lifecycle probes its real media duration, clones the chapter and
SponsorBlock slices before mutation, resolves open ranges against its
own duration, and updates only its own metadata. Existing warning and
fatal semantics are preserved. Transformed media and superseded media
are registered through PR 7 ownership.

## Phase 11 — Subtitle lifecycle

Subtitles run independently for every output:

- language/format selection;
- destination rendering (per-plan metadata);
- download;
- conversion;
- chapter/SponsorBlock subtitle cuts where supported;
- embedding;
- keep/remove policy;
- metadata updates;
- artifact and byte accounting.

Sidecar ownership:

- A sidecar is reused only when the confined final path is identical,
  the content is identical, the conversion and mutation options are
  identical, and neither lifecycle will destructively mutate or remove
  it while another still owns it.
- A subtitle that will be converted, cut, renamed, embedded
  destructively, or removed must have private ownership or reference-
  counted ownership.
- Same language does not imply same ownership.
- Removing output 1's embedded subtitle source must not remove output
  2's required sidecar.
- Different content targeting the same path is a preflight collision.

## Phase 12 — Thumbnail lifecycle

Thumbnails run independently:

- selection;
- path rendering;
- download;
- content-type/extension correction;
- conversion;
- embedding;
- keep/remove policy;
- metadata and artifact updates.

The same ownership discipline as subtitles applies.

## Phase 13 — InfoJSON and related sidecars

InfoJSON and related files describe only the current plan's selected and
merged metadata. `format_id`, `ext`, `requested_formats`, `filename`,
and `filepath` are plan-correct. Playlist-level sidecars remain
playlist-owned and are not duplicated. Every sidecar is preflighted and
transaction-owned. Identical immutable files may be reused only under
PR 7's ownership policy. No sidecar is published before the transaction
permits it.

## Phase 14 — After-download print stages

`PrintPostProcess`, `PrintAfterMove`, `PrintAfterVideo`, and their
print-to-file equivalents run for every successful output using the
final path and the plan's final metadata. The authoritative ordering
from PR 7 is preserved.

## Phase 15 — Events, artifacts, and byte accounting

- Events use the current output's path; deterministic plan order for
  serial operations; no leak of headers, cookies, signed URLs, or
  credentials. Event-handler failure aborts and rolls back
  appropriately.
- Artifacts follow PR 7's authoritative ordering, include only
  published/owned artifacts, exclude temporary and superseded files,
  and do not duplicate a safely shared physical sidecar.
- `Result.Bytes` is the postprocessed final media size plus sidecars
  counted according to PR 7's shared ownership rule. Recalculated
  after conversion, cutting, embedding, remuxing, and moving. Failed
  transactions report no successfully published artifact total.

## Phase 16 — Archive and transaction completion

The archive records the entry only after every required output
lifecycle succeeds and the PR 7 transaction commits. Archive writes
never happen after:

- preflight failure;
- interactive rejection when policy rejects the entry;
- download failure;
- postprocessor failure;
- sidecar failure;
- print failure;
- cancellation;
- publication failure.

Archive write failure follows PR 7's documented post-publication
behaviour.

## Phase 17 — Multi-output restrictions

`validateMultiOutputProduct` is narrowed after replacement coverage
exists. Branches that skip postprocessors, chapter removal,
SponsorBlock removal, subtitle embedding, thumbnail embedding, and
interactive match filtering for multi-output are removed. The
`ErrMultiOutput` rejection tests are replaced with successful two-
output lifecycle tests. The parity-gap ledger and compatibility
documentation are updated.

## Verification

- `go test ./pkg/ytdlp ./internal/format ./internal/media/ffmpeg ./internal/media/postprocess -count=1`
- `go test ./pkg/ytdlp ./internal/media/ffmpeg ./internal/media/postprocess -race -count=1`
- `go test ./pkg/ytdlp -run 'MultiOutput|Lifecycle|Rollback|Cancellation|Ownership|Isolation' -count=20`
- `go vet ./pkg/ytdlp ./internal/format ./internal/media/ffmpeg ./internal/media/postprocess`
- `go run ./cmd/paritycheck`
- `go test ./... -count=1`
- Cross-build (`CGO_ENABLED=0`) for linux/darwin/windows amd64 and
  arm64.
- Python-free Docker build where available.
