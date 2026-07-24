package sponsorblock

import "math"

// normalizeSegment applies the per-segment pinned rules and reports
// whether the segment survives the duration-mismatch filter.
func normalizeSegment(segment RawSegment, knownDuration bool, duration float64) (RawSegment, bool) {
	if !IsValidCategory(segment.Category) || !IsValidAction(segment.ActionType) {
		return segment, false
	}
	if math.IsNaN(segment.Segment[0]) || math.IsInf(segment.Segment[0], 0) ||
		math.IsNaN(segment.Segment[1]) || math.IsInf(segment.Segment[1], 0) {
		return segment, false
	}
	if segment.Segment[0] < 0 || segment.Segment[1] < 0 {
		return segment, false
	}
	if segment.Segment[0] > maxAllowedTimestamp || segment.Segment[1] > maxAllowedTimestamp {
		return segment, false
	}
	// Whole-video marker.
	if segment.Segment[0] == 0 && segment.Segment[1] == 0 {
		return segment, false
	}
	category := Category(segment.Category)
	// Ignore milliseconds difference at the start.
	start := segment.Segment[0]
	if start <= 1 {
		start = 0
	}
	end := segment.Segment[1]
	// POI chapters are extended by exactly one second.
	if IsPOI(category) {
		end += 1
	}
	if start >= end {
		return segment, false
	}
	// Snap end to the known duration when within one second, never
	// exceed the duration.
	if knownDuration && duration-end <= 1 {
		end = duration
	}
	if end <= start {
		return segment, false
	}
	if knownDuration && end > duration {
		end = duration
	}
	if end <= start {
		return segment, false
	}
	// Duration mismatch filter.
	if knownDuration && !matchesDuration(segment.VideoDuration, duration, start, end) {
		return segment, false
	}
	segment.Segment[0] = start
	segment.Segment[1] = end
	return segment, true
}

// matchesDuration implements the pinned <1s or <5s and <5% policy
// without divide-by-zero hazards. The caller is responsible for
// rejecting non-positive reference durations.
func matchesDuration(reported, reference, start, end float64) bool {
	if math.IsNaN(reported) || math.IsInf(reported, 0) {
		return false
	}
	if reported == 0 {
		return true
	}
	if reported < 0 {
		return false
	}
	diff := reported - reference
	if diff < 0 {
		diff = -diff
	}
	if diff < 1 {
		return true
	}
	span := end - start
	if span <= 0 {
		return false
	}
	if diff >= 5 {
		return false
	}
	return diff/span < 0.05
}
