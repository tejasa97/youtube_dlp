package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func requireProbeToolset(t *testing.T) *Toolset {
	t.Helper()
	tools, err := Discover(Config{})
	if errors.Is(err, ErrFFmpegUnavailable) || errors.Is(err, ErrFFprobeUnavailable) {
		t.Skipf("ffmpeg toolchain unavailable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

func generateProbeAudio(t *testing.T, tools *Toolset, destination, codec string) {
	t.Helper()
	args := []string{"-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=1000:duration=0.2", "-vn"}
	switch codec {
	case "aac":
		args = append(args, "-c:a", "aac")
	case "opus":
		args = append(args, "-c:a", "libopus")
	default:
		t.Fatalf("unsupported probe codec %q", codec)
	}
	args = append(args, destination)
	if _, err := tools.execute(context.Background(), tools.ffmpeg, args, nil); err != nil {
		t.Fatalf("generate %s audio: %v", codec, err)
	}
}

func generateProbeVideo(t *testing.T, tools *Toolset, destination string) {
	t.Helper()
	if _, err := tools.execute(context.Background(), tools.ffmpeg, []string{
		"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=black:s=16x16:d=0.2",
		"-an", "-c:v", "mpeg4", "-q:v", "5", destination,
	}, nil); err != nil {
		t.Fatalf("generate video: %v", err)
	}
}

func TestPrepareMergeInputsHLSAACFixupFromProbe(t *testing.T) {
	tools := requireProbeToolset(t)
	root := t.TempDir()
	video := filepath.Join(root, "video.mp4")
	hlsAAC := filepath.Join(root, "hls-aac.m4a")
	hlsOpus := filepath.Join(root, "hls-opus.webm")
	httpAAC := filepath.Join(root, "http-aac.m4a")
	generateProbeVideo(t, tools, video)
	generateProbeAudio(t, tools, hlsAAC, "aac")
	generateProbeAudio(t, tools, hlsOpus, "opus")
	generateProbeAudio(t, tools, httpAAC, "aac")

	tests := []struct {
		name  string
		input MergeInput
		want  bool
	}{
		{
			name:  "hls empty metadata actual aac",
			input: MergeInput{Path: hlsAAC, HasAudio: true, Protocol: "m3u8_native"},
			want:  true,
		},
		{
			name:  "hls metadata aac actual opus",
			input: MergeInput{Path: hlsOpus, HasAudio: true, Protocol: "m3u8"},
			want:  false,
		},
		{
			name:  "non-hls actual aac",
			input: MergeInput{Path: httpAAC, HasAudio: true, Protocol: "http"},
			want:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := tools.prepareMergeInputs(context.Background(), []MergeInput{test.input})
			if err != nil {
				t.Fatal(err)
			}
			if prepared[0].HLSAACFixup != test.want {
				t.Fatalf("HLSAACFixup = %t, want %t", prepared[0].HLSAACFixup, test.want)
			}
		})
	}
}

func TestPrepareMergeInputsHLSCorrectAudioOrdinal(t *testing.T) {
	tools := requireProbeToolset(t)
	root := t.TempDir()
	video := filepath.Join(root, "video.mp4")
	plain := filepath.Join(root, "plain.m4a")
	hls := filepath.Join(root, "hls.m4a")
	generateProbeVideo(t, tools, video)
	generateProbeAudio(t, tools, plain, "aac")
	generateProbeAudio(t, tools, hls, "aac")

	inputs := []MergeInput{
		{Path: video, HasVideo: true},
		{Path: plain, HasAudio: true, Protocol: "http"},
		{Path: hls, HasAudio: true, Protocol: "m3u8_native"},
	}
	prepared, err := tools.prepareMergeInputs(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	args, err := BuildMergeArguments(prepared, "/tmp/out.mkv")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-i", video, "-i", plain, "-i", hls,
		"-map", "0:v:0", "-map", "1:a:0", "-map", "2:a:0", "-bsf:a:1", "aac_adtstoasc",
		"-c", "copy", "-progress", "pipe:1", "-nostats", "/tmp/out.mkv",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestPrepareMergeInputsProbeCancellation(t *testing.T) {
	tools := requireProbeToolset(t)
	root := t.TempDir()
	audio := filepath.Join(root, "hls.m4a")
	generateProbeAudio(t, tools, audio, "aac")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tools.prepareMergeInputs(ctx, []MergeInput{
		{Path: audio, HasAudio: true, Protocol: "m3u8"},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestPrepareMergeInputsProbeFailure(t *testing.T) {
	tools := requireProbeToolset(t)
	_, err := tools.prepareMergeInputs(context.Background(), []MergeInput{
		{Path: filepath.Join(t.TempDir(), "missing.m4a"), HasAudio: true, Protocol: "m3u8"},
	})
	if !errors.Is(err, ErrMediaFailure) {
		t.Fatalf("error = %v, want ErrMediaFailure", err)
	}
}

func TestMergeTracksHLSAACFixupIntegration(t *testing.T) {
	tools := requireProbeToolset(t)
	root := t.TempDir()
	video := filepath.Join(root, "video.mp4")
	audio := filepath.Join(root, "audio.m4a")
	destination := filepath.Join(root, "merged.mp4")
	generateProbeVideo(t, tools, video)
	generateProbeAudio(t, tools, audio, "aac")

	if err := tools.MergeTracks(context.Background(), []MergeInput{
		{Path: video, HasVideo: true},
		{Path: audio, HasAudio: true, Protocol: "m3u8_native"},
	}, destination, true, nil); err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(probe.Streams) != 2 {
		t.Fatalf("streams = %#v", probe.Streams)
	}
	if info, err := os.Stat(destination); err != nil || info.Size() == 0 {
		t.Fatalf("destination = %v, err = %v", destination, err)
	}
}
