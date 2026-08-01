package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/testserver"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestOutputTemplateTypePrecedenceAndValidation(t *testing.T) {
	t.Parallel()
	request := Request{
		OutputTemplate: "legacy/%(id)s.%(ext)s",
		OutputTemplates: OutputTemplates{
			OutputTemplateDefault:     "default/%(id)s.%(ext)s",
			OutputTemplateDescription: "description/%(id)s.%(ext)s",
		},
	}
	if got := request.outputTemplate(OutputTemplateDescription); got != "description/%(id)s.%(ext)s" {
		t.Fatalf("description template = %q", got)
	}
	if got := request.outputTemplate(OutputTemplateSubtitle); got != "default/%(id)s.%(ext)s" {
		t.Fatalf("fallback template = %q", got)
	}
	if got := (Request{OutputTemplate: "legacy"}).outputTemplate(OutputTemplateLink); got != "legacy" {
		t.Fatalf("legacy fallback = %q", got)
	}
	if got := (Request{}).outputTemplate(OutputTemplateDefault); got != "%(title)s.%(ext)s" {
		t.Fatalf("built-in default = %q", got)
	}
	if got := (Request{UseID: true}).outputTemplate(OutputTemplateDefault); got != "%(id)s.%(ext)s" {
		t.Fatalf("--id default = %q", got)
	}
	for _, invalid := range []Request{
		{OutputTemplates: OutputTemplates{"annotation": "%(id)s.%(ext)s"}},
		{OutputTemplates: OutputTemplates{OutputTemplateSubtitle: ""}},
		{OutputTemplates: OutputTemplates{OutputTemplateInfoJSON: "%(id"}},
		{OutputTemplate: "%(id"},
	} {
		if err := validateRequestOptions(invalid); err == nil {
			t.Fatalf("invalid templates accepted: %#v", invalid)
		}
	}
	deterministic := Request{OutputTemplates: OutputTemplates{
		"zeta": "%(id)s.%(ext)s", "alpha": "%(id)s.%(ext)s",
	}}
	for range 100 {
		if err := validateOutputTemplates(deterministic); err == nil || err.Error() != `unsupported output template type "alpha"` {
			t.Fatalf("nondeterministic validation = %v", err)
		}
	}
	if _, err := NewClient().Run(context.Background(), Request{
		URL:             "not-a-url",
		OutputTemplates: OutputTemplates{"annotation": "%(id)s.%(ext)s"},
	}); !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("unsupported type category = %v", err)
	}
}

func TestOutputArtifactTemplatePlaceholderAndAutonumberCompatibility(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture")},
		value.Field{Key: "title", Value: value.String("Fixture")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	operation := operation{request: Request{
		OutputDir: root, OutputTemplate: "%(autonumber)s-%(missing)s-%(id)s.%(ext)s",
		AutonumberStart: 7, AutonumberSize: 5,
		Filesystem: FilesystemOptions{OutputNaPlaceholder: "unknown"},
	}}
	operation.addAutonumber(&info)
	for _, test := range []struct {
		name     string
		template string
		want     string
	}{
		{name: "bare", template: "%(autonumber)s", want: "00007"},
		{name: "arithmetic", template: "%(autonumber+1)s", want: "00008"},
		{name: "default", template: "%(autonumber|fallback)s", want: "00007"},
		{name: "explicit", template: "%(autonumber+1)03d", want: "008"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, err := operation.resolveOutputPath(root, test.template+".mp4", info)
			if err != nil || filepath.Base(path) != test.want+".mp4" {
				t.Fatalf("autonumber path=%q err=%v", path, err)
			}
		})
	}
	operation.request.OutputTemplate = "%(autonumber)03d.%(id)s.%(ext)s"
	operation.addAutonumber(&info)
	path, err := operation.resolveOutputPath(root, operation.request.OutputTemplate, info)
	if err != nil || filepath.Base(path) != "008.fixture.mp4" {
		t.Fatalf("explicit autonumber path=%q err=%v", path, err)
	}
}

