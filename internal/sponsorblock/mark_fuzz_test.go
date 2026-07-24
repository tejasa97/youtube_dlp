package sponsorblock

import (
	"math"
	"reflect"
	"slices"
	"testing"
)

func FuzzMarkChapters(f *testing.F) {
	f.Add(uint8(2), uint8(3), uint16(60))
	f.Add(uint8(0), uint8(0), uint16(1))
	f.Fuzz(func(t *testing.T, normalCount, sponsorCount uint8, durationRaw uint16) {
		duration := float64(durationRaw%600 + 1)
		normalCount %= 16
		sponsorCount %= 16
		normal := make([]NormalChapter, 0, normalCount)
		if normalCount > 0 {
			width := duration / float64(normalCount)
			for index := 0; index < int(normalCount); index++ {
				normal = append(normal, NormalChapter{
					StartTime: float64(index) * width, EndTime: float64(index+1) * width,
					Title: "chapter", Source: index,
				})
			}
		}
		sponsors := make([]Chapter, 0, sponsorCount)
		for index := 0; index < int(sponsorCount); index++ {
			start := math.Mod(float64(index*7), duration)
			end := math.Min(duration, start+math.Max(.01, duration/4))
			sponsors = append(sponsors, Chapter{
				StartTime: start, EndTime: end, Category: "sponsor", Title: "Sponsor", Type: "skip",
			})
		}
		normalBefore := append([]NormalChapter(nil), normal...)
		sponsorsBefore := append([]Chapter(nil), sponsors...)
		first, firstErr := MarkChapters(normal, sponsors, duration, "Video")
		second, secondErr := MarkChapters(normal, sponsors, duration, "Video")
		if (firstErr == nil) != (secondErr == nil) || !reflect.DeepEqual(first, second) {
			t.Fatal("non-deterministic result")
		}
		if !slices.Equal(normal, normalBefore) || !slices.Equal(sponsors, sponsorsBefore) {
			t.Fatal("input mutation")
		}
		var previous float64
		for _, chapter := range first {
			if !finite(chapter.StartTime) || !finite(chapter.EndTime) ||
				chapter.StartTime < previous || chapter.EndTime <= chapter.StartTime ||
				chapter.EndTime > duration {
				t.Fatalf("invalid output %#v", first)
			}
			previous = chapter.EndTime
		}
	})
}
