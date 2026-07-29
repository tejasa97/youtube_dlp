package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ytdlp-go/ytdlp/internal/events"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

// Sentinel errors used by executeOutputLifecycle so callers can
// categorize failures consistently with processMedia's existing
// categorized helper.
var (
	errLifecycleInternal = errors.New("output lifecycle")
)

// outputLifecycle holds the mutable per-output state for one OutputPlan.
//
// PR 8 introduces this internal type so that the complete media
// lifecycle can run once per plan without sharing mutable state across
// outputs. The public Result model is still defined by PR 7; this
// struct never escapes the package.
//
// Lifecycle invariants:
//
//   - Plan is the immutable OutputPlan returned by the planner.
//   - Info is a defensive per-output clone. Mutating Info must not
//     affect the canonical extractor-owned info, the canonical
//     prepared formats, OutputPlan.Metadata, or another lifecycle's
//     Info.
//   - Destination is the per-plan path resolved by PR 7's
//     resolveOutputPlanDestinations. StagedPath, MediaPath, and
//     FinalPath are local to this lifecycle.
//   - Subtitles is a per-output selection. PR 7 deduplicates immutable
//     downloads under its shared-ownership rule; this lifecycle only
//     writes or removes paths it owns.
//   - Artifacts lists this output's published files. The PR 7
//     transaction aggregates them into Result.Artifacts.
//   - Bytes is this output's published byte total. The PR 7
//     transaction produces the final Result.Bytes from these per-output
//     totals.
type outputLifecycle struct {
	Index       int
	Plan        mediaformat.OutputPlan
	Info        value.Info
	Destination string
	StagedPath  string
	MediaPath   string
	FinalPath   string
	Subtitles   []subtitleTrack
	Artifacts   []Artifact
	Prints      []PrintOutput
	Bytes       int64
	Downloaded  bool
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

// executeOutputLifecycle runs the per-output stages for a single plan.
//
// The full PR 8 lifecycle is large; this initial implementation runs
// the per-output stages that PR 7 already isolated (prints, downloads,
// InfoJSON encoding) and delegates stages that remain entry-scoped
// (postprocessors, chapter cuts, subtitle/thumbnail embedding,
// sidecars) to the existing entry-scoped helpers with the
// lifecycle's own Info and destination. Future PR 8 commits move each
// of those entry-scoped stages behind this function.
//
// The transaction is used only for ownership tracking
// (transaction.recordCreated). All filesystem writes for this output
// are routed through the PR 7 transaction or registered as
// transaction-owned files. The lifecycle does not call
// transaction.rollback; the caller in processMedia drives rollback.
func (operation *operation) executeOutputLifecycle(
	ctx context.Context,
	transaction mediaTransaction,
	lifecycle *outputLifecycle,
	sink events.Sink,
) error {
	if lifecycle == nil {
		return fmt.Errorf("%w: nil lifecycle", errLifecycleInternal)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	plan := lifecycle.Plan
	if err := operation.runLifecyclePreDownloadPrints(ctx, lifecycle); err != nil {
		return err
	}
	if err := operation.runLifecycleDownload(ctx, transaction, lifecycle, sink); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := operation.runLifecycleAfterPrints(ctx, lifecycle); err != nil {
		return err
	}
	if err := operation.accountLifecycleArtifacts(lifecycle); err != nil {
		return err
	}
	lifecycle.Downloaded = true
	_ = plan
	return nil
}

// runLifecyclePreDownloadPrints emits PrintVideo and PrintBeforeDL for
// this lifecycle with its own Info and destination.
func (operation *operation) runLifecyclePreDownloadPrints(
	ctx context.Context,
	lifecycle *outputLifecycle,
) error {
	plan := lifecycle.Plan
	for _, stage := range []PrintStage{PrintVideo, PrintBeforeDL} {
		prints, err := operation.capturePrints(
			ctx, stage, lifecycle.Info, &plan, plan.Tracks, lifecycle.Destination,
		)
		if err != nil {
			return fmt.Errorf("%w: render %s: %v", errLifecycleInternal, stage, err)
		}
		lifecycle.Prints = append(lifecycle.Prints, prints...)
		fileArtifacts, fileBytes, err := operation.writePrintFiles(
			ctx, stage, lifecycle.Info, &plan, plan.Tracks, lifecycle.Destination,
		)
		if err != nil {
			return fmt.Errorf("%w: write %s: %v", errLifecycleInternal, stage, err)
		}
		lifecycle.Artifacts = append(lifecycle.Artifacts, fileArtifacts...)
		lifecycle.Bytes += fileBytes
	}
	return nil
}

// runLifecycleDownload uses the PR 6 N-track executor
// (downloadSelections) to materialize this output's media. The
// transaction records the produced path so PR 7 rollback covers it on
// failure.
func (operation *operation) runLifecycleDownload(
	ctx context.Context,
	transaction mediaTransaction,
	lifecycle *outputLifecycle,
	sink events.Sink,
) error {
	path, _, err := operation.downloadSelections(
		ctx,
		lifecycle.Plan.Tracks,
		operation.request.outputRoot(OutputPathHome),
		lifecycle.Destination,
		sink,
	)
	if err != nil {
		return fmt.Errorf("%w: download: %v", errLifecycleInternal, err)
	}
	transaction.recordCreated(path)
	lifecycle.MediaPath = path
	lifecycle.FinalPath = path
	lifecycle.Artifacts = append(lifecycle.Artifacts, Artifact{Path: path, Kind: "media"})
	lifecycle.Info.Set("filepath", value.String(path))
	return nil
}

// runLifecycleAfterPrints emits PrintPostProcess, PrintAfterMove, and
// PrintAfterVideo for this lifecycle using the final path and the
// lifecycle's final Info.
func (operation *operation) runLifecycleAfterPrints(
	ctx context.Context,
	lifecycle *outputLifecycle,
) error {
	plan := lifecycle.Plan
	for _, stage := range []PrintStage{PrintPostProcess, PrintAfterMove, PrintAfterVideo} {
		prints, err := operation.capturePrints(
			ctx, stage, lifecycle.Info, &plan, plan.Tracks, lifecycle.FinalPath,
		)
		if err != nil {
			return fmt.Errorf("%w: render %s: %v", errLifecycleInternal, stage, err)
		}
		lifecycle.Prints = append(lifecycle.Prints, prints...)
		fileArtifacts, fileBytes, err := operation.writePrintFiles(
			ctx, stage, lifecycle.Info, &plan, plan.Tracks, lifecycle.FinalPath,
		)
		if err != nil {
			return fmt.Errorf("%w: write %s: %v", errLifecycleInternal, stage, err)
		}
		lifecycle.Artifacts = append(lifecycle.Artifacts, fileArtifacts...)
		lifecycle.Bytes += fileBytes
	}
	return nil
}

// accountLifecycleArtifacts recomputes the lifecycle's byte total so
// it reflects only published artifacts. The final Result.Bytes is
// computed by PR 7's transaction, but each lifecycle must report its
// own total so the aggregation is straightforward.
func (operation *operation) accountLifecycleArtifacts(lifecycle *outputLifecycle) error {
	var total int64
	for _, artifact := range lifecycle.Artifacts {
		if artifact.Kind != "media" {
			continue
		}
		info, err := os.Stat(artifact.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("%w: stat %s: %v", errLifecycleInternal, artifact.Path, err)
		}
		total += info.Size()
	}
	lifecycle.Bytes = total
	return nil
}

// lifecycleResult aggregates one or more output lifecycles into the
// fields the public Result exposes. Aggregation order is determined by
// the PR 7 contract: prints in plan order, sidecars before media in
// Result.Artifacts, Bytes summing published media sizes.
type lifecycleResult struct {
	Artifacts []Artifact
	Prints    []PrintOutput
	Bytes     int64
	Filename  string
}

// aggregateLifecycles converts a slice of completed lifecycles into the
// public Result payload. The first plan's media path becomes
// Result.Filename (matching PR 7). Artifacts are concatenated in
// planner order; each lifecycle's slice already groups its sidecars
// before its media. Bytes are summed.
func aggregateLifecycles(lifecycles []outputLifecycle) lifecycleResult {
	result := lifecycleResult{}
	for index := range lifecycles {
		lifecycle := &lifecycles[index]
		result.Artifacts = append(result.Artifacts, lifecycle.Artifacts...)
		result.Prints = append(result.Prints, lifecycle.Prints...)
		result.Bytes += lifecycle.Bytes
		if index == 0 && lifecycle.Downloaded {
			result.Filename = lifecycle.FinalPath
		}
	}
	return result
}
