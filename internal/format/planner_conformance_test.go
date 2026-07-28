package format

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

type plannerConformance struct {
	SchemaVersion int                   `json:"schema_version"`
	Reference     plannerConformanceRef `json:"reference"`
	Cases         []json.RawMessage     `json:"cases"`
}

type plannerConformanceRef struct {
	Repository    string   `json:"repository"`
	Commit        string   `json:"commit"`
	PythonVersion string   `json:"python_version"`
	Sources       []string `json:"sources"`
}

type plannerFormatFixture struct {
	FormatID       string   `json:"format_id"`
	URL            string   `json:"url"`
	Ext            string   `json:"ext"`
	VCodec         *string  `json:"vcodec"`
	ACodec         *string  `json:"acodec"`
	Width          *int64   `json:"width"`
	Height         *int64   `json:"height"`
	FPS            *float64 `json:"fps"`
	TBR            *float64 `json:"tbr"`
	VBR            *float64 `json:"vbr"`
	ABR            *float64 `json:"abr"`
	ASR            *float64 `json:"asr"`
	AudioChannels  *int64   `json:"audio_channels"`
	Filesize       *int64   `json:"filesize"`
	FilesizeApprox *int64   `json:"filesize_approx"`
	DynamicRange   *string  `json:"dynamic_range"`
	Language       *string  `json:"language"`
	FormatNote     *string  `json:"format_note"`
}

type plannerExpectedPlan struct {
	Tracks []plannerExpectedTrack `json:"tracks"`
	Merged json.RawMessage        `json:"merged"`
}

type plannerExpectedTrack struct {
	FormatID string `json:"format_id"`
}

type plannerSelectorCase struct {
	ID       string                 `json:"id"`
	Formats  []plannerFormatFixture `json:"formats"`
	Selector string                 `json:"selector"`
	Options  plannerSelectorOptions `json:"options"`
	Plans    []plannerExpectedPlan  `json:"plans"`
}

type plannerSelectorOptions struct {
	AllowMultipleVideoStreams bool `json:"allow_multiple_video_streams"`
	AllowMultipleAudioStreams bool `json:"allow_multiple_audio_streams"`
	PreferFreeFormats         bool `json:"prefer_free_formats"`
}

type plannerCompatibleCase struct {
	ID          string   `json:"id"`
	VCodecs     []string `json:"vcodecs"`
	ACodecs     []string `json:"acodecs"`
	VExts       []string `json:"vexts"`
	AExts       []string `json:"aexts"`
	Preferences []string `json:"preferences"`
	Expected    string   `json:"expected"`
}

type plannerCompatibleBlock struct {
	ID    string                  `json:"id"`
	Cases []plannerCompatibleCase `json:"cases"`
}

const plannerConformanceCommit = "aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8"

func loadPlannerCorpus(t *testing.T) plannerConformance {
	t.Helper()
	path := filepath.Join("testdata", "planner_conformance.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read planner conformance fixture: %v", err)
	}
	var corpus plannerConformance
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("parse planner conformance fixture: %v", err)
	}
	if corpus.Reference.Commit != plannerConformanceCommit {
		t.Fatalf("planner conformance pinned commit = %q, want %q",
			corpus.Reference.Commit, plannerConformanceCommit)
	}
	return corpus
}

