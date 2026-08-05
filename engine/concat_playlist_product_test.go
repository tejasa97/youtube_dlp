package engine

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/archive"
	"github.com/tejasa97/youtube_dlp/internal/extractor"
	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
	"github.com/tejasa97/youtube_dlp/internal/media/postprocess"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func TestProductConcatPlaylistPublishesDeterministicJoinedMedia(t *testing.T) {
	first, second := generateConcatMedia(t)
	server := serveConcatPlaylist(t, first, second)
	defer server.Close()
	root := t.TempDir()
	operation := newConcatPlaylistOperation(t, newBroadTestClient(), root)
	result, err := operation.process(context.Background(), server.URL+"/list", "", nil, make(map[string]bool), 0)
	if err != nil || !result.Downloaded {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if filepath.Base(result.Filename) != "joined.mp4" || filepath.Dir(result.Filename) != filepath.Join(root, "playlist") {
		t.Fatalf("joined filename=%q", result.Filename)
	}
	if stat, statErr := os.Stat(result.Filename); statErr != nil || stat.Size() == 0 {
		t.Fatalf("joined media stat=%v size=%d", statErr, stat.Size())
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries=%d", len(result.Entries))
	}
	found := false
	for _, artifact := range result.Artifacts {
		if artifact.Kind == "playlist_media" && artifact.Path == result.Filename {
			found = true
		}
	}
	if !found {
		t.Fatalf("playlist artifact missing: %+v", result.Artifacts)
	}
}

func TestProductConcatPlaylistFailurePreservesInputsAndDestination(t *testing.T) {
	first, second := generateConcatMedia(t)
	server := serveConcatPlaylist(t, first, second)
	defer server.Close()
	root := t.TempDir()
	playlistRoot := filepath.Join(root, "playlist")
	if err := os.MkdirAll(playlistRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(playlistRoot, "joined.mp4")
	sentinel := []byte("pre-existing playlist destination")
	if err := os.WriteFile(destination, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	mutated := false
	var mutationErr error
	var lifecycleEvents []Event
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		lifecycleEvents = append(lifecycleEvents, event)
		if event.Kind == EventPostprocessStarting && !mutated {
			mutated = true
			mutationErr = os.WriteFile(filepath.Join(root, "001.mp4"), []byte("invalid media"), 0o600)
		}
		return nil
	}))
	operation := newConcatPlaylistOperation(t, client, root)
	archivePath := filepath.Join(root, "archive.txt")
	archiveStore, err := archive.Open(context.Background(), archivePath, archive.Options{})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := archive.NewIdentity("seed", "existing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archiveStore.Record(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	operation.archive = archiveStore
	_, err = operation.process(context.Background(), server.URL+"/list", "", nil, make(map[string]bool), 0)
	if err == nil || !IsCategory(err, ErrorInternal) {
		t.Fatalf("concat failure=%v, want internal error", err)
	}
	if !mutated || mutationErr != nil {
		t.Fatalf("concat did not reach ffmpeg failure: mutated=%t err=%v events=%+v", mutated, mutationErr, lifecycleEvents)
	}
	restored, readErr := os.ReadFile(destination)
	if readErr != nil || !bytes.Equal(restored, sentinel) {
		t.Fatalf("playlist destination changed: err=%v bytes=%q", readErr, restored)
	}
	for _, path := range []string{filepath.Join(root, "001.mp4"), filepath.Join(root, "002.mp4")} {
		if stat, statErr := os.Stat(path); statErr != nil || stat.Size() == 0 {
			t.Fatalf("child input missing after concat failure %s: %v", path, statErr)
		}
	}
	if matches, globErr := filepath.Glob(filepath.Join(playlistRoot, ".ytdlp-*")); globErr != nil || len(matches) != 0 {
		t.Fatalf("concat temporary artifacts=%v err=%v", matches, globErr)
	}
	if _, sidecarErr := os.Stat(filepath.Join(playlistRoot, "playlist.info.json")); sidecarErr != nil {
		t.Fatalf("committed playlist sidecar was removed: %v", sidecarErr)
	}
	archiveBytes, archiveErr := os.ReadFile(archivePath)
	if archiveErr != nil || !bytes.Contains(archiveBytes, []byte("seed")) || bytes.Contains(archiveBytes, []byte("concat-fixture")) {
		t.Fatalf("archive state=%q err=%v", archiveBytes, archiveErr)
	}
	for _, event := range lifecycleEvents {
		if strings.Contains(event.Message, server.URL) || strings.Contains(event.Message, "invalid media") {
			t.Fatalf("concat event leaked source detail: %+v", event)
		}
	}
}

func TestProductConcatPlaylistDefaultMultiVideoNoOpsOrdinaryPlaylist(t *testing.T) {
	first, second := generateConcatMedia(t)
	server := serveConcatPlaylist(t, first, second)
	defer server.Close()
	root := t.TempDir()
	operation := newConcatPlaylistOperation(t, newBroadTestClient(), root)
	operation.request.ConcatPlaylist = ConcatPlaylistMultiVideo
	result, err := operation.process(context.Background(), server.URL+"/list", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Filename != "" {
		t.Fatalf("ordinary playlist unexpectedly concatenated: %q", result.Filename)
	}
	if _, statErr := os.Stat(filepath.Join(root, "playlist", "joined.mp4")); !os.IsNotExist(statErr) {
		t.Fatalf("default multi_video created output: %v", statErr)
	}
}

func TestProductConcatPlaylistCancellationPreservesSidecarAndDestination(t *testing.T) {
	first, second := generateConcatMedia(t)
	server := serveConcatPlaylist(t, first, second)
	defer server.Close()
	root := t.TempDir()
	playlistRoot := filepath.Join(root, "playlist")
	if err := os.MkdirAll(playlistRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(playlistRoot, "joined.mp4")
	sentinel := []byte("pre-existing playlist destination")
	if err := os.WriteFile(destination, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventPostprocessStarting {
			cancel()
		}
		return nil
	}))
	operation := newConcatPlaylistOperation(t, client, root)
	archivePath := filepath.Join(root, "archive.txt")
	archiveStore, err := archive.Open(context.Background(), archivePath, archive.Options{})
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := archive.NewIdentity("seed", "existing")
	if _, err := archiveStore.Record(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	operation.archive = archiveStore
	_, err = operation.process(ctx, server.URL+"/list", "", nil, make(map[string]bool), 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	restored, readErr := os.ReadFile(destination)
	if readErr != nil || !bytes.Equal(restored, sentinel) {
		t.Fatalf("destination changed: err=%v bytes=%q", readErr, restored)
	}
	if _, sidecarErr := os.Stat(filepath.Join(playlistRoot, "playlist.info.json")); sidecarErr != nil {
		t.Fatalf("playlist sidecar missing after cancellation: %v", sidecarErr)
	}
	if matches, globErr := filepath.Glob(filepath.Join(playlistRoot, ".ytdlp-*")); globErr != nil || len(matches) != 0 {
		t.Fatalf("concat temporary artifacts=%v err=%v", matches, globErr)
	}
}

func TestConcatPlaylistRejectsCaseOnlyDestinationCollision(t *testing.T) {
	first, second := generateConcatMedia(t)
	root := filepath.Dir(first)
	operation := newConcatPlaylistOperation(t, newBroadTestClient(), root)
	operation.request.OutputPaths[OutputPathPLVideo] = ""
	operation.request.OutputTemplates[OutputTemplatePLVideo] = "FIRST.%(ext)s"
	_, err := operation.concatPlaylist(context.Background(), concatPlaylistInfo(), []Result{{Filename: first}, {Filename: second}}, 2)
	if !errors.Is(err, ffmpeg.ErrInvalidOperation) {
		t.Fatalf("case-only collision error=%v", err)
	}
}

func TestConcatPlaylistRejectsSymlinkedOutputRoot(t *testing.T) {
	first, second := generateConcatMedia(t)
	home := t.TempDir()
	outside := t.TempDir()
	sentinelPath := filepath.Join(outside, "untouched.txt")
	if err := os.WriteFile(sentinelPath, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "playlist")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	operation := newConcatPlaylistOperation(t, newBroadTestClient(), home)
	_, err := operation.concatPlaylist(context.Background(), concatPlaylistInfo(), []Result{{Filename: first}, {Filename: second}}, 2)
	if !errors.Is(err, postprocess.ErrUnsafePath) {
		t.Fatalf("symlink output root error=%v", err)
	}
	untouched, readErr := os.ReadFile(sentinelPath)
	if readErr != nil || string(untouched) != "outside" {
		t.Fatalf("outside output changed: err=%v bytes=%q", readErr, untouched)
	}
}

func TestConcatPlaylistPolicyValidationIsClosed(t *testing.T) {
	if err := validateRequestOptions(Request{ConcatPlaylist: "shell"}); err == nil || !strings.Contains(err.Error(), "concat playlist policy") {
		t.Fatalf("invalid concat policy error=%v", err)
	}
	for _, policy := range []string{"", ConcatPlaylistNever, ConcatPlaylistAlways, ConcatPlaylistMultiVideo} {
		if err := validateRequestOptions(Request{ConcatPlaylist: policy}); err != nil {
			t.Fatalf("policy %q rejected: %v", policy, err)
		}
	}
}

func newConcatPlaylistOperation(t *testing.T, client *Client, root string) *operation {
	t.Helper()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return &operation{
		client: client,
		request: Request{
			OutputDir: root, OutputTemplate: "%(autonumber)03d.%(ext)s", Overwrite: true,
			ConcatPlaylist: ConcatPlaylistAlways,
			RelatedFiles:   RelatedFileOptions{WriteInfoJSON: true},
			OutputPaths: OutputPaths{
				OutputPathHome: root, OutputPathPLVideo: "playlist", OutputPathPLInfoJSON: "playlist",
			},
			OutputTemplates: OutputTemplates{
				OutputTemplatePLVideo: "joined.%(ext)s", OutputTemplatePLInfoJSON: "playlist.%(ext)s",
			},
		},
		transport: transport,
		registry:  legacyRuntime(concatPlaylistExtractor{}, extractor.NewGeneric()),
	}
}

func concatPlaylistInfo() value.Info {
	return value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("concat-fixture")},
		value.Field{Key: "title", Value: value.String("Concat Fixture")},
	))
}

