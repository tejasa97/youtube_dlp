package sponsorblock

import (
	"math"
	"testing"
)

func FuzzPlanCuts(f *testing.F) {
	f.Add(0.0, 10.0, "sponsor", "skip", 100.0)
	f.Add(0.0, 0.0, "sponsor", "skip", 10.0)
	f.Add(5.0, 5.0, "intro", "skip", 20.0)
	f.Add(-1.0, 50.0, "outro", "skip", 40.0)
	f.Add(1.0, 2.0, "poi_highlight", "poi", 30.0)
	f.Fuzz(func(t *testing.T, start, end float64, category, action string, duration float64) {
		if len(category) > 64 || len(action) > 64 {
			return
		}
		chapters := []Chapter{{StartTime: start, EndTime: end, Category: category, Type: action}}
		remove := FilterRemovableCategories([]string{category, "sponsor"})
		plan, err := PlanCuts(chapters, remove, duration)
		if err != nil {
			if !finite(duration) || duration <= 0 {
				return
			}
			// Entire-media removal is the only other expected failure for fuzz input.
			return
		}
		prev := -1.0
		for _, cut := range plan.Cuts {
			if !finite(cut.Start) || !finite(cut.End) || cut.End <= cut.Start || cut.Start < 0 {
				t.Fatalf("invalid cut %#v", cut)
			}
			if cut.Start < prev {
				t.Fatalf("unsorted cuts %#v", plan.Cuts)
			}
			prev = cut.End
		}
		kept := 0.0
		for _, segment := range plan.Keep {
			in, out := 0.0, duration
			if segment.InPoint != nil {
				in = *segment.InPoint
				if math.IsNaN(in) || math.IsInf(in, 0) {
					t.Fatal("nan inpoint")
				}
			}
			if segment.OutPoint != nil {
				out = *segment.OutPoint
				if math.IsNaN(out) || math.IsInf(out, 0) {
					t.Fatal("nan outpoint")
				}
			}
			if out <= in {
				t.Fatalf("empty keep %#v", segment)
			}
			kept += out - in
		}
		if math.Abs(kept-plan.Duration) > 1e-9 {
			t.Fatalf("duration mismatch kept=%v plan=%v", kept, plan.Duration)
		}
	})
}
