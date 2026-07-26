package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/downloader"
	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestSubtitleSidecarsDownloadWithSkipDownload(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, SkipDownload: true,
		Subtitles: SubtitleOptions{WriteManual: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "Deterministic Fixture.en.vtt")
	body, err := os.ReadFile(wantPath)
	if err != nil || !strings.Contains(string(body), "manual english") {
		t.Fatalf("subtitle body = %q, error = %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Deterministic Fixture.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("media was downloaded with SkipDownload: %v", err)
	}
	if !result.Downloaded || result.Filename != "" || result.Bytes != int64(len(body)) || len(result.Artifacts) != 1 || result.Artifacts[0] != (Artifact{Path: wantPath, Kind: "subtitle"}) {
		t.Fatalf("result = %#v", result)
	}
	var info map[string]any
	if err := json.Unmarshal(result.InfoJSON, &info); err != nil {
		t.Fatal(err)
	}
	requested := info["requested_subtitles"].(map[string]any)["en"].(map[string]any)
	if requested["ext"] != "vtt" || requested["_auto"] != false || requested["filepath"] != wantPath {
		t.Fatalf("requested subtitle = %#v", requested)
	}
}

func TestSubtitleSelectionMatchesPinnedReferenceCases(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	for _, test := range []struct {
		name     string
		options  SubtitleOptions
		files    []string
		contains map[string]string
	}{
		{
			name: "format preference", options: SubtitleOptions{WriteManual: true, Format: "foo/srt"},
			files: []string{"Deterministic Fixture.en.srt"},
		},
		{
			name: "all except english", options: SubtitleOptions{WriteManual: true, Languages: []string{"all", "-en"}},
			files: []string{"Deterministic Fixture.es.vtt", "Deterministic Fixture.fr.vtt"},
		},
		{
			name: "regex", options: SubtitleOptions{WriteManual: true, Languages: []string{"e.+"}},
			files: []string{"Deterministic Fixture.en.vtt", "Deterministic Fixture.es.vtt"},
		},
		{
			name: "manual precedes automatic", options: SubtitleOptions{WriteManual: true, WriteAutomatic: true, Languages: []string{"es", "pt"}},
			files:    []string{"Deterministic Fixture.es.vtt", "Deterministic Fixture.pt.vtt"},
			contains: map[string]string{"Deterministic Fixture.es.vtt": "manual spanish", "Deterministic Fixture.pt.vtt": "automatic portuguese"},
		},
		{
			name: "automatic only", options: SubtitleOptions{WriteAutomatic: true, Languages: []string{"es", "pt"}},
			files:    []string{"Deterministic Fixture.es.vtt", "Deterministic Fixture.pt.vtt"},
			contains: map[string]string{"Deterministic Fixture.es.vtt": "automatic spanish", "Deterministic Fixture.pt.vtt": "automatic portuguese"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			result, err := NewClient().Run(context.Background(), Request{
				URL: server.URL + "/page", OutputDir: root, SkipDownload: true, Subtitles: test.options,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Artifacts) != len(test.files) {
				t.Fatalf("artifacts = %#v", result.Artifacts)
			}
			for _, name := range test.files {
				body, err := os.ReadFile(filepath.Join(root, name))
				if err != nil {
					t.Fatal(err)
				}
				if want := test.contains[name]; want != "" && !strings.Contains(string(body), want) {
					t.Fatalf("%s = %q", name, body)
				}
			}
		})
	}
}

func TestSubtitleAndMediaArtifactsAreBothReported(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root,
		Subtitles: SubtitleOptions{WriteManual: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 2 || result.Artifacts[0].Kind != "subtitle" || result.Artifacts[1].Kind != "media" {
		t.Fatalf("artifacts = %#v", result.Artifacts)
	}
	if result.Filename != filepath.Join(root, "Deterministic Fixture.bin") {
		t.Fatalf("filename = %q", result.Filename)
	}
	for _, artifact := range result.Artifacts {
		if _, err := os.Stat(artifact.Path); err != nil {
			t.Fatalf("artifact %q: %v", artifact.Path, err)
		}
	}
}

func TestSubtitleFormatSelectionFailsBeforeSidecarWrite(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	_, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, Format: "missing",
		Subtitles: SubtitleOptions{WriteManual: true},
	})
	if !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "Deterministic Fixture.en.vtt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("subtitle was written before format validation: %v", statErr)
	}
}

