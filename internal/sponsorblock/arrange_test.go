package sponsorblock

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func ordinary(start, end float64, title string, remove bool) ArrangeChapter {
	return ArrangeChapter{StartTime: start, EndTime: end, Title: title, Remove: remove, Source: 0}
}

func sponsor(start, end float64, category string) ArrangeChapter {
	return ArrangeChapter{
		StartTime: start, EndTime: end, Source: -1,
		Categories: []CategorySpan{{Category: category, Start: start, End: end, Title: category}},
	}
}

func TestArrangeEightOverlapCases(t *testing.T) {
	tests := []struct {
		name     string
		input    []ArrangeChapter
		wantCuts []Range
		want     int
	}{
		{"cut-cut", []ArrangeChapter{ordinary(0, 4, "cut", true), ordinary(2, 6, "cut", true), ordinary(6, 10, "n", false)}, []Range{{0, 6}}, 1},
		{"cut-sponsor", []ArrangeChapter{ordinary(0, 4, "cut", true), sponsor(2, 6, "s")}, []Range{{0, 4}}, 1},
		{"cut-normal", []ArrangeChapter{ordinary(0, 4, "cut", true), ordinary(2, 6, "n", false)}, []Range{{0, 4}}, 1},
		{"sponsor-cut-contained", []ArrangeChapter{sponsor(0, 10, "s"), ordinary(3, 5, "cut", true)}, []Range{{3, 5}}, 1},
		{"normal-cut-contained", []ArrangeChapter{ordinary(0, 10, "n", false), ordinary(3, 5, "cut", true)}, []Range{{3, 5}}, 1},
		{"sponsor-sponsor", []ArrangeChapter{sponsor(0, 5, "a"), sponsor(3, 8, "b")}, []Range{}, 3},
		{"sponsor-normal", []ArrangeChapter{sponsor(0, 5, "a"), ordinary(3, 8, "n", false)}, []Range{}, 2},
		{"normal-sponsor", []ArrangeChapter{ordinary(0, 5, "n", false), sponsor(3, 8, "a")}, []Range{}, 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Arrange(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Cuts, test.wantCuts) || len(got.Chapters) != test.want {
				t.Fatalf("Arrange() = %#v, want cuts %#v and %d chapters", got, test.wantCuts, test.want)
			}
		})
	}
}

func TestArrangeNestedAdjacentCutsAndCategorySplit(t *testing.T) {
	input := []ArrangeChapter{
		sponsor(0, 12, "before"),
		ordinary(3, 5, "cut", true),
		ordinary(5, 7, "cut", true),
		ordinary(4, 6, "cut", true),
	}
	input[0].Categories = []CategorySpan{
		{Category: "before", Start: 0, End: 3, Title: "Before"},
		{Category: "after", Start: 7, End: 12, Title: "After"},
	}
	got, err := Arrange(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Cuts, []Range{{Start: 3, End: 7}}) || len(got.Chapters) != 2 {
		t.Fatalf("Arrange() = %#v", got)
	}
	if got.Chapters[0].Category != "before" || got.Chapters[1].Category != "after" {
		t.Fatalf("category split = %#v", got.Chapters)
	}
}

func TestArrangeStableEqualStarts(t *testing.T) {
	input := []ArrangeChapter{sponsor(0, 3, "first"), sponsor(0, 3, "second")}
	got, err := Arrange(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Chapters) != 1 || got.Chapters[0].Title != "[SponsorBlock]: first, second" {
		t.Fatalf("equal-start ordering = %#v", got.Chapters)
	}
}

