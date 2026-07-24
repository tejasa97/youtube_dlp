package sponsorblock

import (
	"math"
	"sort"
)

const maxCutRanges = MaxSegmentCount

// Range is a half-open media interval [Start, End) in seconds.
type Range struct {
	Start float64
	End   float64
}

// ConcatSegment is one keep segment for the ffmpeg concat demuxer.
// Nil InPoint means the beginning of the file; nil OutPoint means the end.
// The shape mirrors yt-dlp ModifyChaptersPP._make_concat_opts.
type ConcatSegment struct {
	InPoint  *float64
	OutPoint *float64
}

// CutPlan is the deterministic keep/cut layout for one media file.
type CutPlan struct {
	Cuts     []Range
	Keep     []ConcatSegment
	Duration float64 // post-cut duration
}

// IsRemovableCategory reports whether category may be cut. Matches the pinned
// reference: poi_highlight and chapter are never removable even when requested.
func IsRemovableCategory(category string) bool {
	if !IsValidCategory(category) {
		return false
	}
	switch Category(category) {
	case CategoryPOIHighlight, CategoryChapter:
		return false
	default:
		return true
	}
}

// FilterRemovableCategories returns the first-seen removable subset of categories.
// Non-removable and unknown entries are dropped; the input slice is never mutated.
func FilterRemovableCategories(categories []string) []string {
	if len(categories) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(categories))
	out := make([]string, 0, len(categories))
	for _, raw := range categories {
		if !IsRemovableCategory(raw) {
			continue
		}
		if _, dup := seen[raw]; dup {
			continue
		}
		seen[raw] = struct{}{}
		out = append(out, raw)
	}
	return out
}

// PlanCuts merges remove-eligible SponsorBlock chapters into cut ranges and
// derives concat keep segments. It is pure, bounded, and never mutates input.
//
// Eligibility matches the pinned ModifyChaptersPP sponsor-remove path:
// category must be in removeCategories (already filtered to removable), and
// zero-length / non-finite / inverted ranges are ignored. Overlapping and
// adjacent cuts are merged. Removing the entire media fails closed.
func PlanCuts(chapters []Chapter, removeCategories []string, duration float64) (CutPlan, error) {
	if !finite(duration) || duration <= 0 {
		return CutPlan{}, errorf(ErrInvalidInput, "cut duration")
	}
	if len(chapters) > MaxSegmentCount {
		return CutPlan{}, errorf(ErrInvalidInput, "cut chapter limit")
	}
	removeSet := make(map[string]struct{}, len(removeCategories))
	for _, category := range removeCategories {
		if !IsRemovableCategory(category) {
			return CutPlan{}, errorf(ErrInvalidInput, "non-removable category")
		}
		removeSet[category] = struct{}{}
	}
	if len(removeSet) == 0 {
		return CutPlan{Keep: []ConcatSegment{{}}, Duration: duration}, nil
	}

	raw := make([]Range, 0, len(chapters))
	for _, chapter := range chapters {
		if _, ok := removeSet[chapter.Category]; !ok {
			continue
		}
		// POI/chapter action types are never cut even if a skippable category
		// somehow carried them; the reference excludes them by category set.
		if chapter.Type == string(ActionPOI) || chapter.Type == string(ActionChapter) {
			continue
		}
		if !finite(chapter.StartTime) || !finite(chapter.EndTime) {
			continue
		}
		start, end := chapter.StartTime, chapter.EndTime
		if start < 0 {
			start = 0
		}
		if end > duration {
			end = duration
		}
		if end <= start {
			continue
		}
		raw = append(raw, Range{Start: start, End: end})
	}
	cuts := mergeRanges(raw)
	if len(cuts) > maxCutRanges {
		return CutPlan{}, errorf(ErrInvalidInput, "cut range limit")
	}
	if len(cuts) == 0 {
		return CutPlan{Keep: []ConcatSegment{{}}, Duration: duration}, nil
	}
	keep := makeConcatOpts(cuts, duration)
	if len(keep) == 0 {
		return CutPlan{}, errorf(ErrInvalidInput, "entire media removed")
	}
	kept := 0.0
	for _, segment := range keep {
		start := 0.0
		end := duration
		if segment.InPoint != nil {
			start = *segment.InPoint
		}
		if segment.OutPoint != nil {
			end = *segment.OutPoint
		}
		if end > start {
			kept += end - start
		}
	}
	if kept <= 0 {
		return CutPlan{}, errorf(ErrInvalidInput, "entire media removed")
	}
	return CutPlan{Cuts: cuts, Keep: keep, Duration: kept}, nil
}

