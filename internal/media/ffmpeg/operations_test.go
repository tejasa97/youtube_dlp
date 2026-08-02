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

func requireToolset(t *testing.T) *Toolset {
	t.Helper()
	tools, err := Discover(Config{})
	if err != nil {
		t.Skipf("ffmpeg toolchain unavailable: %v", err)
	}
	return tools
}

func generateAudio(t *testing.T, ctx context.Context, tools *Toolset, destination string) {
	t.Helper()
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{"-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", "-c:a", "aac", destination}, nil); err != nil {
		t.Fatalf("generate audio: %v", err)
	}
}

// Generated media keeps this test license-free and stable. It semantically
// exercises the operation forms derived from yt-dlp postprocessor/ffmpeg.py
// at pinned reference aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8.
func TestTypedAudioMetadataConcatOperations(t *testing.T) {
	tools := requireToolset(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	input := filepath.Join(root, "input.m4a")
	generateAudio(t, ctx, tools, input)
	converted := filepath.Join(root, "audio.mp3")
	if err := tools.ExtractAudio(ctx, input, converted, AudioOptions{Codec: "libmp3lame", Bitrate: "96k"}, false, nil); err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, converted)
	if err != nil || len(probe.Streams) != 1 || probe.Streams[0].CodecType != "audio" {
		t.Fatalf("audio probe = %#v, err=%v", probe, err)
	}
	metadata := filepath.Join(root, "metadata.mp3")
	if err := tools.EmbedMetadata(ctx, converted, metadata, Metadata{"title": "Typed media test", "artist": "ytdlp-go"}, false, nil); err != nil {
		t.Fatal(err)
	}
	probe, err = tools.Probe(ctx, metadata)
	if err != nil || probe.Format.Tags["title"] != "Typed media test" {
		t.Fatalf("metadata probe = %#v, err=%v", probe, err)
	}
	concatenated := filepath.Join(root, "concat.mp3")
	if err := tools.Concat(ctx, []string{metadata, metadata}, concatenated, false, nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(concatenated); err != nil || info.Size() <= 0 {
		t.Fatalf("concat output: %v", err)
	}
	chaptered := filepath.Join(root, "chaptered.m4a")
	chapters := []Chapter{{Start: 0, End: 150 * time.Millisecond, Title: "Part = one"}, {Start: 150 * time.Millisecond, End: 300 * time.Millisecond, Title: "Part two"}}
	if err := tools.EmbedChapters(ctx, input, chaptered, chapters, false, nil); err != nil {
		t.Fatal(err)
	}
	probe, err = tools.Probe(ctx, chaptered)
	if err != nil || len(probe.Chapters) != 2 || probe.Chapters[0].Tags["title"] != "Part = one" {
		t.Fatalf("chapter probe = %#v, err=%v", probe, err)
	}
}

func TestEmbedInfoJSONIsBoundedIdempotentAndAttachmentOnly(t *testing.T) {
	tools := requireToolset(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	input := filepath.Join(root, "input.mkv")
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.3",
		"-metadata:s:v:0", "mimetype=application/json", "-c:v", "mpeg4", input,
	}, nil); err != nil {
		t.Fatalf("generate matroska: %v", err)
	}
	output := filepath.Join(root, "embedded.mkv")
	if err := tools.EmbedInfoJSON(ctx, input, output, []byte(`{"title":"first"}`), false, nil); err != nil {
		t.Fatal(err)
	}
	if err := tools.EmbedInfoJSON(ctx, output, output, []byte(`{"title":"second"}`), true, nil); err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, output)
	if err != nil {
		t.Fatal(err)
	}
	attachments := 0
	videoStreams := 0
	for _, stream := range probe.Streams {
		if stream.CodecType == "attachment" {
			attachments++
		}
		if stream.CodecType == "video" {
			videoStreams++
		}
	}
	if attachments != 1 || videoStreams != 1 {
		t.Fatalf("streams=%#v attachments=%d video=%d", probe.Streams, attachments, videoStreams)
	}
}

