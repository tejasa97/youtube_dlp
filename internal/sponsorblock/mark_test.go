package sponsorblock

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestMarkChaptersNoOrdinaryAndOverlap(t *testing.T) {
	sponsors := []Chapter{
		{StartTime: 10, EndTime: 40, Category: "sponsor", Title: "Sponsor", Type: "skip"},
		{StartTime: 20, EndTime: 30, Category: "selfpromo", Title: "Unpaid/Self Promotion", Type: "skip"},
	}
	result, err := MarkChapters(nil, sponsors, 60, "Video")
	if err != nil {
		t.Fatal(err)
	}
	assertMarked(t, result, []MarkedChapter{
		{StartTime: 0, EndTime: 10, Title: "Video", Source: -1},
		{StartTime: 10, EndTime: 20, Title: "[SponsorBlock]: Sponsor", Source: -1, Sponsor: true},
		{StartTime: 20, EndTime: 30, Title: "[SponsorBlock]: Sponsor, Unpaid/Self Promotion", Source: -1, Sponsor: true},
		{StartTime: 30, EndTime: 40, Title: "[SponsorBlock]: Sponsor", Source: -1, Sponsor: true},
		{StartTime: 40, EndTime: 60, Title: "Video", Source: -1},
	})
	if !reflect.DeepEqual(result[2].Categories, []string{"sponsor", "selfpromo"}) ||
		result[2].Category != "selfpromo" || result[2].Name != "Unpaid/Self Promotion" {
		t.Fatalf("overlap metadata = %#v", result[2])
	}
}

func TestMarkChaptersPreservesOrdinarySourcesAndChapterDescriptions(t *testing.T) {
	normal := []NormalChapter{
		{StartTime: 0, EndTime: 20, Title: "First", Source: 0},
		{StartTime: 20, EndTime: 40, Title: "Second", Source: 1},
	}
	sponsors := []Chapter{
		{StartTime: 5, EndTime: 15, Category: "chapter", Title: "Custom chapter", Type: "chapter"},
		{StartTime: 25, EndTime: 35, Category: "preview", Title: "Preview/Recap", Type: "skip"},
	}
	result, err := MarkChapters(normal, sponsors, 40, "Video")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 6 || result[0].Source != 0 || result[2].Source != 0 ||
		result[3].Source != 1 || result[5].Source != 1 {
		t.Fatalf("sources = %#v", result)
	}
	if result[1].Title != "[SponsorBlock]: Custom chapter" || result[1].Category != "chapter" {
		t.Fatalf("chapter marker = %#v", result[1])
	}
}

func TestMarkChaptersPreservesOriginalTinyChapters(t *testing.T) {
	normal := []NormalChapter{
		{StartTime: 0, EndTime: .1, Title: "A", Source: 0},
		{StartTime: .1, EndTime: .2, Title: "B", Source: 1},
		{StartTime: .2, EndTime: .3, Title: "C", Source: 2},
		{StartTime: .3, EndTime: .4, Title: "D", Source: 3},
	}
	result, err := MarkChapters(normal, nil, .4, "Video")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != len(normal) {
		t.Fatalf("result = %#v", result)
	}
	for index := range result {
		if result[index].Title != normal[index].Title ||
			result[index].StartTime != normal[index].StartTime ||
			result[index].EndTime != normal[index].EndTime {
			t.Fatalf("chapter %d = %#v", index, result[index])
		}
	}
}

func TestMarkChaptersTinyCreatedFragmentsFollowPinnedMergePolicy(t *testing.T) {
	normal := []NormalChapter{{StartTime: 0, EndTime: 10, Title: "Video", Source: 0}}
	result, err := MarkChapters(normal, []Chapter{
		{StartTime: 0, EndTime: .5, Category: "sponsor", Title: "Sponsor", Type: "skip"},
	}, 10, "Video")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].StartTime != 0 || result[0].EndTime != 10 ||
		result[0].Title != "Video" {
		t.Fatalf("result = %#v", result)
	}
}

func TestMarkChaptersAdjacentIdenticalSponsorMarkersCoalesce(t *testing.T) {
	result, err := MarkChapters(
		[]NormalChapter{{StartTime: 0, EndTime: 40, Title: "Video", Source: 0}},
		[]Chapter{
			{StartTime: 10, EndTime: 20, Category: "sponsor", Title: "Sponsor", Type: "skip"},
			{StartTime: 20, EndTime: 30, Category: "sponsor", Title: "Sponsor", Type: "skip"},
		}, 40, "Video")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 || result[1].StartTime != 10 || result[1].EndTime != 30 {
		t.Fatalf("result = %#v", result)
	}
}

func TestMarkChaptersValidationDeterminismAndImmutability(t *testing.T) {
	normal := []NormalChapter{{StartTime: 0, EndTime: 30, Title: "Video", Source: 7}}
	sponsors := []Chapter{{StartTime: 5, EndTime: 10, Category: "sponsor", Title: "Sponsor", Type: "skip"}}
	normalBefore := append([]NormalChapter(nil), normal...)
	sponsorsBefore := append([]Chapter(nil), sponsors...)
	first, err := MarkChapters(normal, sponsors, 30, "Video")
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarkChapters(normal, sponsors, 30, "Video")
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%#v second=%#v error=%v", first, second, err)
	}
	if !reflect.DeepEqual(normal, normalBefore) || !reflect.DeepEqual(sponsors, sponsorsBefore) {
		t.Fatal("inputs were mutated")
	}
	for _, test := range []struct {
		normal   []NormalChapter
		sponsors []Chapter
		duration float64
	}{
		{duration: math.NaN()},
		{normal: []NormalChapter{{StartTime: 2, EndTime: 1}}, duration: 3},
		{normal: []NormalChapter{{StartTime: 0, EndTime: 2}, {StartTime: 1, EndTime: 3}}, duration: 3},
		{sponsors: []Chapter{{StartTime: 0, EndTime: 4, Category: "sponsor"}}, duration: 3},
		{sponsors: []Chapter{{StartTime: 0, EndTime: 1, Category: "unknown"}}, duration: 3},
	} {
		if _, err := MarkChapters(test.normal, test.sponsors, test.duration, "Video"); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("error = %v for %#v", err, test)
		}
	}
}

func assertMarked(t *testing.T, got, want []MarkedChapter) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index].StartTime != want[index].StartTime ||
			got[index].EndTime != want[index].EndTime ||
			got[index].Title != want[index].Title ||
			got[index].Source != want[index].Source ||
			got[index].Sponsor != want[index].Sponsor {
			t.Fatalf("chapter %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}
