package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/compat/chapterremove"
	"github.com/tejasa97/youtube_dlp/internal/compat/matchfilter"
	compatmetadata "github.com/tejasa97/youtube_dlp/internal/compat/metadata"
	"github.com/tejasa97/youtube_dlp/internal/compat/progress"
	"github.com/tejasa97/youtube_dlp/internal/compat/sections"
	"github.com/tejasa97/youtube_dlp/internal/compat/simplefilter"
	"github.com/tejasa97/youtube_dlp/internal/events"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

type compatibilityPlan struct {
	selector          *mediaformat.Selector
	formatOptions     mediaformat.Options
	matchFilter       matchfilter.Program
	breakMatchFilter  matchfilter.Program
	simpleFilter      *simplefilter.Checker
	interactive       interactiveMatchFilterKind
	interactiveFormat bool
	metadataActions   []compatmetadata.Action
	chapterRemoval    chapterremove.Program
	sections          sections.Program
	progressTemplate  string
}

type interactiveMatchFilterKind uint8

const (
	interactiveMatchFilterNone interactiveMatchFilterKind = iota
	interactiveMatchFilterOrdinary
	interactiveMatchFilterBreaking
)

type compatibilityDecision struct {
	matchfilter.Decision
	interactive interactiveMatchFilterKind
}

var interactiveIncompleteFormatFields = func() map[string]struct{} {
	fields := make(map[string]struct{}, len(selectedFormatFieldNames))
	for _, field := range selectedFormatFieldNames {
		fields[field] = struct{}{}
	}
	return fields
}()