func TestSubtitleLiteralOutputSuffixIsPreserved(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, OutputTemplate: "archive.subtitle", SkipDownload: true,
		Subtitles: SubtitleOptions{WriteManual: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "archive.subtitle.en.vtt")
	if len(result.Artifacts) != 1 || result.Artifacts[0].Path != want {
		t.Fatalf("artifacts = %#v; want %q", result.Artifacts, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

func TestSubtitleHeadersValidateOnlySelectedTrack(t *testing.T) {
	track := func(headers *value.Object) value.Value {
		return value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String("https://captions.example/sub.vtt")},
			value.Field{Key: "ext", Value: value.String("vtt")},
			value.Field{Key: "http_headers", Value: value.ObjectValue(headers)},
		))
	}
	info := value.NewInfo(value.NewObject(value.Field{
		Key: "subtitles", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "en", Value: value.List(track(value.NewObject()))},
			value.Field{Key: "fr", Value: value.List(track(value.NewObject(
				value.Field{Key: "X-Test", Value: value.String("bad\r\nvalue")},
			)))},
		)),
	}))
	if _, _, err := selectSubtitles(info, SubtitleOptions{WriteManual: true}); err != nil {
		t.Fatalf("unselected malformed headers caused failure: %v", err)
	}
	_, _, err := selectSubtitles(info, SubtitleOptions{WriteManual: true, Languages: []string{"fr"}})
	if !errors.Is(err, mediaformat.ErrInvalidHeaders) {
		t.Fatalf("selected malformed headers error = %v", err)
	}
}

func TestSubtitleExtensionInference(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata *value.Object
		rawURL   string
		want     string
	}{
		{"explicit", value.NewObject(value.Field{Key: "ext", Value: value.String("ass")}), "https://captions.example/file", "ass"},
		{"mime", value.NewObject(value.Field{Key: "mime_type", Value: value.String("text/vtt; charset=utf-8")}), "https://captions.example/file", "vtt"},
		{"path", value.NewObject(), "https://captions.example/file.SRT?token=secret", "srt"},
		{"query", value.NewObject(), "https://captions.example/file.php?fmt=ttml", "ttml"},
		{"unknown path", value.NewObject(), "https://captions.example/file.php", "vtt"},
		{"extensionless", value.NewObject(), "https://captions.example/api/caption?id=1", "vtt"},
		{"invalid explicit", value.NewObject(value.Field{Key: "ext", Value: value.String("../vtt")}), "https://captions.example/file.vtt", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := subtitleExtension(test.metadata, test.rawURL); got != test.want {
				t.Fatalf("extension = %q; want %q", got, test.want)
			}
		})
	}
}

func TestSelectSubtitleWithoutExplicitExtension(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{
		Key: "subtitles", Value: value.ObjectValue(value.NewObject(value.Field{
			Key: "en", Value: value.List(value.ObjectValue(value.NewObject(
				value.Field{Key: "url", Value: value.String("https://captions.example/subtitle.SRT?token=secret")},
			))),
		})),
	}))
	tracks, requested, err := selectSubtitles(info, SubtitleOptions{WriteManual: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].extension != "srt" {
		t.Fatalf("tracks = %#v", tracks)
	}
	selected, _ := requested.Lookup("en").Object()
	if extension, _ := selected.Lookup("ext").StringValue(); extension != "srt" {
		t.Fatalf("requested subtitle extension = %q", extension)
	}
}

func TestSelectSubtitleLanguagesOrderedRules(t *testing.T) {
	// Derived from yt-dlp test/test_YoutubeDL.py subtitle-selection cases at
	// aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8.
	available := []subtitleLanguage{{name: "en"}, {name: "es"}, {name: "fr"}, {name: "pt"}}
	for _, test := range []struct {
		rules []string
		want  string
	}{
		{nil, "en"},
		{[]string{"es", "fr", "it"}, "es,fr"},
		{[]string{"all", "-en"}, "es,fr,pt"},
		{[]string{"en", "fr", "-en"}, "fr"},
		{[]string{"-en", "en"}, "en"},
		{[]string{"e.+"}, "en,es"},
	} {
		got, err := selectSubtitleLanguages(available, 3, test.rules)
		if err != nil || strings.Join(got, ",") != test.want {
			t.Errorf("rules %q = %q, %v; want %q", test.rules, got, err, test.want)
		}
	}
}

