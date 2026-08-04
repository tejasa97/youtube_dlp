package ytdlp

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/compat/sections"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

// sectionPlan pairs an expanded output plan with its normalized section
// bounds and deterministic one-based section number. Info carries the
// section fields so output templates and sidecars can render them.
type sectionPlan struct {
	Plan   mediaformat.OutputPlan
	Info   value.Info
	Bounds sections.Section
	Number int
}

// errInvalidSuppliedSection marks extractor- or URL-supplied section bounds
// that were present but invalid. It is distinct from "fields absent": an
// invalid supplied range must fail before any output mutation rather than
// silently degrading to a full download.
var errInvalidSuppliedSection = errors.New("invalid supplied section bounds")

// activeSectionPlans determines the effective section bounds for this
// extraction and expands the given output plans into per-section plans.
// It is the generic section consumer shared by the CLI (*START-END,
// *START-inf, *from-url) and the extractor-driven section metadata
// (section_start/section_end).
//
// The returned slice is empty when no section is active; callers then
// keep the ordinary single-plan behavior. When the request carries
// explicit DownloadSections, the CLI ranges compose inside the extractor
// window (section_start/section_end) as the base coordinate, matching the
// pinned YoutubeDL.py:3104-3132 behavior.
func (operation *operation) activeSectionPlans(
	outputPlans []mediaformat.OutputPlan,
	info value.Info,
) ([]sectionPlan, error) {
	program := operation.compatibility.sections
	// Resolve the extractor window. Invalid supplied bounds fail closed;
	// absent bounds mean no extractor window.
	extractorBounds, hasExtractor, err := extractorSectionBounds(info)
	if err != nil {
		return nil, err
	}
	// Resolve the effective request sections. *from-url contributes a
	// section from start_time/end_time and is appended after any literal
	// ranges rather than being dropped when literal ranges are present.
	effectiveSections, err := effectiveRequestSections(program, info, hasExtractor, extractorBounds)
	if err != nil {
		return nil, err
	}
	if len(effectiveSections) == 0 && !hasExtractor {
		return nil, nil
	}
	if len(effectiveSections) == 0 && hasExtractor {
		// An extractor-driven section without explicit request ranges means
		// one section covering the extractor window itself.
		effectiveSections = []sections.Section{{Start: extractorBounds.Start, End: cloneEnd(extractorBounds.End)}}
	}
	if len(outputPlans) == 0 {
		return nil, nil
	}
	if len(outputPlans)*len(effectiveSections) > maxSectionOutputPlans {
		return nil, fmt.Errorf("%w: section output plan count exceeds limit", extractor.ErrUnsupported)
	}
	plans := make([]sectionPlan, 0, len(outputPlans)*len(effectiveSections))
	sectionNumber := 0
	for _, plan := range outputPlans {
		for _, section := range effectiveSections {
			sectionNumber++
			composed, composeErr := composeSection(section, extractorBounds, hasExtractor)
			if composeErr != nil {
				return nil, composeErr
			}
			planInfo := value.NewInfo(selectedPlanInfo(info, plan).Fields().Clone())
			applySectionInfo(planInfo, composed, sectionNumber)
			plans = append(plans, sectionPlan{
				Plan: plan, Info: planInfo, Bounds: composed, Number: sectionNumber,
			})
		}
	}
	return plans, nil
}

// maxSectionOutputPlans bounds the Cartesian product of output plans ×
// sections so a hostile request cannot allocate unbounded plans.
const maxSectionOutputPlans = 64

// effectiveRequestSections assembles the request-level sections: literal
// ranges first, then a *from-url range appended after them when requested.
// When *from-url is explicitly requested but the URL bounds are unavailable
// or invalid, it fails closed instead of silently producing an ordinary full
// download.
func effectiveRequestSections(
	program sections.Program,
	info value.Info,
	hasExtractor bool,
	extractorBounds sections.Section,
) ([]sections.Section, error) {
	out := make([]sections.Section, 0, len(program.Sections)+1)
	out = append(out, program.Sections...)
	if program.FromURL {
		fromURL, present, err := fromURLSectionBounds(info)
		if err != nil {
			return nil, err
		}
		if !present {
			return nil, fmt.Errorf("%w: *from-url requested but start_time/end_time are unavailable", errInvalidSuppliedSection)
		}
		out = append(out, fromURL)
	}
	_ = hasExtractor
	_ = extractorBounds
	return out, nil
}

