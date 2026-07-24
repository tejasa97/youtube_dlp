package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

func TestConvertResultSubtitlesSuccessRecursesAndUsesArgv(t *testing.T) {
	root := t.TempDir()
	installFakeFFmpeg(t, root)
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", root+string(os.PathListSeparator)+oldPath)
	first := writeSubtitle(t, root, "one.en.srt")
	second := writeSubtitle(t, root, "nested/two.es.srt")
	info, err := json.Marshal(map[string]any{"requested_subtitles": map[string]any{"en": map[string]any{"filepath": first, "ext": "srt"}}})
	if err != nil {
		t.Fatal(err)
	}
	result := ytdlp.Result{InfoJSON: info, Artifacts: []ytdlp.Artifact{{Path: first, Kind: "subtitle"}}, Entries: []ytdlp.Result{{Artifacts: []ytdlp.Artifact{{Path: second, Kind: "subtitle"}}}}}
	if err := convertResultSubtitles(context.Background(), &result, root, "vtt", false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{first, second} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("source %q remains: %v", path, err)
		}
		if _, err := os.Stat(strings.TrimSuffix(path, ".srt") + ".vtt"); err != nil {
			t.Fatal(err)
		}
	}
	if got := result.Artifacts[0].Path; !strings.HasSuffix(got, ".vtt") {
		t.Fatalf("artifact = %q", got)
	}
	if got := result.Entries[0].Artifacts[0].Path; !strings.HasSuffix(got, ".vtt") {
		t.Fatalf("nested artifact = %q", got)
	}
	var updated map[string]any
	if err := json.Unmarshal(result.InfoJSON, &updated); err != nil {
		t.Fatal(err)
	}
	updatedSubtitle := updated["requested_subtitles"].(map[string]any)["en"].(map[string]any)
	if updatedSubtitle["filepath"] != result.Artifacts[0].Path || updatedSubtitle["ext"] != "vtt" || strings.Contains(string(result.InfoJSON), first) {
		t.Fatalf("stale subtitle metadata: %s", result.InfoJSON)
	}
	arguments, err := os.ReadFile(filepath.Join(root, "ffmpeg.args"))
	if err != nil || !strings.Contains(string(arguments), "-c:s webvtt") || strings.Contains(string(arguments), ";") {
		t.Fatalf("argv=%q err=%v", arguments, err)
	}
}

func TestConvertResultSubtitlesMissingToolFailureAndOverwrite(t *testing.T) {
	root := t.TempDir()
	source := writeSubtitle(t, root, "caption.srt")
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	err := convertResultSubtitles(context.Background(), &ytdlp.Result{Artifacts: []ytdlp.Artifact{{Path: source, Kind: "subtitle"}}}, root, "ass", false)
	if !ytdlp.IsCategory(err, ytdlp.ErrorUnsupported) {
		t.Fatalf("missing tool error=%v", err)
	}
	if _, statErr := os.Stat(source); statErr != nil {
		t.Fatalf("missing tool removed source: %v", statErr)
	}

	installFakeFFmpeg(t, root)
	t.Setenv("PATH", root+string(os.PathListSeparator)+originalPath)
	destination := writeSubtitle(t, root, "caption.vtt")
	if err := os.WriteFile(destination, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := ytdlp.Result{Artifacts: []ytdlp.Artifact{{Path: source, Kind: "subtitle"}}}
	if err := convertResultSubtitles(context.Background(), &result, root, "vtt", true); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(destination)
	if err != nil || strings.Contains(string(body), "old") {
		t.Fatalf("overwrite body=%q err=%v", body, err)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overwrite retained source: %v", err)
	}
}

func TestConvertResultSubtitlesProcessFailurePreservesSource(t *testing.T) {
	root := t.TempDir()
	source := writeSubtitle(t, root, "caption.srt")
	installFailingFFmpeg(t, root)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := convertResultSubtitles(context.Background(), &ytdlp.Result{Artifacts: []ytdlp.Artifact{{Path: source, Kind: "subtitle"}}}, root, "vtt", false)
	if err == nil {
		t.Fatal("expected conversion failure")
	}
	if _, statErr := os.Stat(source); statErr != nil {
		t.Fatalf("failed conversion removed source: %v", statErr)
	}
	if _, statErr := os.Stat(strings.TrimSuffix(source, ".srt") + ".vtt"); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed conversion wrote destination: %v", statErr)
	}
}

func TestConvertResultSubtitlesNoopNoneFailureAndCollision(t *testing.T) {
	root := t.TempDir()
	source := writeSubtitle(t, root, "caption.srt")
	result := ytdlp.Result{Artifacts: []ytdlp.Artifact{{Path: source, Kind: "subtitle"}}}
	if err := convertResultSubtitles(context.Background(), &result, root, "none", false); err != nil || result.Artifacts[0].Path != source {
		t.Fatalf("none: %v %#v", err, result)
	}
	if err := convertResultSubtitles(context.Background(), &result, root, "srt", false); err != nil {
		t.Fatalf("same format: %v", err)
	}
	writeSubtitle(t, root, "caption.vtt")
	installFakeFFmpeg(t, root)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := convertResultSubtitles(context.Background(), &result, root, "vtt", false)
	if !ytdlp.IsCategory(err, ytdlp.ErrorInvalidInput) || !errors.Is(err, os.ErrNotExist) && !strings.Contains(err.Error(), "destination exists") {
		t.Fatalf("collision error = %v", err)
	}
	if _, statErr := os.Stat(source); statErr != nil {
		t.Fatalf("failure removed source: %v", statErr)
	}
	if err := convertResultSubtitles(context.Background(), &ytdlp.Result{}, root, "ass", false); err != nil {
		t.Fatalf("no subtitles: %v", err)
	}
}

