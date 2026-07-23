package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestWriteRelatedFilesAllKindsAndExistingPolicy(t *testing.T) {
	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture")},
		value.Field{Key: "title", Value: value.String("A title\nwith spaces")},
		value.Field{Key: "description", Value: value.String("fixture description")},
		value.Field{Key: "webpage_url", Value: value.String("https://example.invalid/watch?v=one&x=two")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	operation := operation{
		client: NewClient(),
		request: Request{
			OutputDir: root, OutputTemplate: "%(id)s.%(ext)s",
			RelatedFiles: RelatedFileOptions{
				WriteInfoJSON: true, WriteDescription: true,
				WriteURLLink: true, WriteWeblocLink: true, WriteDesktopLink: true,
			},
		},
	}
	artifacts, total, err := operation.writeRelatedFiles(context.Background(), info, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 5 || total <= 0 {
		t.Fatalf("artifacts=%#v total=%d", artifacts, total)
	}
	for _, artifact := range artifacts {
		if !strings.HasPrefix(artifact.Path, root+string(filepath.Separator)) {
			t.Fatalf("artifact escaped root: %#v", artifact)
		}
	}
	infoJSON, err := os.ReadFile(filepath.Join(root, "fixture.info.json"))
	if err != nil || !json.Valid(infoJSON) || !strings.HasSuffix(string(infoJSON), "\n") {
		t.Fatalf("info JSON=%q error=%v", infoJSON, err)
	}
	description, err := os.ReadFile(filepath.Join(root, "fixture.description"))
	if err != nil || string(description) != "fixture description" {
		t.Fatalf("description=%q error=%v", description, err)
	}
	urlLink, _ := os.ReadFile(filepath.Join(root, "fixture.url"))
	if string(urlLink) != "[InternetShortcut]\r\nURL=https://example.invalid/watch?v=one&x=two\r\n" {
		t.Fatalf("URL link=%q", urlLink)
	}
	webloc, _ := os.ReadFile(filepath.Join(root, "fixture.webloc"))
	if !strings.Contains(string(webloc), "v=one&amp;x=two") {
		t.Fatalf("webloc did not XML-escape URL: %q", webloc)
	}
	desktop, _ := os.ReadFile(filepath.Join(root, "fixture.desktop"))
	wantDesktopName := "Name=" + desktopEscape(filepath.Join(root, "fixture"))
	if !strings.Contains(string(desktop), wantDesktopName+"\n") {
		t.Fatalf("desktop name was not escaped: %q", desktop)
	}

	if err := os.WriteFile(filepath.Join(root, "fixture.description"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := operation.writeRelatedFiles(context.Background(), info, false); err != nil {
		t.Fatal(err)
	}
	kept, _ := os.ReadFile(filepath.Join(root, "fixture.description"))
	if string(kept) != "keep" {
		t.Fatalf("existing sidecar overwritten without permission: %q", kept)
	}
	operation.request.Overwrite = true
	if _, _, err := operation.writeRelatedFiles(context.Background(), info, false); err != nil {
		t.Fatal(err)
	}
	replaced, _ := os.ReadFile(filepath.Join(root, "fixture.description"))
	if string(replaced) != "fixture description" {
		t.Fatalf("sidecar not overwritten: %q", replaced)
	}
}

func TestWriteRelatedFilesPlaylistScopeUnsafeLinksAndCancellation(t *testing.T) {
	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("playlist")},
		value.Field{Key: "title", Value: value.String("Playlist")},
		value.Field{Key: "description", Value: value.String("playlist description")},
		value.Field{Key: "webpage_url", Value: value.String("file:///private")},
	))
	var warnings int
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventMetadataWarning {
			warnings++
		}
		return nil
	}))
	operation := operation{client: client, request: Request{
		OutputDir: root, RelatedFiles: RelatedFileOptions{
			WriteInfoJSON: true, WriteDescription: true, WriteURLLink: true,
		},
	}}
	artifacts, _, err := operation.writeRelatedFiles(context.Background(), info, true)
	if err != nil || len(artifacts) != 2 || warnings != 0 {
		t.Fatalf("playlist artifacts=%#v warnings=%d error=%v", artifacts, warnings, err)
	}
	artifacts, _, err = operation.writeRelatedFiles(context.Background(), info, false)
	if err != nil || len(artifacts) != 2 || warnings != 1 {
		t.Fatalf("video artifacts=%#v warnings=%d error=%v", artifacts, warnings, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := operation.writeRelatedFiles(ctx, info, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
}

func TestWriteAtomicRelatedFileRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "sidecar")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := writeAtomicRelatedFile(context.Background(), link, []byte("replace"), true); err == nil {
		t.Fatal("symlink destination accepted")
	}
	body, _ := os.ReadFile(target)
	if string(body) != "secret" {
		t.Fatalf("symlink target changed: %q", body)
	}
}

func TestPrepareRelatedDestinationRejectsNestedSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "nested")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	destination := filepath.Join(root, "nested", "escape.info.json")
	if err := prepareRelatedDestination(root, destination); err == nil {
		t.Fatal("nested symlink accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.info.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside destination changed: %v", err)
	}
}

func TestOperationWritesFinalPlaylistAndEntryMetadataSidecars(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	operation := &operation{
		client: NewClient(),
		request: Request{
			OutputDir: root, SkipDownload: true,
			RelatedFiles: RelatedFileOptions{WriteInfoJSON: true},
		},
		transport: transport,
		registry:  extractor.NewRegistry(playlistFixtureExtractor{}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), server.URL+"/list", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Kind != "infojson" {
		t.Fatalf("playlist artifacts=%#v", result.Artifacts)
	}
	rootJSON, err := os.ReadFile(filepath.Join(root, "Root Playlist.info.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(rootJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	entries, ok := metadata["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("final playlist info JSON entries=%#v", metadata["entries"])
	}
	if _, err := os.Stat(filepath.Join(root, "Nested Playlist.info.json")); err != nil {
		t.Fatalf("nested playlist sidecar: %v", err)
	}

	operation.request.RelatedFiles.NoPlaylist = true
	operation.request.OutputDir = t.TempDir()
	result, err = operation.process(context.Background(), server.URL+"/list", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 0 {
		t.Fatalf("NoPlaylist root artifacts=%#v", result.Artifacts)
	}
}

func FuzzSafeLinkURL(f *testing.F) {
	for _, seed := range []string{
		"https://example.invalid/watch?v=one",
		"http://例.example/path",
		"file:///tmp/secret",
		"https://user:pass@example.invalid/",
		"https://example.invalid/\r\nInjected: true",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		output, err := safeLinkURL(input)
		if err != nil {
			return
		}
		if strings.ContainsAny(output, "\x00\r\n") || (!strings.HasPrefix(output, "http://") && !strings.HasPrefix(output, "https://")) {
			t.Fatalf("unsafe accepted output %q from %q", output, input)
		}
	})
}

func TestSafeLinkURLConvertsIRIHostAndEscapesPath(t *testing.T) {
	got, err := safeLinkURL("https://例.example/über uns")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://xn--fsq.example/%C3%BCber%20uns" {
		t.Fatalf("safeLinkURL()=%q", got)
	}
}
