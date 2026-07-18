package pipeline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/dash"
)

func TestDASHDownloadAndFFmpegMergeEndToEnd(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{FFmpegPath: ffmpegPath})
	if err != nil {
		t.Skipf("ffmpeg toolchain unavailable: %v", err)
	}
	root := t.TempDir()
	video := filepath.Join(root, "source-video.mp4")
	audio := filepath.Join(root, "source-audio.m4a")
	generate := func(arguments ...string) {
		command := exec.Command(ffmpegPath, arguments...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("generate media: %v: %s", err, output)
		}
	}
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "color=c=blue:s=16x16:d=0.2", "-an", "-c:v", "mpeg4", video)
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=800:duration=0.2", "-vn", "-c:a", "aac", audio)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.mpd":
			_, _ = fmt.Fprint(writer, `<MPD type="static"><Period>
<AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="video" bandwidth="1000"><BaseURL>video.mp4</BaseURL></Representation></AdaptationSet>
<AdaptationSet contentType="audio" mimeType="audio/mp4"><Representation id="audio" bandwidth="128"><BaseURL>audio.m4a</BaseURL></Representation></AdaptationSet>
</Period></MPD>`)
		case "/video.mp4":
			http.ServeFile(writer, request, video)
		case "/audio.m4a":
			http.ServeFile(writer, request, audio)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	destination := filepath.Join(root, "final.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := dash.NewDownloader(transport, dash.Config{}).Download(ctx, server.URL+"/manifest.mpd", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := FinalizeDASH(ctx, result, destination, false, tools, nil); err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	types := make(map[string]bool)
	for _, stream := range probe.Streams {
		types[stream.CodecType] = true
	}
	if !types["video"] || !types["audio"] {
		t.Fatalf("final streams = %#v", probe.Streams)
	}
	for _, track := range result.Tracks {
		if _, err := os.Stat(track.Download.Path); !os.IsNotExist(err) {
			t.Fatalf("temporary track remains: %s", track.Download.Path)
		}
	}
}

func TestRemuxDownloadFinalizesThenRemovesSource(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{FFmpegPath: ffmpegPath})
	if err != nil {
		t.Skipf("ffmpeg toolchain unavailable: %v", err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "source.mp4")
	command := exec.Command(ffmpegPath,
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=green:s=16x16:d=0.2",
		"-an", "-c:v", "mpeg4", source)
	if output, commandErr := command.CombinedOutput(); commandErr != nil {
		t.Fatalf("generate media: %v: %s", commandErr, output)
	}
	destination := filepath.Join(root, "remuxed.mkv")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := RemuxDownload(ctx, source, destination, false, tools, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source remains after remux: %v", err)
	}
	probe, err := tools.Probe(ctx, destination)
	if err != nil || len(probe.Streams) != 1 || probe.Streams[0].CodecType != "video" {
		t.Fatalf("probe = %#v, error = %v", probe, err)
	}
}

func TestMediaPipelineRejectsMissingToolset(t *testing.T) {
	if err := RemuxDownload(context.Background(), "source", "destination", false, nil, nil); !errors.Is(err, ErrMissingToolset) {
		t.Fatalf("RemuxDownload() error = %v", err)
	}
	if err := FinalizeDASH(context.Background(), dash.Result{MergeRequired: true}, "destination", false, nil, nil); !errors.Is(err, ErrMissingDASHTracks) {
		t.Fatalf("FinalizeDASH() missing tracks error = %v", err)
	}
}
