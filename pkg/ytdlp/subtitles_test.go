package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/downloader"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestSubtitleSidecarsDownloadWithSkipDownload(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, SkipDownload: true,
		Subtitles: SubtitleOptions{WriteManual: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "Deterministic Fixture.en.vtt")
	body, err := os.ReadFile(wantPath)
	if err != nil || !strings.Contains(string(body), "manual english") {
		t.Fatalf("subtitle body = %q, error = %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Deterministic Fixture.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("media was downloaded with SkipDownload: %v", err)
	}
	if !result.Downloaded || result.Filename != "" || result.Bytes != int64(len(body)) || len(result.Artifacts) != 1 || result.Artifacts[0] != (Artifact{Path: wantPath, Kind: "subtitle"}) {
		t.Fatalf("result = %#v", result)
	}
	var info map[string]any
	if err := json.Unmarshal(result.InfoJSON, &info); err != nil {
		t.Fatal(err)
	}
	requested := info["requested_subtitles"].(map[string]any)["en"].(map[string]any)
	if requested["ext"] != "vtt" || requested["_auto"] != false || requested["filepath"] != wantPath {
		t.Fatalf("requested subtitle = %#v", requested)
	}
}

func TestSubtitleSelectionMatchesPinnedReferenceCases(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	for _, test := range []struct {
		name     string
		options  SubtitleOptions
		files    []string
		contains map[string]string
	}{
		{
			name: "format preference", options: SubtitleOptions{WriteManual: true, Format: "foo/srt"},
			files: []string{"Deterministic Fixture.en.srt"},
		},
		{
			name: "all except english", options: SubtitleOptions{WriteManual: true, Languages: []string{"all", "-en"}},
			files: []string{"Deterministic Fixture.es.vtt", "Deterministic Fixture.fr.vtt"},
		},
		{
			name: "regex", options: SubtitleOptions{WriteManual: true, Languages: []string{"e.+"}},
			files: []string{"Deterministic Fixture.en.vtt", "Deterministic Fixture.es.vtt"},
		},
		{
			name: "manual precedes automatic", options: SubtitleOptions{WriteManual: true, WriteAutomatic: true, Languages: []string{"es", "pt"}},
			files:    []string{"Deterministic Fixture.es.vtt", "Deterministic Fixture.pt.vtt"},
			contains: map[string]string{"Deterministic Fixture.es.vtt": "manual spanish", "Deterministic Fixture.pt.vtt": "automatic portuguese"},
		},
		{
			name: "automatic only", options: SubtitleOptions{WriteAutomatic: true, Languages: []string{"es", "pt"}},
			files:    []string{"Deterministic Fixture.es.vtt", "Deterministic Fixture.pt.vtt"},
			contains: map[string]string{"Deterministic Fixture.es.vtt": "automatic spanish", "Deterministic Fixture.pt.vtt": "automatic portuguese"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			result, err := NewClient().Run(context.Background(), Request{
				URL: server.URL + "/page", OutputDir: root, SkipDownload: true, Subtitles: test.options,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Artifacts) != len(test.files) {
				t.Fatalf("artifacts = %#v", result.Artifacts)
			}
			for _, name := range test.files {
				body, err := os.ReadFile(filepath.Join(root, name))
				if err != nil {
					t.Fatal(err)
				}
				if want := test.contains[name]; want != "" && !strings.Contains(string(body), want) {
					t.Fatalf("%s = %q", name, body)
				}
			}
		})
	}
}

func TestSubtitleAndMediaArtifactsAreBothReported(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root,
		Subtitles: SubtitleOptions{WriteManual: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 2 || result.Artifacts[0].Kind != "subtitle" || result.Artifacts[1].Kind != "media" {
		t.Fatalf("artifacts = %#v", result.Artifacts)
	}
	if result.Filename != filepath.Join(root, "Deterministic Fixture.bin") {
		t.Fatalf("filename = %q", result.Filename)
	}
	for _, artifact := range result.Artifacts {
		if _, err := os.Stat(artifact.Path); err != nil {
			t.Fatalf("artifact %q: %v", artifact.Path, err)
		}
	}
}

func TestSelectSubtitleLanguagesOrderedRules(t *testing.T) {
	// Derived from yt-dlp test/test_YoutubeDL.py subtitle-selection cases at
	// aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8.
	available := []subtitleLanguage{{name: "en"}, {name: "es"}, {name: "fr"}, {name: "pt"}}
	for _, test := range []struct {
		rules []string
		want  string
	}{
		{nil, "en"},
		{[]string{"es", "fr", "it"}, "es,fr"},
		{[]string{"all", "-en"}, "es,fr,pt"},
		{[]string{"en", "fr", "-en"}, "fr"},
		{[]string{"-en", "en"}, "en"},
		{[]string{"e.+"}, "en,es"},
	} {
		got, err := selectSubtitleLanguages(available, 3, test.rules)
		if err != nil || strings.Join(got, ",") != test.want {
			t.Errorf("rules %q = %q, %v; want %q", test.rules, got, err, test.want)
		}
	}
}

func TestSubtitleDestinationExistingFileFailsClosed(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	destination := filepath.Join(root, "Deterministic Fixture.en.vtt")
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, SkipDownload: true,
		Subtitles: SubtitleOptions{WriteManual: true},
	})
	if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, downloader.ErrDestinationExists) {
		t.Fatalf("error = %v", err)
	}
	if body, readErr := os.ReadFile(destination); readErr != nil || string(body) != "keep" {
		t.Fatalf("existing destination = %q, %v", body, readErr)
	}
}

