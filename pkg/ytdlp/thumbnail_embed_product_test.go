package ytdlp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
)

func TestProductEmbedsMislabeledWebPThumbnailAndAppliesRetention(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	mediaPath := filepath.Join(fixtureRoot, "media.mp4")
	if output, err := exec.Command(ffmpegPath,
		"-nostdin", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=32x32:d=0.3",
		"-f", "lavfi", "-i", "sine=frequency=700:duration=0.3",
		"-shortest", "-c:v", "mpeg4", "-c:a", "aac", mediaPath,
	).CombinedOutput(); err != nil {
		t.Fatalf("generate media: %v: %s", err, output)
	}
	image, err := base64.StdEncoding.DecodeString(
		"UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEALmk0mk0iIiIiIgBoSygABc6zbAAA",
	)
	if err != nil {
		t.Fatal(err)
	}
	media, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{
				"id":"thumbnail-embed","title":"Thumbnail Embed","ext":"mp4",
				"formats":[{"format_id":"media","url":%q,"ext":"mp4","vcodec":"mpeg4","acodec":"aac"}],
				"thumbnail":%q
			}`, server.URL+"/media.mp4", server.URL+"/cover.jpg")
		case "/media.mp4":
			writer.Header().Set("Content-Length", fmt.Sprint(len(media)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write(media)
			}
		case "/cover.jpg":
			_, _ = writer.Write(image)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	for _, keep := range []bool{false, true} {
		t.Run(fmt.Sprintf("keep=%t", keep), func(t *testing.T) {
			root := t.TempDir()
			result, err := NewClient().Run(context.Background(), Request{
				URL: server.URL + "/page", OutputDir: root,
				Thumbnails: ThumbnailOptions{
					Embed: true, KeepFiles: keep,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			tools, err := ffmpeg.Discover(ffmpeg.Config{})
			if err != nil {
				t.Fatal(err)
			}
			probe, err := tools.Probe(context.Background(), result.Filename)
			if err != nil {
				t.Fatal(err)
			}
			attached := 0
			for _, stream := range probe.Streams {
				if stream.Disposition["attached_pic"] == 1 {
					attached++
				}
			}
			if attached != 1 {
				t.Fatalf("attached thumbnails=%d streams=%#v", attached, probe.Streams)
			}
			wantArtifacts := 1
			if keep {
				wantArtifacts = 2
			}
			if len(result.Artifacts) != wantArtifacts ||
				result.Artifacts[len(result.Artifacts)-1].Kind != "media" {
				t.Fatalf("artifacts=%#v", result.Artifacts)
			}
			if keep && filepath.Ext(result.Artifacts[0].Path) != ".webp" {
				t.Fatalf("corrected thumbnail artifact=%#v", result.Artifacts[0])
			}
			wantBytes, err := artifactBytes(result.Artifacts)
			if err != nil || result.Bytes != wantBytes {
				t.Fatalf("bytes=%d want=%d error=%v", result.Bytes, wantBytes, err)
			}
			var metadata struct {
				Thumbnails []struct {
					Embedded bool `json:"embedded"`
				} `json:"thumbnails"`
			}
			if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil ||
				len(metadata.Thumbnails) != 1 || !metadata.Thumbnails[0].Embedded {
				t.Fatalf("metadata=%#v error=%v", metadata, err)
			}
		})
	}
}

func TestProductEmbedsThumbnailIntoXiphContainers(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	imagePath := filepath.Join(fixtureRoot, "cover.png")
	if output, err := exec.Command(ffmpegPath,
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=green:s=32x32:d=0.1",
		"-frames:v", "1", imagePath,
	).CombinedOutput(); err != nil {
		t.Fatalf("generate image: %v: %s", err, output)
	}
	image := mustReadEmbedProductFile(t, imagePath)
	type mediaFixture struct {
		extension string
		codec     string
		body      []byte
	}
	fixtures := make([]mediaFixture, 0, 3)
	for _, item := range []struct {
		extension string
		codec     string
	}{
		{"flac", "flac"},
		{"ogg", "libopus"},
		{"opus", "libopus"},
	} {
		path := filepath.Join(fixtureRoot, "media."+item.extension)
		if output, err := exec.Command(ffmpegPath,
			"-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=600:duration=0.3",
			"-c:a", item.codec, "-metadata", "title=preserved", path,
		).CombinedOutput(); err != nil {
			t.Fatalf("generate %s: %v: %s", item.extension, err, output)
		}
		fixtures = append(fixtures, mediaFixture{
			extension: item.extension, codec: item.codec,
			body: mustReadEmbedProductFile(t, path),
		})
	}
	for _, fixture := range fixtures {
		t.Run(fixture.extension, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/page":
					writer.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprintf(writer, `{
						"id":"xiph-%s","title":"Xiph %s","ext":%q,
						"formats":[{"format_id":"audio","url":%q,"ext":%q,"vcodec":"none","acodec":"%s"}],
						"thumbnail":%q
					}`, fixture.extension, fixture.extension, fixture.extension,
						server.URL+"/media."+fixture.extension, fixture.extension, fixture.codec,
						server.URL+"/cover.png")
				case "/media." + fixture.extension:
					writer.Header().Set("Content-Length", fmt.Sprint(len(fixture.body)))
					if request.Method != http.MethodHead {
						_, _ = writer.Write(fixture.body)
					}
				case "/cover.png":
					_, _ = writer.Write(image)
				default:
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()
			result, err := NewClient().Run(context.Background(), Request{
				URL: server.URL + "/page", OutputDir: t.TempDir(),
				OutputTemplate: "%(title)s.%(ext)s",
				Thumbnails:     ThumbnailOptions{Embed: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Ext(result.Filename) != "."+fixture.extension {
				t.Fatalf("filename=%q", result.Filename)
			}
			assertOneEmbeddedThumbnail(t, result.Filename)
		})
	}
}

func TestProductPromotesMergedWebMToMatroskaForThumbnail(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	videoPath := filepath.Join(fixtureRoot, "video.webm")
	audioPath := filepath.Join(fixtureRoot, "audio.webm")
	imagePath := filepath.Join(fixtureRoot, "cover.png")
	commands := [][]string{
		{"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=32x32:d=0.3", "-an", "-c:v", "libvpx-vp9", videoPath},
		{"-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=500:duration=0.3", "-vn", "-c:a", "libopus", audioPath},
		{"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=yellow:s=32x32:d=0.1", "-frames:v", "1", imagePath},
	}
	for _, args := range commands {
		if output, err := exec.Command(ffmpegPath, args...).CombinedOutput(); err != nil {
			t.Fatalf("generate fixture: %v: %s", err, output)
		}
	}
	video := mustReadEmbedProductFile(t, videoPath)
	audio := mustReadEmbedProductFile(t, audioPath)
	image := mustReadEmbedProductFile(t, imagePath)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{
				"id":"webm-pair","title":"WebM Pair","ext":"webm",
				"formats":[
					{"format_id":"video","url":%q,"ext":"webm","vcodec":"vp9","acodec":"none","height":720},
					{"format_id":"audio","url":%q,"ext":"webm","vcodec":"none","acodec":"opus","abr":128}
				],
				"thumbnail":%q
			}`, server.URL+"/video.webm", server.URL+"/audio.webm", server.URL+"/cover.png")
		case "/video.webm":
			_, _ = writer.Write(video)
		case "/audio.webm":
			_, _ = writer.Write(audio)
		case "/cover.png":
			_, _ = writer.Write(image)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: t.TempDir(),
		OutputTemplate: "fixed.webm",
		Thumbnails:     ThumbnailOptions{Embed: true},
		PrintRules: []PrintRule{
			{Stage: PrintVideo, Template: "%(ext)s|%(filename)s"},
			{Stage: PrintBeforeDL, Template: "%(ext)s|%(filename)s"},
			{Stage: PrintPostProcess, Template: "%(ext)s|%(filepath)s"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(result.Filename) != ".mkv" || !strings.HasSuffix(result.Filename, "fixed.mkv") {
		t.Fatalf("filename=%q", result.Filename)
	}
	assertOneEmbeddedThumbnail(t, result.Filename)
	var info struct {
		Ext string `json:"ext"`
	}
	if err := json.Unmarshal(result.InfoJSON, &info); err != nil || info.Ext != "mkv" {
		t.Fatalf("info=%#v error=%v", info, err)
	}
	if len(result.Prints) != 3 {
		t.Fatalf("prints=%#v", result.Prints)
	}
	for _, output := range result.Prints {
		if !strings.HasPrefix(output.Text, "mkv|") || !strings.HasSuffix(output.Text, ".mkv") {
			t.Fatalf("stage %s output=%q", output.Stage, output.Text)
		}
	}
}

func TestProductRollsBackExplicitWebMForThumbnailEmbedding(t *testing.T) {
	server, _, _, _ := newWebMPairThumbnailServer(t)
	defer server.Close()
	outputDir := t.TempDir()
	_, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: outputDir,
		OutputTemplate:    "fixed.webm",
		MergeOutputFormat: "webm",
		Thumbnails:        ThumbnailOptions{Embed: true},
	})
	if err == nil || !errors.Is(err, ffmpeg.ErrInvalidOperation) {
		t.Fatalf("error = %v, want unsupported container", err)
	}
	merged := filepath.Join(outputDir, "fixed.webm")
	if _, statErr := os.Stat(merged); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed explicit webm left partial media: %v", statErr)
	}
	if matches, _ := filepath.Glob(filepath.Join(outputDir, "*.mkv")); len(matches) != 0 {
		t.Fatalf("silent mkv rewrite created %v", matches)
	}
	assertNoPostprocessTemps(t, outputDir)
}

