package chapterremove

import (
	"context"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPinnedChapterRemovalCorpus(t *testing.T) {
	data, err := os.ReadFile("../../../conformance/compatibility/phase2/chapter_removal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Duration float64 `yaml:"duration"`
		Cases    []struct {
			Name           string      `yaml:"name"`
			Specifications []string    `yaml:"specifications"`
			Title          string      `yaml:"title"`
			Matches        *bool       `yaml:"matches"`
			Ranges         [][]float64 `yaml:"ranges"`
		} `yaml:"cases"`
	}
	if err := yaml.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("empty conformance corpus")
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			program, err := Parse(test.Specifications)
			if err != nil {
				t.Fatal(err)
			}
			if test.Matches != nil {
				got, err := program.MatchTitle(context.Background(), test.Title)
				if err != nil {
					t.Fatal(err)
				}
				if got != *test.Matches {
					t.Fatalf("match = %t, want %t", got, *test.Matches)
				}
			}
			if test.Ranges != nil {
				got, err := program.ResolveRanges(fixture.Duration)
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != len(test.Ranges) {
					t.Fatalf("ranges = %#v", got)
				}
				for index, want := range test.Ranges {
					if len(want) != 2 || got[index].End == nil ||
						got[index].Start != want[0] || *got[index].End != want[1] {
						t.Fatalf("range %d = %#v, want %#v", index, got[index], want)
					}
				}
			}
		})
	}
}
