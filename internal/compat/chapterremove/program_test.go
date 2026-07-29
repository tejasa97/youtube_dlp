package chapterremove

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestParseMatchesTitlesWithSearchSemantics(t *testing.T) {
	program, err := Parse([]string{`(?i)\bintro\b`, `credits$`})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		title string
		want  bool
	}{
		{"An Intro Chapter", true},
		{"Opening credits", true},
		{"Introduction", false},
	} {
		got, err := program.MatchTitle(context.Background(), test.title)
		if err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Fatalf("MatchTitle(%q) = %t, want %t", test.title, got, test.want)
		}
	}
}

func TestParseUsesBoundedPythonRegexSearch(t *testing.T) {
	program, err := Parse([]string{
		`(?<=\b)intro(?=\b)`,            // look-around
		`(?P<word>chapter)\s+(?P=word)`, // named capture/backreference
		`(?im)^über$`,                   // flags and Unicode
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		title string
		want  bool
	}{
		{"the intro chapter", true},
		{"chapter chapter", true},
		{"ÜBER", true},
		{"introduction", false},
	} {
		got, matchErr := program.MatchTitle(context.Background(), test.title)
		if matchErr != nil || got != test.want {
			t.Fatalf("MatchTitle(%q) = %t, %v; want %t", test.title, got, matchErr, test.want)
		}
	}
}

func TestMatchTitleBoundsInputAndAggregateWork(t *testing.T) {
	program, err := Parse([]string{`never-match`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := program.MatchTitle(context.Background(), strings.Repeat("x", MaxRegexInputBytes+1)); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized title error = %v", err)
	}
	budget := NewEvaluationBudget()
	for index := 0; index < MaxRegexAttempts; index++ {
		if matched, err := program.MatchTitleWithBudget(context.Background(), "fixture", budget); err != nil || matched {
			t.Fatalf("attempt %d = %t, %v", index, matched, err)
		}
	}
	if _, err := program.MatchTitleWithBudget(context.Background(), "fixture", budget); !errors.Is(err, ErrLimit) {
		t.Fatalf("aggregate budget error = %v", err)
	}
}

func TestParseManualRangesAndResolve(t *testing.T) {
	program, err := Parse([]string{"*1:30-2:00, -10, 3m-", "*5-inf"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := program.ResolveRanges(200)
	if err != nil {
		t.Fatal(err)
	}
	end := func(value float64) *float64 { return &value }
	want := []Range{
		{Start: 90, End: end(120)},
		{Start: 0, End: end(10)},
		{Start: 180, End: end(200)},
		{Start: 5, End: end(200)},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveRanges() = %#v, want %#v", got, want)
	}
}

func TestParsePinnedDurationForms(t *testing.T) {
	tests := map[string]float64{
		"1":                         1,
		"1337:12":                   80232,
		"9:12:43":                   33163,
		"3h11m53s":                  11513,
		"3m":                        180,
		"3 hours, 11 mins, 53 secs": 11513,
		"01:02:03.05":               3723.05,
		"T30M38S":                   1838,
		"2.5 hours":                 9000,
		"01:02:03:050":              3723.05,
		"103:050":                   103.05,
		"PT1H0.040S":                3600.04,
		"P0Y0M0DT0H4M20.880S":       260.88,
	}
	for input, want := range tests {
		got, ok := parseTimestamp(input, false)
		if !ok || math.Abs(got-want) > 0.000001 {
			t.Errorf("parseTimestamp(%q) = %v, %t; want %v", input, got, ok, want)
		}
	}
}

func TestParseRejectsMalformedNegativeAndInvertedRanges(t *testing.T) {
	for _, input := range []string{
		"*", "*-", "*1", "*-1-2", "*--10", "*10-5", "*10-10", "*inf-",
		"*bad-10", "*1--2", "*1-2,", "*1--inf", "*1-nan",
	} {
		if _, err := Parse([]string{input}); !errors.Is(err, ErrInvalidSpecification) {
			t.Errorf("Parse(%q) err = %v", input, err)
		}
	}
}

func TestParseRejectsInvalidRegexAndBounds(t *testing.T) {
	if _, err := Parse([]string{"("}); !errors.Is(err, ErrInvalidSpecification) {
		t.Fatalf("invalid regex err = %v", err)
	}
	tooMany := make([]string, MaxSpecifications+1)
	if _, err := Parse(tooMany); !errors.Is(err, ErrLimit) {
		t.Fatalf("specification limit err = %v", err)
	}
	if _, err := Parse([]string{strings.Repeat("x", MaxSpecificationBytes+1)}); !errors.Is(err, ErrLimit) {
		t.Fatalf("byte limit err = %v", err)
	}
	ranges := make([]string, MaxRanges+1)
	for index := range ranges {
		ranges[index] = "0-1"
	}
	if _, err := Parse([]string{"*" + strings.Join(ranges, ",")}); !errors.Is(err, ErrLimit) {
		t.Fatalf("range limit err = %v", err)
	}
}

func TestMatchTitleHonorsCancellationAndProgramIsImmutable(t *testing.T) {
	input := []string{"intro"}
	program, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = "credits"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := program.MatchTitle(ctx, "intro"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel err = %v", err)
	}
	got, err := program.MatchTitle(context.Background(), "intro")
	if err != nil || !got {
		t.Fatalf("immutable match = %t, %v", got, err)
	}
}

func TestResolveRangesDropsOutsideMediaAndRejectsDuration(t *testing.T) {
	program, err := Parse([]string{"*10-20,30-40"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := program.ResolveRanges(15)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Start != 10 || got[0].End == nil || *got[0].End != 15 {
		t.Fatalf("resolved ranges = %#v", got)
	}
	for _, duration := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := program.ResolveRanges(duration); !errors.Is(err, ErrInvalidSpecification) {
			t.Errorf("duration %v err = %v", duration, err)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, seed := range []string{"intro", "(?i)credits$", "*1:30-2:00", "*-10,20-", "*bad"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, specification string) {
		program, err := Parse([]string{specification})
		if err != nil {
			return
		}
		_, _ = program.MatchTitle(context.Background(), "fixture chapter")
		ranges, resolveErr := program.ResolveRanges(3600)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		for _, item := range ranges {
			if item.Start < 0 || item.End == nil || *item.End <= item.Start || *item.End > 3600 {
				t.Fatalf("unsafe range %#v", item)
			}
		}
	})
}
