package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func generateSubtitleEmbedVideo(t *testing.T, ctx context.Context, tools *Toolset, destination string) {
	t.Helper()
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{
		"-nostdin", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.4",
		"-c:v", "mpeg4", "-q:v", "5", "-c:a", "aac", "-shortest", destination,
	}, nil); err != nil {
		t.Fatalf("generate media: %v", err)
	}
}

func writeSubtitleFixture(t *testing.T, path, text string) {
	t.Helper()
	var body string
	switch strings.TrimPrefix(filepath.Ext(path), ".") {
	case "vtt":
		body = "WEBVTT\n\n00:00.000 --> 00:00.300\n" + text + "\n"
	case "srt":
		body = "1\n00:00:00,000 --> 00:00:00,300\n" + text + "\n"
	default:
		t.Fatalf("unsupported test subtitle extension: %s", path)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestEmbedSubtitleTracksMP4MapsMetadataAndReplacesExisting(t *testing.T) {
	tools := requireToolset(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	input := filepath.Join(root, "input.mp4")
	generateSubtitleEmbedVideo(t, ctx, tools, input)
	english := filepath.Join(root, "english.vtt")
	french := filepath.Join(root, "french.srt")
	writeSubtitleFixture(t, english, "hello")
	writeSubtitleFixture(t, french, "bonjour")

	first := filepath.Join(root, "first.mp4")
	err := tools.EmbedSubtitleTracks(ctx, input, []SubtitleInput{
		{Path: english, Language: "eng", Name: "English captions", Extension: "vtt"},
		{Path: french, Language: "fra", Name: "French captions", Extension: ".srt"},
	}, first, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	var video, audio int
	var subtitles []Stream
	for _, stream := range probe.Streams {
		switch stream.CodecType {
		case "video":
			video++
		case "audio":
			audio++
		case "subtitle":
			subtitles = append(subtitles, stream)
		}
	}
	if video != 1 || audio != 1 || len(subtitles) != 2 {
		t.Fatalf("streams = %#v", probe.Streams)
	}
	for index, want := range []struct {
		language string
		name     string
	}{{"eng", "English captions"}, {"fra", "French captions"}} {
		if subtitles[index].CodecName != "mov_text" ||
			subtitles[index].Tags["language"] != want.language ||
			subtitles[index].Tags["handler_name"] != want.name {
			t.Fatalf("subtitle %d = %#v", index, subtitles[index])
		}
	}

	replacement := filepath.Join(root, "replacement.mp4")
	if err := tools.EmbedSubtitleTracks(ctx, first, []SubtitleInput{{
		Path: french, Language: "fra", Name: "Replacement", Extension: "srt",
	}}, replacement, false, nil); err != nil {
		t.Fatal(err)
	}
	replacedProbe, err := tools.Probe(ctx, replacement)
	if err != nil {
		t.Fatal(err)
	}
	subtitles = subtitles[:0]
	for _, stream := range replacedProbe.Streams {
		if stream.CodecType == "subtitle" {
			subtitles = append(subtitles, stream)
		}
	}
	if len(subtitles) != 1 || subtitles[0].Tags["language"] != "fra" ||
		subtitles[0].Tags["handler_name"] != "Replacement" {
		t.Fatalf("replacement subtitles = %#v", subtitles)
	}
}

func TestEmbedSubtitleTracksMKVAndLegacyEntryPoint(t *testing.T) {
	tools := requireToolset(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	input := filepath.Join(root, "input.mp4")
	generateSubtitleEmbedVideo(t, ctx, tools, input)
	subtitle := filepath.Join(root, "caption.vtt")
	writeSubtitleFixture(t, subtitle, "hello")

	output := filepath.Join(root, "output.mkv")
	if err := tools.EmbedSubtitles(ctx, input, subtitle, output, false, nil); err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, output)
	if err != nil {
		t.Fatal(err)
	}
	var subtitleCount int
	for _, stream := range probe.Streams {
		if stream.CodecType == "subtitle" {
			subtitleCount++
			if stream.CodecName != "webvtt" {
				t.Fatalf("subtitle stream = %#v", stream)
			}
		}
	}
	if subtitleCount != 1 {
		t.Fatalf("streams = %#v", probe.Streams)
	}
}

func TestEmbedSubtitleTracksRemainingContainerMatrix(t *testing.T) {
	tools := requireToolset(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	video := filepath.Join(root, "input.mp4")
	generateSubtitleEmbedVideo(t, ctx, tools, video)
	audio := filepath.Join(root, "input.m4a")
	generateAudio(t, ctx, tools, audio)
	subtitle := filepath.Join(root, "caption.vtt")
	writeSubtitleFixture(t, subtitle, "matrix")

	for _, test := range []struct {
		container string
		input     string
		codec     string
	}{
		{container: "mov", input: video, codec: "mov_text"},
		{container: "m4a", input: audio, codec: "mov_text"},
		{container: "mka", input: audio, codec: "webvtt"},
	} {
		t.Run(test.container, func(t *testing.T) {
			output := filepath.Join(root, "output."+test.container)
			if err := tools.EmbedSubtitleTracks(ctx, test.input, []SubtitleInput{{
				Path: subtitle, Language: "eng", Extension: "vtt",
			}}, output, false, nil); err != nil {
				t.Fatal(err)
			}
			probe, err := tools.Probe(ctx, output)
			if err != nil {
				t.Fatal(err)
			}
			var subtitles []Stream
			for _, stream := range probe.Streams {
				if stream.CodecType == "subtitle" {
					subtitles = append(subtitles, stream)
				}
			}
			if len(subtitles) != 1 || subtitles[0].CodecName != test.codec {
				t.Fatalf("subtitle streams = %#v", subtitles)
			}
		})
	}
}

func TestEmbedSubtitleTracksWebMRequiresAndAcceptsVTT(t *testing.T) {
	tools := requireToolset(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	input := filepath.Join(root, "input.webm")
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.2",
		"-an", "-c:v", "libvpx-vp9", input,
	}, nil); err != nil {
		t.Fatalf("generate webm: %v", err)
	}
	vtt := filepath.Join(root, "caption.vtt")
	writeSubtitleFixture(t, vtt, "hello")
	output := filepath.Join(root, "output.webm")
	if err := tools.EmbedSubtitleTracks(ctx, input, []SubtitleInput{{
		Path: vtt, Language: "eng", Extension: "vtt",
	}}, output, false, nil); err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, output)
	if err != nil {
		t.Fatal(err)
	}
	var subtitles []Stream
	for _, stream := range probe.Streams {
		if stream.CodecType == "subtitle" {
			subtitles = append(subtitles, stream)
		}
	}
	if len(subtitles) != 1 || subtitles[0].CodecName != "webvtt" ||
		subtitles[0].Tags["language"] != "eng" {
		t.Fatalf("subtitle streams = %#v", subtitles)
	}
}

func TestEmbedSubtitleTracksRejectsInvalidInputsBeforeOutput(t *testing.T) {
	tools := requireToolset(t)
	root := t.TempDir()
	input := filepath.Join(root, "input.mp4")
	if err := os.WriteFile(input, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	vtt := filepath.Join(root, "caption.vtt")
	writeSubtitleFixture(t, vtt, "hello")
	destination := filepath.Join(root, "output.webm")

	tests := []struct {
		name        string
		input       string
		subtitles   []SubtitleInput
		destination string
	}{
		{"empty", input, nil, destination},
		{"too many", input, make([]SubtitleInput, maxSubtitleInputs+1), destination},
		{"unsupported container", input, []SubtitleInput{{Path: vtt, Extension: "vtt"}}, filepath.Join(root, "output.avi")},
		{"webm non-vtt", input, []SubtitleInput{{Path: vtt, Extension: "srt"}}, destination},
		{"unsupported subtitle", input, []SubtitleInput{{Path: vtt, Extension: "json"}}, filepath.Join(root, "output.mkv")},
		{"invalid language", input, []SubtitleInput{{Path: vtt, Extension: "vtt", Language: "en\nbad"}}, destination},
		{"invalid name", input, []SubtitleInput{{Path: vtt, Extension: "vtt", Name: "bad\x00name"}}, destination},
		{"duplicate path", input, []SubtitleInput{{Path: vtt, Extension: "vtt"}, {Path: vtt, Extension: "vtt"}}, destination},
		{"missing media", filepath.Join(root, "missing.mp4"), []SubtitleInput{{Path: vtt, Extension: "vtt"}}, destination},
		{"missing subtitle", input, []SubtitleInput{{Path: filepath.Join(root, "missing.vtt"), Extension: "vtt"}}, destination},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := tools.EmbedSubtitleTracks(context.Background(), test.input, test.subtitles, test.destination, false, nil)
			if !errors.Is(err, ErrInvalidOperation) {
				t.Fatalf("error = %v", err)
			}
			if _, statErr := os.Stat(test.destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("destination exists: %v", statErr)
			}
		})
	}

	symlink := filepath.Join(root, "caption-link.vtt")
	if err := os.Symlink(vtt, symlink); err == nil {
		err := tools.EmbedSubtitleTracks(context.Background(), input, []SubtitleInput{{
			Path: symlink, Extension: "vtt",
		}}, destination, false, nil)
		if !errors.Is(err, ErrInvalidOperation) {
			t.Fatalf("symlink error = %v", err)
		}
	}
}

func TestEmbedSubtitleTracksCancellationPreservesInputs(t *testing.T) {
	tools := requireToolset(t)
	root := t.TempDir()
	input := filepath.Join(root, "input.mp4")
	subtitle := filepath.Join(root, "caption.vtt")
	if err := os.WriteFile(input, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSubtitleFixture(t, subtitle, "hello")
	destination := filepath.Join(root, "output.mp4")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := tools.EmbedSubtitleTracks(ctx, input, []SubtitleInput{{
		Path: subtitle, Extension: "vtt",
	}}, destination, false, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	for _, path := range []string{input, subtitle} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("input %q changed: %v", path, statErr)
		}
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination exists: %v", statErr)
	}
	temporary, globErr := filepath.Glob(filepath.Join(root, ".ytdlp-postprocess-*"))
	if globErr != nil || len(temporary) != 0 {
		t.Fatalf("temporary output = %v, %v", temporary, globErr)
	}
}

func TestSubtitleEmbedArgsDropsDataAndUnknownStreams(t *testing.T) {
	args := subtitleEmbedArgs("input.mkv", []SubtitleInput{{
		Path: "subtitle.vtt", Language: "en", Name: "English", Extension: "vtt",
	}}, "mkv", "output.mkv")
	joined := strings.Join(args, "\x00")
	for _, expected := range []string{
		"-map\x000", "-map\x00-0:s", "-dn", "-ignore_unknown",
		"-map\x001:s:0", "-c\x00copy",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("args %q do not contain %q", args, expected)
		}
	}
}

func FuzzSubtitleEmbedMetadataValidation(f *testing.F) {
	f.Add("eng", "English", "vtt")
	f.Fuzz(func(t *testing.T, language, name, extension string) {
		if len(language)+len(name)+len(extension) > 4096 {
			t.Skip()
		}
		if safeSubtitleMetadata(language, 64, true) {
			if len(language) > 64 || strings.ContainsAny(language, "\x00\r\n") {
				t.Fatalf("unsafe language accepted: %q", language)
			}
			for _, character := range language {
				if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-", character) {
					t.Fatalf("restricted language character accepted: %q", language)
				}
			}
		}
		if safeSubtitleMetadata(name, 1024, false) &&
			(len(name) > 1024 || strings.ContainsAny(name, "\x00\r\n")) {
			t.Fatalf("unsafe name accepted: %q", name)
		}
		if supportedSubtitleContainer(extension) {
			switch extension {
			case "mp4", "mov", "m4a", "webm", "mkv", "mka":
			default:
				t.Fatalf("unsupported container accepted: %q", extension)
			}
		}
	})
}