// MapTimeThroughCuts maps an original-timeline timestamp onto the post-cut
// timeline by subtracting every removed duration that precedes it.
func MapTimeThroughCuts(timestamp float64, cuts []Range) float64 {
	if !finite(timestamp) {
		return timestamp
	}
	offset := 0.0
	for _, cut := range cuts {
		if cut.End <= timestamp {
			offset += cut.End - cut.Start
			continue
		}
		if cut.Start < timestamp {
			offset += timestamp - cut.Start
		}
		break
	}
	mapped := timestamp - offset
	if mapped < 0 {
		return 0
	}
	return mapped
}

// RewriteChapterTimes remaps chapter boundaries onto the post-cut timeline.
// Chapters that collapse to empty after cutting are dropped. Input is not mutated.
func RewriteChapterTimes(chapters []MarkedChapter, cuts []Range) []MarkedChapter {
	if len(chapters) == 0 {
		return nil
	}
	if len(cuts) == 0 {
		out := make([]MarkedChapter, len(chapters))
		copy(out, chapters)
		return out
	}
	out := make([]MarkedChapter, 0, len(chapters))
	for _, chapter := range chapters {
		start := MapTimeThroughCuts(chapter.StartTime, cuts)
		end := MapTimeThroughCuts(chapter.EndTime, cuts)
		if !finite(start) || !finite(end) || end <= start {
			continue
		}
		rewritten := chapter
		rewritten.StartTime = start
		rewritten.EndTime = end
		out = append(out, rewritten)
	}
	return out
}

func mergeRanges(input []Range) []Range {
	if len(input) == 0 {
		return nil
	}
	ordered := append([]Range(nil), input...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Start != ordered[j].Start {
			return ordered[i].Start < ordered[j].Start
		}
		return ordered[i].End < ordered[j].End
	})
	out := make([]Range, 0, len(ordered))
	current := ordered[0]
	for _, next := range ordered[1:] {
		if next.Start <= current.End {
			if next.End > current.End {
				current.End = next.End
			}
			continue
		}
		out = append(out, current)
		current = next
	}
	out = append(out, current)
	return out
}

// makeConcatOpts mirrors ModifyChaptersPP._make_concat_opts: convert cut
// ranges into keep segments with inpoint/outpoint, omitting zero-length
// chunks at the start or end.
func makeConcatOpts(cuts []Range, duration float64) []ConcatSegment {
	if len(cuts) == 0 {
		return []ConcatSegment{{}}
	}
	opts := []ConcatSegment{{}}
	for _, cut := range cuts {
		if cut.Start == 0 {
			end := cut.End
			opts[len(opts)-1].InPoint = floatPtr(end)
			continue
		}
		start := cut.Start
		opts[len(opts)-1].OutPoint = floatPtr(start)
		if cut.End < duration {
			end := cut.End
			opts = append(opts, ConcatSegment{InPoint: floatPtr(end)})
		}
	}
	// Drop empty keep segments that can appear from adjacent full-span cuts.
	compacted := make([]ConcatSegment, 0, len(opts))
	for _, segment := range opts {
		start := 0.0
		end := duration
		if segment.InPoint != nil {
			start = *segment.InPoint
		}
		if segment.OutPoint != nil {
			end = *segment.OutPoint
		}
		if !finite(start) || !finite(end) || end <= start {
			continue
		}
		compacted = append(compacted, segment)
	}
	return compacted
}

func floatPtr(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	copied := value
	return &copied
}
