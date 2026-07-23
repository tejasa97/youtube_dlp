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
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestProductEmbedsSelectedSubtitleTracksAndAppliesRetention(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	mediaPath := filepath.Join(fixtureRoot, "media.mp4")
	output, err := exec.Command(ffmpegPath,
		"-nostdin", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=32x32:d=0.4",
		"-f", "lavfi", "-i", "sine=frequency=700:duration=0.4",
		"-shortest", "-c:v", "mpeg4", "-c:a", "aac", mediaPath,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("generate media: %v: %s", err, output)
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
				"id":"embed-fixture","title":"Embedded Fixture","ext":"mp4",
				"formats":[{"format_id":"media","url":%q,"ext":"mp4","vcodec":"mpeg4","acodec":"aac"}],
				"subtitles":{
					"en":[{"url":%q,"ext":"vtt","name":"English"}],
					"fr":[{"url":%q,"ext":"srt","name":"French"}]
				}
			}`, server.URL+"/media.mp4", server.URL+"/en.vtt", server.URL+"/fr.srt")
		case "/media.mp4":
			writer.Header().Set("Content-Type", "video/mp4")
			writer.Header().Set("Content-Length", fmt.Sprint(len(media)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write(media)
			}
		case "/en.vtt":
			_, _ = writer.Write([]byte("WEBVTT\n\n00:00.000 --> 00:00.300\nEnglish\n"))
		case "/fr.srt":
			_, _ = writer.Write([]byte("1\n00:00:00,000 --> 00:00:00,300\nFrench\n"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	for _, keep := range []bool{false, true} {
		root := t.TempDir()
		result, err := NewClient().Run(context.Background(), Request{
			URL: server.URL + "/page", OutputDir: root,
			Subtitles: SubtitleOptions{
				Embed: true, KeepFiles: keep, Languages: []string{"all"},
			},
		})
		if err != nil {
			t.Fatalf("keep=%v: %v", keep, err)
		}
		tools, err := ffmpeg.Discover(ffmpeg.Config{})
		if err != nil {
			t.Fatal(err)
		}
		probe, err := tools.Probe(context.Background(), result.Filename)
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		for _, stream := range probe.Streams {
			counts[stream.CodecType]++
		}
		if counts["video"] != 1 || counts["audio"] != 1 || counts["subtitle"] != 2 {
			t.Fatalf("keep=%v streams=%#v", keep, probe.Streams)
		}
		wantArtifacts := 1
		if keep {
			wantArtifacts = 3
		}
		if len(result.Artifacts) != wantArtifacts || result.Artifacts[len(result.Artifacts)-1].Kind != "media" {
			t.Fatalf("keep=%v artifacts=%#v", keep, result.Artifacts)
		}
		for _, name := range []string{"Embedded Fixture.en.vtt", "Embedded Fixture.fr.srt"} {
			_, statErr := os.Stat(filepath.Join(root, name))
			if keep && statErr != nil {
				t.Fatalf("keep=%v %s: %v", keep, name, statErr)
			}
			if !keep && !os.IsNotExist(statErr) {
				t.Fatalf("temporary sidecar retained: %s (%v)", name, statErr)
			}
		}
		stat, err := os.Stat(result.Filename)
		if err != nil || result.Bytes < stat.Size() {
			t.Fatalf("keep=%v bytes=%d media=%v err=%v", keep, result.Bytes, stat, err)
		}
		var metadata map[string]any
		if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
			t.Fatal(err)
		}
		requested := metadata["requested_subtitles"].(map[string]any)
		for _, language := range []string{"en", "fr"} {
			if requested[language].(map[string]any)["embedded"] != true {
				t.Fatalf("keep=%v requested=%#v", keep, requested)
			}
		}
	}
}

func TestProductSkipsUnsupportedEmbeddingContainerWithoutDeletingSidecar(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	var warnings []string
	result, err := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventMetadataWarning {
			warnings = append(warnings, event.Message)
		}
		return nil
	})).Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root,
		Subtitles: SubtitleOptions{Embed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 2 || result.Artifacts[0].Kind != "subtitle" ||
		result.Artifacts[1].Kind != "media" || len(warnings) != 1 {
		t.Fatalf("result=%#v warnings=%#v", result, warnings)
	}
	if _, err := os.Stat(filepath.Join(root, "Deterministic Fixture.en.vtt")); err != nil {
		t.Fatal(err)
	}
}

func TestProductSkipsUnsupportedWebMTrackWithoutDeletingSidecar(t *testing.T) {
	root := t.TempDir()
	mediaPath := filepath.Join(root, "media.webm")
	sidecar := filepath.Join(root, "caption.srt")
	mediaBody := []byte("unchanged synthetic media")
	if err := os.WriteFile(mediaPath, mediaBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecar, []byte("subtitle"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warnings []string
	operation := &operation{
		client: NewClient(WithEventHandler(func(_ context.Context, event Event) error {
			if event.Kind == EventMetadataWarning {
				warnings = append(warnings, event.Message)
			}
			return nil
		})),
		request: Request{Subtitles: SubtitleOptions{Embed: true}},
	}
	metadata := value.NewObject(value.Field{Key: "filepath", Value: value.String(sidecar)})
	artifacts := []Artifact{{Path: sidecar, Kind: "subtitle"}, {Path: mediaPath, Kind: "media"}}
	info := value.NewInfo(value.NewObject())
	got, embedded, err := operation.embedSelectedSubtitles(
		context.Background(), &info, mediaPath,
		[]subtitleTrack{{language: "en", extension: "srt", metadata: metadata}},
		artifacts, nil,
	)
	if err != nil || embedded || len(got) != 2 || len(warnings) != 1 {
		t.Fatalf("embedded=%v artifacts=%#v warnings=%#v err=%v", embedded, got, warnings, err)
	}
	if body, err := os.ReadFile(mediaPath); err != nil || string(body) != string(mediaBody) {
		t.Fatalf("media changed: %q, %v", body, err)
	}
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("sidecar removed: %v", err)
	}
}

func TestProductConvertsBeforeEmbeddingForWebM(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	mediaPath := filepath.Join(fixtureRoot, "media.webm")
	output, err := exec.Command(ffmpegPath,
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=32x32:d=0.3",
		"-c:v", "libvpx-vp9", mediaPath,
	).CombinedOutput()
	if err != nil {
		t.Skipf("webm encoder unavailable: %v: %s", err, output)
	}
	media, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			_, _ = fmt.Fprintf(writer, `{
				"id":"webm-embed","title":"WebM Embed","ext":"webm",
				"formats":[{"format_id":"media","url":%q,"ext":"webm","vcodec":"vp9","acodec":"none"}],
				"subtitles":{"en":[{"url":%q,"ext":"srt"}]}
			}`, server.URL+"/media.webm", server.URL+"/en.srt")
		case "/media.webm":
			writer.Header().Set("Content-Length", fmt.Sprint(len(media)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write(media)
			}
		case "/en.srt":
			_, _ = writer.Write([]byte("1\n00:00:00,000 --> 00:00:00,200\nConverted first\n"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root,
		Subtitles: SubtitleOptions{Embed: true, ConvertFormat: "webvtt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := ffmpeg.Discover(ffmpeg.Config{})
	probe, err := tools.Probe(context.Background(), result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	subtitles := 0
	for _, stream := range probe.Streams {
		if stream.CodecType == "subtitle" {
			subtitles++
			if stream.CodecName != "webvtt" {
				t.Fatalf("subtitle codec=%q", stream.CodecName)
			}
		}
	}
	if subtitles != 1 || len(result.Artifacts) != 1 {
		t.Fatalf("streams=%#v artifacts=%#v", probe.Streams, result.Artifacts)
	}
	for _, path := range []string{
		filepath.Join(root, "WebM Embed.en.srt"),
		filepath.Join(root, "WebM Embed.en.vtt"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("temporary subtitle remains at %s: %v", path, err)
		}
	}
}

func TestSubtitleCleanupFailureIsNonVetoableAndReported(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	mediaPath := filepath.Join(root, "media.mp4")
	output, err := exec.Command(ffmpegPath,
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=32x32:d=0.3",
		"-c:v", "mpeg4", mediaPath,
	).CombinedOutput()
	if err != nil {
		t.Fatalf("generate media: %v: %s", err, output)
	}
	source := filepath.Join(root, "caption.srt")
	if err := os.WriteFile(source, []byte("1\n00:00:00,000 --> 00:00:00,200\nRetained\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warnings []Event
	operation := &operation{
		client: NewClient(WithEventHandler(func(_ context.Context, event Event) error {
			if event.Kind == EventMetadataWarning {
				warnings = append(warnings, event)
				return errors.New("warning observer failure")
			}
			return nil
		})),
		request: Request{
			OutputDir: root,
			Subtitles: SubtitleOptions{Embed: true, ConvertFormat: "webvtt"},
		},
		removeFile: func(string) error { return errors.New("injected cleanup failure") },
	}
	metadata := value.NewObject(
		value.Field{Key: "filepath", Value: value.String(source)},
		value.Field{Key: "ext", Value: value.String("srt")},
	)
	tracks := []subtitleTrack{{language: "en", extension: "srt", metadata: metadata}}
	artifacts := []Artifact{{Path: source, Kind: "subtitle"}, {Path: mediaPath, Kind: "media"}}
	tracks, artifacts, converted, err := operation.convertSelectedSubtitles(
		context.Background(), tracks, artifacts, nil,
	)
	if err != nil || !converted {
		t.Fatalf("convert: converted=%v err=%v", converted, err)
	}
	info := value.NewInfo(value.NewObject())
	artifacts, embedded, err := operation.embedSelectedSubtitles(
		context.Background(), &info, mediaPath, tracks, artifacts, nil,
	)
	if err != nil || !embedded {
		t.Fatalf("embed: embedded=%v err=%v", embedded, err)
	}
	if len(warnings) != 2 || len(artifacts) != 3 {
		t.Fatalf("warnings=%#v artifacts=%#v", warnings, artifacts)
	}
	for _, path := range []string{source, filepath.Join(root, "caption.vtt"), mediaPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained artifact %q: %v", path, err)
		}
	}
}
