package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/compat/sections"
	"github.com/tejasa97/ytdlp-go/internal/events"
	mediaformat "github.com/tejasa97/ytdlp-go/internal/format"
	"github.com/tejasa97/ytdlp-go/internal/value"
)

// errLifecycleInternal is the wrapped sentinel that lets callers detect
// lifecycle failures via errors.Is. It never appears as a plain
// surface error: every lifecycle failure wraps an underlying error with
// %w so categorised error categories (network/auth/security/etc.) and
// identity checks (errors.Is(err, ErrDestinationExists) and friends)
// continue to work.
var errLifecycleInternal = errors.New("output lifecycle")

// errMissingLifecycleArtifact flags an internal accounting failure:
// the lifecycle registered an artifact path with the transaction,
// but the file is not on disk when accountLifecycleArtifacts runs.
// Lifecycle bookkeeping has drifted from reality and the
// transaction must abort so the caller can rollback cleanly.
var errMissingLifecycleArtifact = errors.New("lifecycle artifact missing from filesystem")

// wrapLifecycleError produces an error that satisfies
// errors.Is(err, errLifecycleInternal) while preserving the underlying
// cause for errors.Is matching of concrete error values such as
// downloader.ErrDestinationExists or context.Canceled.
func wrapLifecycleError(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w: %w", op, errLifecycleInternal, err)
}

// outputLifecycle holds the mutable per-output state for one OutputPlan.
//
// This internal type lets the complete media lifecycle run once per plan
// without sharing mutable state across outputs. The struct never escapes the
// package; the public Result model remains the external contract.
//
// Fields:
//
//   - Index is the plan's stable index in the planner result.
//   - Plan is the immutable OutputPlan returned by the planner.
//   - Info is a defensive per-output clone. Mutating Info must not
//     affect the canonical extractor-owned info, the canonical
//     prepared formats, OutputPlan.Metadata, or another lifecycle's
//     Info.
//   - Destination is the per-plan path resolved by
//     `resolveOutputPlanDestinations`. The plan destination is both
//     the lifecycle's commit point and the path the executor writes
//     to; this lifecycle does not separate staging from publication.
//   - MediaPath tracks the current media file for this output. It
//     advances through postprocessor outputs and cuts.
//   - FinalPath tracks the path the lifecycle reports as
//     Result.Filename when Index == 0.
//   - Sidecars collects this output's pre-download sidecars
//     (thumbnails, related files, subtitles, converted subtitles,
//     print files). The result contract places sidecars before
//     media in Result.Artifacts.
//   - MediaArtifacts collects this output's media artifacts in
//     publication order. They are appended to Result.Artifacts after
//     all sidecars.
//   - Prints collects this output's print outputs in the order
//     determined by the print stage sequence.
//   - Bytes is this output's published byte total. The full Result.Bytes is
//     the sum of all published media plus sidecars
//     counted by their kind and the shared-ownership rule.
type outputLifecycle struct {
	Index       int
	Plan        mediaformat.OutputPlan
	Info        value.Info
	Destination string
	// Section, when set, routes the download through the ffmpeg section
	// consumer instead of the ordinary downloader. The bounds are the
	// effective (composed) section for this lifecycle.
	Section        *sections.Section
	MediaPath      string
	FinalPath      string
	Sidecars       []Artifact
	MediaArtifacts []Artifact
	Prints         []PrintOutput
	Bytes          int64
	Downloaded     bool
}

// newOutputLifecycleForPlan builds the mutable lifecycle for a single
// plan. The plan is stored by value; Info is a defensive clone
// produced by selectedPlanInfo so each lifecycle may mutate Info
// without affecting siblings or the canonical extractor-owned info.
func newOutputLifecycleForPlan(
	index int,
	plan mediaformat.OutputPlan,
	info value.Info,
	destination string,
) outputLifecycle {
	lifecycle := outputLifecycle{
		Index:       index,
		Plan:        plan,
		Info:        selectedPlanInfo(info, plan),
		Destination: destination,
	}
	lifecycle.MediaPath = destination
	lifecycle.FinalPath = destination
	return lifecycle
}

// outputPreDownloadArtifact captures a sidecar that the lifecycle
// writes before any media publication. The authoritative order is
// thumbnails, related files, subtitles,
// converted subtitles, print files. Media follows after every plan's
// pre-download sidecars.
type outputPreDownloadArtifact struct {
	Artifact Artifact
	Bytes    int64
}

