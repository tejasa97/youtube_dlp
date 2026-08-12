package format

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

func advancedSelectorInfo() value.Info {
	format := func(id, ext, vcodec, acodec string, height int64, tbr float64) value.Value {
		return value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String(id)},
			value.Field{Key: "url", Value: value.String("https://example.invalid/" + id)},
			value.Field{Key: "ext", Value: value.String(ext)},
			value.Field{Key: "vcodec", Value: value.String(vcodec)},
			value.Field{Key: "acodec", Value: value.String(acodec)},
			value.Field{Key: "height", Value: value.Int(height)},
			value.Field{Key: "tbr", Value: value.Float(tbr)},
		))
	}
	return value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(
		format("360", "mp4", "avc1", "none", 360, 500),
		format("720", "webm", "vp9", "none", 720, 1500),
		format("audio-low", "m4a", "none", "aac", 0, 64),
		format("audio-high", "m4a", "none", "aac", 0, 128),
		format("mux", "mp4", "avc1", "aac", 360, 400),
	)}))
}

func TestAdvancedSelectorOperatorTable(t *testing.T) {
	info := advancedSelectorInfo()
	tests := []struct {
		expression string
		plans      [][]string
		wantErr    error
	}{
		{"bestvideo+bestaudio/best", [][]string{{"720", "audio-high"}}, nil},
		{"(bestvideo+bestaudio)/best", [][]string{{"720", "audio-high"}}, nil},
		{"bestvideo,(worstaudio/worst)", [][]string{{"720"}, {"audio-low"}}, nil},
		{"(bv*+ba)[height<=1080]", [][]string{{"720", "audio-high"}}, nil},
		{"best.2", nil, ErrNoMatch},
		{"worstvideo.2", [][]string{{"720"}}, nil},
		{"mp4", [][]string{{"mux"}}, nil},
		{"m4a", [][]string{{"audio-high"}}, nil},
		{"bestvideo[height>9000]", nil, ErrNoMatch},
		{"bestvideo,,best", nil, ErrInvalidSelector},
	}
	for _, test := range tests {
		t.Run(test.expression, func(t *testing.T) {
			selector, err := ParseSelector(test.expression)
			if test.wantErr == ErrInvalidSelector {
				if !errors.Is(err, ErrInvalidSelector) {
					t.Fatalf("ParseSelector() = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			plans, err := PlanSelect(info, selector)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("PlanSelect() = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(plans) != len(test.plans) {
				t.Fatalf("plans = %#v", plans)
			}
			for index, wantTracks := range test.plans {
				if len(plans[index].Tracks) != len(wantTracks) {
					t.Fatalf("plan[%d] = %#v", index, plans[index])
				}
				for trackIndex, want := range wantTracks {
					if plans[index].Tracks[trackIndex].ID != want {
						t.Fatalf("plan[%d].tracks[%d] = %q, want %q", index, trackIndex, plans[index].Tracks[trackIndex].ID, want)
					}
				}
			}
		})
	}
}

func TestAdvancedSelectorLegacyFlatAPIRejectsComma(t *testing.T) {
	selector, err := ParseSelector("bestvideo,bestaudio")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Select(advancedSelectorInfo(), selector)
	if !errors.Is(err, ErrMultiOutput) {
		t.Fatalf("Select() = %v", err)
	}
}

func TestAdvancedSelectorAdversarialBounds(t *testing.T) {
	if _, err := ParseSelector(strings.Repeat("best,", 65) + "best"); !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("comma overflow = %v", err)
	}
	merge := "best"
	for index := 0; index < maxMergeTerms-1; index++ {
		merge += "+best"
	}
	if _, err := ParseSelector(merge + "+best"); !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("merge overflow = %v", err)
	}
	separate := merge + "," + merge
	if _, err := ParseSelector(separate); err != nil {
		t.Fatalf("separate merge budgets = %v", err)
	}
}

func TestAdvancedSelectorEvaluatorOutputBounds(t *testing.T) {
	within := boundedFormatsInfo(maxCommaOutputs)
	selector, err := ParseSelector("all")
	if err != nil {
		t.Fatal(err)
	}
	plans, err := PlanSelect(within, selector)
	if err != nil || len(plans) != maxCommaOutputs {
		t.Fatalf("all within limit = %#v, %v", plans, err)
	}
	over := boundedFormatsInfo(maxCommaOutputs + 1)
	if _, err := PlanSelect(over, selector); !errors.Is(err, ErrSelectorLimit) {
		t.Fatalf("all over limit = %v", err)
	}
}

func TestAdvancedSelectorMergeAllTrackBounds(t *testing.T) {
	within := boundedFormatsInfo(maxMergeTerms)
	selector, err := ParseSelector("mergeall")
	if err != nil {
		t.Fatal(err)
	}
	multistream := Options{AllowMultipleVideoStreams: true, AllowMultipleAudioStreams: true}
	plans, err := PlanSelectWithOptions(within, selector, multistream)
	if err != nil || len(plans) != 1 || len(plans[0].Tracks) != maxMergeTerms {
		t.Fatalf("mergeall within limit = %#v, %v", plans, err)
	}
	over := boundedFormatsInfo(maxMergeTerms + 1)
	if _, err := PlanSelectWithOptions(over, selector, multistream); !errors.Is(err, ErrSelectorLimit) {
		t.Fatalf("mergeall over limit = %v", err)
	}
}

func boundedFormatsInfo(count int) value.Info {
	formats := make([]value.Value, 0, count)
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("f%03d", index)
		formats = append(formats, value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String(id)},
			value.Field{Key: "url", Value: value.String("https://example.invalid/" + id)},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("aac")},
		)))
	}
	return value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: value.List(formats...)}))
}

