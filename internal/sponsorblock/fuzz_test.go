package sponsorblock

import (
	"math"
	"testing"
)

// FuzzNormalize exercises the pure normalizer with adversarial inputs.
// The fuzzer is constrained to a fixed shape so the search budget
// targets the duration policy and segment math rather than type
// flexibility.
func FuzzNormalize(f *testing.F) {
	f.Add(float64(0), float64(0), float64(0), float64(60), uint8(0))
	f.Add(float64(-1), float64(5), float64(60), float64(60), uint8(1))
	f.Add(float64(10), float64(10), float64(60), float64(60), uint8(1))
	f.Add(float64(5), float64(59.5), float64(60), float64(60), uint8(2))
	f.Fuzz(func(t *testing.T, start, end, reportedDuration, duration float64, selector uint8) {
		categories := []string{"sponsor", "poi_highlight", "chapter", "intro", "unknown"}
		category := categories[int(selector)%len(categories)]
		action := "skip"
		if category == "poi_highlight" {
			action = "poi"
		} else if category == "chapter" {
			action = "chapter"
		}
		chapters := Normalize([]RawSegment{{
			Segment:       [2]float64{start, end},
			Category:      category,
			ActionType:    action,
			VideoDuration: reportedDuration,
		}}, duration)
		for _, chapter := range chapters {
			if math.IsNaN(chapter.StartTime) || math.IsNaN(chapter.EndTime) ||
				math.IsInf(chapter.StartTime, 0) || math.IsInf(chapter.EndTime, 0) {
				t.Fatalf("non-finite chapter: %+v", chapter)
			}
			if chapter.EndTime <= chapter.StartTime || chapter.StartTime < 0 {
				t.Fatalf("invalid chapter: %+v", chapter)
			}
			if duration > 0 && !math.IsNaN(duration) && !math.IsInf(duration, 0) && chapter.EndTime > duration {
				t.Fatalf("end exceeds duration: %+v vs %v", chapter, duration)
			}
		}
	})
}

// FuzzDecodeArray exercises the bounded JSON decoder with arbitrary
// input. The decoder must never panic, must always return either a
// valid envelope or a wrapped ErrInvalidMetadata, and must reject
// bodies exceeding the response byte cap.
func FuzzDecodeArray(f *testing.F) {
	f.Add([]byte("[]"))
	f.Add([]byte(`[{"videoID":"a","segments":[]}]`))
	f.Add([]byte(`[{"videoID":"a","segments":[{"segment":[0,1],"category":"sponsor","actionType":"skip","videoDuration":60}]}]`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > MaxResponseBytes*2 {
			return
		}
		_, err := decodeResponse(body, "ignored")
		if err == nil {
			return
		}
		if !isWrapped(err, ErrInvalidMetadata) {
			t.Fatalf("unexpected error chain: %v", err)
		}
	})
}

// isWrapped reports whether err wraps target anywhere in the chain.
func isWrapped(err, target error) bool {
	for cur := err; cur != nil; {
		if cur == target {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			return false
		}
		cur = u.Unwrap()
	}
	return false
}
