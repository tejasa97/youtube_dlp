package sponsorblock

import (
	"container/heap"
	"strings"
)

const maxArrangeChapters = 4000

// CategorySpan is one SponsorBlock category that contributes to a chapter.
type CategorySpan struct {
	Category string
	Start    float64
	End      float64
	Title    string
}

// ArrangeChapter is a chapter before or after applying marked cuts.
// A non-empty Categories slice identifies a SponsorBlock chapter.
type ArrangeChapter struct {
	StartTime, EndTime float64
	Title              string
	Remove             bool
	Categories         []CategorySpan
	Source             int

	Sponsor       bool
	Category      string
	CategoryList  []string
	Name          string
	CategoryNames []string
}

// ArrangeResult contains the post-cut chapter timeline and original-timeline
// cut ranges. Chapters may be empty when all input is removed.
type ArrangeResult struct {
	Chapters []ArrangeChapter
	Cuts     []Range
}

type arrangeWork struct {
	ArrangeChapter
	wasCut bool
	cutIdx *int
	index  int
}

type arrangeHeap []*arrangeWork

func (h arrangeHeap) Len() int { return len(h) }
func (h arrangeHeap) Less(i, j int) bool {
	if h[i].StartTime != h[j].StartTime {
		return h[i].StartTime < h[j].StartTime
	}
	return h[i].index < h[j].index
}
func (h arrangeHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *arrangeHeap) Push(value any) { *h = append(*h, value.(*arrangeWork)) }
func (h *arrangeHeap) Pop() any {
	old := *h
	n := len(old)
	value := old[n-1]
	*h = old[:n-1]
	return value
}

// Arrange faithfully ports yt-dlp's marked-chapter arrangement. It is pure:
// neither the input chapters nor their category slices are mutated.
func Arrange(chapters []ArrangeChapter) (ArrangeResult, error) {
	if len(chapters) > maxArrangeChapters {
		return ArrangeResult{}, errorf(ErrInvalidInput, "arrange chapter limit")
	}
	work := make(arrangeHeap, len(chapters))
	for i, input := range chapters {
		if err := validateArrangeChapter(input); err != nil {
			return ArrangeResult{}, err
		}
		copied := cloneArrangeChapter(input)
		copied.Remove = input.Remove
		work[i] = &arrangeWork{ArrangeChapter: copied, index: i}
	}
	if len(work) == 0 {
		return ArrangeResult{}, nil
	}
	heap.Init(&work)

	cuts := make([]Range, 0)
	appendCut := func(c *arrangeWork) int {
		cut := Range{Start: c.StartTime, End: c.EndTime}
		if n := len(cuts); n > 0 && cuts[n-1].End >= cut.Start {
			if cut.End > cuts[n-1].End {
				cuts[n-1].End = cut.End
			}
			return n - 1
		}
		cuts = append(cuts, cut)
		return len(cuts) - 1
	}
	excessDuration := func(c *arrangeWork) float64 {
		index := len(cuts)
		if c.cutIdx != nil {
			index = *c.cutIdx
			c.cutIdx = nil
		}
		excess := 0.0
		for ; index < len(cuts); index++ {
			cut := cuts[index]
			if cut.Start >= c.EndTime {
				break
			}
			if cut.End > c.StartTime {
				excess += minFloat(cut.End, c.EndTime) - maxFloat(cut.Start, c.StartTime)
			}
		}
		return excess
	}
	out := make([]*arrangeWork, 0, len(chapters))
	appendChapter := func(c *arrangeWork) {
		length := c.EndTime - c.StartTime - excessDuration(c)
		if length <= 0 {
			return
		}
		start := 0.0
		if n := len(out); n > 0 {
			start = out[n-1].EndTime
		}
		c.StartTime, c.EndTime = start, start+length
		c.Remove = false
		out = append(out, c)
	}

	current := heap.Pop(&work).(*arrangeWork)
	for work.Len() > 0 {
		next := heap.Pop(&work).(*arrangeWork)
		if current.EndTime <= next.StartTime {
			if current.Remove {
				appendCut(current)
			} else {
				appendChapter(current)
			}
			current = next
			continue
		}

		if current.Remove {
			if next.Remove {
				current.EndTime = maxFloat(current.EndTime, next.EndTime)
			} else if current.EndTime < next.EndTime {
				next.StartTime = current.EndTime
				next.wasCut = true
				heap.Push(&work, next)
			}
			continue
		}
		if next.Remove {
			current.wasCut = true
			if current.EndTime <= next.EndTime {
				current.EndTime = next.StartTime
				appendChapter(current)
				current = next
				continue
			}
			if len(current.Categories) > 0 {
				after := cloneWork(current)
				after.StartTime = next.EndTime
				after.Categories = after.Categories[:0]
				before := make([]CategorySpan, 0, len(current.Categories))
				for _, category := range current.Categories {
					if category.Start < next.StartTime {
						before = append(before, category)
					}
					if category.End > next.EndTime {
						after.Categories = append(after.Categories, category)
					}
				}
				current.Categories = before
				if !sameCategorySpans(current.Categories, after.Categories) {
					heap.Push(&work, after)
					current.EndTime = next.StartTime
					appendChapter(current)
					current = next
					continue
				}
			}
			index := appendCut(next)
			current.cutIdx = &index
			continue
		}
		if len(current.Categories) > 0 && len(next.Categories) == 0 {
			if current.EndTime < next.EndTime {
				next.StartTime = current.EndTime
				next.wasCut = true
				heap.Push(&work, next)
			}
			continue
		}

		// normal/sponsor or sponsor/sponsor
		if len(next.Categories) == 0 {
			return ArrangeResult{}, errorf(ErrInvalidInput, "overlapping ordinary chapters")
		}
		current.wasCut, next.wasCut = true, true
		if current.EndTime > next.EndTime {
			after := cloneWork(current)
			after.StartTime = next.EndTime
			heap.Push(&work, after)
		} else if next.EndTime > current.EndTime {
			// Pinned ModifyChapters pushes the later chapter's after-part with
			// cur_i (not the later entry's index) so equal-start tie-breaks match.
			after := cloneWork(next)
			after.StartTime = current.EndTime
			after.index = current.index
			heap.Push(&work, after)
			next.EndTime = current.EndTime
		}
		if len(current.Categories) > 0 {
			next.Categories = append(cloneCategorySpans(current.Categories), next.Categories...)
		}
		if current.cutIdx != nil {
			index := *current.cutIdx
			next.cutIdx = &index
		}
		current.EndTime = next.StartTime
		appendChapter(current)
		current = next
	}
	if current.Remove {
		appendCut(current)
	} else {
		appendChapter(current)
	}

	return ArrangeResult{Chapters: removeTinyRenameSponsors(out), Cuts: cuts}, nil
}