func TestAdvancedSelectorMergeRetainsDistinctSameKindTracks(t *testing.T) {
	info := advancedSelectorInfo()
	selector, err := ParseSelector("bestvideo+bestvideo.2+bestaudio")
	if err != nil {
		t.Fatal(err)
	}
	plans, err := PlanSelect(info, selector)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].Tracks) != 2 {
		t.Fatalf("plans = %#v", plans)
	}
	// The evaluator applies Python-compatible multistream suppression. With
	// AllowMultipleVideoStreams=false the planner keeps the first
	// video-bearing track and removes the later one; bestaudio is the
	// only audio-bearing track and survives.
	want := []string{"720", "audio-high"}
	for index, id := range want {
		if plans[0].Tracks[index].ID != id {
			t.Fatalf("track[%d] = %q, want %q", index, plans[0].Tracks[index].ID, id)
		}
	}
}

func TestAdvancedSelectorDeterminismConcurrent(t *testing.T) {
	info := advancedSelectorInfo()
	selector, err := ParseSelector("bestvideo+bestaudio")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	const workers = 16
	errs := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			selected, selectErr := Select(info, selector)
			if selectErr != nil {
				errs <- selectErr
				return
			}
			if len(selected) != 2 || selected[0].ID != "720" || selected[1].ID != "audio-high" {
				errs <- ErrNoMatch
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdvancedSelectorFilterOnlyImplicitBest(t *testing.T) {
	// Pinned implicit-best behavior: with no filter type/star requires both
	// vcodec and acodec. The audio-high format is audio-only and so does
	// not match — the test exercises the positive path through a
	// combined format with a format_id filter.
	selector, err := ParseSelector("[format_id=mux]")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := Select(advancedSelectorInfo(), selector)
	if err != nil || len(selected) != 1 || selected[0].ID != "mux" {
		t.Fatalf("selected = %#v, %v", selected, err)
	}
}

func TestAdvancedSelectorEmptyGroupRejected(t *testing.T) {
	selector, err := ParseSelector("()")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PlanSelect(advancedSelectorInfo(), selector); !errors.Is(err, ErrNoMatch) {
		t.Fatalf("empty group = %v", err)
	}
}

func FuzzEvaluateSelector(f *testing.F) {
	f.Add("bestvideo+bestaudio/best")
	f.Add("best[ext=mp4]")
	f.Add("(bv+ba)/best")
	f.Add("all")
	f.Fuzz(func(t *testing.T, input string) {
		selector, err := ParseSelector(input)
		if err != nil {
			return
		}
		plans, err := PlanSelect(advancedSelectorInfo(), selector)
		if err != nil {
			if errors.Is(err, ErrSelectorLimit) || errors.Is(err, ErrNoMatch) || errors.Is(err, ErrNoFormats) {
				return
			}
			t.Fatalf("PlanSelect(%q) = %v", input, err)
		}
		if len(plans) > maxCommaOutputs {
			t.Fatalf("plans = %d", len(plans))
		}
		for _, plan := range plans {
			if len(plan.Tracks) > maxMergeTerms {
				t.Fatalf("tracks = %d", len(plan.Tracks))
			}
		}
	})
}