func prepareCompatibility(request Request) (compatibilityPlan, error) {
	plan := compatibilityPlan{progressTemplate: request.ProgressTemplate}
	if request.Format == "-" {
		if request.InteractiveFormat == nil {
			return compatibilityPlan{}, categorized(
				"configure interactive format", fmt.Errorf("%w: prompt callback is required", ErrInteractiveInput),
			)
		}
		plan.interactiveFormat = true
	} else if request.Format != "" {
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
		Sort: sortFields, SortForce: request.FormatSortForce, PreferFreeFormats: request.PreferFreeFormats,
		PreferExtensions:          append([]string(nil), request.PreferredExtensions...),
		AllowDRM:                  request.AllowUnplayableFormats,
		AllowMultipleVideoStreams: request.AllowMultipleVideoStreams,
		AllowMultipleAudioStreams: request.AllowMultipleAudioStreams,
	}
	filters := make([]string, 0, len(request.MatchFilters))
	for _, filter := range request.MatchFilters {
		if filter == "-" {
			plan.interactive = interactiveMatchFilterOrdinary
			continue
		}
		filters = append(filters, filter)
	}
	breakFilters := make([]string, 0, len(request.BreakMatchFilters))
	for _, filter := range request.BreakMatchFilters {
		if filter == "-" {
			plan.interactive = interactiveMatchFilterBreaking
			continue
		}
		breakFilters = append(breakFilters, filter)
	}
	if plan.interactive != interactiveMatchFilterNone && request.InteractiveMatchFilter == nil {
		return compatibilityPlan{}, categorized(
			"configure interactive match filter",
			fmt.Errorf("%w: prompt callback is required", ErrInteractiveInput),
		)
	}
	plan.matchFilter, err = matchfilter.Parse(filters)
	if err != nil {
		return compatibilityPlan{}, categorized("parse match filter", err)
	}
	plan.breakMatchFilter, err = matchfilter.Parse(breakFilters)
	if err != nil {
		return compatibilityPlan{}, categorized("parse break match filter", err)
	}
	if request.SimpleFilters.hasSimpleFilters() {
		checker, filterErr := simplefilter.New(simplefilter.Options{
			MatchTitle: request.SimpleFilters.MatchTitle, RejectTitle: request.SimpleFilters.RejectTitle,
			Date: request.SimpleFilters.Date, DateAfter: request.SimpleFilters.DateAfter, DateBefore: request.SimpleFilters.DateBefore,
			MinViews: request.SimpleFilters.MinViews, MaxViews: request.SimpleFilters.MaxViews,
			AgeLimit: request.SimpleFilters.AgeLimit,
		})
		if filterErr != nil {
			return compatibilityPlan{}, categorized("parse simple filter", filterErr)
		}
		plan.simpleFilter = checker
	}
	plan.chapterRemoval, err = chapterremove.Parse(request.RemoveChapters)
	if err != nil {
		return compatibilityPlan{}, categorized("parse remove chapters", err)
	}
	plan.sections, err = sections.Parse(request.DownloadSections)
	if err != nil {
		return compatibilityPlan{}, categorized("parse download sections", err)
	}
	appendParse := func(specification string) error {
		action, parseErr := compatmetadata.ParseFromField(specification)
		if parseErr != nil {
			return categorized("parse metadata action", parseErr)
		}
		plan.metadataActions = append(plan.metadataActions, action)
		return nil
	}
	appendReplace := func(fields, search, replacement string) error {
		actions, parseErr := compatmetadata.ParseReplaceFields(fields, search, replacement)
		if parseErr != nil {
			return categorized("parse metadata replacement", parseErr)
		}
		plan.metadataActions = append(plan.metadataActions, actions...)
		return nil
	}
	for _, specification := range request.MetadataActions {
		switch specification.Kind {
		case MetadataActionParse:
			if err := appendParse(specification.Parse); err != nil {
				return compatibilityPlan{}, err
			}
		case MetadataActionReplace:
			if specification.Fields == "" {
				action, parseErr := compatmetadata.ParseReplace(specification.Parse)
				if parseErr != nil {
					return compatibilityPlan{}, categorized("parse metadata replacement", parseErr)
				}
				plan.metadataActions = append(plan.metadataActions, action)
			} else if err := appendReplace(specification.Fields, specification.Search, specification.Replacement); err != nil {
				return compatibilityPlan{}, err
			}
		default:
			return compatibilityPlan{}, categorized("parse metadata action", fmt.Errorf("unknown metadata action"))
		}
	}
	for _, specification := range request.ParseMetadata {
		if err := appendParse(specification); err != nil {
			return compatibilityPlan{}, err
		}
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

func (operation *operation) applyCompatibility(ctx context.Context, ctxInfo *value.Info, incomplete bool) (compatibilityDecision, error) {
	options := matchfilter.EvaluationOptions{IncompleteAll: incomplete}
	if !incomplete && operation.compatibility.interactive != interactiveMatchFilterNone {
		options.IncompleteFields = interactiveIncompleteFormatFields
	}
	// Simple filters (title/date/views/age) run before the generic
	// breaking and ordinary match filters, mirroring check_filter's order.
	if operation.compatibility.simpleFilter != nil {
		reason, rejected, checkErr := operation.compatibility.simpleFilter.Check(ctx, *ctxInfo)
		if checkErr != nil {
			return compatibilityDecision{}, categorized("evaluate simple filter", checkErr)
		}
		if rejected {
			return compatibilityDecision{Decision: matchfilter.Decision{Reason: reason}}, nil
		}
	}
	breakDecision, err := operation.compatibility.breakMatchFilter.EvaluateContext(
		ctx,
		*ctxInfo,
		options,
	)
	if err != nil {
		return compatibilityDecision{}, categorized("evaluate break match filter", err)
	}
	if !breakDecision.Pass {
		// --break-match-filter rejections always stop the run, regardless of
		// --break-on-reject (reference match_filter_func raises
		// RejectedVideoReached directly).
		operation.setStop(StopBreakMatchFilter, breakDecision.Reason)
		return compatibilityDecision{Decision: breakDecision}, nil
	}
	decision, err := operation.compatibility.matchFilter.EvaluateContext(
		ctx,
		*ctxInfo,
		options,
	)
	if err != nil {
		return compatibilityDecision{}, categorized("evaluate match filter", err)
	}
	resultDecision := compatibilityDecision{Decision: decision}
	if decision.Pass && !incomplete && operation.compatibility.interactive != interactiveMatchFilterNone {
		resultDecision.interactive = operation.compatibility.interactive
	}
	return resultDecision, nil
}

// applyMetadataActions runs the preprocessing metadata transforms only after
// entry selection. yt-dlp's _match_entry observes extractor metadata before
// pre_process, while later filename, format and output stages observe the
// transformed view.
func (operation *operation) applyMetadataActions(ctx context.Context, info *value.Info) error {
	result, err := compatmetadata.ApplyContext(ctx, info, operation.compatibility.metadataActions)
	if err != nil {
		return categorized("apply metadata actions", err)
	}
	for _, warning := range result.Warnings {
		if err := operation.client.emit(ctx, Event{Kind: EventMetadataWarning, Message: warning}); err != nil {
			return &Error{Category: ErrorInternal, Op: "emit metadata warning", Err: err}
		}
	}
	return nil
}

func (operation *operation) resolveInteractiveCompatibility(
	ctx context.Context,
	ctxInfo value.Info,
	decision compatibilityDecision,
	filename string,
) (matchfilter.Decision, error) {
	if decision.interactive == interactiveMatchFilterNone {
		return decision.Decision, nil
	}
	breakDecision, err := operation.compatibility.breakMatchFilter.EvaluateContext(
		ctx, ctxInfo, matchfilter.EvaluationOptions{},
	)
	if err != nil {
		return matchfilter.Decision{}, categorized("evaluate break match filter", err)
	}
	if !breakDecision.Pass {
		operation.setStop(StopBreakMatchFilter, breakDecision.Reason)
		return breakDecision, nil
	}
	if decision.interactive == interactiveMatchFilterBreaking {
		// Python's accepted breaking prompt returns before ordinary filters.
		return operation.promptInteractiveMatchFilter(ctx, ctxInfo, decision.interactive, filename)
	}
	ordinaryDecision, err := operation.compatibility.matchFilter.EvaluateContext(
		ctx, ctxInfo, matchfilter.EvaluationOptions{},
	)
	if err != nil {
		return matchfilter.Decision{}, categorized("evaluate match filter", err)
	}
	if !ordinaryDecision.Pass {
		return ordinaryDecision, nil
	}
	return operation.promptInteractiveMatchFilter(ctx, ctxInfo, decision.interactive, filename)
}

func (operation *operation) promptInteractiveMatchFilter(
	ctx context.Context,
	ctxInfo value.Info,
	kind interactiveMatchFilterKind,
	filename string,
) (matchfilter.Decision, error) {
	id, _ := ctxInfo.ID()
	title, _ := ctxInfo.Title()
	accepted, err := operation.request.InteractiveMatchFilter(ctx, InteractiveMatchFilterPrompt{
		ID: id, Title: title, Filename: filename,
	})
	if err != nil {
		if errors.Is(err, ErrInteractiveInput) ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return matchfilter.Decision{}, categorized("interactive match filter", err)
		}
		return matchfilter.Decision{}, &Error{
			Category: ErrorInternal, Op: "interactive match filter", Err: err,
		}
	}
	if !accepted {
		if title == "" {
			title = id
		}
		if title == "" {
			title = "entry"
		}
		rejection := matchfilter.Decision{Reason: "Skipping " + title}
		if kind == interactiveMatchFilterBreaking {
			operation.setStop(StopBreakMatchFilter, rejection.Reason)
		}
		return rejection, nil
	}
	return matchfilter.Decision{Pass: true}, nil
}

func (operation *operation) selectFormats(info value.Info) ([]mediaformat.Selection, error) {
	prepared, err := mediaformat.Prepare(info, operation.compatibility.formatOptions)
	if err != nil {
		return nil, err
	}
	return operation.selectPreparedFormats(prepared)
}

func (operation *operation) selectPreparedFormats(prepared mediaformat.Prepared) ([]mediaformat.Selection, error) {
	plans, err := operation.planPreparedFormats(prepared)
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
	prepared, err := mediaformat.Prepare(info, operation.compatibility.formatOptions)
	if err != nil {
		return nil, err
	}
	return operation.planPreparedFormats(prepared)
}

func (operation *operation) planPreparedFormats(prepared mediaformat.Prepared) ([]mediaformat.OutputPlan, error) {
	return operation.planPreparedFormatsContext(context.Background(), prepared)
}

func (operation *operation) planPreparedFormatsContext(ctx context.Context, prepared mediaformat.Prepared) ([]mediaformat.OutputPlan, error) {
	if operation.formatAvailabilityChecker != nil {
		headers, err := mediaformat.MergeHeaders(prepared.Info().Lookup("http_headers"))
		if err != nil {
			return nil, err
		}
		operation.formatAvailabilityChecker.setBaseHeaders(headers)
		if operation.request.CheckFormats == FormatCheckAll {
			formats, _ := prepared.Info().Lookup("formats").ListValue()
			for _, candidate := range formats {
				object, ok := candidate.Object()
				if !ok || object == nil {
					continue
				}
				if _, err := operation.formatAvailabilityChecker.IsAvailable(object); err != nil {
					return nil, err
				}
			}
		}
	}
	evaluation := mediaformat.EvaluationOptions{Availability: operation.formatAvailability}
	if operation.compatibility.interactiveFormat {
		return operation.planInteractiveFormat(ctx, prepared, evaluation)
	}
	if operation.compatibility.selector == nil {
		capabilities := mediaformat.PlannerCapabilities{CanMergeFormats: true}
		if operation.plannerCapabilities != nil {
			capabilities = *operation.plannerCapabilities
		}
		isLive, _ := prepared.Info().Lookup("is_live").Bool()
		return prepared.DefaultWithContext(
			capabilities,
			mediaformat.DefaultSelectorContext{IsLive: isLive, LiveFromStart: operation.request.LiveFromStart},
			evaluation,
		)
	}
	return prepared.PlanWithOptions(*operation.compatibility.selector, evaluation)
}

const maxInteractiveFormatAttempts = 3

func (operation *operation) planInteractiveFormat(ctx context.Context, prepared mediaformat.Prepared, evaluation mediaformat.EvaluationOptions) ([]mediaformat.OutputPlan, error) {
	var diagnostic string
	for attempt := 1; attempt <= maxInteractiveFormatAttempts; attempt++ {
		selectorText, err := operation.request.InteractiveFormat(ctx, InteractiveFormatPrompt{Attempt: attempt, Error: diagnostic})
		if err != nil {
			return nil, err
		}
		selectorText = strings.TrimSpace(selectorText)
		if selectorText == "" {
			capabilities := mediaformat.PlannerCapabilities{CanMergeFormats: true}
			if operation.plannerCapabilities != nil {
				capabilities = *operation.plannerCapabilities
			}
			isLive, _ := prepared.Info().Lookup("is_live").Bool()
			return prepared.DefaultWithContext(capabilities, mediaformat.DefaultSelectorContext{
				IsLive: isLive, LiveFromStart: operation.request.LiveFromStart,
			}, evaluation)
		}
		selector, parseErr := mediaformat.ParseSelector(selectorText)
		if parseErr != nil {
			diagnostic = "invalid format selector"
			continue
		}
		plans, planErr := prepared.PlanWithOptions(selector, evaluation)
		if errors.Is(planErr, mediaformat.ErrNoMatch) {
			diagnostic = "no matching available format"
			continue
		}
		if planErr != nil {
			return nil, planErr
		}
		return plans, nil
	}
	return nil, fmt.Errorf("%w: format selection attempts exhausted", ErrInteractiveInput)
}

// validateMultiOutputProduct retains the product validation seam introduced
// before per-output lifecycles existed. PR 8 executes every supported stage
// independently for each plan, so the completed lifecycle has no blanket
// multi-output exclusions.
func validateMultiOutputProduct(request Request, planCount int) error {
	if planCount <= 1 {
		return nil
	}
	for _, step := range request.Postprocessors {
		if destination := postprocessorExplicitDestination(step); destination != "" {
			return fmt.Errorf("%w: fixed postprocessor destination %q collides across output plans", mediaformat.ErrMultiOutput, destination)
		}
	}
	return nil
}

func postprocessorExplicitDestination(step Postprocessor) string {
	switch {
	case step.ExtractAudio != nil:
		return step.ExtractAudio.Destination
	case step.Remux != nil:
		return step.Remux.Destination
	case step.ConvertSubtitle != nil:
		return step.ConvertSubtitle.Destination
	case step.ConvertThumbnail != nil:
		return step.ConvertThumbnail.Destination
	case step.EmbedMetadata != nil:
		return step.EmbedMetadata.Destination
	case step.EmbedChapters != nil:
		return step.EmbedChapters.Destination
	case step.EmbedThumbnail != nil:
		return step.EmbedThumbnail.Destination
	case step.EmbedSubtitle != nil:
		return step.EmbedSubtitle.Destination
	case step.Fixup != nil:
		return step.Fixup.Destination
	case step.Concat != nil:
		return step.Concat.Destination
	case step.Move != nil:
		return step.Move.Destination
	default:
		return ""
	}
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