func TestProductExplicitWebMMergeDestinationWithoutThumbnail(t *testing.T) {
	server, _, _, _ := newWebMPairThumbnailServer(t)
	defer server.Close()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: t.TempDir(),
		OutputTemplate:    "fixed.%(ext)s",
		MergeOutputFormat: "webm",
		PrintRules: []PrintRule{
			{Stage: PrintVideo, Template: "%(ext)s|%(filename)s"},
			{Stage: PrintBeforeDL, Template: "%(ext)s|%(filename)s"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(result.Filename) != ".webm" {
		t.Fatalf("filename = %q", result.Filename)
	}
	for _, output := range result.Prints {
		if !strings.HasPrefix(output.Text, "webm|") || !strings.HasSuffix(output.Text, ".webm") {
			t.Fatalf("stage %s output=%q", output.Stage, output.Text)
		}
	}
}

func TestProductExplicitMKVMergeFormatForThumbnail(t *testing.T) {
	server, _, _, _ := newWebMPairThumbnailServer(t)
	defer server.Close()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: t.TempDir(),
		OutputTemplate:    "fixed.%(ext)s",
		MergeOutputFormat: "mkv",
		Thumbnails:        ThumbnailOptions{Embed: true},
		PrintRules: []PrintRule{
			{Stage: PrintVideo, Template: "%(ext)s|%(filename)s"},
			{Stage: PrintPostProcess, Template: "%(ext)s|%(filepath)s"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(result.Filename) != ".mkv" {
		t.Fatalf("filename = %q", result.Filename)
	}
	assertOneEmbeddedThumbnail(t, result.Filename)
	for _, output := range result.Prints {
		if !strings.HasPrefix(output.Text, "mkv|") {
			t.Fatalf("stage %s output=%q", output.Stage, output.Text)
		}
	}
}

func newWebMPairThumbnailServer(t *testing.T) (*httptest.Server, []byte, []byte, []byte) {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	videoPath := filepath.Join(fixtureRoot, "video.webm")
	audioPath := filepath.Join(fixtureRoot, "audio.webm")
	imagePath := filepath.Join(fixtureRoot, "cover.png")
	commands := [][]string{
		{"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=32x32:d=0.3", "-an", "-c:v", "libvpx-vp9", videoPath},
		{"-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=500:duration=0.3", "-vn", "-c:a", "libopus", audioPath},
		{"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=yellow:s=32x32:d=0.1", "-frames:v", "1", imagePath},
	}
	for _, args := range commands {
		if output, err := exec.Command(ffmpegPath, args...).CombinedOutput(); err != nil {
			t.Fatalf("generate fixture: %v: %s", err, output)
		}
	}
	video := mustReadEmbedProductFile(t, videoPath)
	audio := mustReadEmbedProductFile(t, audioPath)
	image := mustReadEmbedProductFile(t, imagePath)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{
				"id":"webm-pair","title":"WebM Pair","ext":"webm",
				"formats":[
					{"format_id":"video","url":%q,"ext":"webm","vcodec":"vp9","acodec":"none","height":720},
					{"format_id":"audio","url":%q,"ext":"webm","vcodec":"none","acodec":"opus","abr":128}
				],
				"thumbnail":%q
			}`, server.URL+"/video.webm", server.URL+"/audio.webm", server.URL+"/cover.png")
		case "/video.webm":
			_, _ = writer.Write(video)
		case "/audio.webm":
			_, _ = writer.Write(audio)
		case "/cover.png":
			_, _ = writer.Write(image)
		default:
			http.NotFound(writer, request)
		}
	}))
	return server, video, audio, image
}

func mustReadEmbedProductFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertOneEmbeddedThumbnail(t *testing.T, path string) {
	t.Helper()
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	attached := 0
	for _, stream := range probe.Streams {
		if stream.Disposition["attached_pic"] == 1 {
			attached++
		}
	}
	if attached != 1 {
		t.Fatalf("attached thumbnails=%d streams=%#v", attached, probe.Streams)
	}
}