// composeSection maps a request-level section into the extractor coordinate
// space. The extractor window (section_start/section_end) is the base:
// request starts and ends are offset by the window start, request ends are
// then clamped to the window end, open-ended requests close at the window
// end, and a request start beyond the window is rejected. The endpoint is
// deep-copied so composition never mutates the shared program state.
func composeSection(
	section sections.Section,
	extractorBounds sections.Section,
	hasExtractor bool,
) (sections.Section, error) {
	composed := sections.Section{Start: section.Start}
	if section.End != nil {
		end := *section.End
		composed.End = &end
	}
	if !hasExtractor {
		return composed, nil
	}
	// Offset the start by the extractor window start.
	composed.Start += extractorBounds.Start
	// Reject a request start beyond the extractor window.
	if extractorBounds.End != nil && composed.Start > *extractorBounds.End {
		return sections.Section{}, fmt.Errorf("%w: section start %v is beyond the extractor window", errInvalidSuppliedSection, section.Start)
	}
	// Offset the request end by the window start, then clamp it to the window
	// end. An open-ended request closes at the window end.
	windowEnd := math.Inf(1)
	if extractorBounds.End != nil {
		windowEnd = *extractorBounds.End
	}
	if composed.End != nil {
		*composed.End += extractorBounds.Start
		if *composed.End > windowEnd {
			*composed.End = windowEnd
		}
		if *composed.End <= composed.Start {
			return sections.Section{}, fmt.Errorf("%w: section ends before it starts after clamping", errInvalidSuppliedSection)
		}
	} else if extractorBounds.End != nil {
		end := windowEnd
		composed.End = &end
	}
	return composed, nil
}

// fromURLSectionBounds reads start_time/end_time from the info for the
// *from-url specification. It distinguishes "absent" (present=false,
// err=nil) from "present but invalid" (err set) so a malformed URL range
// fails closed rather than degrading to a full download.
func fromURLSectionBounds(info value.Info) (sections.Section, bool, error) {
	return sectionBoundsFromFields(info, "start_time", "end_time", "start_time/end_time")
}

// extractorSectionBounds reads section_start/section_end from the info
// envelope. It distinguishes "absent" (present=false, err=nil) from
// "present but invalid" (err set). The extractor's section is the PR5
// contract: an extractor section triggers ffmpeg section downloading even
// without --download-sections, and malformed bounds must not silently become
// a full download.
func extractorSectionBounds(info value.Info) (sections.Section, bool, error) {
	return sectionBoundsFromFields(info, "section_start", "section_end", "section_start/section_end")
}

// sectionBoundsFromFields reads a start/end field pair from the info
// envelope, distinguishing absent from present-but-invalid. Numeric fields
// are read tolerantly: JSON integers (e.g. "start_time": 0) decode as Int
// and must be accepted alongside Float values.
// sectionFieldFloat reads a numeric info field, accepting both Int and
// Float representations (JSON integers decode as Int and must still be
// recognized as section bounds).
func sectionFieldFloat(info value.Info, field string) (float64, bool) {
	if value, ok := info.Lookup(field).Float(); ok {
		return value, true
	}
	if value, ok := info.Lookup(field).Int(); ok {
		return float64(value), true
	}
	return 0, false
}

func sectionBoundsFromFields(info value.Info, startField, endField, label string) (sections.Section, bool, error) {
	start, hasStart := sectionFieldFloat(info, startField)
	endVal, hasEnd := sectionFieldFloat(info, endField)
	if !hasStart && !hasEnd {
		return sections.Section{}, false, nil
	}
	if hasStart && (math.IsNaN(start) || math.IsInf(start, 0) || start < 0) {
		return sections.Section{}, false, fmt.Errorf("%w: %s has invalid start", errInvalidSuppliedSection, label)
	}
	if hasEnd && (math.IsNaN(endVal) || math.IsInf(endVal, 0) || endVal < 0) {
		return sections.Section{}, false, fmt.Errorf("%w: %s has invalid end", errInvalidSuppliedSection, label)
	}
	bounds := sections.Section{Start: start}
	if hasEnd {
		end := endVal
		bounds.End = &end
	}
	if bounds.End != nil && *bounds.End <= bounds.Start {
		return sections.Section{}, false, fmt.Errorf("%w: %s end must exceed start", errInvalidSuppliedSection, label)
	}
	return bounds, true, nil
}

