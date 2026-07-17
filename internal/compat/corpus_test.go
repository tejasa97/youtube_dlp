package compat_test

import (
	"os"
	"testing"

	compattemplate "github.com/ytdlp-go/ytdlp/internal/compat/template"
	"github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/value"
	"gopkg.in/yaml.v3"
)

type corpus struct {
	Version     int `yaml:"version"`
	Reference   reference
	FormatCases []struct {
		Name       string   `yaml:"name"`
		Expression string   `yaml:"expression"`
		Selected   []string `yaml:"selected"`
	} `yaml:"format_cases"`
	TemplateCases []struct {
		Name     string `yaml:"name"`
		Pattern  string `yaml:"pattern"`
		Rendered string `yaml:"rendered"`
	} `yaml:"template_cases"`
}

type reference struct {
	Repository string `yaml:"repository"`
	Commit     string `yaml:"commit"`
	Captured   string `yaml:"captured"`
	Provenance string `yaml:"provenance"`
}

func TestPinnedCompatibilityCorpus(t *testing.T) {
	data, err := os.ReadFile("../../conformance/compatibility/pilot.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cases corpus
	if err := yaml.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if cases.Version != 1 || cases.Reference.Commit == "" || cases.Reference.Provenance == "" {
		t.Fatalf("invalid corpus provenance: %#v", cases.Reference)
	}
	info := corpusInfo()
	for _, test := range cases.FormatCases {
		t.Run("format/"+test.Name, func(t *testing.T) {
			selector, err := format.ParseSelector(test.Expression)
			if err != nil {
				t.Fatal(err)
			}
			selected, err := format.Select(info, selector)
			if err != nil {
				t.Fatal(err)
			}
			if len(selected) != len(test.Selected) {
				t.Fatalf("selected = %#v, want %v", selected, test.Selected)
			}
			for index := range selected {
				if selected[index].ID != test.Selected[index] {
					t.Fatalf("selected[%d] = %q, want %q", index, selected[index].ID, test.Selected[index])
				}
			}
		})
	}
	for _, test := range cases.TemplateCases {
		t.Run("template/"+test.Name, func(t *testing.T) {
			rendered, err := compattemplate.Render(test.Pattern, info)
			if err != nil {
				t.Fatal(err)
			}
			if rendered != test.Rendered {
				t.Fatalf("Render() = %q, want %q", rendered, test.Rendered)
			}
		})
	}
}

func corpusInfo() value.Info {
	mediaFormat := func(id, ext, vcodec, acodec string, height int64, tbr float64) value.Value {
		return value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String(id)},
			value.Field{Key: "url", Value: value.String("https://media.example/" + id)},
			value.Field{Key: "ext", Value: value.String(ext)},
			value.Field{Key: "vcodec", Value: value.String(vcodec)},
			value.Field{Key: "acodec", Value: value.String(acodec)},
			value.Field{Key: "height", Value: value.Int(height)},
			value.Field{Key: "tbr", Value: value.Float(tbr)},
		))
	}
	return value.NewInfo(value.NewObject(
		value.Field{Key: "title", Value: value.String("Synthetic video")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "uploader", Value: value.String("alice")},
		value.Field{Key: "view_count", Value: value.Int(42)},
		value.Field{Key: "rating", Value: value.Float(4.25)},
		value.Field{Key: "upload_date", Value: value.String("20260717")},
		value.Field{Key: "chapters", Value: value.List(
			value.ObjectValue(value.NewObject(value.Field{Key: "title", Value: value.String("first")})),
			value.ObjectValue(value.NewObject(value.Field{Key: "title", Value: value.String("last")})),
		)},
		value.Field{Key: "formats", Value: value.List(
			mediaFormat("360", "mp4", "avc1", "none", 360, 500),
			mediaFormat("720", "webm", "vp9", "none", 720, 1500),
			mediaFormat("audio-low", "m4a", "none", "aac", 0, 64),
			mediaFormat("audio-high", "m4a", "none", "aac", 0, 128),
		)},
	))
}
