package ffmpeg

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/format"
)

func TestBuildMergeArgumentsOrdering(t *testing.T) {
	tests := []struct {
		name   string
		inputs []MergeInput
		want   []string
	}{
		{
			name: "one audio one video",
			inputs: []MergeInput{
				{Path: "/v.mp4", HasVideo: true},
				{Path: "/a.m4a", HasAudio: true},
			},
			want: []string{
				"-i", "/v.mp4", "-i", "/a.m4a",
				"-map", "0:v:0", "-map", "1:a:0",
				"-c", "copy", "-progress", "pipe:1", "-nostats", "/tmp/out.mkv",
			},
		},
		{
			name: "video then audio in planner order",
			inputs: []MergeInput{
				{Path: "/first.mp4", HasVideo: true},
				{Path: "/second.m4a", HasAudio: true},
			},
			want: []string{
				"-i", "/first.mp4", "-i", "/second.m4a",
				"-map", "0:v:0", "-map", "1:a:0",
				"-c", "copy", "-progress", "pipe:1", "-nostats", "/tmp/out.mkv",
			},
		},
		{
			name: "audio then video in planner order",
			inputs: []MergeInput{
				{Path: "/audio.m4a", HasAudio: true},
				{Path: "/video.mp4", HasVideo: true},
			},
			want: []string{
				"-i", "/audio.m4a", "-i", "/video.mp4",
				"-map", "0:a:0", "-map", "1:v:0",
				"-c", "copy", "-progress", "pipe:1", "-nostats", "/tmp/out.mkv",
			},
		},
		{
			name: "combined audio video input",
			inputs: []MergeInput{
				{Path: "/av.mp4", HasAudio: true, HasVideo: true},
			},
			want: []string{
				"-i", "/av.mp4",
				"-map", "0:a:0", "-map", "0:v:0",
				"-c", "copy", "-progress", "pipe:1", "-nostats", "/tmp/out.mkv",
			},
		},
		{
			name: "two video three audio",
			inputs: []MergeInput{
				{Path: "/v1.mp4", HasVideo: true},
				{Path: "/v2.mp4", HasVideo: true},
				{Path: "/a1.m4a", HasAudio: true},
				{Path: "/a2.m4a", HasAudio: true},
				{Path: "/a3.m4a", HasAudio: true},
			},
			want: []string{
				"-i", "/v1.mp4", "-i", "/v2.mp4", "-i", "/a1.m4a", "-i", "/a2.m4a", "-i", "/a3.m4a",
				"-map", "0:v:0", "-map", "1:v:0", "-map", "2:a:0", "-map", "3:a:0", "-map", "4:a:0",
				"-c", "copy", "-progress", "pipe:1", "-nostats", "/tmp/out.mkv",
			},
		},
		{
			name: "mixed combined and single-stream inputs",
			inputs: []MergeInput{
				{Path: "/av.webm", HasAudio: true, HasVideo: true},
				{Path: "/extra.m4a", HasAudio: true},
				{Path: "/alt.mp4", HasVideo: true},
			},
			want: []string{
				"-i", "/av.webm", "-i", "/extra.m4a", "-i", "/alt.mp4",
				"-map", "0:a:0", "-map", "0:v:0", "-map", "1:a:0", "-map", "2:v:0",
				"-c", "copy", "-progress", "pipe:1", "-nostats", "/tmp/out.mkv",
			},
		},
		{
			name: "hls aac audio among several audio tracks",
			inputs: []MergeInput{
				{Path: "/v.mp4", HasVideo: true},
				{Path: "/hls.m4a", HasAudio: true, HLSAACFixup: true},
				{Path: "/plain.m4a", HasAudio: true, Protocol: "http"},
			},
			want: []string{
				"-i", "/v.mp4", "-i", "/hls.m4a", "-i", "/plain.m4a",
				"-map", "0:v:0", "-map", "1:a:0", "-bsf:a:0", "aac_adtstoasc", "-map", "2:a:0",
				"-c", "copy", "-progress", "pipe:1", "-nostats", "/tmp/out.mkv",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := BuildMergeArguments(test.inputs, "/tmp/out.mkv")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("args = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestBuildMergeArgumentsValidation(t *testing.T) {
	_, err := BuildMergeArguments(nil, "/tmp/out.mkv")
	if !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("empty inputs = %v", err)
	}
	_, err = BuildMergeArguments([]MergeInput{{Path: "/x", HasVideo: false, HasAudio: false}}, "/tmp/out.mkv")
	if !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("no streams = %v", err)
	}
	inputs := make([]MergeInput, format.MaxMergeTracks+1)
	for index := range inputs {
		inputs[index] = MergeInput{Path: "/x", HasVideo: true}
	}
	_, err = BuildMergeArguments(inputs, "/tmp/out.mkv")
	if !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("too many inputs = %v", err)
	}
	maxInputs := make([]MergeInput, format.MaxMergeTracks)
	for index := range maxInputs {
		maxInputs[index] = MergeInput{Path: "/x", HasVideo: true}
	}
	if _, err := BuildMergeArguments(maxInputs, "/tmp/out.mkv"); err != nil {
		t.Fatalf("max inputs = %v", err)
	}
}