// applySectionInfo sets the per-section metadata on a cloned Info:
// section_start, section_end when bounded, and a deterministic one-based
// section_number. It does not set section_title (only attributable callers
// add that later).
func applySectionInfo(info value.Info, bounds sections.Section, number int) {
	info.Fields().Set("section_start", value.Float(bounds.Start))
	if bounds.End != nil {
		info.Fields().Set("section_end", value.Float(*bounds.End))
	} else {
		info.Fields().Delete("section_end")
	}
	info.Fields().Set("section_number", value.Int(int64(number)))
	// Derive the base info duration as section_end - section_start when both
	// bounds are present, matching the pinned reference.
	if bounds.End != nil {
		info.Fields().Set("duration", value.Float(roundSectionDuration(*bounds.End-bounds.Start)))
	}
}

// roundSectionDuration rounds a section duration to 3 decimals, matching
// the pinned round(section_end - section_start, 3) derived duration.
func roundSectionDuration(seconds float64) float64 {
	return math.Round(seconds*1000) / 1000
}

func floatPtr(value float64) *float64 { return &value }

// cloneEnd deep-copies an optional End pointer so composition never mutates
// the shared program state.
func cloneEnd(end *float64) *float64 {
	if end == nil {
		return nil
	}
	value := *end
	return &value
}

// buildSectionLifecycles expands the ordinary output plans into the
// effective list of lifecycles, expanding each plan into its requested
// sections when a section is active. When no section is active it returns
// the ordinary set unchanged.
//
// Each section lifecycle gets a defensive clone of the plan Info with the
// section fields applied and a collision-safe destination. A lone
// extractor-driven section retains ordinary destination behavior (it is
// rendered from the section Info, which for a single section has no
// section_number suffix by default).
func (operation *operation) buildSectionLifecycles(
	info value.Info,
	outputPlans []mediaformat.OutputPlan,
	planDestinations []string,
) ([]outputLifecycle, error) {
	sectionPlans, err := operation.activeSectionPlans(outputPlans, info)
	if err != nil {
		return nil, err
	}
	if len(sectionPlans) == 0 {
		// Ordinary path: one lifecycle per output plan.
		lifecycles := make([]outputLifecycle, len(outputPlans))
		for index, plan := range outputPlans {
			lifecycles[index] = newOutputLifecycleForPlan(index, plan, info, planDestinations[index])
		}
		return lifecycles, nil
	}
	// Section path: one lifecycle per (plan × section), with a
	// collision-safe destination and deterministic section metadata.
	lifecycles := make([]outputLifecycle, 0, len(sectionPlans))
	used := make(map[string]struct{}, len(sectionPlans))
	for _, sp := range sectionPlans {
		destination, renderErr := operation.renderSectionDestination(sp.Info, sp.Number, used)
		if renderErr != nil {
			return nil, renderErr
		}
		lifecycle := newOutputLifecycleForPlan(sp.Number-1, sp.Plan, info, destination)
		lifecycle.Info = sp.Info
		section := sp.Bounds
		lifecycle.Section = &section
		lifecycles = append(lifecycles, lifecycle)
	}
	return lifecycles, nil
}

// renderSectionDestination renders a collision-safe destination for a
// section. If the user's template already includes section_number the
// rendered path is used as-is; otherwise a deterministic numeric suffix is
// appended only when it would collide with another section destination. A
// rendering error is returned rather than swallowed into the output root.
func (operation *operation) renderSectionDestination(
	sectionInfo value.Info,
	number int,
	used map[string]struct{},
) (string, error) {
	base, err := operation.renderFilenameBase(sectionInfo)
	if err != nil {
		return "", err
	}
	base = filepath.Clean(base)
	if _, exists := used[base]; !exists {
		used[base] = struct{}{}
		return base, nil
	}
	// Collision: mechanically suffix with the section number rather than
	// overwriting the earlier section.
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	candidate := fmt.Sprintf("%s.%d%s", stem, number, ext)
	for {
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate, nil
		}
		number++
		candidate = fmt.Sprintf("%s.%d%s", stem, number, ext)
	}
}