func TestSubtitleDownloadCancellation(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{"id":"slow-sub","title":"Slow subtitle","formats":[{"format_id":"media","url":%q,"ext":"bin"}],"subtitles":{"en":[{"url":%q,"ext":"vtt"}]}}`, server.URL+"/media", server.URL+"/slow")
		case "/slow":
			<-request.Context().Done()
		case "/media":
			_, _ = writer.Write([]byte("media"))
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	root := t.TempDir()
	_, err := NewClient().Run(ctx, Request{
		URL: server.URL + "/page", OutputDir: root, SkipDownload: true,
		Subtitles: SubtitleOptions{WriteManual: true},
	})
	if !IsCategory(err, ErrorCancelled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "Slow subtitle.en.vtt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled subtitle was published: %v", statErr)
	}
}

func TestSubtitleOptionsRejectInvalidRegexBeforeNetwork(t *testing.T) {
	_, err := NewClient().Run(context.Background(), Request{
		URL: "https://example.invalid/page", SkipDownload: true,
		Subtitles: SubtitleOptions{WriteManual: true, Languages: []string{"["}},
	})
	if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, errInvalidRequestOptions) {
		t.Fatalf("error = %v", err)
	}
}

func TestSubtitleMetadataCombinedLanguageLimit(t *testing.T) {
	collection := func(prefix string) *value.Object {
		object := value.NewObject()
		for index := 0; index < 200; index++ {
			object.Set(fmt.Sprintf("%s%d", prefix, index), value.List(value.ObjectValue(value.NewObject(
				value.Field{Key: "url", Value: value.String("https://captions.example/sub.vtt")},
				value.Field{Key: "ext", Value: value.String("vtt")},
			))))
		}
		return object
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "subtitles", Value: value.ObjectValue(collection("m"))},
		value.Field{Key: "automatic_captions", Value: value.ObjectValue(collection("a"))},
	))
	_, _, err := selectSubtitles(info, SubtitleOptions{WriteManual: true, WriteAutomatic: true})
	if !errors.Is(err, extractor.ErrInvalidMetadata) {
		t.Fatalf("error = %v", err)
	}
}

func FuzzValidateSubtitleOptions(f *testing.F) {
	f.Add("en", "srt/vtt/best")
	f.Add("all,-live_chat", "best")
	f.Add("[", "bad format")
	f.Fuzz(func(t *testing.T, language, format string) {
		_ = validateSubtitleOptions(SubtitleOptions{
			WriteManual: true, Languages: []string{language}, Format: format,
		})
	})
}