func TestArrangeLaterSponsorAfterPartUsesCurrentIndexTieBreak(t *testing.T) {
	// normal/sponsor where the sponsor extends past current: the after-part must
	// re-enter the heap with cur_i. An ordinary chapter queued at the same start
	// with an intermediate index exposes next.index vs cur_i ordering.
	input := []ArrangeChapter{
		ordinary(0, 10, "early", false),
		ordinary(10, 20, "queued", false),
		sponsor(5, 15, "extend"),
	}
	input[0].Source = 0
	input[1].Source = 1
	got, err := Arrange(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Chapters) != 3 {
		t.Fatalf("chapters = %#v", got.Chapters)
	}
	// With cur_i, the sponsor after-part (start 10) precedes the queued ordinary
	// chapter (also start 10, index 1), yielding sponsor then ordinary.
	if !got.Chapters[1].Sponsor || got.Chapters[1].Category != "extend" {
		t.Fatalf("expected sponsor after-part before queued ordinary, got %#v", got.Chapters)
	}
	if got.Chapters[2].Sponsor || got.Chapters[2].Title != "queued" {
		t.Fatalf("expected queued ordinary last, got %#v", got.Chapters)
	}
}

func TestArrangeTinyPolicyAndNoMatch(t *testing.T) {
	tiny := []ArrangeChapter{ordinary(0, .5, "tiny", false), ordinary(.5, 3, "rest", false)}
	got, err := Arrange(tiny)
	if err != nil || len(got.Chapters) != 2 {
		t.Fatalf("unmodified tiny = %#v, %v", got, err)
	}
	got, err = Arrange([]ArrangeChapter{ordinary(0, 2, "a", false), ordinary(2, 2.5, "cut", true), ordinary(2.5, 5, "b", false)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Chapters) != 2 || got.Chapters[0].EndTime != 2 || got.Chapters[1].StartTime != 2 {
		t.Fatalf("cut tiny policy = %#v", got.Chapters)
	}
	noMatch, err := Arrange([]ArrangeChapter{ordinary(10, 12, "a", false), ordinary(12, 15, "b", false)})
	if err != nil {
		t.Fatal(err)
	}
	if noMatch.Chapters[0].StartTime != 0 || noMatch.Chapters[1].StartTime != 2 || noMatch.Chapters[1].EndTime != 5 {
		t.Fatalf("retimed no-match = %#v", noMatch.Chapters)
	}
}

func TestArrangeCompleteRemovalImmutabilityAndDeterminism(t *testing.T) {
	input := []ArrangeChapter{
		ordinary(0, 2, "cut", true),
		sponsor(2, 4, "s"),
		ordinary(4, 6, "cut", true),
	}
	before := cloneArrangeInputs(input)
	first, err := Arrange(input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Arrange(input)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("non-deterministic: %#v %#v %v", first, second, err)
	}
	if len(first.Chapters) != 1 || !reflect.DeepEqual(input, before) {
		t.Fatalf("result=%#v input=%#v before=%#v", first, input, before)
	}
	all, err := Arrange([]ArrangeChapter{ordinary(0, 10, "cut", true)})
	if err != nil || len(all.Chapters) != 0 || !reflect.DeepEqual(all.Cuts, []Range{{0, 10}}) {
		t.Fatalf("complete removal = %#v, %v", all, err)
	}
}

func TestArrangeRemoveOnlyRetimesOrdinaryAndMarkRemoveMix(t *testing.T) {
	removeOnly := []ArrangeChapter{
		ordinary(0, 40, "Intro", false),
		ordinary(40, 100, "Main", false),
		{StartTime: 10, EndTime: 20, Title: "Sponsor", Remove: true, Source: -1,
			Categories: []CategorySpan{{Category: "sponsor", Start: 10, End: 20, Title: "Sponsor"}}},
	}
	got, err := Arrange(removeOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Cuts, []Range{{10, 20}}) || len(got.Chapters) != 2 {
		t.Fatalf("remove-only = %#v", got)
	}
	if got.Chapters[0].Title != "Intro" || got.Chapters[0].EndTime != 30 {
		t.Fatalf("retimed intro = %#v", got.Chapters[0])
	}
	if got.Chapters[1].Title != "Main" || got.Chapters[1].StartTime != 30 || got.Chapters[1].EndTime != 90 {
		t.Fatalf("retimed main = %#v", got.Chapters[1])
	}

	markRemove := []ArrangeChapter{
		ordinary(0, 100, "Video", false),
		sponsor(10, 20, "sponsor"),
		{StartTime: 40, EndTime: 50, Title: "Intro", Remove: true, Source: -1,
			Categories: []CategorySpan{{Category: "intro", Start: 40, End: 50, Title: "Intro"}}},
		sponsor(60, 70, "selfpromo"),
	}
	markRemove[1].Remove = false
	got, err = Arrange(markRemove)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Cuts, []Range{{40, 50}}) {
		t.Fatalf("cuts = %#v", got.Cuts)
	}
	sponsors := 0
	for _, chapter := range got.Chapters {
		if chapter.Sponsor {
			sponsors++
		}
	}
	if sponsors != 2 {
		t.Fatalf("mark+remove chapters = %#v", got.Chapters)
	}
}

