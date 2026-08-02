package ytdlp

import (
	"context"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
)

func TestFixupPolicyValidationAndTypedDetection(t *testing.T) {
	for _, policy := range []string{"", "never", "ignore", "warn", "detect_or_warn", "force"} {
		if err := validateRequestOptions(Request{FixupPolicy: policy}); err != nil {
			t.Fatalf("policy %q: %v", policy, err)
		}
	}
	if err := validateRequestOptions(Request{FixupPolicy: "arbitrary"}); err == nil {
		t.Fatal("arbitrary fixup policy accepted")
	}
	if got := detectFixupKind("video.ts", ffmpeg.Probe{Format: ffmpeg.Format{FormatName: "mpegts"}}); got != ffmpeg.FixupMPEGTS {
		t.Fatalf("mpegts fixup=%q", got)
	}
	if got := detectFixupKind("audio.m4a", ffmpeg.Probe{Streams: []ffmpeg.Stream{{CodecType: "audio", CodecName: "aac"}}}); got != ffmpeg.FixupM4AAudio {
		t.Fatalf("m4a fixup=%q", got)
	}
	if got := detectFixupKind("video.mp4", ffmpeg.Probe{}); got != ffmpeg.FixupNone {
		t.Fatalf("unexpected fixup=%q", got)
	}
}

func TestFixupNeverDoesNotDiscoverOrMutate(t *testing.T) {
	operation := &operation{request: Request{FixupPolicy: FixupNever}}
	path, err := operation.applyFixupPolicy(context.Background(), "missing.mp4", nil)
	if err != nil || path != "missing.mp4" {
		t.Fatalf("path=%q err=%v", path, err)
	}
}