func TestSubtitleConversionRejectsUnsafeAndCancellation(t *testing.T) {
	root := t.TempDir()
	external := writeSubtitle(t, t.TempDir(), "outside.srt")
	for _, source := range []string{external, root} {
		_, _, _, _, err := subtitleConversionPlan(root, source, "srt")
		if !ytdlp.IsCategory(err, ytdlp.ErrorSecurity) {
			t.Fatalf("source=%q err=%v", source, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := convertResultSubtitles(ctx, &ytdlp.Result{Artifacts: []ytdlp.Artifact{{Path: writeSubtitle(t, root, "x.srt"), Kind: "subtitle"}}}, root, "ass", false)
	if !ytdlp.IsCategory(err, ytdlp.ErrorCancelled) {
		t.Fatalf("cancel error=%v", err)
	}
	for _, value := range []string{"mov_text", "json", "../srt"} {
		if _, err := parseSubtitleConvertFormat(value); !ytdlp.IsCategory(err, ytdlp.ErrorInvalidInput) {
			t.Fatalf("format %q: %v", value, err)
		}
	}
}

func TestSubtitleConversionArtifactLimit(t *testing.T) {
	root := t.TempDir()
	artifacts := make([]ytdlp.Artifact, maxSubtitleConversions+1)
	for i := range artifacts {
		artifacts[i] = ytdlp.Artifact{Path: writeSubtitle(t, root, "x"+string(rune('a'+i%26))+".srt"), Kind: "subtitle"}
	}
	err := convertResultSubtitles(context.Background(), &ytdlp.Result{Artifacts: artifacts}, root, "srt", false)
	if !ytdlp.IsCategory(err, ytdlp.ErrorInvalidInput) {
		t.Fatalf("limit error=%v", err)
	}
}

func FuzzSubtitleConversionPlanner(f *testing.F) {
	f.Add("caption.srt", "vtt")
	f.Add("../escape.srt", "ass")
	f.Fuzz(func(t *testing.T, name, format string) {
		root := t.TempDir()
		if !strings.ContainsAny(name, `/\\`) && name != "" && len(name) < 80 {
			_ = os.WriteFile(filepath.Join(root, name), []byte("x"), 0o600)
		}
		mapped, err := parseSubtitleConvertFormat(format)
		if err != nil || mapped == "" {
			return
		}
		_, destination, _, _, err := subtitleConversionPlan(root, filepath.Join(root, name), mapped)
		if err == nil && !isWithin(root, destination) {
			t.Fatalf("escaped root: %q", destination)
		}
	})
}

func TestSubtitleConversionMetadataPreflightAndRelativePaths(t *testing.T) {
	root := t.TempDir()
	installFakeFFmpeg(t, root)
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, raw := range []json.RawMessage{[]byte("{"), json.RawMessage(strings.Repeat("x", maxSubtitleInfoJSONBytes+1))} {
		source := writeSubtitle(t, root, "preflight.srt")
		result := ytdlp.Result{InfoJSON: raw, Artifacts: []ytdlp.Artifact{{Path: source, Kind: "subtitle"}}}
		if err := convertResultSubtitles(context.Background(), &result, root, "vtt", false); err == nil {
			t.Fatal("expected metadata preflight error")
		}
		if _, err := os.Stat(strings.TrimSuffix(source, ".srt") + ".vtt"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("metadata failure wrote destination: %v", err)
		}
		if _, err := os.Stat(source); err != nil {
			t.Fatalf("metadata failure removed source: %v", err)
		}
	}
	relative := "relative/caption.srt"
	source := writeSubtitle(t, root, relative)
	info, err := json.Marshal(map[string]any{"requested_subtitles": map[string]any{"en": map[string]any{"filepath": relative, "ext": "srt"}}})
	if err != nil {
		t.Fatal(err)
	}
	result := ytdlp.Result{InfoJSON: info, Artifacts: []ytdlp.Artifact{{Path: relative, Kind: "subtitle"}}}
	if err := convertResultSubtitles(context.Background(), &result, root, "vtt", false); err != nil {
		t.Fatal(err)
	}
	if result.Artifacts[0].Path != "relative/caption.vtt" {
		t.Fatalf("relative artifact=%q", result.Artifacts[0].Path)
	}
	var updated map[string]any
	if err := json.Unmarshal(result.InfoJSON, &updated); err != nil {
		t.Fatal(err)
	}
	if got := updated["requested_subtitles"].(map[string]any)["en"].(map[string]any)["filepath"]; got != "relative/caption.vtt" {
		t.Fatalf("relative metadata=%q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "relative/caption.vtt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("relative source remains: %v", err)
	}
}

func writeSubtitle(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("1\n00:00:00,000 --> 00:00:01,000\nhello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func installFakeFFmpeg(t *testing.T, root string) {
	t.Helper()
	// The test double receives argv directly; it copies the declared input to
	// ffmpeg's private temporary output and records arguments without a shell.
	const program = "#!/bin/sh\nargs=\"$*\"\ninput=''\nlast=''\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = '-i' ]; then shift; input=$1; fi\n  last=$1\n  shift\ndone\nprintf '%s' \"$args\" > \"$(dirname \"$0\")/ffmpeg.args\"\ncp \"$input\" \"$last\"\nprintf 'progress=end\\n'\n"
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		path := filepath.Join(root, name)
		body := program
		if name == "ffprobe" {
			body = "#!/bin/sh\nexit 0\n"
		}
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func installFailingFFmpeg(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		body := "#!/bin/sh\nexit 0\n"
		if name == "ffmpeg" {
			body = "#!/bin/sh\nexit 7\n"
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}
