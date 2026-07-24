package sponsorblock

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestIsRemovableCategory(t *testing.T) {
	if !IsRemovableCategory("sponsor") || !IsRemovableCategory("filler") {
		t.Fatal("expected skippable categories to be removable")
	}
	if IsRemovableCategory("poi_highlight") || IsRemovableCategory("chapter") {
		t.Fatal("poi/chapter must not be removable")
	}
	if IsRemovableCategory("unknown") || IsRemovableCategory("") {
		t.Fatal("unknown categories must not be removable")
	}
}

func TestFilterRemovableCategories(t *testing.T) {
	got := FilterRemovableCategories([]string{"sponsor", "poi_highlight", "sponsor", "chapter", "intro"})
	want := []string{"sponsor", "intro"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPlanCutsMergesAdjacentAndOverlapping(t *testing.T) {
	chapters := []Chapter{
		{StartTime: 10, EndTime: 20, Category: "sponsor", Type: "skip"},
		{StartTime: 18, EndTime: 25, Category: "selfpromo", Type: "skip"},
		{StartTime: 25, EndTime: 30, Category: "intro", Type: "skip"},
		{StartTime: 40, EndTime: 50, Category: "outro", Type: "skip"},
		{StartTime: 5, EndTime: 6, Category: "poi_highlight", Type: "poi"},
	}
	plan, err := PlanCuts(chapters, []string{"sponsor", "selfpromo", "intro", "outro"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	wantCuts := []Range{{Start: 10, End: 30}, {Start: 40, End: 50}}
	if !reflect.DeepEqual(plan.Cuts, wantCuts) {
		t.Fatalf("cuts = %#v want %#v", plan.Cuts, wantCuts)
	}
	if len(plan.Keep) != 3 {
		t.Fatalf("keep len = %d want 3: %#v", len(plan.Keep), plan.Keep)
	}
	assertPoint(t, plan.Keep[0].OutPoint, 10)
	assertPoint(t, plan.Keep[1].InPoint, 30)
	assertPoint(t, plan.Keep[1].OutPoint, 40)
	assertPoint(t, plan.Keep[2].InPoint, 50)
	if plan.Duration != 70 {
		t.Fatalf("duration = %v want 70", plan.Duration)
	}
}

func TestPlanCutsStartAndEndEdgeChunks(t *testing.T) {
	chapters := []Chapter{
		{StartTime: 0, EndTime: 5, Category: "sponsor", Type: "skip"},
		{StartTime: 90, EndTime: 100, Category: "outro", Type: "skip"},
	}
	plan, err := PlanCuts(chapters, []string{"sponsor", "outro"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Keep) != 1 {
		t.Fatalf("keep = %#v", plan.Keep)
	}
	assertPoint(t, plan.Keep[0].InPoint, 5)
	assertPoint(t, plan.Keep[0].OutPoint, 90)
	if plan.Duration != 85 {
		t.Fatalf("duration = %v", plan.Duration)
	}
}

func TestPlanCutsRejectsEntireRemovalAndBadInput(t *testing.T) {
	_, err := PlanCuts([]Chapter{{StartTime: 0, EndTime: 10, Category: "sponsor", Type: "skip"}}, []string{"sponsor"}, 10)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("entire removal err = %v", err)
	}
	_, err = PlanCuts(nil, []string{"poi_highlight"}, 10)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("non-removable err = %v", err)
	}
	_, err = PlanCuts(nil, []string{"sponsor"}, math.NaN())
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nan duration err = %v", err)
	}
}

func TestPlanCutsRejectsExcessKeepSegments(t *testing.T) {
	if MaxKeepSegments != 128 {
		t.Fatalf("MaxKeepSegments = %d want 128", MaxKeepSegments)
	}
	chapters := make([]Chapter, 0, MaxKeepSegments)
	// MaxKeepSegments non-overlapping interior cuts produce MaxKeepSegments+1 keep
	// segments (leading chunk + one after each cut).
	for index := 0; index < MaxKeepSegments; index++ {
		start := float64(2*index + 1)
		chapters = append(chapters, Chapter{
			StartTime: start, EndTime: start + 0.5, Category: "sponsor", Type: "skip",
		})
	}
	_, err := PlanCuts(chapters, []string{"sponsor"}, float64(2*MaxKeepSegments+2))
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("keep segment limit err = %v", err)
	}
}

func TestPlanCutsRejectsExcessCutRanges(t *testing.T) {
	if MaxForceKeyframeTimestamps != 512 {
		t.Fatalf("MaxForceKeyframeTimestamps = %d want 512", MaxForceKeyframeTimestamps)
	}
	if maxCutRanges*2 > MaxForceKeyframeTimestamps {
		t.Fatalf("maxCutRanges=%d exceeds force-keyframe budget", maxCutRanges)
	}
	chapters := make([]Chapter, 0, maxCutRanges+1)
	for index := 0; index < maxCutRanges+1; index++ {
		start := float64(index*3 + 1)
		chapters = append(chapters, Chapter{
			StartTime: start, EndTime: start + 1, Category: "sponsor", Type: "skip",
		})
	}
	_, err := PlanCuts(chapters, []string{"sponsor"}, float64((maxCutRanges+1)*3+2))
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cut range limit err = %v", err)
	}
}

func TestPlanCutsNoMatchingCategoriesIsNoop(t *testing.T) {
	plan, err := PlanCuts([]Chapter{{StartTime: 1, EndTime: 2, Category: "sponsor", Type: "skip"}}, []string{"intro"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Cuts) != 0 || plan.Duration != 10 || len(plan.Keep) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestMapTimeAndRewriteChapters(t *testing.T) {
	cuts := []Range{{Start: 10, End: 20}, {Start: 40, End: 50}}
	if got := MapTimeThroughCuts(5, cuts); got != 5 {
		t.Fatalf("pre-cut map = %v", got)
	}
	if got := MapTimeThroughCuts(15, cuts); got != 10 {
		t.Fatalf("inside-cut map = %v", got)
	}
	if got := MapTimeThroughCuts(30, cuts); got != 20 {
		t.Fatalf("between-cut map = %v", got)
	}
	if got := MapTimeThroughCuts(60, cuts); got != 40 {
		t.Fatalf("after-cut map = %v", got)
	}
	chapters := []MarkedChapter{
		{StartTime: 0, EndTime: 12, Title: "a"},
		{StartTime: 12, EndTime: 18, Title: "removed"},
		{StartTime: 25, EndTime: 45, Title: "b"},
	}
	got := RewriteChapterTimes(chapters, cuts)
	if len(got) != 2 {
		t.Fatalf("rewritten = %#v", got)
	}
	if got[0].StartTime != 0 || got[0].EndTime != 10 {
		t.Fatalf("first = %#v", got[0])
	}
	if got[1].StartTime != 15 || got[1].EndTime != 30 {
		t.Fatalf("second = %#v", got[1])
	}
}

func assertPoint(t *testing.T, point *float64, want float64) {
	t.Helper()
	if point == nil || *point != want {
		t.Fatalf("point = %v want %v", point, want)
	}
}
