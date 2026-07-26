package ytdlp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
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
