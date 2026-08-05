package ytdlp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/events"
	"github.com/tejasa97/youtube_dlp/internal/extractor"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

const mtimeLifecycleUploadDate = "20000102"

func newMtimeLifecycleProductServer(t *testing.T) *httptest.Server {
	t.Helper()
	const media = "registered-client-media"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{
				"id":"mtime-lifecycle","title":"Mtime lifecycle",
				"upload_date":%q,"description":"sidecar",
				"formats":[
					{"format_id":"video","url":%q,"ext":"mp4","vcodec":"avc1","acodec":"aac"},
					{"format_id":"audio","url":%q,"ext":"m4a","vcodec":"none","acodec":"mp4a.40.2"}
				]
			}`, mtimeLifecycleUploadDate, server.URL+"/video.mp4", server.URL+"/audio.m4a")
		case "/video.mp4", "/audio.m4a":
			writer.Header().Set("Content-Length", fmt.Sprint(len(media)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write([]byte(media))
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func mtimeLifecycleExpectedTime() time.Time {
	return time.Date(2000, 1, 2, 0, 0, 0, 0, time.UTC)
}

func assertMtimeLifecycleMtime(t *testing.T, path string, want time.Time) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().UTC().Equal(want) {
		t.Fatalf("%s mtime=%s want=%s", path, info.ModTime().UTC(), want)
	}
}

func TestProductClientRunAppliesMtimeInActiveLifecycle(t *testing.T) {
	server := newMtimeLifecycleProductServer(t)
	root := t.TempDir()
	result, err := NewClient().Run(t.Context(), Request{
		URL: server.URL + "/page", OutputDir: root, Format: "video", Overwrite: true,
		OutputTemplate: "%(id)s.%(ext)s",
		RelatedFiles:   RelatedFileOptions{WriteDescription: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || result.Filename == "" {
		t.Fatalf("result=%+v", result)
	}
	want := mtimeLifecycleExpectedTime()
	assertMtimeLifecycleMtime(t, result.Filename, want)

	for _, artifact := range result.Artifacts {
		if artifact.Kind != "description" {
			continue
		}
		if artifact.Path == result.Filename {
			t.Fatalf("description artifact aliases media: %#v", artifact)
		}
		info, statErr := os.Stat(artifact.Path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.ModTime().UTC().Equal(want) {
			t.Fatalf("sidecar %s received media mtime %s", artifact.Path, want)
		}
	}
}

func TestProductClientRunNoMtimeLeavesActiveLifecycleMtimeUntouched(t *testing.T) {
	server := newMtimeLifecycleProductServer(t)
	root := t.TempDir()
	result, err := NewClient().Run(t.Context(), Request{
		URL: server.URL + "/page", OutputDir: root, Format: "video", Overwrite: true,
		Filesystem: FilesystemOptions{NoMtime: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || result.Filename == "" {
		t.Fatalf("result=%+v", result)
	}
	if osInfo, statErr := os.Stat(result.Filename); statErr != nil {
		t.Fatal(statErr)
	} else if osInfo.ModTime().UTC().Equal(mtimeLifecycleExpectedTime()) {
		t.Fatalf("disabled mtime unexpectedly applied to %s", result.Filename)
	}
}

func TestProductClientRunAppliesMtimeToEachActiveMultiOutputLifecycle(t *testing.T) {
	server := newMtimeLifecycleProductServer(t)
	root := t.TempDir()
	result, err := NewClient().Run(t.Context(), Request{
		URL: server.URL + "/page", OutputDir: root, Format: "video,audio", Overwrite: true,
		OutputTemplate: "%(format_id)s.%(ext)s",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := mtimeLifecycleExpectedTime()
	var media int
	for _, artifact := range result.Artifacts {
		if artifact.Kind != "media" {
			continue
		}
		media++
		assertMtimeLifecycleMtime(t, artifact.Path, want)
	}
	if media != 2 {
		t.Fatalf("media artifacts=%d result=%+v", media, result)
	}
}

func TestApplyOutputMtimeFailureCanRollBackTransaction(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/video.mp4":
			const media = "registered-client-media"
			writer.Header().Set("Content-Length", fmt.Sprint(len(media)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write([]byte(media))
			}
		case "/cover.jpg":
			_, _ = writer.Write([]byte("thumbnail"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	request := Request{
		OutputDir: root, Format: "video", Overwrite: true,
		Thumbnails: ThumbnailOptions{Embed: true, KeepFiles: true},
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("mtime-failure")},
		value.Field{Key: "title", Value: value.String("Mtime failure")},
		value.Field{Key: "upload_date", Value: value.String(mtimeLifecycleUploadDate)},
		value.Field{Key: "thumbnail", Value: value.String(server.URL + "/cover.jpg")},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("video")},
			value.Field{Key: "url", Value: value.String(server.URL + "/video.mp4")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("aac")},
		)))},
	))
	operation := &operation{
		client: NewClient(), request: request, transport: transport,
		compatibility: compatibility,
		thumbnailEmbed: func(_ context.Context, media, _ string, _ events.Sink) error {
			return os.Remove(media)
		},
	}
	_, err = operation.processMedia(t.Context(), extractor.Media(info), "fixture")
	if err == nil || !strings.Contains(err.Error(), "set output mtime") {
		t.Fatalf("processMedia error=%v, want set output mtime failure", err)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("rollback left output artifacts: %v", entries)
	}
}
