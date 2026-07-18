package ytdlp

import (
	"context"
	"fmt"

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

func (operation *operation) applyCompatibility(ctx context.Context, ctxInfo *value.Info) (matchfilter.Decision, error) {
	result, err := compatmetadata.Apply(ctxInfo, operation.compatibility.metadataActions)
	if err != nil {
		return matchfilter.Decision{}, categorized("apply metadata actions", err)
	}
	for _, warning := range result.Warnings {
		if err := operation.client.emit(ctx, Event{Kind: EventMetadataWarning, Message: warning}); err != nil {
			return matchfilter.Decision{}, &Error{Category: ErrorInternal, Op: "emit metadata warning", Err: err}
		}
	}
	return operation.compatibility.matchFilter.Evaluate(*ctxInfo, false), nil
}

func (operation *operation) selectFormats(info value.Info) ([]mediaformat.Selection, error) {
	if operation.compatibility.selector == nil {
		selected, err := mediaformat.Best(info)
		if err != nil {
			return nil, err
		}
		return []mediaformat.Selection{selected}, nil
	}
	selected, err := mediaformat.SelectWithOptions(info, *operation.compatibility.selector, operation.compatibility.formatOptions)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("%w: selector returned no formats", mediaformat.ErrNoFormats)
	}
	return selected, nil
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
