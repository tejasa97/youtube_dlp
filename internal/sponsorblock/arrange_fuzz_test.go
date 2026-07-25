package sponsorblock

import (
	"math"
	"reflect"
	"testing"
)

func FuzzArrange(f *testing.F) {
	f.Add([]byte{3, 4, 1, 2, 5, 0})
	f.Add([]byte{1, 20, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		chapters := fuzzArrangeChapters(data)
		before := cloneArrangeInputs(chapters)
		first, err := Arrange(chapters)
		if err != nil {
			if !reflect.DeepEqual(chapters, before) {
				t.Fatal("invalid input was mutated")
			}
			return
		}
		second, err := Arrange(chapters)
		if err != nil || !reflect.DeepEqual(first, second) {
			t.Fatalf("non-deterministic result: %#v %#v %v", first, second, err)
		}
		if !reflect.DeepEqual(chapters, before) {
			t.Fatal("input was mutated")
		}
		for i, cut := range first.Cuts {
			if !finite(cut.Start) || !finite(cut.End) || cut.Start < 0 || cut.End <= cut.Start {
				t.Fatalf("invalid cut %#v", cut)
			}
			if i > 0 && first.Cuts[i-1].End >= cut.Start {
				t.Fatalf("unmerged cuts %#v", first.Cuts)
			}
		}
		for i, chapter := range first.Chapters {
			if !finite(chapter.StartTime) || !finite(chapter.EndTime) || chapter.EndTime <= chapter.StartTime || chapter.Remove {
				t.Fatalf("invalid chapter %#v", chapter)
			}
			if i > 0 && first.Chapters[i-1].EndTime != chapter.StartTime {
				t.Fatalf("non-contiguous output %#v", first.Chapters)
			}
		}
		if len(chapters) > 0 {
			original := chapters[len(chapters)-1].EndTime - chapters[0].StartTime
			kept, cut := 0.0, 0.0
			for _, chapter := range first.Chapters {
				kept += chapter.EndTime - chapter.StartTime
			}
			for _, item := range first.Cuts {
				cut += item.End - item.Start
			}
			if math.Abs(kept+cut-original) > 1e-9 {
				t.Fatalf("duration not conserved: kept=%v cut=%v original=%v", kept, cut, original)
			}
		}
	})
}

func fuzzArrangeChapters(data []byte) []ArrangeChapter {
	if len(data) == 0 {
		return nil
	}
	count := int(data[0]%16) + 1
	chapters := make([]ArrangeChapter, 0, count)
	start := 0.0
	for i := 0; i < count; i++ {
		value := byte(i)
		if i+1 < len(data) {
			value = data[i+1]
		}
		end := start + float64(value%10+1)/2
		if value&0x20 != 0 {
			chapters = append(chapters, ArrangeChapter{
				StartTime: start, EndTime: end, Source: -1,
				Categories: []CategorySpan{{Category: "sponsor", Start: start, End: end, Title: "Sponsor"}},
			})
		} else {
			chapters = append(chapters, ordinary(start, end, "chapter", value&0x40 != 0))
		}
		start = end
	}
	return chapters
}
