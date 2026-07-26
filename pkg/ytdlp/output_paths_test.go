package ytdlp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestOutputPathResolutionAndValidation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	request := Request{
		OutputDir: filepath.Join(root, "legacy"),
		OutputPaths: OutputPaths{
			OutputPathHome:        filepath.Join(root, "home"),
			OutputPathSubtitle:    "captions",
			OutputPathThumbnail:   filepath.Join("images", "entry"),
			OutputPathDescription: "metadata",
		},
	}
	if got, want := request.outputRoot(OutputPathHome), filepath.Join(root, "home"); got != want {
		t.Fatalf("home root = %q, want %q", got, want)
	}
	if got, want := request.outputRoot(OutputPathSubtitle), filepath.Join(root, "home", "captions"); got != want {
		t.Fatalf("subtitle root = %q, want %q", got, want)
	}
	if got, want := request.outputRoot(OutputPathThumbnail), filepath.Join(root, "home", "images", "entry"); got != want {
		t.Fatalf("thumbnail root = %q, want %q", got, want)
	}
	if got, want := request.outputRoot(OutputPathInfoJSON), filepath.Join(root, "home"); got != want {
		t.Fatalf("fallback root = %q, want %q", got, want)
	}
	request.OutputPaths[OutputPathSubtitle] = "."
	if got, want := request.outputRoot(OutputPathSubtitle), filepath.Join(root, "home"); got != want {
		t.Fatalf("dot reset root = %q, want %q", got, want)
	}
	request.OutputPaths[OutputPathSubtitle] = ""
	if got, want := request.outputRoot(OutputPathSubtitle), filepath.Join(root, "home"); got != want {
		t.Fatalf("empty reset root = %q, want %q", got, want)
	}

	for _, invalid := range []OutputPaths{
		{OutputPathSubtitle: ".."},
		{OutputPathSubtitle: filepath.Join("..", "escape")},
		{OutputPathSubtitle: filepath.Join(root, "absolute")},
		{OutputPathSubtitle: "unsafe\x00path"},
		{"unknown": "child"},
	} {
		if err := validateOutputPaths(Request{OutputPaths: invalid}); err == nil {
			t.Fatalf("invalid output paths accepted: %#v", invalid)
		}
	}
	deterministic := Request{OutputPaths: OutputPaths{"zeta": "z", "alpha": "a"}}
	for range 100 {
		if err := validateOutputPaths(deterministic); err == nil || err.Error() != `unsupported output path type "alpha"` {
			t.Fatalf("nondeterministic validation = %v", err)
		}
	}
}

func TestClientRoutesMediaSubtitlesAndRelatedFilesByType(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page",
		OutputPaths: OutputPaths{
			OutputPathHome:     root,
			OutputPathSubtitle: "captions",
			OutputPathInfoJSON: "metadata",
			OutputPathLink:     "links",
		},
		Subtitles: SubtitleOptions{WriteManual: true},
		RelatedFiles: RelatedFileOptions{
			WriteInfoJSON: true,
			WriteURLLink:  true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Filename, root+string(filepath.Separator)) {
		t.Fatalf("media escaped home: %q", result.Filename)
	}
	for _, pattern := range []string{
		filepath.Join(root, "captions", "*.vtt"),
		filepath.Join(root, "metadata", "*.info.json"),
		filepath.Join(root, "links", "*.url"),
	} {
		matches, globErr := filepath.Glob(pattern)
		if globErr != nil || len(matches) == 0 {
			t.Fatalf("pattern %q matches=%v error=%v", pattern, matches, globErr)
		}
	}
}

