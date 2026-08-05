package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/extractor"
	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func TestMultiOutputLifecycleSidecarsPrintsAndMetadataIsolation(t *testing.T) {
	pageURL := multiOutputSelectorFixture(t, nil)
	root := t.TempDir()
	result, err := newBroadTestClient().Run(t.Context(), Request{
		URL: pageURL, OutputDir: root, Format: "video,audio", Overwrite: true,
		OutputTemplate: "%(format_id)s.%(ext)s",
		RelatedFiles:   RelatedFileOptions{WriteInfoJSON: true},
		PrintRules: []PrintRule{
			{Stage: PrintVideo, Template: "%(format_id)s|video"},
			{Stage: PrintBeforeDL, Template: "%(format_id)s|before"},
			{Stage: PrintPostProcess, Template: "%(format_id)s|post"},
			{Stage: PrintAfterMove, Template: "%(format_id)s|move"},
			{Stage: PrintAfterVideo, Template: "%(format_id)s|after"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Prints) != 10 {
		t.Fatalf("prints=%#v", result.Prints)
	}
	for index, id := range []string{"video", "audio"} {
		for offset, suffix := range []string{"video", "before", "post", "move", "after"} {
			want := id + "|" + suffix
			if got := result.Prints[index*5+offset].Text; got != want {
				t.Fatalf("print[%d]=%q want %q; all=%#v", index*5+offset, got, want, result.Prints)
			}
		}
	}
	var sidecars, media []Artifact
	for _, artifact := range result.Artifacts {
		switch artifact.Kind {
		case "media":
			media = append(media, artifact)
		case "infojson":
			sidecars = append(sidecars, artifact)
		}
	}
	if len(sidecars) != 2 || len(media) != 2 {
		t.Fatalf("artifacts=%#v", result.Artifacts)
	}
	for index := 0; index < 2; index++ {
		if result.Artifacts[index].Kind != "infojson" || result.Artifacts[index+2].Kind != "media" {
			t.Fatalf("artifact order=%#v", result.Artifacts)
		}
	}
	for _, artifact := range sidecars {
		raw, readErr := os.ReadFile(artifact.Path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var info struct {
			FormatID string `json:"format_id"`
		}
		if json.Unmarshal(raw, &info) != nil || !strings.Contains(filepath.Base(artifact.Path), info.FormatID) {
			t.Fatalf("sidecar=%q info=%#v raw=%s", artifact.Path, info, raw)
		}
	}
}

func TestMultiOutputLifecycleRejectsSharedSidecarBeforeDownload(t *testing.T) {
	mediaHits := 0
	pageURL := multiOutputSelectorFixture(t, func() { mediaHits++ })
	root := t.TempDir()
	_, err := newBroadTestClient().Run(t.Context(), Request{
		URL: pageURL, OutputDir: root, Format: "video,audio", Overwrite: true,
		RelatedFiles: RelatedFileOptions{WriteInfoJSON: true},
	})
	if !errors.Is(err, errDestinationCollision) {
		t.Fatalf("Run()=%v", err)
	}
	if mediaHits != 0 {
		t.Fatalf("media downloads=%d", mediaHits)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("output directory entries=%v err=%v", entries, readErr)
	}
}

func TestMultiOutputLifecycleRunsDefaultPostprocessorPerPlan(t *testing.T) {
	server := newMultiOutputMediaServer(t, false, false)
	root := t.TempDir()
	result, err := newBroadTestClient().Run(t.Context(), Request{
		URL: server.URL + "/page", OutputDir: root, Format: "first,second", Overwrite: true,
		OutputTemplate: "%(format_id)s.%(ext)s",
		Postprocessors: []Postprocessor{{Remux: &RemuxPostprocessor{Format: "mkv"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	media := mediaArtifactsOnly(result.Artifacts)
	if len(media) != 2 || result.Filename != media[0].Path {
		t.Fatalf("result=%#v", result)
	}
	var finalInfo struct {
		Ext      string `json:"ext"`
		Filepath string `json:"filepath"`
	}
	if err := json.Unmarshal(result.InfoJSON, &finalInfo); err != nil || finalInfo.Ext != "mkv" || finalInfo.Filepath != media[0].Path {
		t.Fatalf("final info=%#v err=%v", finalInfo, err)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range media {
		if filepath.Ext(artifact.Path) != ".mkv" {
			t.Fatalf("media=%#v", media)
		}
		probe, probeErr := tools.Probe(t.Context(), artifact.Path)
		if probeErr != nil || len(probe.Streams) < 2 {
			t.Fatalf("probe %q=%#v err=%v", artifact.Path, probe, probeErr)
		}
	}
}

func TestMultiOutputLifecycleRollbackRestoresPostprocessorOverwrite(t *testing.T) {
	server := newMultiOutputMediaServer(t, false, true)
	root := t.TempDir()
	existing := filepath.Join(root, "first.mkv")
	if err := os.WriteFile(existing, []byte("preexisting-remux"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := newBroadTestClient().Run(t.Context(), Request{
		URL: server.URL + "/page", OutputDir: root, Format: "first,second", Overwrite: true,
		OutputTemplate: "%(format_id)s.%(ext)s",
		Postprocessors: []Postprocessor{{Remux: &RemuxPostprocessor{Format: "mkv"}}},
	})
	if err == nil {
		t.Fatal("expected second-output download failure")
	}
	body, readErr := os.ReadFile(existing)
	if readErr != nil || string(body) != "preexisting-remux" {
		t.Fatalf("restored remux=%q err=%v", body, readErr)
	}
	for _, name := range []string{"first.mp4", "second.mp4", "second.mkv"} {
		if _, statErr := os.Stat(filepath.Join(root, name)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("transaction artifact %q remains: %v", name, statErr)
		}
	}
}

func TestMultiOutputLifecycleEmbedsSubtitlesAndThumbnailsPerPlan(t *testing.T) {
	server := newMultiOutputMediaServer(t, true, false)
	root := t.TempDir()
	result, err := newBroadTestClient().Run(t.Context(), Request{
		URL: server.URL + "/page", OutputDir: root, Format: "first,second", Overwrite: true,
		OutputTemplate: "%(format_id)s.%(ext)s",
		Subtitles: SubtitleOptions{
			Embed: true, KeepFiles: false, Languages: []string{"en"},
		},
		Thumbnails: ThumbnailOptions{Embed: true, KeepFiles: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	media := mediaArtifactsOnly(result.Artifacts)
	if len(media) != 2 {
		t.Fatalf("artifacts=%#v", result.Artifacts)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range media {
		probe, probeErr := tools.Probe(t.Context(), artifact.Path)
		if probeErr != nil {
			t.Fatal(probeErr)
		}
		counts := map[string]int{}
		attached := 0
		for _, stream := range probe.Streams {
			counts[stream.CodecType]++
			attached += stream.Disposition["attached_pic"]
		}
		if counts["subtitle"] != 1 || attached != 1 {
			t.Fatalf("probe %q streams=%#v", artifact.Path, probe.Streams)
		}
	}
}

func TestMultiOutputLifecycleRemovesChapterRangesPerPlan(t *testing.T) {
	server := newMultiOutputMediaServer(t, false, false)
	root := t.TempDir()
	result, err := newBroadTestClient().Run(t.Context(), Request{
		URL: server.URL + "/page", OutputDir: root, Format: "first,second", Overwrite: true,
		OutputTemplate: "%(format_id)s.%(ext)s",
		RemoveChapters: []string{"*0.1-0.3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	media := mediaArtifactsOnly(result.Artifacts)
	if len(media) != 2 {
		t.Fatalf("artifacts=%#v", result.Artifacts)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range media {
		probe, probeErr := tools.Probe(t.Context(), artifact.Path)
		if probeErr != nil {
			t.Fatal(probeErr)
		}
		duration, parseErr := strconv.ParseFloat(probe.Format.Duration, 64)
		if parseErr != nil || duration <= 0 || duration >= 0.4 {
			t.Fatalf("probe %q duration=%q err=%v", artifact.Path, probe.Format.Duration, parseErr)
		}
	}
}

func TestMultiOutputLifecycleAppliesSponsorBlockMarkRemovePerPlan(t *testing.T) {
	mediaServer := newMultiOutputMediaServer(t, true, false)
	sponsorServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" ||
			request.URL.Query().Get("service") != "YouTube" {
			t.Errorf("SponsorBlock request = %#v", request)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `[{"videoID":"multi-sponsor","segments":[{"segment":[0.1,0.2],"category":"sponsor","actionType":"skip"}]}]`)
	}))
	defer sponsorServer.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	root := t.TempDir()
	request := Request{
		OutputDir: root, Format: "first,second", Overwrite: true,
		OutputTemplate: "%(format_id)s.%(ext)s",
		RelatedFiles:   RelatedFileOptions{WriteInfoJSON: true},
		Subtitles:      SubtitleOptions{WriteManual: true},
		PrintRules:     []PrintRule{{Stage: PrintAfterMove, Template: "%(format_id)s|%(duration)s"}},
		SponsorBlock: SponsorBlockOptions{
			Enabled: true, Mark: true, Remove: true, Categories: []string{"sponsor"},
			ForceKeyframes: true, APIBase: sponsorServer.URL,
		},
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("multi-sponsor")},
		value.Field{Key: "title", Value: value.String("Multi Sponsor")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("first")},
				value.Field{Key: "url", Value: value.String(mediaServer.URL + "/first.mp4")},
				value.Field{Key: "ext", Value: value.String("mp4")},
				value.Field{Key: "vcodec", Value: value.String("mpeg4")},
				value.Field{Key: "acodec", Value: value.String("aac")},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("second")},
				value.Field{Key: "url", Value: value.String(mediaServer.URL + "/second.mp4")},
				value.Field{Key: "ext", Value: value.String("mp4")},
				value.Field{Key: "vcodec", Value: value.String("mpeg4")},
				value.Field{Key: "acodec", Value: value.String("aac")},
			)),
		)},
		value.Field{Key: "subtitles", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "en", Value: value.List(value.ObjectValue(value.NewObject(
				value.Field{Key: "url", Value: value.String(mediaServer.URL + "/en.vtt")},
				value.Field{Key: "ext", Value: value.String("vtt")},
			)))},
		))},
	))
	operation := &operation{client: newBroadTestClient(), request: request, transport: transport, compatibility: compatibility}
	result, err := operation.processMedia(t.Context(), extractor.Media(info), "youtube")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Prints) != 2 || result.Bytes <= 0 {
		t.Fatalf("prints=%#v bytes=%d", result.Prints, result.Bytes)
	}
	media := mediaArtifactsOnly(result.Artifacts)
	if len(media) != 2 {
		t.Fatalf("artifacts=%#v", result.Artifacts)
	}
	var subtitles, sidecars int
	for _, artifact := range result.Artifacts {
		switch artifact.Kind {
		case "subtitle":
			subtitles++
		case "infojson":
			sidecars++
		}
	}
	if subtitles != 2 || sidecars != 2 {
		t.Fatalf("artifacts=%#v", result.Artifacts)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range media {
		probe, probeErr := tools.Probe(t.Context(), artifact.Path)
		if probeErr != nil {
			t.Fatal(probeErr)
		}
		duration, parseErr := strconv.ParseFloat(probe.Format.Duration, 64)
		if parseErr != nil || duration <= 0 || duration >= 0.35 {
			t.Fatalf("probe %q duration=%q err=%v", artifact.Path, probe.Format.Duration, parseErr)
		}
	}
}

func mediaArtifactsOnly(artifacts []Artifact) []Artifact {
	var media []Artifact
	for _, artifact := range artifacts {
		if artifact.Kind == "media" {
			media = append(media, artifact)
		}
	}
	return media
}

func newMultiOutputMediaServer(t *testing.T, sidecars, failSecond bool) *httptest.Server {
	t.Helper()
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	mediaPath := filepath.Join(root, "media.mp4")
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
	image := []byte{}
	if sidecars {
		imagePath := filepath.Join(root, "cover.png")
		output, err = exec.Command(ffmpegPath,
			"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=red:s=32x32:d=0.1",
			"-frames:v", "1", imagePath,
		).CombinedOutput()
		if err != nil {
			t.Fatalf("generate image: %v: %s", err, output)
		}
		image, err = os.ReadFile(imagePath)
		if err != nil {
			t.Fatal(err)
		}
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			extra := ""
			if sidecars {
				extra = fmt.Sprintf(`,
				"thumbnail":%q,
				"subtitles":{"en":[{"url":%q,"ext":"vtt","name":"English"}]}`,
					server.URL+"/cover.png", server.URL+"/en.vtt")
			}
			secondURL := server.URL + "/second.mp4"
			if failSecond {
				secondURL = "://missing"
			}
			_, _ = fmt.Fprintf(writer, `{
				"id":"multi-lifecycle","title":"Multi Lifecycle","description":"lifecycle description","ext":"mp4",
				"formats":[
					{"format_id":"first","url":%q,"ext":"mp4","vcodec":"mpeg4","acodec":"aac"},
					{"format_id":"second","url":%q,"ext":"mp4","vcodec":"mpeg4","acodec":"aac"}
				]%s
			}`, server.URL+"/first.mp4", secondURL, extra)
		case "/first.mp4", "/second.mp4":
			writer.Header().Set("Content-Type", "video/mp4")
			writer.Header().Set("Content-Length", fmt.Sprint(len(media)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write(media)
			}
		case "/cover.png":
			_, _ = writer.Write(image)
		case "/en.vtt":
			_, _ = writer.Write([]byte("WEBVTT\n\n00:00.000 --> 00:00.300\nEnglish\n"))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestMultiOutputLifecycleArtifactOrderDeterministic(t *testing.T) {
	server := newMultiOutputMediaServer(t, false, false)
	root := t.TempDir()
	result, err := newBroadTestClient().Run(t.Context(), Request{
		URL: server.URL + "/page", OutputDir: root, Format: "first,second", Overwrite: true,
		OutputTemplate: "%(format_id)s.%(ext)s",
		RelatedFiles:   RelatedFileOptions{WriteDescription: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, artifact := range result.Artifacts {
		paths = append(paths, artifact.Kind+":"+filepath.Base(artifact.Path))
	}
	if len(paths) != 4 || !strings.HasPrefix(paths[0], "description:") || !strings.HasPrefix(paths[1], "description:") ||
		!strings.HasPrefix(paths[2], "media:") || !strings.HasPrefix(paths[3], "media:") {
		t.Fatalf("order=%v", paths)
	}
}