func TestSubtitleDestinationExistingFileFailsClosed(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	destination := filepath.Join(root, "Deterministic Fixture.en.vtt")
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, SkipDownload: true,
		Subtitles: SubtitleOptions{WriteManual: true},
	})
	if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, downloader.ErrDestinationExists) {
		t.Fatalf("error = %v", err)
	}
	if body, readErr := os.ReadFile(destination); readErr != nil || string(body) != "keep" {
		t.Fatalf("existing destination = %q, %v", body, readErr)
	}
}

func TestSubtitleDownloadCancellation(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/page":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{"id":"slow-sub","title":"Slow subtitle","formats":[{"format_id":"media","url":%q,"ext":"bin"}],"subtitles":{"en":[{"url":%q,"ext":"vtt"}]}}`, server.URL+"/media", server.URL+"/slow")
		case "/slow":
			<-request.Context().Done()
		case "/media":
			_, _ = writer.Write([]byte("media"))
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	root := t.TempDir()
	_, err := NewClient().Run(ctx, Request{
		URL: server.URL + "/page", OutputDir: root, SkipDownload: true,
		Subtitles: SubtitleOptions{WriteManual: true},
	})
	if !IsCategory(err, ErrorCancelled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "Slow subtitle.en.vtt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled subtitle was published: %v", statErr)
	}
}

func TestSubtitleOptionsRejectInvalidRegexBeforeNetwork(t *testing.T) {
	_, err := NewClient().Run(context.Background(), Request{
		URL: "https://example.invalid/page", SkipDownload: true,
		Subtitles: SubtitleOptions{WriteManual: true, Languages: []string{"["}},
	})
	if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, errInvalidRequestOptions) {
		t.Fatalf("error = %v", err)
	}
}

func TestSubtitleEmbeddingImplicitlySelectsManualTracks(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: root, SkipDownload: true,
		Subtitles: SubtitleOptions{Embed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Kind != "subtitle" {
		t.Fatalf("result=%#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "Deterministic Fixture.en.vtt")); err != nil {
		t.Fatal(err)
	}

	autoRoot := t.TempDir()
	autoResult, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/page", OutputDir: autoRoot, SkipDownload: true,
		Subtitles: SubtitleOptions{
			Embed: true, WriteAutomatic: true, Languages: []string{"pt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(autoResult.Artifacts) != 1 {
		t.Fatalf("automatic result=%#v", autoResult)
	}
	body, err := os.ReadFile(filepath.Join(autoRoot, "Deterministic Fixture.pt.vtt"))
	if err != nil || !strings.Contains(string(body), "automatic portuguese") {
		t.Fatalf("automatic subtitle=%q err=%v", body, err)
	}
}

func TestSubtitleKeepFilesRequiresEmbedding(t *testing.T) {
	_, err := NewClient().Run(context.Background(), Request{
		URL: "https://example.invalid/page", SkipDownload: true,
		Subtitles: SubtitleOptions{WriteManual: true, KeepFiles: true},
	})
	if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, errInvalidRequestOptions) {
		t.Fatalf("error=%v", err)
	}
}

func TestSubtitleMetadataCombinedLanguageLimit(t *testing.T) {
	collection := func(prefix string) *value.Object {
		object := value.NewObject()
		for index := 0; index < 200; index++ {
			object.Set(fmt.Sprintf("%s%d", prefix, index), value.List(value.ObjectValue(value.NewObject(
				value.Field{Key: "url", Value: value.String("https://captions.example/sub.vtt")},
				value.Field{Key: "ext", Value: value.String("vtt")},
			))))
		}
		return object
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "subtitles", Value: value.ObjectValue(collection("m"))},
		value.Field{Key: "automatic_captions", Value: value.ObjectValue(collection("a"))},
	))
	_, _, err := selectSubtitles(info, SubtitleOptions{WriteManual: true, WriteAutomatic: true})
	if !errors.Is(err, extractor.ErrInvalidMetadata) {
		t.Fatalf("error = %v", err)
	}
}