func plannerFixtureToValues(formats []plannerFormatFixture) []value.Value {
	out := make([]value.Value, 0, len(formats))
	for _, f := range formats {
		object := value.NewObject()
		object.Set("format_id", value.String(f.FormatID))
		object.Set("url", value.String(f.URL))
		object.Set("ext", value.String(f.Ext))
		if f.VCodec != nil {
			object.Set("vcodec", value.String(*f.VCodec))
		}
		if f.ACodec != nil {
			object.Set("acodec", value.String(*f.ACodec))
		}
		if f.Height != nil {
			object.Set("height", value.Int(*f.Height))
		}
		if f.Width != nil {
			object.Set("width", value.Int(*f.Width))
		}
		if f.FPS != nil {
			object.Set("fps", value.Float(*f.FPS))
		}
		if f.TBR != nil {
			object.Set("tbr", value.Float(*f.TBR))
		}
		if f.ABR != nil {
			object.Set("abr", value.Float(*f.ABR))
		}
		if f.VBR != nil {
			object.Set("vbr", value.Float(*f.VBR))
		}
		if f.ASR != nil {
			object.Set("asr", value.Float(*f.ASR))
		}
		if f.AudioChannels != nil {
			object.Set("audio_channels", value.Int(*f.AudioChannels))
		}
		if f.Filesize != nil {
			object.Set("filesize", value.Int(*f.Filesize))
		}
		if f.FilesizeApprox != nil {
			object.Set("filesize_approx", value.Int(*f.FilesizeApprox))
		}
		if f.DynamicRange != nil {
			object.Set("dynamic_range", value.String(*f.DynamicRange))
		}
		if f.Language != nil {
			object.Set("language", value.String(*f.Language))
		}
		if f.FormatNote != nil {
			object.Set("format_note", value.String(*f.FormatNote))
		}
		out = append(out, value.ObjectValue(object))
	}
	return out
}

func plannerRunSelector(t *testing.T, formats []plannerFormatFixture, selector string, options plannerSelectorOptions) ([]OutputPlan, error) {
	t.Helper()
	info := value.NewInfo(value.NewObject(value.Field{
		Key:   "formats",
		Value: value.List(plannerFixtureToValues(formats)...),
	}))
	opts := Options{
		AllowMultipleVideoStreams: options.AllowMultipleVideoStreams,
		AllowMultipleAudioStreams: options.AllowMultipleAudioStreams,
		PreferFreeFormats:         options.PreferFreeFormats,
	}
	parsed, err := ParseSelector(selector)
	if err != nil {
		return nil, err
	}
	return PlanSelectWithOptions(info, parsed, opts)
}

func TestPlannerConformanceCorpus(t *testing.T) {
	corpus := loadPlannerCorpus(t)
	for _, raw := range corpus.Cases {
		var probe struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("parse case probe: %v", err)
		}
		if probe.ID == "compatible_extension.cases" {
			var block plannerCompatibleBlock
			if err := json.Unmarshal(raw, &block); err != nil {
				t.Fatalf("parse compatible_extension block: %v", err)
			}
			for _, c := range block.Cases {
				got := compatibleExtension(c.VCodecs, c.ACodecs, c.VExts, c.AExts, c.Preferences)
				if got != c.Expected {
					t.Errorf("%s: compatibleExtension = %q, want %q", c.ID, got, c.Expected)
				}
			}
			continue
		}
		var sc plannerSelectorCase
		if err := json.Unmarshal(raw, &sc); err != nil {
			t.Fatalf("parse selector case %q: %v", probe.ID, err)
		}
		t.Run(sc.ID, func(t *testing.T) {
			plans, err := plannerRunSelector(t, sc.Formats, sc.Selector, sc.Options)
			if err != nil && !errors.Is(err, ErrNoMatch) {
				t.Fatalf("PlanSelect: %v", err)
			}
			if len(plans) != len(sc.Plans) {
				t.Fatalf("plan count = %d, want %d", len(plans), len(sc.Plans))
			}
			for planIndex, expected := range sc.Plans {
				if len(plans[planIndex].Tracks) != len(expected.Tracks) {
					t.Fatalf("plan[%d] track count = %d, want %d",
						planIndex, len(plans[planIndex].Tracks), len(expected.Tracks))
				}
				for trackIndex, want := range expected.Tracks {
					got := plans[planIndex].Tracks[trackIndex]
					if got.ID != want.FormatID {
						t.Fatalf("plan[%d].track[%d] id = %q, want %q",
							planIndex, trackIndex, got.ID, want.FormatID)
					}
				}
				actualMetadata, err := json.Marshal(plans[planIndex].Metadata.Fields())
				if err != nil {
					t.Fatalf("marshal plan[%d] metadata: %v", planIndex, err)
				}
				if !jsonEqual(actualMetadata, expected.Merged) {
					t.Fatalf("plan[%d] metadata = %s, want %s", planIndex, actualMetadata, expected.Merged)
				}
			}
		})
	}
}