func TestArrangeIdenticalSponsorCoalesce(t *testing.T) {
	got, err := Arrange([]ArrangeChapter{
		ordinary(0, 20, "n", false),
		sponsor(2, 5, "sponsor"),
		sponsor(5, 8, "sponsor"),
	})
	if err != nil {
		t.Fatal(err)
	}
	sponsors := 0
	for _, chapter := range got.Chapters {
		if chapter.Sponsor {
			sponsors++
			if chapter.Title != "[SponsorBlock]: sponsor" {
				t.Fatalf("title = %q", chapter.Title)
			}
		}
	}
	if sponsors != 1 {
		t.Fatalf("coalesced sponsors = %#v", got.Chapters)
	}
}

func TestArrangeValidation(t *testing.T) {
	for _, input := range [][]ArrangeChapter{
		{{StartTime: math.NaN(), EndTime: 1}},
		{{StartTime: -1, EndTime: 1}},
		{{StartTime: 2, EndTime: 1}},
		make([]ArrangeChapter, maxArrangeChapters+1),
	} {
		if _, err := Arrange(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Arrange(%#v) error = %v", input, err)
		}
	}
}

func cloneArrangeInputs(input []ArrangeChapter) []ArrangeChapter {
	out := make([]ArrangeChapter, len(input))
	if input == nil {
		return nil
	}
	for i, chapter := range input {
		out[i] = cloneArrangeChapter(chapter)
	}
	return out
}

func TestArrangeConformanceMarkRemoveFixtureShape(t *testing.T) {
	input := []ArrangeChapter{
		ordinary(0, 40, "Intro", false),
		ordinary(40, 100, "Main", false),
		{StartTime: 10, EndTime: 20, Title: "Sponsor", Remove: true, Source: -1,
			Categories: []CategorySpan{{Category: "sponsor", Start: 10, End: 20, Title: "Sponsor"}}},
		{StartTime: 55, EndTime: 65, Title: "Unpaid/Self Promotion", Source: -1,
			Categories: []CategorySpan{{Category: "selfpromo", Start: 55, End: 65, Title: "Unpaid/Self Promotion"}}},
	}
	got, err := Arrange(input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Cuts, []Range{{10, 20}}) {
		t.Fatalf("cuts=%#v", got.Cuts)
	}
	if len(got.Chapters) != 4 {
		t.Fatalf("chapters=%#v", got.Chapters)
	}
	want := []struct {
		start, end float64
		title      string
		sponsor    bool
		category   string
	}{
		{0, 30, "Intro", false, ""},
		{30, 45, "Main", false, ""},
		{45, 55, "[SponsorBlock]: Unpaid/Self Promotion", true, "selfpromo"},
		{55, 90, "Main", false, ""},
	}
	for i, w := range want {
		c := got.Chapters[i]
		if c.StartTime != w.start || c.EndTime != w.end || c.Title != w.title || c.Sponsor != w.sponsor || c.Category != w.category {
			t.Fatalf("chapter[%d]=%#v want %#v", i, c, w)
		}
	}
}
