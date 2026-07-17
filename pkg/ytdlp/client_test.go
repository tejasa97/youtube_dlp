package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
)

func TestIsCategory(t *testing.T) {
	err := &Error{Category: ErrorNetwork, Op: "fetch", Err: errors.New("offline")}
	if !IsCategory(err, ErrorNetwork) {
		t.Fatal("IsCategory() = false, want true")
	}
	if IsCategory(err, ErrorInvalidInput) {
		t.Fatal("IsCategory() matched the wrong category")
	}
	if !errors.Is(err, err.Err) {
		t.Fatal("Error does not unwrap its cause")
	}
}

func TestClientCancellationReachesTransport(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := NewClient().Run(ctx, Request{URL: server.URL + "/slow?delay=1s", SkipDownload: true})
	if !IsCategory(err, ErrorCancelled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestClientWalkingSkeleton(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	var events []Event
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	}))
	result, err := client.Run(context.Background(), Request{URL: server.URL + "/page", OutputDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || result.Extractor != "fixture" {
		t.Fatalf("result = %#v", result)
	}
	if !json.Valid(result.InfoJSON) {
		t.Fatalf("invalid metadata JSON: %s", result.InfoJSON)
	}
	downloaded, err := os.ReadFile(result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(downloaded) != string(server.Media()) {
		t.Fatal("downloaded media mismatch")
	}
	if len(events) < 4 || events[0].Kind != "extracting" || events[len(events)-1].Kind != "download_completed" {
		t.Fatalf("events = %#v", events)
	}
}

func TestClientHLSAndDASHDispatch(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	for _, test := range []struct {
		name     string
		path     string
		expected []byte
	}{
		{"HLS", "/hls/master.m3u8", server.HLSMedia()},
		{"DASH", "/dash/manifest.mpd", server.DASHMedia()},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := NewClient().Run(context.Background(), Request{URL: server.URL + test.path, OutputDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(result.Filename)
			if err != nil {
				t.Fatal(err)
			}
			if string(contents) != string(test.expected) {
				t.Fatalf("contents = %q, want %q", contents, test.expected)
			}
		})
	}
}

func TestClientDASHMergeDispatch(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	video := filepath.Join(root, "source-video.mp4")
	audio := filepath.Join(root, "source-audio.m4a")
	generate := func(arguments ...string) {
		output, err := exec.Command(ffmpegPath, arguments...).CombinedOutput()
		if err != nil {
			t.Fatalf("generate fixture: %v: %s", err, output)
		}
	}
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "color=c=green:s=16x16:d=0.2", "-an", "-c:v", "mpeg4", video)
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=700:duration=0.2", "-vn", "-c:a", "aac", audio)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.mpd":
			writer.Header().Set("Content-Type", "application/dash+xml")
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := NewClient().Run(ctx, Request{URL: server.URL + "/manifest.mpd", OutputDir: root, Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	types := make(map[string]bool)
	for _, stream := range probe.Streams {
		types[stream.CodecType] = true
	}
	if !types["video"] || !types["audio"] {
		t.Fatalf("merged streams = %#v", probe.Streams)
	}
}

func TestClientConcurrentOperationsDoNotShareState(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	client := NewClient()
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := client.Run(context.Background(), Request{
				URL: server.URL + "/page", OutputDir: filepath.Join(t.TempDir(), "operation"),
			})
			errorsSeen <- err
		}(index)
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
}