func validateArrangeChapter(chapter ArrangeChapter) error {
	if !finite(chapter.StartTime) || !finite(chapter.EndTime) ||
		chapter.StartTime < 0 || chapter.EndTime <= chapter.StartTime ||
		len(chapter.Title) > MaxStringBytes {
		return errorf(ErrInvalidInput, "invalid arrange chapter")
	}
	for _, category := range chapter.Categories {
		if !finite(category.Start) || !finite(category.End) || category.Start < 0 ||
			category.End <= category.Start || len(category.Category) > MaxStringBytes ||
			len(category.Title) > MaxStringBytes {
			return errorf(ErrInvalidInput, "invalid arrange category")
		}
	}
	return nil
}

func removeTinyRenameSponsors(chapters []*arrangeWork) []ArrangeChapter {
	out := make([]ArrangeChapter, 0, len(chapters))
	for i, chapter := range chapters {
		if (chapter.wasCut || len(chapter.Categories) > 0) &&
			chapter.EndTime-chapter.StartTime < tinyChapter {
			if len(out) == 0 {
				if i+1 < len(chapters) {
					chapters[i+1].StartTime = chapter.StartTime
					continue
				}
			} else {
				previous := &out[len(out)-1]
				if i+1 < len(chapters) {
					next := chapters[i+1]
					if (len(chapter.Categories) == 0 && previous.Sponsor && len(next.Categories) == 0) ||
						(len(chapter.Categories) > 0 && !previous.Sponsor && len(next.Categories) > 0) {
						next.StartTime = chapter.StartTime
						continue
					}
				}
				previous.EndTime = chapter.EndTime
				continue
			}
		}
		chapter.Remove = false
		chapter.Sponsor = false
		if len(chapter.Categories) > 0 {
			shortest := 0
			categoryList := make([]string, 0, len(chapter.Categories))
			names := make([]string, 0, len(chapter.Categories))
			for i, category := range chapter.Categories {
				categoryList = appendUnique(categoryList, category.Category)
				names = appendUnique(names, category.Title)
				if category.End-category.Start < chapter.Categories[shortest].End-chapter.Categories[shortest].Start {
					shortest = i
				}
			}
			selected := chapter.Categories[shortest]
			chapter.Sponsor, chapter.Category = true, selected.Category
			chapter.CategoryList, chapter.Name, chapter.CategoryNames = categoryList, selected.Title, names
			chapter.Source = -1
			chapter.Title = "[SponsorBlock]: " + strings.Join(names, ", ")
			if n := len(out); n > 0 && out[n-1].Sponsor && out[n-1].Title == chapter.Title {
				out[n-1].EndTime = chapter.EndTime
				continue
			}
		}
		out = append(out, cloneArrangeChapter(chapter.ArrangeChapter))
	}
	return out
}

func cloneWork(chapter *arrangeWork) *arrangeWork {
	return &arrangeWork{ArrangeChapter: cloneArrangeChapter(chapter.ArrangeChapter), wasCut: chapter.wasCut, cutIdx: chapter.cutIdx, index: chapter.index}
}

func cloneArrangeChapter(chapter ArrangeChapter) ArrangeChapter {
	chapter.Categories = cloneCategorySpans(chapter.Categories)
	chapter.CategoryList = append([]string(nil), chapter.CategoryList...)
	chapter.CategoryNames = append([]string(nil), chapter.CategoryNames...)
	return chapter
}

func cloneCategorySpans(categories []CategorySpan) []CategorySpan {
	return append([]CategorySpan(nil), categories...)
}

func sameCategorySpans(left, right []CategorySpan) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func minFloat(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}
