package sponsorblock

import (
	"errors"
	"reflect"
	"testing"
)

func TestMarkChaptersCustomTitleReceivesArrangedFields(t *testing.T) {
	var seen []ChapterTitleFields
	renderer := func(fields ChapterTitleFields) (string, error) {
		record := fields
		record.Categories = append([]string(nil), fields.Categories...)
		record.CategoryNames = append([]string(nil), fields.CategoryNames...)
		seen = append(seen, record)
		fields.Categories[0] = "mutated"
		fields.CategoryNames[0] = "mutated"
		return fields.Category + ":" + fields.Name, nil
	}
	chapters, err := MarkChaptersWithTitle(nil, []Chapter{
		{StartTime: 10, EndTime: 40, Category: "sponsor", Title: "Sponsor", Type: "skip"},
		{StartTime: 20, EndTime: 30, Category: "selfpromo", Title: "Self", Type: "mute"},
	}, 50, "Video", renderer)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || seen[1].StartTime != 20 || seen[1].EndTime != 30 ||
		seen[1].Category != "selfpromo" || seen[1].Name != "Self" ||
		!reflect.DeepEqual(seen[1].Categories, []string{"sponsor", "selfpromo"}) ||
		!reflect.DeepEqual(seen[1].CategoryNames, []string{"Sponsor", "Self"}) {
		t.Fatalf("fields = %#v", seen)
	}
	if chapters[2].Title != "selfpromo:Self" ||
		!reflect.DeepEqual(chapters[2].Categories, []string{"sponsor", "selfpromo"}) {
		t.Fatalf("chapters = %#v", chapters)
	}
}

func TestCustomTitleControlsCoalescingAndAllowsEmpty(t *testing.T) {
	constant := func(ChapterTitleFields) (string, error) { return "same", nil }
	marked, err := MarkChaptersWithTitle(nil, []Chapter{
		{StartTime: 1, EndTime: 3, Category: "sponsor", Title: "Sponsor", Type: "skip"},
		{StartTime: 3, EndTime: 5, Category: "intro", Title: "Intro", Type: "skip"},
	}, 6, "Video", constant)
	if err != nil {
		t.Fatal(err)
	}
	sponsorCount := 0
	for _, chapter := range marked {
		if chapter.Sponsor {
			sponsorCount++
			if chapter.StartTime != 1 || chapter.EndTime != 5 || chapter.Title != "same" {
				t.Fatalf("coalesced marker = %#v", chapter)
			}
		}
	}
	if sponsorCount != 1 {
		t.Fatalf("chapters = %#v", marked)
	}

	empty := func(ChapterTitleFields) (string, error) { return "", nil }
	arranged, err := ArrangeWithTitle([]ArrangeChapter{
		{StartTime: 0, EndTime: 6, Title: "Video", Source: 0},
		{StartTime: 1, EndTime: 3, Source: -1, Categories: []CategorySpan{{
			Category: "sponsor", Start: 1, End: 3, Title: "Sponsor",
		}}},
	}, empty)
	if err != nil {
		t.Fatal(err)
	}
	if len(arranged.Chapters) != 3 || arranged.Chapters[1].Title != "" {
		t.Fatalf("arranged = %#v", arranged.Chapters)
	}
}

func TestChapterTitleRendererFailureAndBoundAreSecretSafe(t *testing.T) {
	boom := func(ChapterTitleFields) (string, error) { return "", errors.New("secret-template") }
	if _, err := MarkChaptersWithTitle(nil, []Chapter{{
		StartTime: 1, EndTime: 2, Category: "sponsor", Title: "Sponsor", Type: "skip",
	}}, 3, "Video", boom); !errors.Is(err, ErrInvalidInput) || err.Error() != "sponsorblock invalid input: chapter title template" {
		t.Fatalf("error = %v", err)
	}
	huge := func(ChapterTitleFields) (string, error) {
		return string(make([]byte, MaxChapterTitleBytes+1)), nil
	}
	input := []ArrangeChapter{{
		StartTime: 1, EndTime: 2, Categories: []CategorySpan{{
			Category: "sponsor", Start: 1, End: 2, Title: "Sponsor",
		}},
	}}
	before := []ArrangeChapter{cloneArrangeChapter(input[0])}
	if _, err := ArrangeWithTitle(input, huge); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatalf("input mutated: %#v", input)
	}
}

func TestArrangeCustomTitleObservesTinyPrependBoundary(t *testing.T) {
	var seen []ChapterTitleFields
	renderer := func(fields ChapterTitleFields) (string, error) {
		seen = append(seen, fields)
		return "custom", nil
	}
	result, err := ArrangeWithTitle([]ArrangeChapter{
		{StartTime: 0, EndTime: 4, Title: "Video", Source: 0},
		{StartTime: 1, EndTime: 1.5, Source: -1, Categories: []CategorySpan{{
			Category: "intro", Start: 1, End: 1.5, Title: "Intro",
		}}},
		{StartTime: 1.5, EndTime: 3, Source: -1, Categories: []CategorySpan{{
			Category: "sponsor", Start: 1.5, End: 3, Title: "Sponsor",
		}}},
	}, renderer)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].StartTime != 1 || seen[0].EndTime != 3 ||
		seen[0].Category != "sponsor" {
		t.Fatalf("rendered fields = %#v; chapters=%#v", seen, result.Chapters)
	}
}
