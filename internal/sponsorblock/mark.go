package sponsorblock

import (
	"math"
	"sort"
	"strings"
)

const (
	maxMarkedChapters = 4000
	tinyChapter       = 1.0
)

// NormalChapter is one ordinary chapter before SponsorBlock overlay. Source
// identifies the original metadata object so callers can preserve extra fields.
type NormalChapter struct {
	StartTime float64
	EndTime   float64
	Title     string
	Source    int
}

// MarkedChapter is one non-overlapping chapter after SponsorBlock overlay.
type MarkedChapter struct {
	StartTime     float64
	EndTime       float64
	Title         string
	Source        int
	Sponsor       bool
	Category      string
	Categories    []string
	Name          string
	CategoryNames []string
	Type          string
	modified      bool
}

// MarkChapters overlays normalized SponsorBlock chapters onto ordinary
// chapters. It is pure and bounded and never mutates either input slice.
func MarkChapters(normal []NormalChapter, sponsors []Chapter, duration float64, videoTitle string) ([]MarkedChapter, error) {
	if !finite(duration) || duration <= 0 {
		return nil, errorf(ErrInvalidInput, "mark duration")
	}
	if len(normal) > maxMarkedChapters || len(sponsors) > MaxSegmentCount {
		return nil, errorf(ErrInvalidInput, "mark chapter limit")
	}
	for index, chapter := range normal {
		if !validRange(chapter.StartTime, chapter.EndTime, duration) || len(chapter.Title) > MaxStringBytes {
			return nil, errorf(ErrInvalidInput, "invalid ordinary chapter")
		}
		if index > 0 && chapter.StartTime < normal[index-1].EndTime {
			return nil, errorf(ErrInvalidInput, "overlapping ordinary chapters")
		}
	}
	for _, chapter := range sponsors {
		if !validRange(chapter.StartTime, chapter.EndTime, duration) ||
			!IsValidCategory(chapter.Category) || len(chapter.Title) > MaxStringBytes {
			return nil, errorf(ErrInvalidInput, "invalid sponsor chapter")
		}
	}
	if len(sponsors) == 0 {
		if len(normal) == 0 {
			return []MarkedChapter{{
				StartTime: 0, EndTime: duration, Title: videoTitle, Source: -1,
			}}, nil
		}
		result := make([]MarkedChapter, len(normal))
		for index, chapter := range normal {
			result[index] = MarkedChapter{
				StartTime: chapter.StartTime, EndTime: chapter.EndTime,
				Title: chapter.Title, Source: chapter.Source,
			}
		}
		return result, nil
	}

	orderedSponsors := append([]Chapter(nil), sponsors...)
	sort.SliceStable(orderedSponsors, func(i, j int) bool {
		if orderedSponsors[i].StartTime != orderedSponsors[j].StartTime {
			return orderedSponsors[i].StartTime < orderedSponsors[j].StartTime
		}
		return orderedSponsors[i].EndTime < orderedSponsors[j].EndTime
	})
	boundaries := make([]float64, 0, 2+len(normal)*2+len(sponsors)*2)
	boundaries = append(boundaries, 0, duration)
	for _, chapter := range normal {
		boundaries = append(boundaries, chapter.StartTime, chapter.EndTime)
	}
	for _, chapter := range orderedSponsors {
		boundaries = append(boundaries, chapter.StartTime, chapter.EndTime)
	}
	sort.Float64s(boundaries)
	boundaries = uniqueBoundaries(boundaries)
	if len(boundaries) > maxMarkedChapters*2+2 {
		return nil, errorf(ErrInvalidInput, "mark boundary limit")
	}

	atomic := make([]MarkedChapter, 0, len(boundaries)-1)
	for index := 0; index+1 < len(boundaries); index++ {
		start, end := boundaries[index], boundaries[index+1]
		if end <= start {
			continue
		}
		base, hasBase := ordinaryAt(normal, start, end)
		if !hasBase {
			base = NormalChapter{StartTime: start, EndTime: end, Title: videoTitle, Source: -1}
		}
		active := sponsorsAt(orderedSponsors, start, end)
		if len(active) == 0 {
			atomic = append(atomic, MarkedChapter{
				StartTime: start, EndTime: end, Title: base.Title, Source: base.Source,
				modified: start != base.StartTime || end != base.EndTime,
			})
			continue
		}
		atomic = append(atomic, sponsorMarkedChapter(active, start, end))
	}
	atomic = mergeTinyMarked(atomic)
	result := coalesceMarkedSponsors(atomic)
	if len(result) > maxMarkedChapters {
		return nil, errorf(ErrInvalidInput, "marked chapter limit")
	}
	return result, nil
}

