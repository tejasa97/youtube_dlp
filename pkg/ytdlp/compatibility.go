package ytdlp

import (
	"context"
	"fmt"
	"os"

	"github.com/ytdlp-go/ytdlp/internal/compat/matchfilter"
	compatmetadata "github.com/ytdlp-go/ytdlp/internal/compat/metadata"
	"github.com/ytdlp-go/ytdlp/internal/compat/progress"
	"github.com/ytdlp-go/ytdlp/internal/events"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

type compatibilityPlan struct {
	selector         *mediaformat.Selector
	formatOptions    mediaformat.Options
	matchFilter      matchfilter.Program
	breakMatchFilter matchfilter.Program
	metadataActions  []compatmetadata.Action
	progressTemplate string
}

func prepareCompatibility(request Request) (compatibilityPlan, error) {
	plan := compatibilityPlan{progressTemplate: request.ProgressTemplate}
	if request.Format != "" {
		selector, err := mediaformat.ParseSelector(request.Format)
		if err != nil {
			return compatibilityPlan{}, categorized("parse format selector", err)
		}
		plan.selector = &selector
	}
	sortFields, err := mediaformat.ParseSortFields(request.FormatSort)
	if err != nil {
		return compatibilityPlan{}, categorized("parse format sorting", err)
	}
	plan.formatOptions = mediaformat.Options{
		Sort: sortFields, PreferFreeFormats: request.PreferFreeFormats,
		PreferExtensions: append([]string(nil), request.PreferredExtensions...),
		AllowDRM:         request.AllowUnplayableFormats,
	}
	plan.matchFilter, err = matchfilter.Parse(request.MatchFilters)
	if err != nil {
		return compatibilityPlan{}, categorized("parse match filter", err)
	}
	plan.breakMatchFilter, err = matchfilter.Parse(request.BreakMatchFilters)
	if err != nil {
		return compatibilityPlan{}, categorized("parse break match filter", err)
	}
	for _, specification := range request.ParseMetadata {
		action, parseErr := compatmetadata.ParseFromField(specification)
		if parseErr != nil {
			return compatibilityPlan{}, categorized("parse metadata action", parseErr)
		}
		plan.metadataActions = append(plan.metadataActions, action)
	}
	for _, specification := range request.ReplaceMetadata {
		action, parseErr := compatmetadata.ParseReplace(specification)
		if parseErr != nil {
			return compatibilityPlan{}, categorized("parse metadata replacement", parseErr)
		}
		plan.metadataActions = append(plan.metadataActions, action)
	}
	if request.ProgressTemplate != "" {
		if _, err := progress.Render(request.ProgressTemplate, progress.Snapshot{}); err != nil {
			return compatibilityPlan{}, categorized("parse progress template", err)
		}
	}
	return plan, nil
}

func (operation *operation) applyCompatibility(ctx context.Context, ctxInfo *value.Info, incomplete bool) (matchfilter.Decision, error) {
	result, err := compatmetadata.Apply(ctxInfo, operation.compatibility.metadataActions)
	if err != nil {
		return matchfilter.Decision{}, categorized("apply metadata actions", err)
	}
	for _, warning := range result.Warnings {
		if err := operation.client.emit(ctx, Event{Kind: EventMetadataWarning, Message: warning}); err != nil {
			return matchfilter.Decision{}, &Error{Category: ErrorInternal, Op: "emit metadata warning", Err: err}
		}
	}
	options := matchfilter.EvaluationOptions{IncompleteAll: incomplete}
	breakDecision, err := operation.compatibility.breakMatchFilter.EvaluateContext(
		ctx,
		*ctxInfo,
		options,
	)
	if err != nil {
		return matchfilter.Decision{}, categorized("evaluate break match filter", err)
	}
	if !breakDecision.Pass {
		operation.breakMatchTriggered = true
		operation.breakMatchReason = breakDecision.Reason
		return breakDecision, nil
	}
	decision, err := operation.compatibility.matchFilter.EvaluateContext(
		ctx,
		*ctxInfo,
		options,
	)
	if err != nil {
		return matchfilter.Decision{}, categorized("evaluate match filter", err)
	}
	return decision, nil
}

func (operation *operation) selectFormats(info value.Info) ([]mediaformat.Selection, error) {
	plans, err := operation.planFormats(info)
	if err != nil {
		return nil, err
	}
	if len(plans) != 1 {
		return nil, fmt.Errorf("%w: selector yields %d independent outputs", mediaformat.ErrMultiOutput, len(plans))
	}
	if len(plans[0].Tracks) == 0 {
		return nil, fmt.Errorf("%w: selector returned no formats", mediaformat.ErrNoFormats)
	}
	return plans[0].Tracks, nil
}

func (operation *operation) planFormats(info value.Info) ([]mediaformat.OutputPlan, error) {
	if operation.compatibility.selector == nil {
		selected, err := mediaformat.Default(info, operation.compatibility.formatOptions)
		if err != nil {
			return nil, err
		}
		return []mediaformat.OutputPlan{{Tracks: selected}}, nil
	}
	return mediaformat.PlanSelectWithOptions(info, *operation.compatibility.selector, operation.compatibility.formatOptions)
}

// validateMultiOutputProduct rejects multi-plan downloads when requested
// product stages cannot be applied safely to every output. Multi-output
// execution supports only the no-postprocessor download path. After-download
// print stages intentionally render only the first plan's selections and primary path.
func validateMultiOutputProduct(request Request, planCount int) error {
	if planCount <= 1 {
		return nil
	}
	if len(request.Postprocessors) > 0 {
		return fmt.Errorf("%w: postprocessors with multi-output selectors", mediaformat.ErrMultiOutput)
	}
	if request.SponsorBlock.Enabled && request.SponsorBlock.Remove {
		return fmt.Errorf("%w: SponsorBlock remove with multi-output selectors", mediaformat.ErrMultiOutput)
	}
	if request.Subtitles.Embed {
		return fmt.Errorf("%w: subtitle embedding with multi-output selectors", mediaformat.ErrMultiOutput)
	}
	return nil
}

func mediaArtifactBytes(artifacts []Artifact) (int64, error) {
	var total int64
	for _, artifact := range artifacts {
		if artifact.Kind != "media" {
			continue
		}
		info, err := os.Stat(artifact.Path)
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

func (operation *operation) eventSink() events.Sink {
	return events.SinkFunc(func(ctx context.Context, event events.Event) error {
		message := event.Message
		if operation.compatibility.progressTemplate != "" {
			rendered, err := progress.Render(operation.compatibility.progressTemplate, progress.Snapshot{
				Status: string(event.Kind), Filename: event.Path,
				DownloadedBytes: event.Bytes, TotalBytes: event.Total,
			})
			if err != nil {
				return err
			}
			message = rendered
		}
		return operation.client.emit(ctx, Event{
			Kind: string(event.Kind), URL: network.RedactRawURL(event.URL), Path: event.Path,
			Bytes: event.Bytes, Total: event.Total, Attempt: event.Attempt,
			Resuming: event.Resuming, Message: message, Fragment: event.Fragment,
			Fragments: event.Fragments,
		})
	})
}