func TestTypedRelatedFileTemplatesDistinguishEntryAndPlaylist(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("item")},
		value.Field{Key: "title", Value: value.String("Item")},
		value.Field{Key: "description", Value: value.String("description")},
		value.Field{Key: "webpage_url", Value: value.String("https://example.invalid/item")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	operation := operation{client: NewClient(), request: Request{
		OutputDir: root,
		OutputTemplates: OutputTemplates{
			OutputTemplateDefault:       "fallback/%(id)s.%(ext)s",
			OutputTemplateDescription:   "entry-description/%(id)s.%(ext)s",
			OutputTemplateInfoJSON:      "entry-json/%(id)s.%(ext)s",
			OutputTemplateLink:          "entry-link/%(id)s.%(ext)s",
			OutputTemplatePLDescription: "playlist-description/%(id)s.%(ext)s",
			OutputTemplatePLInfoJSON:    "playlist-json/%(id)s.%(ext)s",
		},
		RelatedFiles: RelatedFileOptions{
			WriteInfoJSON: true, WriteDescription: true, WriteURLLink: true,
		},
	}}
	artifacts, _, err := operation.writeRelatedFiles(context.Background(), info, false)
	if err != nil || len(artifacts) != 3 {
		t.Fatalf("entry artifacts=%#v error=%v", artifacts, err)
	}
	for _, relative := range []string{
		"entry-description/item.description",
		"entry-json/item.info.json",
		"entry-link/item.url",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("%s: %v", relative, err)
		}
	}
	artifacts, _, err = operation.writeRelatedFiles(context.Background(), info, true)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("playlist artifacts=%#v error=%v", artifacts, err)
	}
	for _, relative := range []string{
		"playlist-description/item.description",
		"playlist-json/item.info.json",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("%s: %v", relative, err)
		}
	}
}

func TestTypedOutputTemplatesPreserveSafetyAndSimulation(t *testing.T) {
	t.Parallel()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("item")},
		value.Field{Key: "title", Value: value.String("Item")},
		value.Field{Key: "description", Value: value.String("description")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	root := t.TempDir()
	operation := operation{client: NewClient(), request: Request{
		OutputDir: root,
		OutputTemplates: OutputTemplates{
			OutputTemplateDescription: "../escape/%(id)s.%(ext)s",
		},
		RelatedFiles: RelatedFileOptions{WriteDescription: true},
	}}
	if _, _, err := operation.writeRelatedFiles(context.Background(), info, false); err == nil {
		t.Fatalf("typed traversal accepted: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err == nil {
		operation.request.OutputTemplates[OutputTemplateDescription] = "linked/%(id)s.%(ext)s"
		if _, _, err := operation.writeRelatedFiles(context.Background(), info, false); err == nil {
			t.Fatal("typed symlink parent accepted")
		}
	}
	operation.request.OutputTemplates[OutputTemplateDescription] = "cancelled/%(id)s.%(ext)s"
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := operation.writeRelatedFiles(cancelled, info, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("typed cancellation = %v", err)
	}

	server := testserver.New()
	defer server.Close()
	simulatedRoot := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: simulatedRoot, Simulate: true,
		OutputTemplates: OutputTemplates{
			OutputTemplateDescription: "typed/%(id)s.%(ext)s",
		},
		RelatedFiles: RelatedFileOptions{WriteDescription: true},
	})
	if err != nil || len(result.Artifacts) != 0 {
		t.Fatalf("simulation result=%#v error=%v", result, err)
	}
	if entries, err := os.ReadDir(simulatedRoot); err != nil || len(entries) != 0 {
		t.Fatalf("simulation wrote files: %v, %v", entries, err)
	}
}

func TestTypedSubtitleAndDefaultMediaTemplates(t *testing.T) {
	t.Parallel()
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root,
		OutputTemplates: OutputTemplates{
			OutputTemplateDefault:  "media/%(id)s.%(ext)s",
			OutputTemplateSubtitle: "captions/%(id)s.%(ext)s",
		},
		Subtitles: SubtitleOptions{WriteManual: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantMedia := filepath.Join(root, "media", "fixture-direct.bin")
	wantSubtitle := filepath.Join(root, "captions", "fixture-direct.en.vtt")
	if result.Filename != wantMedia {
		t.Fatalf("media filename = %q", result.Filename)
	}
	for _, path := range []string{wantMedia, wantSubtitle} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func FuzzOutputTemplateTypeSelection(f *testing.F) {
	f.Add("default", "%(id)s.%(ext)s")
	f.Add("thumbnail", "../escape")
	f.Fuzz(func(t *testing.T, rawType, pattern string) {
		templateType := OutputTemplateType(rawType)
		request := Request{OutputTemplates: OutputTemplates{templateType: pattern}}
		err := validateOutputTemplates(request)
		if err != nil {
			return
		}
		if _, supported := supportedOutputTemplateTypes[templateType]; !supported || pattern == "" {
			t.Fatalf("unsupported template accepted: %q=%q", templateType, pattern)
		}
	})
}