// executeOutputLifecycle runs the per-output stages for a single plan
// and returns the resulting lifecycle. Errors wrap the underlying
// cause with %w so categorised error categories remain detectable via
// errors.Is.
//
// The transaction owns media publication and sidecar overwrite protection.
// All filesystem writes for this output are routed through the active
// transaction or registered as transaction-owned files.
func (operation *operation) executeOutputLifecycle(
	ctx context.Context,
	transaction *mediaTransaction,
	lifecycle *outputLifecycle,
	sink events.Sink,
) error {
	if err := operation.executeOutputLifecyclePhases(ctx, transaction, lifecycle, sink); err != nil {
		return err
	}
	if err := operation.runLifecycleAfterPrints(ctx, transaction, lifecycle); err != nil {
		return err
	}
	if err := operation.accountLifecycleArtifacts(lifecycle); err != nil {
		return err
	}
	lifecycle.Downloaded = true
	return nil
}

// executeOutputLifecyclePhases runs the pre-download prints and the
// download stage for one plan. Callers that need to run entry-scoped
// post-process stages between download and after-prints (the
// historical single-output order: download → postprocessors →
// chapter cuts → embeds → after-prints) call this helper, run their
// entry-scoped stages, and then invoke runLifecycleAfterPrints.
//
// executeOutputLifecyclePhases preserves the historical stage order
// for the pre-download phases: PrintVideo, then PrintBeforeDL, then
// download. PrintVideo and PrintBeforeDL match the existing
// single-output flow exactly; the print file artifacts are appended
// to lifecycle.Sidecars and registered with the transaction.
func (operation *operation) executeOutputLifecyclePhases(
	ctx context.Context,
	transaction *mediaTransaction,
	lifecycle *outputLifecycle,
	sink events.Sink,
) error {
	if lifecycle == nil {
		return fmt.Errorf("%w: nil lifecycle", errLifecycleInternal)
	}
	if transaction == nil {
		return fmt.Errorf("%w: nil transaction", errLifecycleInternal)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := operation.runLifecyclePreDownloadPrints(ctx, transaction, lifecycle); err != nil {
		return err
	}
	if err := operation.runLifecycleDownload(ctx, transaction, lifecycle, sink); err != nil {
		return err
	}
	return nil
}

// runLifecyclePreDownloadPrints emits PrintVideo and PrintBeforeDL for
// this lifecycle with its own Info and destination. Print file
// artifacts participate in the transaction: each printed file
// is tracked by the transaction so rollback can clean up partially-written
// print files or restore an append target.
func (operation *operation) runLifecyclePreDownloadPrints(
	ctx context.Context,
	transaction *mediaTransaction,
	lifecycle *outputLifecycle,
) error {
	for _, stage := range []PrintStage{PrintVideo, PrintBeforeDL} {
		if err := operation.runLifecyclePrintStage(ctx, transaction, lifecycle, stage, lifecycle.Destination); err != nil {
			return err
		}
	}
	return nil
}

// runLifecyclePrintStage runs a single print stage for the lifecycle.
// It captures the print outputs, writes any print file artifacts,
// registers them with the transaction, and appends them to the
// lifecycle's Sidecars and Prints slices.
//
// The caller may invoke this helper individually (so the
// single-output product path can interleave PrintVideo before
// thumbnails and PrintBeforeDL after related files, preserving the
// historical artifact ordering) or as part of the higher-level
// runLifecyclePreDownloadPrints / runLifecycleAfterPrints helpers.
func (operation *operation) runLifecyclePrintStage(
	ctx context.Context,
	transaction *mediaTransaction,
	lifecycle *outputLifecycle,
	stage PrintStage,
	filename string,
) error {
	plan := lifecycle.Plan
	// capturePrints and writePrintFiles expect a *OutputPlan that is
	// non-nil only for multi-track plans (matching the historical
	// singlePrintPlan logic in processMedia). For single-track
	// plans they take selections directly so the URL/format
	// fields are populated from the merged selection.
	var printPlan *mediaformat.OutputPlan
	if len(plan.Tracks) > 1 {
		printPlan = &plan
	}
	prints, err := operation.capturePrints(
		ctx, stage, lifecycle.Info, printPlan, plan.Tracks, filename,
	)
	if err != nil {
		return wrapLifecycleError("render "+string(stage)+" print", err)
	}
	lifecycle.Prints = append(lifecycle.Prints, prints...)
	fileArtifacts, fileBytes, err := operation.writePrintFiles(
		ctx, stage, lifecycle.Info, printPlan, plan.Tracks, filename,
	)
	if err != nil {
		return wrapLifecycleError("write "+string(stage)+" print file", err)
	}
	trackTransactionArtifacts(transaction, fileArtifacts)
	lifecycle.Sidecars = mergePrintArtifacts(lifecycle.Sidecars, fileArtifacts)
	lifecycle.Bytes += fileBytes
	return nil
}

// runLifecycleDownload uses the N-track executor (`downloadSelections`) to
// materialize this output's media. The transaction records the produced path
// so rollback covers it on
// failure.
func (operation *operation) runLifecycleDownload(
	ctx context.Context,
	transaction *mediaTransaction,
	lifecycle *outputLifecycle,
	sink events.Sink,
) error {
	var path string
	var err error
	if lifecycle.Section != nil {
		path, _, err = operation.sectionDownloadSelections(
			ctx,
			lifecycle.Plan.Tracks,
			*lifecycle.Section,
			lifecycle.Destination,
			operation.request.Overwrite,
			operation.request.ForceKeyframesAtCuts,
			sink,
		)
		if err != nil {
			return wrapLifecycleError("download selected section", err)
		}
	} else {
		path, _, err = operation.downloadSelections(
			ctx,
			lifecycle.Plan.Tracks,
			operation.request.outputRoot(OutputPathHome),
			lifecycle.Destination,
			sink,
		)
		if err != nil {
			return wrapLifecycleError("download selected formats", err)
		}
	}
	transaction.markPublished(path)
	lifecycle.MediaPath = path
	lifecycle.FinalPath = path
	lifecycle.MediaArtifacts = append(lifecycle.MediaArtifacts, Artifact{Path: path, Kind: "media"})
	lifecycle.Info.Set("filepath", value.String(path))
	return nil
}

// runLifecycleAfterPrints emits PrintPostProcess, PrintAfterMove, and
// PrintAfterVideo for this lifecycle using the final path and the
// lifecycle's final Info. Print files are registered with the
// transaction for ownership-aware rollback.
func (operation *operation) runLifecycleAfterPrints(
	ctx context.Context,
	transaction *mediaTransaction,
	lifecycle *outputLifecycle,
) error {
	for _, stage := range []PrintStage{PrintPostProcess, PrintAfterMove, PrintAfterVideo} {
		if err := operation.runLifecyclePrintStage(ctx, transaction, lifecycle, stage, lifecycle.FinalPath); err != nil {
			return err
		}
	}
	return nil
}

// accountLifecycleArtifacts recomputes the lifecycle's byte total so
// it reflects only the published artifacts owned by this output. The
// final Result.Bytes is computed by the transaction using the
// authoritative artifact ordering (sidecars before media).
//
// Accounting is strict: every artifact the lifecycle registered must
// exist on disk once the lifecycle returns successfully. A missing
// artifact is an internal accounting error and aborts the
// transaction. The strict check catches lifecycle bookkeeping bugs
// (a recorded path that was never written, or a sidecar removed by
// a stray cleanup) before they leak into Result.Bytes or
// Result.Artifacts.
func (operation *operation) accountLifecycleArtifacts(lifecycle *outputLifecycle) error {
	var total int64
	for _, artifact := range append(append([]Artifact{}, lifecycle.Sidecars...), lifecycle.MediaArtifacts...) {
		info, err := os.Stat(artifact.Path)
		if err != nil {
			return wrapLifecycleError(
				"account lifecycle artifact "+artifact.Path,
				fmt.Errorf("%w: %v", errMissingLifecycleArtifact, err),
			)
		}
		total += info.Size()
	}
	lifecycle.Bytes = total
	return nil
}

// lifecycleResult aggregates one or more output lifecycles into the
// fields the public Result exposes.
//
// Aggregation matches the authoritative artifact ordering:
//
//  1. prints in plan order;
//  2. Result.Artifacts = sidecars of plan 1, sidecars of plan 2, ...,
//     media of plan 1, media of plan 2, ...;
//  3. Result.Filename = plan 0's FinalPath when Downloaded;
//  4. Result.Bytes = sum of per-lifecycle Bytes totals.
type lifecycleResult struct {
	Artifacts []Artifact
	Prints    []PrintOutput
	Bytes     int64
	Filename  string
}

// aggregateLifecycles converts a slice of completed lifecycles into
// the public Result payload, preserving the documented artifact order.
func aggregateLifecycles(lifecycles []outputLifecycle) lifecycleResult {
	result := lifecycleResult{}
	for index := range lifecycles {
		lifecycle := &lifecycles[index]
		result.Artifacts = append(result.Artifacts, lifecycle.Sidecars...)
		result.Prints = append(result.Prints, lifecycle.Prints...)
		result.Bytes += lifecycle.Bytes
		if index == 0 && lifecycle.Downloaded {
			result.Filename = lifecycle.FinalPath
		}
	}
	for index := range lifecycles {
		lifecycle := &lifecycles[index]
		result.Artifacts = append(result.Artifacts, lifecycle.MediaArtifacts...)
	}
	return result
}

// cleanLifecyclePath removes a single artifact path that this
// lifecycle wrote but did not publish (for example, an intermediate
// output replaced by a later step). Pre-existing files are never
// removed by this helper; the transaction guards that contract.
func cleanLifecyclePath(path string) {
	if path == "" {
		return
	}
	cleaned := filepath.Clean(path)
	if cleaned == "" || cleaned == "-" {
		return
	}
	_ = os.Remove(cleaned)
}

// executePlanLifecycle drives the complete product path for one output plan
// through the per-output lifecycle abstraction while preserving the
// historical single-output Result contract and extending it per plan:
//
//   - Result.Artifacts order:
//     PrintVideo print files, thumbnails, related files,
//     PrintBeforeDL print files, subtitles (downloaded + converted),
//     media (post-processed, cut, embedded).
//   - Result.Filename = the plan's post-processed media path.
//   - Result.Bytes = artifactBytes(result.Artifacts) when chapter
//     cuts or embeds happen, otherwise mediaArtifactBytes for the
//     post-processed media.
//   - Result.InfoJSON = the latest encoded info after every
//     transformation.
//   - Result.Prints = lifecycle.Prints (in stage order).
//
// The lifecycle rolls back failures at stages where the historical
// single-output path did so. processMedia additionally rolls back the shared
// transaction when any plan in a multi-output product fails, including the
// historical thumbnail-embed exception. The transaction owns every path the
// lifecycle or its sidecar stages write.
//
// Simulate mode short-circuits to PrintVideo-only: no thumbnails,
// related, subtitles, downloads, or post-process stages run. This
// matches the historical single-output Simulate contract.
func (operation *operation) executePlanLifecycle(
	ctx context.Context,
	transaction *mediaTransaction,
	lifecycle *outputLifecycle,
	selectedSubtitles []subtitleTrack,
	sink events.Sink,
) (Result, error) {
	var result Result
	fail := func(err error) (Result, error) {
		return rollbackTransactionResult(transaction, err)
	}
	mergeLifecyclePrintArtifacts := func() {
		result.Artifacts = mergePrintArtifacts(result.Artifacts, lifecycle.Sidecars)
	}
	registerArtifacts := func(artifacts []Artifact) {
		trackTransactionArtifacts(transaction, artifacts)
	}
	finish := func() (Result, error) {
		mergeLifecyclePrintArtifacts()
		lifecycle.Sidecars = lifecycle.Sidecars[:0]
		lifecycle.MediaArtifacts = lifecycle.MediaArtifacts[:0]
		for _, artifact := range result.Artifacts {
			if artifact.Kind == "media" {
				lifecycle.MediaArtifacts = append(lifecycle.MediaArtifacts, artifact)
			} else {
				lifecycle.Sidecars = append(lifecycle.Sidecars, artifact)
			}
		}
		if err := operation.accountLifecycleArtifacts(lifecycle); err != nil {
			return fail(err)
		}
		result.Bytes = lifecycle.Bytes
		encoded, err := encodeInfo(lifecycle.Info)
		if err != nil {
			return fail(err)
		}
		result.InfoJSON = encoded
		result.Downloaded = result.Downloaded || len(result.Artifacts) > 0 || lifecycle.Downloaded
		return result, nil
	}

	if operation.request.Simulate {
		if err := operation.runLifecyclePrintStage(ctx, transaction, lifecycle, PrintVideo, lifecycle.Destination); err != nil {
			return Result{}, categorized("render video print", err)
		}
		result.Prints = append(result.Prints, lifecycle.Prints...)
		return finish()
	}

	if len(selectedSubtitles) > 0 {
		requestedSubtitles := value.NewObject()
		cloned := make([]subtitleTrack, len(selectedSubtitles))
		for index, track := range selectedSubtitles {
			cloned[index] = track
			cloned[index].headers = track.headers.Clone()
			cloned[index].metadata = track.metadata.Clone()
			requestedSubtitles.Set(track.language, value.ObjectValue(cloned[index].metadata))
		}
		selectedSubtitles = cloned
		lifecycle.Info.Set("requested_subtitles", value.ObjectValue(requestedSubtitles))
	}

	// Phase 1: PrintVideo (before thumbnails, related, subtitles).
	if err := operation.runLifecyclePrintStage(ctx, transaction, lifecycle, PrintVideo, lifecycle.Destination); err != nil {
		return Result{}, categorized("render video print", err)
	}
	mergeLifecyclePrintArtifacts()

	// Phase 2: entry-scoped pre-download sidecars (thumbnails,
	// related files, subtitles). These remain entry-scoped until
	// Phases 11-13 move them into the lifecycle.
	thumbnailArtifacts, _, err := operation.writeThumbnails(ctx, &lifecycle.Info, false)
	if err != nil {
		return fail(categorized("write thumbnails", err))
	}
	registerArtifacts(thumbnailArtifacts)
	result.Artifacts = append(result.Artifacts, thumbnailArtifacts...)

	relatedArtifacts, _, err := operation.writeRelatedFiles(ctx, lifecycle.Info, false)
	if err != nil {
		return fail(categorized("write related files", err))
	}
	registerArtifacts(relatedArtifacts)
	result.Artifacts = append(result.Artifacts, relatedArtifacts...)

	// Phase 3: PrintBeforeDL (after thumbnails/related).
	if err := operation.runLifecyclePrintStage(ctx, transaction, lifecycle, PrintBeforeDL, lifecycle.Destination); err != nil {
		return Result{}, categorized("render before-download print", err)
	}
	mergeLifecyclePrintArtifacts()

	subtitleArtifacts, _, err := operation.downloadSubtitles(ctx, lifecycle.Info, selectedSubtitles, operation.eventSink())
	if err != nil {
		return fail(categorized("download subtitles", err))
	}
	registerArtifacts(subtitleArtifacts)
	result.Artifacts = append(result.Artifacts, subtitleArtifacts...)

	selectedSubtitles, result.Artifacts, _, err = operation.convertSelectedSubtitles(
		ctx, selectedSubtitles, result.Artifacts, operation.eventSink(),
	)
	if err != nil {
		return fail(categorized("convert subtitles", err))
	}
	registerArtifacts(result.Artifacts)

	if operation.request.SkipDownload {
		// After-prints still run for skip-download mode.
		if err := operation.runLifecycleAfterPrints(ctx, transaction, lifecycle); err != nil {
			return fail(err)
		}
		result.Prints = append(result.Prints, lifecycle.Prints...)
		return finish()
	}

	// The transaction protects overwrite targets before a producer mutates them.
	// Media destinations were acquired as one set by processMedia; derived
	// postprocessor outputs are plan-specific and are protected here before the
	// download/postprocessor chain starts.
	postprocessorPaths, err := operation.postprocessorDestinations(lifecycle.Destination)
	if err != nil {
		return fail(categorized("preflight postprocessor destinations", err))
	}
	for _, path := range postprocessorPaths {
		if err := transaction.protectPath(path, operation.request.postprocessorOverwrites()); err != nil {
			return fail(categorized("prepare postprocessor destination", err))
		}
	}

	// Phase 4: download via the lifecycle.
	if err := operation.runLifecycleDownload(ctx, transaction, lifecycle, sink); err != nil {
		return fail(categorized("download selected formats", err))
	}

	// Phase 5: entry-scoped post-process stages using lifecycle.MediaPath.
	outputDir := operation.request.outputRoot(OutputPathHome)
	var mediaArtifacts []Artifact
	lifecycle.MediaPath, mediaArtifacts, err = operation.applyPostprocessors(ctx, outputDir, lifecycle.MediaPath, sink)
	if err != nil {
		return fail(categorized("run postprocessors", err))
	}
	trackTransactionArtifacts(transaction, mediaArtifacts)
	lifecycle.MediaArtifacts = mediaArtifacts
	lifecycle.FinalPath = lifecycle.MediaPath
	// Update InfoJSON with the postprocessed extension and path.
	if extension := strings.TrimPrefix(filepath.Ext(lifecycle.MediaPath), "."); extension != "" {
		lifecycle.Info.Set("ext", value.String(extension))
	}
	lifecycle.Info.Set("filepath", value.String(lifecycle.MediaPath))
	result.Artifacts = append(result.Artifacts, mediaArtifacts...)

	// mediaArtifactStart records the slice index where post-process
	// media artifacts begin. The post-process stages (subtitle embedding,
	// chapter cuts, metadata embedding, thumbnail embedding) append more artifacts
	// to result.Artifacts; the byte accounting below needs to know
	// which slice to read from.
	mediaArtifactStart := len(result.Artifacts) - len(mediaArtifacts)
	// Pinned order: subtitles must be inside the container before
	// ModifyChapters cuts it; metadata/chapters are written afterward using the
	// final post-cut timeline.
	var embeddedSubtitles bool
	result.Artifacts, embeddedSubtitles, err = operation.embedSelectedSubtitles(
		ctx, &lifecycle.Info, lifecycle.MediaPath, selectedSubtitles, result.Artifacts, sink,
	)
	if err != nil {
		return fail(categorized("embed subtitles", err))
	}
	registerArtifacts(result.Artifacts)

	var cutApplied bool
	lifecycle.MediaPath, result.Artifacts, cutApplied, err = operation.applyChapterCuts(ctx, &lifecycle.Info, lifecycle.MediaPath, result.Artifacts, sink)
	if err != nil {
		return fail(err)
	}
	registerArtifacts(result.Artifacts)
	lifecycle.FinalPath = lifecycle.MediaPath

	var embeddedMetadata bool
	embeddedMetadata, err = operation.applyAutomaticMetadataEmbedding(ctx, lifecycle.Info, lifecycle.MediaPath, sink)
	if err != nil {
		return fail(categorized("embed metadata", err))
	}

	var embeddedThumbnail bool
	result.Artifacts, embeddedThumbnail, err = operation.embedSelectedThumbnail(
		ctx, &lifecycle.Info, lifecycle.MediaPath, result.Artifacts, sink,
	)
	if err != nil {
		return fail(categorized("embed thumbnail", err))
	}
	registerArtifacts(result.Artifacts)
	chapterArtifacts, err := operation.splitChapters(ctx, lifecycle.Info, lifecycle.MediaPath, sink)
	if err != nil {
		return fail(categorized("split chapters", err))
	}
	registerArtifacts(chapterArtifacts)
	result.Artifacts = append(result.Artifacts, chapterArtifacts...)
	if err := operation.applyXattrs(ctx, lifecycle.Info, lifecycle.MediaPath); err != nil {
		return fail(categorized("write xattrs", err))
	}

	// Apply the media timestamp only after every media-mutating postprocessor
	// has completed. This is deliberately per lifecycle so multi-output runs
	// update each media destination exactly once; sidecar artifacts are never
	// touched. A failure is routed through fail so the shared transaction rolls
	// back the media and any sidecars produced by this entry.
	if err := operation.applyOutputMtime(lifecycle.FinalPath, lifecycle.Info); err != nil {
		return fail(categorized("set output mtime", err))
	}

	result.Downloaded = true
	result.Filename = lifecycle.FinalPath
	_ = cutApplied
	_ = embeddedSubtitles
	_ = embeddedMetadata
	_ = embeddedThumbnail
	_ = mediaArtifactStart

	// Phase 6: after-download prints via the lifecycle.
	if err := operation.runLifecycleAfterPrints(ctx, transaction, lifecycle); err != nil {
		return fail(err)
	}

	result.Prints = append(result.Prints, lifecycle.Prints...)
	return finish()
}
