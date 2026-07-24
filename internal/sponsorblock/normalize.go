package sponsorblock

import (
	"math"
	"sort"
)

// RawSegment is one decoded SponsorBlock segment before normalization.
// Only the fields consumed by the pinned normalizer are exposed.
type RawSegment struct {
	Segment       [2]float64
	Category      string
	ActionType    string
	VideoDuration float64
	Description   string
	// UUID is decoded only to enforce the response string bound. It is never
	// used for ordering or exposed publicly.
	UUID string
}

// Chapter is one normalized, deterministic chapter ready to be exposed
// under sponsorblock_chapters. The fields match the pinned reference:
// start_time, end_time, category, title, and type.
type Chapter struct {
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
	Category  string  `json:"category"`
	Title     string  `json:"title"`
	Type      string  `json:"type"`
}

// maxAllowedTimestamp caps hostile timestamp magnitudes while remaining far
// above practical long-form and archived-live media durations.
const maxAllowedTimestamp = 10 * 365 * 24 * 60 * 60

// Normalize converts the raw segments for one video into a bounded,
// sorted chapter list. The function is pure: it has no I/O, no global
// state, and never panics on adversarial input. The returned slice is
// either empty or sorted by (start, end, source order).
//
// duration is the known video duration in seconds. A non-positive
// duration disables the duration-snap and duration-mismatch filters:
// entries are normalized by category rules only and are sorted
// deterministically.
//
// The implementation follows the pinned yt-dlp reference: discard
// (0,0) whole-video markers, snap starts <=1s to zero, extend
// poi_highlight by one second, snap ends within one second of the
// known duration to the duration, and filter duration-mismatched
// segments using the <1s or <5s and <5% policy without divide-by-zero
// hazards.
func Normalize(segments []RawSegment, duration float64) []Chapter {
	if len(segments) == 0 {
		return []Chapter{}
	}
	if len(segments) > MaxSegmentCount {
		segments = segments[:MaxSegmentCount]
	}
	knownDuration := duration > 0 && !math.IsNaN(duration) && !math.IsInf(duration, 0)
	filtered := make([]RawSegment, 0, len(segments))
	for _, segment := range segments {
		normalized, ok := normalizeSegment(segment, knownDuration, duration)
		if !ok {
			continue
		}
		filtered = append(filtered, normalized)
	}
	if len(filtered) == 0 {
		return []Chapter{}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		a, b := filtered[i].Segment, filtered[j].Segment
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		if a[1] != b[1] {
			return a[1] < b[1]
		}
		return false
	})
	chapters := make([]Chapter, 0, len(filtered))
	for _, segment := range filtered {
		title := canonicalChapterTitle(segment)
		chapters = append(chapters, Chapter{
			StartTime: segment.Segment[0],
			EndTime:   segment.Segment[1],
			Category:  string(Category(segment.Category)),
			Title:     title,
			Type:      segment.ActionType,
		})
	}
	return chapters
}

// canonicalChapterTitle returns the chapter's title per the pinned
// rules. "chapter" uses the bounded description verbatim, including empty;
// other categories use the canonical mapping.
func canonicalChapterTitle(segment RawSegment) string {
	category := Category(segment.Category)
	if category == CategoryChapter {
		return segment.Description
	}
	if title, ok := CanonicalTitle(category); ok {
		return title
	}
	return string(category)
}