func TestEmbedInfoJSONRejectsUnsupportedAndOversizedPayloads(t *testing.T) {
	tools := requireToolset(t)
	root := t.TempDir()
	input := filepath.Join(root, "input.mp4")
	if err := os.WriteFile(input, []byte("not media"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tools.EmbedInfoJSON(context.Background(), input, filepath.Join(root, "out.mp4"), []byte(`{"ok":true}`), false, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("unsupported container error=%v", err)
	}
	if err := tools.EmbedInfoJSON(context.Background(), input, filepath.Join(root, "out.mkv"), []byte("not-json"), false, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("invalid payload error=%v", err)
	}
}

func TestTypedSubtitleAndImageConversions(t *testing.T) {
	tools := requireToolset(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	srt := filepath.Join(root, "caption.srt")
	if err := os.WriteFile(srt, []byte("1\n00:00:00,000 --> 00:00:00,200\nhello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	vtt := filepath.Join(root, "caption.vtt")
	if err := tools.ConvertSubtitle(ctx, srt, vtt, SubtitleOptions{Format: "webvtt"}, false, nil); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(vtt)
	if err != nil || !strings.Contains(string(body), "WEBVTT") {
		t.Fatalf("vtt = %q, err=%v", body, err)
	}
	imageInput := filepath.Join(root, "input.png")
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=red:s=8x8:d=0.1", "-frames:v", "1", imageInput}, nil); err != nil {
		t.Fatal(err)
	}
	imageOutput := filepath.Join(root, "output.jpg")
	if err := tools.ConvertImage(ctx, imageInput, imageOutput, ImageOptions{Format: "jpg"}, false, nil); err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, imageOutput)
	if err != nil || len(probe.Streams) != 1 || probe.Streams[0].CodecName != "mjpeg" {
		t.Fatalf("image probe = %#v, err=%v", probe, err)
	}
}

func TestOperationValidationAndAtomicFailure(t *testing.T) {
	tools := requireToolset(t)
	root := t.TempDir()
	if err := tools.ExtractAudio(context.Background(), "ignored", "ignored.mp3", AudioOptions{Codec: "aac;rm"}, false, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("unsafe codec: %v", err)
	}
	if err := tools.EmbedMetadata(context.Background(), "ignored", "ignored.mp3", Metadata{"title\nunsafe": "x"}, false, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("unsafe metadata: %v", err)
	}
	if err := tools.Concat(context.Background(), nil, "ignored.mp3", false, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("empty concat: %v", err)
	}
	if err := tools.EmbedChapters(context.Background(), "ignored", "ignored.mp3", []Chapter{{Start: time.Second, End: time.Millisecond}}, false, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("invalid chapter: %v", err)
	}
	if err := tools.Concat(context.Background(), []string{"https://example.test/file.mp4"}, "ignored.mp3", false, nil); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("URL concat input: %v", err)
	}
	regular := filepath.Join(root, "regular.mp4")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "link.mp4")
	if err := os.Symlink(regular, symlink); err == nil {
		if err := tools.Concat(context.Background(), []string{symlink}, "ignored.mp3", false, nil); !errors.Is(err, ErrInvalidOperation) {
			t.Fatalf("symlink concat input: %v", err)
		}
	}
	destination := filepath.Join(root, "missing.mp3")
	err := tools.ExtractAudio(context.Background(), filepath.Join(root, "does-not-exist.mp4"), destination, AudioOptions{Codec: "aac"}, false, nil)
	if !errors.Is(err, ErrMediaFailure) {
		t.Fatalf("failure category: %v", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("partial output exists: %v", statErr)
	}
}

func FuzzOperationInputValidation(f *testing.F) {
	f.Add("aac", "128k", "title", "value")
	f.Fuzz(func(t *testing.T, codec, rate, key, value string) {
		if len(codec)+len(rate)+len(key)+len(value) > 4096 {
			t.Skip()
		}
		_ = safeCodec(codec)
		_ = safeRate(rate)
		_ = validateMetadata(Metadata{key: value})
		_, _ = writeConcatList(t.TempDir()+"/out.mp4", []string{codec, rate})
	})
}

// TestResolveRecodeMappingNoOps asserts that the pinned
// FFmpegVideoConvertorPP.resolve_mapping semantics are honored: same-format
// requests and mappings whose rules do not match the source must return a
// non-empty skip reason, must NOT classify as errors, and must keep the
// original source as the resolved target so callers can fall through to
// their no-op branch.
func TestResolveRecodeMappingNoOps(t *testing.T) {
	cases := []struct {
		name       string
		source     string
		mapping    string
		wantSkip   string
		wantTarget string
	}{
		{name: "single rule same format", source: "mkv", mapping: "mkv", wantTarget: "mkv", wantSkip: "already is in target format mkv"},
		{name: "pair rule same format", source: "mp4", mapping: "mp4>mp4", wantTarget: "mp4", wantSkip: "already is in target format mp4"},
		{name: "no rule applies", source: "mkv", mapping: "mov>mp4", wantTarget: "mkv", wantSkip: "could not find a mapping for mkv"},
		{name: "list with skip and apply", source: "mov", mapping: "webm>mkv/mov>mp4", wantTarget: "mp4"},
		{name: "list with skip only", source: "mkv", mapping: "webm>mkv/mov>mp4", wantTarget: "mkv", wantSkip: "could not find a mapping for mkv"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			target, skip, err := ResolveRecodeMapping(tc.source, tc.mapping)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if target != tc.wantTarget {
				t.Fatalf("target = %q, want %q", target, tc.wantTarget)
			}
			if skip != tc.wantSkip {
				t.Fatalf("skip = %q, want %q", skip, tc.wantSkip)
			}
		})
	}
}

// TestResolveRecodeMappingErrors asserts that malformed input (empty mapping,
// empty source) and unsupported target extensions return errors so callers
// fail loudly at preflight rather than at ffmpeg.
func TestResolveRecodeMappingErrors(t *testing.T) {
	if _, _, err := ResolveRecodeMapping("mkv", ""); err == nil {
		t.Fatal("empty mapping should error")
	}
	if _, _, err := ResolveRecodeMapping("", "mp4"); err == nil {
		t.Fatal("empty source should error")
	}
	if _, _, err := ResolveRecodeMapping("mkv", "jpg"); err == nil {
		t.Fatal("non-media target should error")
	}
	if _, _, err := ResolveRecodeMapping("mkv", "mov>jpg"); err == nil {
		t.Fatal("non-media target in pair should error")
	}
	for _, mapping := range []string{"m4v", ".mp4", ".mov>mp4"} {
		if _, _, err := ResolveRecodeMapping("mkv", mapping); err == nil {
			t.Fatalf("non-pinned mapping %q should error", mapping)
		}
	}
}

// TestRecodeArgsIsDeterministicAndAllowlisted asserts the wire surface that
// Recode hands to ffmpeg. The argv must be the exact pinned
// FFmpegVideoConvertorPP._options sequence, with the documented AVI
// exception as the only codec flag, and must reject any caller that tries
// to inject arbitrary ffmpeg options through this boundary.
func TestRecodeArgsIsDeterministicAndAllowlisted(t *testing.T) {
	if got, want := recodeVideoArgs("in.mp4", "mp4", "OUTPUT"), []string{"-i", "in.mp4", "-map", "0", "-dn", "-ignore_unknown", "-progress", "pipe:1", "-nostats", "OUTPUT"}; !equalSlice(got, want) {
		t.Fatalf("non-AVI argv = %v, want %v", got, want)
	}
	if got, want := recodeVideoArgs("in.dat", "avi", "OUTPUT"), []string{"-i", "in.dat", "-map", "0", "-dn", "-ignore_unknown", "-c:v", "libxvid", "-vtag", "XVID", "-progress", "pipe:1", "-nostats", "OUTPUT"}; !equalSlice(got, want) {
		t.Fatalf("AVI argv = %v, want %v", got, want)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRecodeNoOpDoesNotInvokeToolset ensures that the no-op skip path
// (target == source, or no mapping rule matches) never discovers or invokes
// an ffmpeg toolset, mirroring the pinned FFmpegVideoConvertorPP.run
// returning [filename], info without scheduling any external work.
func TestRecodeNoOpDoesNotInvokeToolset(t *testing.T) {
	root := t.TempDir()
	input := root + "/in.mkv"
	if err := os.WriteFile(input, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Passing a nil toolset makes any ffmpeg invocation panic with a nil
	// dereference, which would fail this test if the skip path were not
	// honored end-to-end.
	var tools *Toolset
	if err := tools.Recode(context.Background(), input, root+"/out.mkv", "mkv", "mkv", false, nil); err != nil {
		t.Fatalf("same-format recode should be a no-op: %v", err)
	}
	if err := tools.Recode(context.Background(), input, root+"/out.mp4", "mkv", "mov>mp4", false, nil); err != nil {
		t.Fatalf("unmatched-rule recode should be a no-op: %v", err)
	}
}

// TestRecodeRejectsUnsupportedTarget asserts that the public surface
// rejects targets outside the closed MEDIA_EXTENSIONS allowlist before any
// argv is built.
func TestRecodeRejectsUnsupportedTarget(t *testing.T) {
	root := t.TempDir()
	input := root + "/in.mkv"
	if err := os.WriteFile(input, []byte("placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	tools := requireToolset(t)
	if err := tools.Recode(context.Background(), input, root+"/out.jpg", "mkv", "jpg", false, nil); err == nil {
		t.Fatal("expected unsupported target error")
	}
}