func TestSubtitleEmbeddingTrackLimitFailsBeforeDownload(t *testing.T) {
	collection := value.NewObject()
	for index := 0; index <= maxEmbeddedSubtitleTracks; index++ {
		collection.Set(fmt.Sprintf("l%d", index), value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String("https://captions.example/sub.vtt")},
			value.Field{Key: "ext", Value: value.String("vtt")},
		))))
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "subtitles", Value: value.ObjectValue(collection)},
	))
	_, _, err := selectSubtitles(info, SubtitleOptions{
		Embed: true, Languages: []string{"all"},
	})
	if !errors.Is(err, extractor.ErrInvalidMetadata) {
		t.Fatalf("error=%v", err)
	}
}

func FuzzValidateSubtitleOptions(f *testing.F) {
	f.Add("en", "srt/vtt/best")
	f.Add("all,-live_chat", "best")
	f.Add("[", "bad format")
	f.Fuzz(func(t *testing.T, language, format string) {
		_ = validateSubtitleOptions(SubtitleOptions{
			WriteManual: true, Languages: []string{language}, Format: format,
		})
	})
}

func FuzzSubtitleExtension(f *testing.F) {
	f.Add("", "", "https://captions.example/file.vtt")
	f.Add("", "text/srt", "https://captions.example/file")
	f.Add("ass", "", "https://captions.example/file")
	f.Fuzz(func(t *testing.T, extension, mimeType, rawURL string) {
		metadata := value.NewObject()
		if extension != "" {
			metadata.Set("ext", value.String(extension))
		}
		if mimeType != "" {
			metadata.Set("mime_type", value.String(mimeType))
		}
		got := subtitleExtension(metadata, rawURL)
		if got != "" && !subtitleExtensionPattern.MatchString(got) {
			t.Fatalf("unsafe inferred extension %q", got)
		}
	})
}

func TestSubtitleDownloaderAssemblesHLSSubtitlePlaylists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/subs_en.m3u8":
			writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = writer.Write([]byte("#EXTM3U\n#EXTINF:1,\nseg0.vtt\n#EXT-X-ENDLIST\n"))
		case "/seg0.vtt":
			writer.Header().Set("Content-Type", "text/vtt")
			_, _ = writer.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nassembled english\n"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture")},
		value.Field{Key: "title", Value: value.String("Fixture")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	info.Set("subtitles", value.ObjectValue(value.NewObject(
		value.Field{Key: "en", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(server.URL + "/subs_en.m3u8")},
			value.Field{Key: "ext", Value: value.String("vtt")},
		)))},
	)))
	operation := &operation{
		client: NewClient(),
		request: Request{
			OutputDir: root, SkipDownload: true,
			Subtitles: SubtitleOptions{WriteManual: true, Languages: []string{"en"}},
		},
		transport: transport,
	}
	tracks, _, err := selectSubtitles(info, operation.request.Subtitles)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, _, err := operation.downloadSubtitles(context.Background(), info, tracks, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifacts = %#v", artifacts)
	}
	body, err := os.ReadFile(artifacts[0].Path)
	if err != nil || !strings.Contains(string(body), "assembled english") {
		t.Fatalf("subtitle body = %q, error = %v", body, err)
	}
}

func TestSubtitleHLSDownloadRejectsEncryptedPlaylists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			http.Error(writer, "credential leakage", http.StatusForbidden)
			return
		}
		switch request.URL.Path {
		case "/subs_en.m3u8":
			_, _ = writer.Write([]byte("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n#EXTINF:1,\nseg0.vtt\n#EXT-X-ENDLIST\n"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture")},
		value.Field{Key: "title", Value: value.String("Fixture")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	info.Set("subtitles", value.ObjectValue(value.NewObject(
		value.Field{Key: "en", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(server.URL + "/subs_en.m3u8")},
			value.Field{Key: "ext", Value: value.String("vtt")},
		)))},
	)))
	operation := &operation{
		client: NewClient(),
		request: Request{
			OutputDir: root, SkipDownload: true,
			Subtitles: SubtitleOptions{WriteManual: true, Languages: []string{"en"}},
		},
		transport: transport,
	}
	tracks, _, err := selectSubtitles(info, operation.request.Subtitles)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = operation.downloadSubtitles(context.Background(), info, tracks, nil)
	if !errors.Is(err, hls.ErrUnsupportedEncryption) {
		t.Fatalf("error = %v", err)
	}
}

func TestSubtitleHLSWriteEmitsEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/subs_en.m3u8":
			_, _ = writer.Write([]byte("#EXTM3U\n#EXTINF:1,\nseg0.vtt\n#EXT-X-ENDLIST\n"))
		case "/seg0.vtt":
			_, _ = writer.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\nassembled english\n"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture")},
		value.Field{Key: "title", Value: value.String("Fixture")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	info.Set("subtitles", value.ObjectValue(value.NewObject(
		value.Field{Key: "en", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(server.URL + "/subs_en.m3u8")},
			value.Field{Key: "ext", Value: value.String("vtt")},
		)))},
	)))
	var mu sync.Mutex
	var emitted []events.Event
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		mu.Lock()
		defer mu.Unlock()
		emitted = append(emitted, event)
		return nil
	})
	operation := &operation{
		client: NewClient(),
		request: Request{
			OutputDir: root, SkipDownload: true,
			Subtitles: SubtitleOptions{WriteManual: true, Languages: []string{"en"}},
		},
		transport: transport,
	}
	tracks, _, err := selectSubtitles(info, operation.request.Subtitles)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, _, err := operation.downloadSubtitles(context.Background(), info, tracks, sink)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifacts = %#v", artifacts)
	}
	mu.Lock()
	defer mu.Unlock()
	started := false
	completed := false
	for _, event := range emitted {
		switch event.Kind {
		case events.KindStarting:
			started = true
		case events.KindCompleted:
			completed = true
			if event.Bytes <= 0 {
				t.Fatalf("completed event bytes=%d, want > 0", event.Bytes)
			}
		}
	}
	if !started {
		t.Fatal("expected KindStarting event")
	}
	if !completed {
		t.Fatal("expected KindCompleted event")
	}
}

func TestSubtitleHLSDestinationExistsFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/subs_en.m3u8":
			_, _ = writer.Write([]byte("#EXTM3U\n#EXTINF:1,\nseg0.vtt\n#EXT-X-ENDLIST\n"))
		case "/seg0.vtt":
			_, _ = writer.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\ncontent\n"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture")},
		value.Field{Key: "title", Value: value.String("Fixture")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	existing := filepath.Join(root, "Fixture.en.vtt")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	info.Set("subtitles", value.ObjectValue(value.NewObject(
		value.Field{Key: "en", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(server.URL + "/subs_en.m3u8")},
			value.Field{Key: "ext", Value: value.String("vtt")},
		)))},
	)))
	operation := &operation{
		client: NewClient(),
		request: Request{
			OutputDir: root, SkipDownload: true, Overwrite: false,
			Subtitles: SubtitleOptions{WriteManual: true, Languages: []string{"en"}},
		},
		transport: transport,
	}
	tracks, _, err := selectSubtitles(info, operation.request.Subtitles)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = operation.downloadSubtitles(context.Background(), info, tracks, nil)
	if !errors.Is(err, downloader.ErrDestinationExists) {
		t.Fatalf("error = %v", err)
	}
	body, _ := os.ReadFile(existing)
	if string(body) != "old" {
		t.Fatalf("existing file modified: %q", body)
	}
}

func TestSubtitleHLSAssemblyCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/subs_en.m3u8":
			_, _ = writer.Write([]byte("#EXTM3U\n#EXTINF:1,\nseg0.vtt\n#EXT-X-ENDLIST\n"))
		case "/seg0.vtt":
			_, _ = writer.Write([]byte("WEBVTT\n\n00:00.000 --> 00:01.000\ncontent\n"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture")},
		value.Field{Key: "title", Value: value.String("Fixture")},
		value.Field{Key: "ext", Value: value.String("mp4")},
	))
	info.Set("subtitles", value.ObjectValue(value.NewObject(
		value.Field{Key: "en", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(server.URL + "/subs_en.m3u8")},
			value.Field{Key: "ext", Value: value.String("vtt")},
		)))},
	)))
	operation := &operation{
		client: NewClient(),
		request: Request{
			OutputDir: root, SkipDownload: true,
			Subtitles: SubtitleOptions{WriteManual: true, Languages: []string{"en"}},
		},
		transport: transport,
	}
	tracks, _, err := selectSubtitles(info, operation.request.Subtitles)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = operation.downloadSubtitles(ctx, info, tracks, nil)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
