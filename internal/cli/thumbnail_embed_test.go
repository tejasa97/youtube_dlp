package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
)

func TestRunEmbedsThumbnailAndHonorsRetention(t *testing.T) {
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
	imagePath := filepath.Join(fixtureRoot, "cover.png")
	if output, err := exec.Command(ffmpegPath,
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=red:s=32x32:d=0.1",
		"-frames:v", "1", imagePath,
	).CombinedOutput(); err != nil {
		t.Fatalf("generate image: %v: %s", err, output)
	}
	media, _ := os.ReadFile(mediaPath)
	image, _ := os.ReadFile(imagePath)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{
				"id":"cli-thumbnail","title":"CLI Thumbnail","ext":"mp4",
				"formats":[{"format_id":"media","url":%q,"ext":"mp4","vcodec":"mpeg4","acodec":"aac"}],
				"thumbnail":%q
			}`, server.URL+"/media.mp4", server.URL+"/cover.png")
		case "/media.mp4":
			writer.Header().Set("Content-Length", fmt.Sprint(len(media)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write(media)
			}
		case "/cover.png":
			_, _ = writer.Write(image)
		}
	}))
	defer server.Close()

	for _, test := range []struct {
		name string
		args []string
		keep bool
	}{
		{"implicit thumbnail", []string{"--embed-thumbnail"}, false},
		{"explicit thumbnail", []string{"--embed-thumbnail", "--write-thumbnail"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			args := append([]string(nil), test.args...)
			args = append(args, "--output-dir", root, server.URL+"/page")
			var stdout, stderr bytes.Buffer
			if code := Run(args, &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			entries, err := os.ReadDir(root)
			want := 1
			if test.keep {
				want = 2
			}
			if err != nil || len(entries) != want {
				t.Fatalf("entries=%#v error=%v", entries, err)
			}
			var mediaOutput string
			for _, entry := range entries {
				if filepath.Ext(entry.Name()) == ".mp4" {
					mediaOutput = filepath.Join(root, entry.Name())
				}
			}
			tools, err := ffmpeg.Discover(ffmpeg.Config{})
			if err != nil {
				t.Fatal(err)
			}
			probe, err := tools.Probe(context.Background(), mediaOutput)
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
				t.Fatalf("attached=%d streams=%#v", attached, probe.Streams)
			}
		})
	}
}

func TestRunNoEmbedThumbnailOverridesConfiguration(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "yt-dlp.conf")
	if err := os.WriteFile(configPath, []byte("--embed-thumbnail\n--skip-download\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(writer, `<meta property="og:title" content="Reset">
<meta property="og:video" content="%s/media.mp4">
<meta property="og:image" content="%s/cover.jpg">`, server.URL, server.URL)
	}))
	defer server.Close()
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--config-location", configPath, "--no-embed-thumbnail",
		"--output-dir", root, server.URL,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("entries=%#v error=%v", entries, err)
	}
}
