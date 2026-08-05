package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/downloader"
	"github.com/tejasa97/youtube_dlp/internal/events"
	"github.com/tejasa97/youtube_dlp/internal/extractor"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
	"github.com/tejasa97/youtube_dlp/internal/network"
)

func requireFFmpegToolset(t *testing.T) *ffmpeg.Toolset {
	t.Helper()
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if errors.Is(err, ffmpeg.ErrFFmpegUnavailable) || errors.Is(err, ffmpeg.ErrFFprobeUnavailable) {
		t.Skipf("ffmpeg toolchain unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func generateShortVideo(t *testing.T, destination string) {
	t.Helper()
	output, err := exec.Command("ffmpeg", "-nostdin", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.2",
		"-an", "-c:v", "mpeg4", "-q:v", "5", destination,
	).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg unavailable for fixture generation: %v: %s", err, output)
	}
}

func generateShortAudio(t *testing.T, destination string) {
	t.Helper()
	output, err := exec.Command("ffmpeg", "-nostdin", "-y",
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=0.2",
		"-vn", "-c:a", "aac", destination,
	).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg unavailable for fixture generation: %v: %s", err, output)
	}
}

func serveMediaFixtures(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path, ok := files[request.URL.Path]
		if !ok {
			http.NotFound(writer, request)
			return
		}
		http.ServeFile(writer, request, path)
	}))
}

func testOperation(t *testing.T, request Request) *operation {
	t.Helper()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return &operation{client: NewClient(), request: request, transport: transport}
}

func TestNTrackMergeRealMediaTwoTrack(t *testing.T) {
	requireFFmpegToolset(t)
	root := t.TempDir()
	video := filepath.Join(root, "video.mp4")
	audio := filepath.Join(root, "audio.m4a")
	generateShortVideo(t, video)
	generateShortAudio(t, audio)
	server := serveMediaFixtures(t, map[string]string{"/v": video, "/a": audio})
	defer server.Close()

	operation := testOperation(t, Request{Overwrite: true})
	destination := filepath.Join(root, "merged.mp4")
	path, bytes, err := operation.downloadAndMergeTracks(context.Background(), []mediaformat.Selection{
		{Ext: "mp4", VCodec: "mpeg4", ACodec: "none", Protocol: "http", URL: server.URL + "/v"},
		{Ext: "m4a", VCodec: "none", ACodec: "aac", Protocol: "http", URL: server.URL + "/a"},
	}, root, destination, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if path != destination || bytes <= 0 {
		t.Fatalf("result = %q, %d", path, bytes)
	}
	tools := requireFFmpegToolset(t)
	probe, err := tools.Probe(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(probe.Streams) != 2 || probe.Streams[0].CodecType != "video" || probe.Streams[1].CodecType != "audio" {
		t.Fatalf("streams = %#v", probe.Streams)
	}
}

func TestNTrackMergePreservesPlannerOrder(t *testing.T) {
	tools := requireFFmpegToolset(t)
	root := t.TempDir()
	fixtures := t.TempDir()
	paths := map[string]string{
		"/v1": filepath.Join(fixtures, "v1.mp4"),
		"/v2": filepath.Join(fixtures, "v2.mp4"),
		"/a1": filepath.Join(fixtures, "a1.m4a"),
		"/a2": filepath.Join(fixtures, "a2.m4a"),
		"/a3": filepath.Join(fixtures, "a3.m4a"),
	}
	for _, path := range []string{paths["/v1"], paths["/v2"]} {
		generateShortVideo(t, path)
	}
	for _, path := range []string{paths["/a1"], paths["/a2"], paths["/a3"]} {
		generateShortAudio(t, path)
	}
	server := serveMediaFixtures(t, paths)
	defer server.Close()

	operation := testOperation(t, Request{Overwrite: true})
	destination := filepath.Join(root, "merged.mkv")
	_, _, err := operation.downloadAndMergeTracks(context.Background(), []mediaformat.Selection{
		{Ext: "mp4", VCodec: "mpeg4", ACodec: "none", Protocol: "http", URL: server.URL + "/v1"},
		{Ext: "mp4", VCodec: "mpeg4", ACodec: "none", Protocol: "http", URL: server.URL + "/v2"},
		{Ext: "m4a", VCodec: "none", ACodec: "aac", Protocol: "http", URL: server.URL + "/a1"},
		{Ext: "m4a", VCodec: "none", ACodec: "aac", Protocol: "http", URL: server.URL + "/a2"},
		{Ext: "m4a", VCodec: "none", ACodec: "aac", Protocol: "http", URL: server.URL + "/a3"},
	}, root, destination, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(probe.Streams) != 5 {
		t.Fatalf("streams = %d, want 5", len(probe.Streams))
	}
	wantTypes := []string{"video", "video", "audio", "audio", "audio"}
	for index, stream := range probe.Streams {
		if stream.CodecType != wantTypes[index] {
			t.Fatalf("stream[%d] = %q, want %q", index, stream.CodecType, wantTypes[index])
		}
	}
}

func TestNTrackHeaderIsolationConcurrent(t *testing.T) {
	requireFFmpegToolset(t)
	const tracks = 3
	var hits [tracks]atomic.Int32
	servers := make([]*httptest.Server, tracks)
	for index := range servers {
		trackIndex := index
		expected := fmt.Sprintf("track-%d-secret", trackIndex)
		servers[trackIndex] = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if got := request.Header.Get("X-Track-Auth"); got != expected {
				http.Error(writer, "forbidden", http.StatusForbidden)
				return
			}
			hits[trackIndex].Add(1)
			_, _ = writer.Write([]byte("not-valid-media"))
		}))
	}
	defer func() {
		for _, server := range servers {
			server.Close()
		}
	}()

	selections := make([]mediaformat.Selection, tracks)
	for index := range selections {
		selections[index] = mediaformat.Selection{
			ID:       fmt.Sprintf("t%d", index),
			URL:      servers[index].URL,
			Ext:      "bin",
			VCodec:   "mpeg4",
			ACodec:   "none",
			Protocol: "http",
			Headers:  http.Header{"X-Track-Auth": {fmt.Sprintf("track-%d-secret", index)}},
		}
	}
	operation := testOperation(t, Request{Overwrite: true})
	root := t.TempDir()
	_, _, _ = operation.downloadAndMergeTracks(context.Background(), selections, root, filepath.Join(root, "out.mkv"), events.Nop())
	for index := range hits {
		if hits[index].Load() != 1 {
			t.Fatalf("track %d hits = %d, want 1", index, hits[index].Load())
		}
	}
}

