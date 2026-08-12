# Multi-output lifecycle evidence

The transaction layer defines per-plan destinations, rollback, artifact
ordering, byte accounting, and the public multi-output `Result` contract.

## Implemented contract

`processMedia` constructs one defensive `outputLifecycle` for every planned
output and executes those lifecycles serially in planner order. Each lifecycle
owns its plan metadata, destination, current/final media path, sidecars,
prints, and artifact accounting.

For every plan, `executePlanLifecycle` runs:

1. `PrintVideo`.
2. Thumbnail and related-file production.
3. `PrintBeforeDL`.
4. Subtitle download and conversion.
5. N-track download/merge through the shared executor.
6. The configured postprocessor chain.
7. Chapter and SponsorBlock cuts.
8. Subtitle embedding.
9. Thumbnail embedding.
10. `PrintPostProcess`, `PrintAfterMove`, and `PrintAfterVideo`.
11. Strict artifact accounting and final metadata encoding.

Simulation remains PrintVideo-only. Skip-download mode runs the requested
sidecars and staged prints without publishing media or recording the archive.
The single-output path uses the same lifecycle and remains covered by the
pre-existing product regression suite.

## Metadata and ownership

Each lifecycle starts from `selectedPlanInfo`, so `format_id`, `ext`,
`requested_formats`, headers, and selected-format fields belong to that plan.
Mutable subtitle metadata and header maps are cloned again before download so
one output cannot consume or rewrite another output's subtitle state.

Postprocessor outputs update both `filepath` and `ext` in the lifecycle's
final InfoJSON. Thumbnail embedding extension changes are applied to each
plan's clone rather than the canonical extractor metadata.

All files produced by a lifecycle are registered with the shared
`mediaTransaction`. A failure in any output of a multi-output product rolls
back transaction-owned files from all earlier outputs. Paths that existed
before the transaction are never registered as newly created and are not
removed by rollback.

`accountLifecycleArtifacts` is strict: every artifact returned by a successful
lifecycle must still exist. Missing files return `errMissingLifecycleArtifact`
wrapped with `errLifecycleInternal`; they are not silently omitted from byte
accounting.

## Complete deterministic preflight

Before the first lifecycle writes or downloads, `preflightOutputLifecycles`
resolves the deterministic destinations for every plan:

- media outputs;
- InfoJSON, description, and link sidecars;
- subtitle downloads and converted subtitles;
- thumbnail downloads and declared conversions;
- every derived postprocessor output.

The complete path set is passed to the transaction collision/existence
preflight. Thus
two plan-specific sidecars cannot silently overwrite one another. Fixed
explicit postprocessor destinations are rejected for multi-output products
because the current request model provides one literal destination shared by
all plans. Print-to-file paths are intentionally excluded from collision
preflight because they are append sinks: each plan contributes one line in
stable order and the public artifact list contains the physical file once.

Runtime-only temporary paths remain owned by their individual atomic media
operations. Thumbnail paths whose extension changes only after content
sniffing are protected by the thumbnail operation's confined atomic move and
duplicate checks.

## Interactive decisions and prints

Interactive match-filter callbacks run once per plan, serially, before
publication. Each prompt receives that plan's rendered destination. A callback
error aborts the entry; a rejection follows the existing whole-entry terminal
decision policy.

Print-rule validation and every format-dependent print stage use the current
plan metadata and tracks. `Result.Prints` is grouped in plan order and stage
order. Print-file artifacts are de-duplicated by physical path.

## Public result and archive

After all plans succeed:

- `Result.Filename` is the first plan's final media path.
- `Result.InfoJSON` is the first plan's final metadata.
- `Result.Prints` is the concatenation of plan prints in stable order.
- `Result.Artifacts` contains all sidecars in plan order, followed by all media
  in plan order, with duplicate physical artifacts removed.
- `Result.Bytes` is recalculated from exactly those published artifacts.
- the download archive is recorded once, after all lifecycles and accounting
  succeed.

No archive record is written for simulation, skip-download, preflight failure,
interactive rejection, lifecycle failure, or accounting failure.

## Restrictions removed

The blanket multi-output guards for postprocessors, chapter removal,
SponsorBlock removal, subtitle embedding, thumbnail embedding, and interactive
match filtering are removed. The retained restriction is a concrete collision
rule: a literal postprocessor destination shared by multiple plans is invalid.
Default postprocessor destinations derive from each plan's current path and
are accepted.

## Focused evidence

- `TestMultiOutputLifecycleSidecarsPrintsAndMetadataIsolation` proves per-plan
  InfoJSON, all five plan-dependent print stages, and sidecars-before-media
  ordering.
- `TestMultiOutputLifecycleRejectsSharedSidecarBeforeDownload` proves a
  sidecar collision fails before either media URL is requested.
- `TestMultiOutputLifecycleRunsDefaultPostprocessorPerPlan` remuxes two valid
  A/V files, probes both MKV outputs, and checks final extension/path metadata.
- `TestMultiOutputLifecycleEmbedsSubtitlesAndThumbnailsPerPlan` probes an
  independently embedded subtitle stream and attached image on both outputs.
- `TestMultiOutputLifecycleRemovesChapterRangesPerPlan` applies and probes a
  real media cut on both outputs.
- `TestMultiOutputLifecycleArtifactOrderDeterministic` pins sidecar/media
  ordering.
- `TestMultiOutputProductRollbackThroughRun` proves a second-output failure
  removes the first output.
- `TestClientInteractiveMatchFilterPromptsPerOutput` proves serial distinct
  per-plan prompts.
- `TestAccountLifecycleArtifactsReportsMissingFileAsError` pins strict
  accounting.

The existing postprocessor unit suite remains authoritative for individual
operation-family semantics; the multi-output product test proves the same
postprocessor chain is invoked independently for each plan. Explicit-source
auxiliary conversion operations remain subject to deterministic destination
collision checks when one request would target the same output for every plan.

## Verification

Required local gates:

- `go test ./pkg/ytdlp -count=1`
- `go test ./... -count=1`
- `go test ./pkg/ytdlp ./internal/media/ffmpeg ./internal/media/postprocess -race -count=1`
- `go vet ./pkg/ytdlp ./internal/format ./internal/media/ffmpeg ./internal/media/postprocess`
- `go run ./cmd/paritycheck`
- CGO-disabled cross-builds for supported Linux, macOS, and Windows targets.