func generateConcatMedia(t *testing.T) (string, string) {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	paths := []string{filepath.Join(root, "first.mp4"), filepath.Join(root, "second.mp4")}
	for _, path := range paths {
		if output, err := exec.Command(ffmpegPath, "-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.4", "-c:v", "mpeg4", path).CombinedOutput(); err != nil {
			t.Fatalf("generate concat fixture: %v: %s", err, output)
		}
	}
	return paths[0], paths[1]
}

func serveConcatPlaylist(t *testing.T, first, second string) *httptest.Server {
	t.Helper()
	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/first.mp4":
			writer.Header().Set("Content-Type", "video/mp4")
			_, _ = writer.Write(firstBytes)
		case "/second.mp4":
			writer.Header().Set("Content-Type", "video/mp4")
			_, _ = writer.Write(secondBytes)
		default:
			http.NotFound(writer, request)
		}
	}))
	return server
}

type concatPlaylistExtractor struct{}

func (concatPlaylistExtractor) Name() string { return "concat-playlist-fixture" }

func (concatPlaylistExtractor) Suitable(parsed *url.URL) bool { return parsed.Path == "/list" }

func (concatPlaylistExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, _ := url.Parse(request.URL)
	base := parsed.Scheme + "://" + parsed.Host
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("concat-fixture")},
		value.Field{Key: "title", Value: value.String("Concat Fixture")},
		value.Field{Key: "webpage_url", Value: value.String(request.URL)},
	))
	return extractor.Playlist(info, extractor.StaticEntries(
		extractor.Entry{URL: base + "/first.mp4", ExtractorKey: "generic", ID: "first"},
		extractor.Entry{URL: base + "/second.mp4", ExtractorKey: "generic", ID: "second"},
	))
}