func TestNTrackSiblingCancellationPreservesRootError(t *testing.T) {
	var slowStarted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ok":
			_, _ = writer.Write([]byte("ok"))
		case "/fail":
			http.Error(writer, "fail", http.StatusInternalServerError)
		case "/slow":
			slowStarted.Store(true)
			time.Sleep(2 * time.Second)
			_, _ = writer.Write([]byte("slow"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	operation := testOperation(t, Request{Overwrite: true})
	root := t.TempDir()
	_, _, err := operation.downloadAndMergeTracks(context.Background(), []mediaformat.Selection{
		{ID: "ok", Ext: "mp4", VCodec: "mpeg4", ACodec: "none", Protocol: "http", URL: server.URL + "/ok"},
		{ID: "fail", Ext: "m4a", VCodec: "none", ACodec: "aac", Protocol: "http", URL: server.URL + "/fail"},
		{ID: "slow", Ext: "m4a", VCodec: "none", ACodec: "aac", Protocol: "http", URL: server.URL + "/slow"},
	}, root, filepath.Join(root, "out.mkv"), events.Nop())
	if err == nil {
		t.Fatal("expected download failure")
	}
	var statusErr *downloader.HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusInternalServerError {
		t.Fatalf("error = %v, want HTTP 500 failure", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(root, ".ytdlp-formats-*")); len(matches) != 0 {
		t.Fatalf("workspace remains: %v", matches)
	}
}

func TestNTrackCleanupOnMergeFailure(t *testing.T) {
	requireFFmpegToolset(t)
	root := t.TempDir()
	video := filepath.Join(root, "video.mp4")
	audio := filepath.Join(root, "audio.m4a")
	generateShortVideo(t, video)
	generateShortAudio(t, audio)
	server := serveMediaFixtures(t, map[string]string{"/v": video, "/a": audio})
	defer server.Close()

	destination := filepath.Join(root, "existing.mkv")
	if err := os.WriteFile(destination, []byte("preexisting"), 0o600); err != nil {
		t.Fatal(err)
	}
	operation := testOperation(t, Request{Overwrite: false})
	_, _, err := operation.downloadAndMergeTracks(context.Background(), []mediaformat.Selection{
		{Ext: "mp4", VCodec: "mpeg4", ACodec: "none", Protocol: "http", URL: server.URL + "/v"},
		{Ext: "m4a", VCodec: "none", ACodec: "aac", Protocol: "http", URL: server.URL + "/a"},
	}, root, destination, events.Nop())
	if !errors.Is(err, ffmpeg.ErrDestinationExists) {
		t.Fatalf("error = %v", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "preexisting" {
		t.Fatalf("destination modified: %q, %v", data, err)
	}
}

func TestProductExecutesNTrackMerge(t *testing.T) {
	tools := requireFFmpegToolset(t)
	root := t.TempDir()
	fixtures := t.TempDir()
	v1080 := filepath.Join(fixtures, "v1080.mp4")
	v720 := filepath.Join(fixtures, "v720.mp4")
	a128 := filepath.Join(fixtures, "a128.m4a")
	generateShortVideo(t, v1080)
	generateShortVideo(t, v720)
	generateShortAudio(t, a128)

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{
				"id":"merge","title":"Merge","ext":"mp4",
				"formats":[
					{"format_id":"v1080","url":%q,"ext":"mp4","vcodec":"avc1","acodec":"none","height":1080},
					{"format_id":"v720","url":%q,"ext":"mp4","vcodec":"avc1","acodec":"none","height":720},
					{"format_id":"a128","url":%q,"ext":"m4a","vcodec":"none","acodec":"mp4a","abr":128}
				]
			}`, server.URL+"/v1080", server.URL+"/v720", server.URL+"/a128")
		case "/v1080":
			http.ServeFile(writer, request, v1080)
		case "/v720":
			http.ServeFile(writer, request, v720)
		case "/a128":
			http.ServeFile(writer, request, a128)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root,
		Format: "bestvideo+bestvideo.2+bestaudio", Overwrite: true,
		AllowMultipleVideoStreams: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(result.Filename) != ".mkv" {
		t.Fatalf("filename = %q", result.Filename)
	}
	probe, err := tools.Probe(context.Background(), result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if len(probe.Streams) != 3 {
		t.Fatalf("streams = %#v", probe.Streams)
	}
}

func TestMergeOutputFormatPreferenceProduct(t *testing.T) {
	requireFFmpegToolset(t)
	root := t.TempDir()
	fixtures := t.TempDir()
	video := filepath.Join(fixtures, "video.mp4")
	audio := filepath.Join(fixtures, "audio.m4a")
	generateShortVideo(t, video)
	generateShortAudio(t, audio)

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{
				"id":"pref","title":"Pref","ext":"mp4",
				"formats":[
					{"format_id":"v","url":%q,"ext":"mp4","vcodec":"avc1","acodec":"none"},
					{"format_id":"a","url":%q,"ext":"m4a","vcodec":"none","acodec":"mp4a"}
				]
			}`, server.URL+"/v", server.URL+"/a")
		case "/v":
			http.ServeFile(writer, request, video)
		case "/a":
			http.ServeFile(writer, request, audio)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root,
		Format: "bestvideo+bestaudio", MergeOutputFormat: "mp4/mkv", Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(result.Filename) != ".mp4" {
		t.Fatalf("filename = %q, want .mp4", result.Filename)
	}
}

func TestMergeableTracksRejectsEmptyStream(t *testing.T) {
	if mergeableTracks([]mediaformat.Selection{
		{Ext: "mp4", VCodec: "none", ACodec: "none"},
		{Ext: "m4a", VCodec: "none", ACodec: "aac"},
	}) {
		t.Fatal("expected false for empty stream track")
	}
}

func TestPlanDestinationExtensionRejectsOverLimit(t *testing.T) {
	tracks := make([]mediaformat.Selection, mediaformat.MaxMergeTracks+1)
	for index := range tracks {
		tracks[index] = mediaformat.Selection{Ext: "mp4", VCodec: "mpeg4", ACodec: "none"}
	}
	_, err := planDestinationExtension(mediaformat.OutputPlan{Tracks: tracks}, nil)
	if !errors.Is(err, extractor.ErrUnsupported) {
		t.Fatalf("error = %v", err)
	}
}

func TestMergeAllProductExecution(t *testing.T) {
	requireFFmpegToolset(t)
	root := t.TempDir()
	fixtures := t.TempDir()
	video := filepath.Join(fixtures, "video.mp4")
	audio := filepath.Join(fixtures, "audio.m4a")
	generateShortVideo(t, video)
	generateShortAudio(t, audio)

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{
				"id":"all","title":"All","ext":"mp4",
				"formats":[
					{"format_id":"v","url":%q,"ext":"mp4","vcodec":"avc1","acodec":"none","height":720},
					{"format_id":"a","url":%q,"ext":"m4a","vcodec":"none","acodec":"mp4a","abr":128}
				]
			}`, server.URL+"/v", server.URL+"/a")
		case "/v":
			http.ServeFile(writer, request, video)
		case "/a":
			http.ServeFile(writer, request, audio)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root,
		Format: "mergeall", Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(result.Filename) != ".mp4" {
		t.Fatalf("filename = %q, want .mp4", result.Filename)
	}
	tools := requireFFmpegToolset(t)
	probe, err := tools.Probe(context.Background(), result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if len(probe.Streams) != 2 {
		t.Fatalf("streams = %#v", probe.Streams)
	}
}

func TestTrackTemporaryPathWindowsSafe(t *testing.T) {
	path := trackTemporaryPath(`C:\out`, 0, "mp4")
	if strings.ContainsAny(filepath.Base(path), `<>:"|?*`) {
		t.Fatalf("unsafe basename: %q", path)
	}
	if !strings.Contains(filepath.Base(path), "track-000") {
		t.Fatalf("expected indexed basename: %q", path)
	}
}