func TestPlaylistAndThumbnailOutputPaths(t *testing.T) {
	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture")},
		value.Field{Key: "title", Value: value.String("Fixture")},
		value.Field{Key: "description", Value: value.String("description")},
		value.Field{Key: "webpage_url", Value: value.String("https://example.invalid/item")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	operation := operation{client: NewClient(), request: Request{
		OutputDir: root,
		OutputPaths: OutputPaths{
			OutputPathDescription:   "entry/descriptions",
			OutputPathInfoJSON:      "entry/metadata",
			OutputPathPLDescription: "playlist/descriptions",
			OutputPathPLInfoJSON:    "playlist/metadata",
		},
		RelatedFiles: RelatedFileOptions{WriteDescription: true, WriteInfoJSON: true},
	}}
	if _, _, err := operation.writeRelatedFiles(context.Background(), info, false); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{
		"entry/descriptions/Fixture.description",
		"entry/metadata/Fixture.info.json",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("%s: %v", relative, err)
		}
	}
	if _, _, err := operation.writeRelatedFiles(context.Background(), info, true); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{
		"playlist/descriptions/Fixture.description",
		"playlist/metadata/Fixture.info.json",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("%s: %v", relative, err)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/best.jpg", "/middle.png", "/small.webp":
			_, _ = writer.Write([]byte("image"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	thumbnail := thumbnailInfo(server.URL)
	operation.transport = transport
	operation.request.Thumbnails = ThumbnailOptions{Write: true}
	operation.request.OutputPaths[OutputPathThumbnail] = "images/entry"
	if _, _, err := operation.writeThumbnails(context.Background(), &thumbnail, false); err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(root, "images", "entry", "*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("thumbnail matches=%v error=%v", matches, err)
	}
	thumbnail = thumbnailInfo(server.URL)
	operation.request.OutputPaths[OutputPathPLThumbnail] = "images/playlist"
	if _, _, err := operation.writeThumbnails(context.Background(), &thumbnail, true); err != nil {
		t.Fatal(err)
	}
	matches, err = filepath.Glob(filepath.Join(root, "images", "playlist", "*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("playlist thumbnail matches=%v error=%v", matches, err)
	}
}

func TestClientRejectsUnsafeOutputPathsBeforeExtractionAndClonesMap(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	paths := OutputPaths{OutputPathSubtitle: filepath.Join("..", "escape")}
	_, err := NewClient().Run(context.Background(), Request{URL: server.URL + "/page", OutputPaths: paths})
	if !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("unsafe path category = %v", err)
	}

	root := t.TempDir()
	paths = OutputPaths{OutputPathHome: root, OutputPathSubtitle: "captions"}
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventExtracting {
			paths[OutputPathSubtitle] = filepath.Join("..", "mutated")
		}
		return nil
	}))
	_, err = client.Run(context.Background(), Request{
		URL: server.URL + "/page", OutputPaths: paths, SkipDownload: true,
		Subtitles: SubtitleOptions{WriteManual: true},
	})
	if err != nil {
		t.Fatalf("cloned output paths changed during operation: %v", err)
	}
	matches, globErr := filepath.Glob(filepath.Join(root, "captions", "*.vtt"))
	if globErr != nil || len(matches) == 0 {
		t.Fatalf("cloned path matches=%v error=%v", matches, globErr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewClient().Run(ctx, Request{URL: server.URL + "/page", OutputPaths: OutputPaths{OutputPathHome: t.TempDir()}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run = %v", err)
	}
}

func TestTypedOutputRootsRejectSymlinkEscapesFromHome(t *testing.T) {
	server := testserver.New()
	defer server.Close()

	t.Run("subtitle downloader", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(home, "captions")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		_, err := NewClient().Run(context.Background(), Request{
			URL: server.URL + "/page", OutputDir: home, SkipDownload: true,
			OutputPaths: OutputPaths{OutputPathSubtitle: "captions"},
			Subtitles:   SubtitleOptions{WriteManual: true},
		})
		if err == nil {
			t.Fatal("symlinked subtitle root accepted")
		}
		if entries, readErr := os.ReadDir(outside); readErr != nil || len(entries) != 0 {
			t.Fatalf("outside subtitle directory changed: entries=%v error=%v", entries, readErr)
		}
	})

	t.Run("nested related-file parent", func(t *testing.T) {
		home := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(home, "metadata")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		info := value.NewInfo(value.NewObject(
			value.Field{Key: "id", Value: value.String("fixture")},
			value.Field{Key: "title", Value: value.String("Fixture")},
			value.Field{Key: "description", Value: value.String("description")},
			value.Field{Key: "ext", Value: value.String("mp4")},
		))
		operation := operation{client: NewClient(), request: Request{
			OutputDir: home,
			OutputPaths: OutputPaths{
				OutputPathDescription: filepath.Join("metadata", "descriptions"),
			},
			RelatedFiles: RelatedFileOptions{WriteDescription: true},
		}}
		if _, _, err := operation.writeRelatedFiles(context.Background(), info, false); err == nil {
			t.Fatal("nested symlinked related-file root accepted")
		}
		if entries, readErr := os.ReadDir(outside); readErr != nil || len(entries) != 0 {
			t.Fatalf("outside related-file directory changed: entries=%v error=%v", entries, readErr)
		}
	})
}

func FuzzValidateOutputPaths(f *testing.F) {
	f.Add("subtitle", "captions")
	f.Add("thumbnail", "../escape")
	f.Add("unknown", "child")
	f.Fuzz(func(t *testing.T, rawType, path string) {
		request := Request{OutputPaths: OutputPaths{OutputPathType(rawType): path}}
		if validateOutputPaths(request) != nil {
			return
		}
		root := request.outputRoot(OutputPathType(rawType))
		if root == "" {
			t.Fatal("accepted output path resolved to an empty root")
		}
	})
}
