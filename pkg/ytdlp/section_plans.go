package ytdlp

import (
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

// activeSectionPlans determines the effective section bounds for this
// extraction and expands the given output plans into per-section plans.
// It is the generic section consumer shared by the CLI (*START-END,
// *START-inf, *from-url) and the extractor-driven section metadata
// (section_start/section_end).
//
// The returned slice is empty when no section is active; callers then
// keep the ordinary single-plan behavior. When the request carries
// explicit DownloadSections, the CLI ranges compose with an extractor
// section using the extractor's start_time as the base offset, matching
// the pinned YoutubeDL.py:3104-3132 behavior.
func (operation *operation) activeSectionPlans(
	outputPlans []mediaformat.OutputPlan,
	info value.Info,
) ([]sectionPlan, error) {
	program := operation.compatibility.sections
	// Extractor-driven section metadata (section_start/section_end) also
	// triggers section download, even without --download-sections.
	extractorBounds, hasExtractor := extractorSectionBounds(info)
	baseOffset := 0.0
	if hasExtractor {
		baseOffset = extractorBounds.Start
	}
	// *from-url consumes the extractor's start_time/end_time bounds as a
	// section, but only when explicitly requested. Without --download-sections
	// those fields do not trigger partial downloading.
	if program.FromURL && len(program.Sections) == 0 {
		if fromURLBounds, ok := fromURLSectionBounds(info); ok {
			program.Sections = []sections.Section{fromURLBounds}
		}
	}
	if len(program.Sections) == 0 && !hasExtractor {
		return nil, nil
	}
	if len(program.Sections) == 0 && hasExtractor {
		// Extractor-only section: one section from the extractor bounds.
		if len(outputPlans) == 0 {
			return nil, nil
		}
		plans := make([]sectionPlan, 0, len(outputPlans))
		for index, plan := range outputPlans {
			planInfo := value.NewInfo(selectedPlanInfo(info, plan).Fields().Clone())
			applySectionInfo(planInfo, sections.Section{Start: extractorBounds.Start, End: extractorBounds.End}, 1)
			plans = append(plans, sectionPlan{Plan: plan, Info: planInfo, Bounds: sections.Section{Start: extractorBounds.Start, End: extractorBounds.End}, Number: 1})
			_ = index
		}
		return plans, nil
	}
	// CLI ranges: compose with extractor base offset.
	if len(outputPlans) == 0 {
		return nil, nil
	}
	if len(outputPlans)*len(program.Sections) > maxSectionOutputPlans {
		return nil, fmt.Errorf("%w: section output plan count exceeds limit", extractor.ErrUnsupported)
	}
	plans := make([]sectionPlan, 0, len(outputPlans)*len(program.Sections))
	sectionNumber := 0
	for _, plan := range outputPlans {
		for _, section := range program.Sections {
			sectionNumber++
			composed := section
			if hasExtractor {
				composed.Start += baseOffset
				if composed.End != nil {
					*composed.End += baseOffset
				}
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

// fromURLSectionBounds reads start_time/end_time from the info for the
// *from-url specification. Like extractorSectionBounds, at least one bound
// must be present and both must be finite, nonnegative, and ordered when
// both are present.
func fromURLSectionBounds(info value.Info) (sections.Section, bool) {
	start, hasStart := info.Lookup("start_time").Float()
	endVal, hasEnd := info.Lookup("end_time").Float()
	if !hasStart && !hasEnd {
		return sections.Section{}, false
	}
	if hasStart && (math.IsNaN(start) || math.IsInf(start, 0) || start < 0) {
		return sections.Section{}, false
	}
	if hasEnd && (math.IsNaN(endVal) || math.IsInf(endVal, 0) || endVal < 0) {
		return sections.Section{}, false
	}
	bounds := sections.Section{Start: start}
	if hasEnd {
		bounds.End = floatPtr(endVal)
	}
	if bounds.End != nil && *bounds.End <= bounds.Start {
		return sections.Section{}, false
	}
	return bounds, true
}

// extractorSectionBounds reads section_start/section_end from the info
// envelope. At least one bound must be present; both must be finite and
// nonnegative; when both are present, end must exceed start. The
// extractor's section is the PR5 contract: an extractor section triggers
// ffmpeg section downloading even without --download-sections.
func extractorSectionBounds(info value.Info) (sections.Section, bool) {
	start, hasStart := info.Lookup("section_start").Float()
	endVal, hasEnd := info.Lookup("section_end").Float()
	if !hasStart && !hasEnd {
		return sections.Section{}, false
	}
	if hasStart && (math.IsNaN(start) || math.IsInf(start, 0) || start < 0) {
		return sections.Section{}, false
	}
	if hasEnd && (math.IsNaN(endVal) || math.IsInf(endVal, 0) || endVal < 0) {
		return sections.Section{}, false
	}
	bounds := sections.Section{Start: start}
	if hasEnd {
		bounds.End = floatPtr(endVal)
	}
	if bounds.End != nil && *bounds.End <= bounds.Start {
		return sections.Section{}, false
	}
	return bounds, true
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
		destination := operation.renderSectionDestination(sp.Info, sp.Number, used)
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
// appended only when it would collide with another section destination.
func (operation *operation) renderSectionDestination(
	sectionInfo value.Info,
	number int,
	used map[string]struct{},
) string {
	base, err := operation.renderFilenameBase(sectionInfo)
	if err != nil {
		base = operation.request.outputRoot(OutputPathHome)
	}
	base = filepath.Clean(base)
	if _, exists := used[base]; !exists {
		used[base] = struct{}{}
		return base
	}
	// Collision: mechanically suffix with the section number rather than
	// overwriting the earlier section.
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	candidate := fmt.Sprintf("%s.%d%s", stem, number, ext)
	for {
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
		number++
		candidate = fmt.Sprintf("%s.%d%s", stem, number, ext)
	}
}
