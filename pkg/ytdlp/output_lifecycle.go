package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ytdlp-go/ytdlp/internal/events"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

// errLifecycleInternal is the wrapped sentinel that lets callers detect
// lifecycle failures via errors.Is. It never appears as a plain
// surface error: every lifecycle failure wraps an underlying error with
// %w so categorised error categories (network/auth/security/etc.) and
// identity checks (errors.Is(err, ErrDestinationExists) and friends)
// continue to work.
var errLifecycleInternal = errors.New("output lifecycle")

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
// PR 8 introduces this internal type so that the complete media
// lifecycle can run once per plan without sharing mutable state across
// outputs. The public Result model is still defined by PR 7; this
// struct never escapes the package.
//
// Fields:
//
//   - Index is the plan's stable index in the planner result.
//   - Plan is the immutable OutputPlan returned by the planner.
//   - Info is a defensive per-output clone. Mutating Info must not
//     affect the canonical extractor-owned info, the canonical
//     prepared formats, OutputPlan.Metadata, or another lifecycle's
//     Info.
//   - Destination is the per-plan path resolved by PR 7's
//     resolveOutputPlanDestinations. The plan destination is both
//     the lifecycle's commit point and the path the executor writes
//     to; PR 7 does not separate staging from publication.
//   - MediaPath tracks the current media file for this output. It
//     advances through postprocessor outputs and cuts.
//   - FinalPath tracks the path the lifecycle reports as
//     Result.Filename when Index == 0.
//   - Sidecars collects this output's pre-download sidecars
//     (thumbnails, related files, subtitles, converted subtitles,
//     print files). The PR 7 result contract places sidecars before
//     media in Result.Artifacts.
//   - MediaArtifacts collects this output's media artifacts in
//     publication order. They are appended to Result.Artifacts after
//     all sidecars.
//   - Prints collects this output's print outputs in the order
//     determined by the print stage sequence.
//   - Bytes is this output's published byte total. PR 7's full
//     result.Bytes is the sum of all published media plus sidecars
//     counted by their kind and the shared-ownership rule.
type outputLifecycle struct {
	Index          int
	Plan           mediaformat.OutputPlan
	Info           value.Info
	Destination    string
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
// writes before any media publication. The slice order matches the
// PR 7 authoritative order: thumbnails, related files, subtitles,
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
// The transaction is used only for ownership tracking
// (transaction.recordCreated). All filesystem writes for this output
// are routed through the PR 7 transaction or registered as
// transaction-owned files. The lifecycle does not call
// transaction.rollback; the caller in processMedia drives rollback.
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
// to lifecycle.Sidecars and registered with the PR 7 transaction.
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
// artifacts participate in the PR 7 transaction: each printed file
// is registered with transaction.recordCreated so rollback can clean
// up partially-written print files.
func (operation *operation) runLifecyclePreDownloadPrints(
	ctx context.Context,
	transaction *mediaTransaction,
	lifecycle *outputLifecycle,
) error {
	plan := lifecycle.Plan
	for _, stage := range []PrintStage{PrintVideo, PrintBeforeDL} {
		prints, err := operation.capturePrints(
			ctx, stage, lifecycle.Info, &plan, plan.Tracks, lifecycle.Destination,
		)
		if err != nil {
			return wrapLifecycleError("render "+string(stage)+" print", err)
		}
		lifecycle.Prints = append(lifecycle.Prints, prints...)
		fileArtifacts, fileBytes, err := operation.writePrintFiles(
			ctx, stage, lifecycle.Info, &plan, plan.Tracks, lifecycle.Destination,
		)
		if err != nil {
			return wrapLifecycleError("write "+string(stage)+" print file", err)
		}
		for _, artifact := range fileArtifacts {
			transaction.recordCreated(artifact.Path)
		}
		lifecycle.Sidecars = append(lifecycle.Sidecars, fileArtifacts...)
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
	transaction *mediaTransaction,
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
		return wrapLifecycleError("download selected formats", err)
	}
	transaction.recordCreated(path)
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
	plan := lifecycle.Plan
	for _, stage := range []PrintStage{PrintPostProcess, PrintAfterMove, PrintAfterVideo} {
		prints, err := operation.capturePrints(
			ctx, stage, lifecycle.Info, &plan, plan.Tracks, lifecycle.FinalPath,
		)
		if err != nil {
			return wrapLifecycleError("render "+string(stage)+" print", err)
		}
		lifecycle.Prints = append(lifecycle.Prints, prints...)
		fileArtifacts, fileBytes, err := operation.writePrintFiles(
			ctx, stage, lifecycle.Info, &plan, plan.Tracks, lifecycle.FinalPath,
		)
		if err != nil {
			return wrapLifecycleError("write "+string(stage)+" print file", err)
		}
		for _, artifact := range fileArtifacts {
			transaction.recordCreated(artifact.Path)
		}
		lifecycle.Sidecars = append(lifecycle.Sidecars, fileArtifacts...)
		lifecycle.Bytes += fileBytes
	}
	return nil
}

// accountLifecycleArtifacts recomputes the lifecycle's byte total so
// it reflects only the published artifacts owned by this output. The
// final Result.Bytes is computed by PR 7's transaction using the
// authoritative artifact ordering (sidecars before media).
func (operation *operation) accountLifecycleArtifacts(lifecycle *outputLifecycle) error {
	var total int64
	for _, artifact := range append(append([]Artifact{}, lifecycle.Sidecars...), lifecycle.MediaArtifacts...) {
		info, err := os.Stat(artifact.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return wrapLifecycleError("stat "+artifact.Path, err)
		}
		total += info.Size()
	}
	lifecycle.Bytes = total
	return nil
}

// lifecycleResult aggregates one or more output lifecycles into the
// fields the public Result exposes.
//
// Aggregation matches the PR 7 authoritative artifact ordering:
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
// the public Result payload, preserving the PR 7 artifact order.
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
// removed by this helper; the PR 7 transaction guards that contract.
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