func validRange(start, end, duration float64) bool {
	return finite(start) && finite(end) && start >= 0 && start < end && end <= duration
}

func finite(number float64) bool {
	return !math.IsNaN(number) && !math.IsInf(number, 0)
}

func uniqueBoundaries(input []float64) []float64 {
	if len(input) == 0 {
		return nil
	}
	output := input[:1]
	for _, boundary := range input[1:] {
		if boundary != output[len(output)-1] {
			output = append(output, boundary)
		}
	}
	return output
}

func ordinaryAt(chapters []NormalChapter, start, end float64) (NormalChapter, bool) {
	for _, chapter := range chapters {
		if chapter.StartTime <= start && chapter.EndTime >= end {
			return chapter, true
		}
	}
	return NormalChapter{}, false
}

func sponsorsAt(chapters []Chapter, start, end float64) []Chapter {
	active := make([]Chapter, 0, len(chapters))
	for _, chapter := range chapters {
		if chapter.StartTime < end && chapter.EndTime > start {
			active = append(active, chapter)
		}
	}
	return active
}

func sponsorMarkedChapter(active []Chapter, start, end float64) MarkedChapter {
	categories := make([]string, 0, len(active))
	names := make([]string, 0, len(active))
	shortest := 0
	for index, chapter := range active {
		categories = appendUnique(categories, chapter.Category)
		names = appendUnique(names, chapter.Title)
		if chapter.EndTime-chapter.StartTime < active[shortest].EndTime-active[shortest].StartTime {
			shortest = index
		}
	}
	return MarkedChapter{
		StartTime: start, EndTime: end, Title: "[SponsorBlock]: " + strings.Join(names, ", "),
		Source: -1, Sponsor: true, Category: active[shortest].Category,
		Categories: categories, Name: active[shortest].Title, CategoryNames: names,
		Type: active[shortest].Type, modified: true,
	}
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func mergeTinyMarked(chapters []MarkedChapter) []MarkedChapter {
	result := make([]MarkedChapter, 0, len(chapters))
	for index := 0; index < len(chapters); index++ {
		chapter := chapters[index]
		if !chapter.modified || chapter.EndTime-chapter.StartTime >= tinyChapter {
			result = append(result, chapter)
			continue
		}
		if len(result) == 0 {
			if index+1 < len(chapters) {
				chapters[index+1].StartTime = chapter.StartTime
				chapters[index+1].modified = true
				continue
			}
			result = append(result, chapter)
			continue
		}
		if index+1 < len(chapters) {
			previousSponsor := result[len(result)-1].Sponsor
			nextSponsor := chapters[index+1].Sponsor
			if (!chapter.Sponsor && previousSponsor && !nextSponsor) ||
				(chapter.Sponsor && !previousSponsor && nextSponsor) {
				chapters[index+1].StartTime = chapter.StartTime
				chapters[index+1].modified = true
				continue
			}
		}
		result[len(result)-1].EndTime = chapter.EndTime
	}
	return result
}

func coalesceMarkedSponsors(chapters []MarkedChapter) []MarkedChapter {
	result := make([]MarkedChapter, 0, len(chapters))
	for _, chapter := range chapters {
		if chapter.EndTime <= chapter.StartTime {
			continue
		}
		if len(result) > 0 && chapter.Sponsor && result[len(result)-1].Sponsor &&
			result[len(result)-1].Title == chapter.Title &&
			result[len(result)-1].EndTime == chapter.StartTime {
			result[len(result)-1].EndTime = chapter.EndTime
			continue
		}
		result = append(result, chapter)
	}
	return result
}
