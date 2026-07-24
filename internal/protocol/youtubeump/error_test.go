package youtubeump

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRequestFailurePreservesCause(t *testing.T) {
	root := context.Canceled
	err := requestFailure(root, "https://rr1---sn-fixture.googlevideo.com/videoplayback?sig=secret")
	if !errors.Is(err, ErrDownloadFailed) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if containsSecret(err.Error()) {
		t.Fatalf("leaked secret: %v", err)
	}
}

func TestRedirectFailureRedactsURL(t *testing.T) {
	err := redirectFailure("https://rr1---sn-fixture.googlevideo.com/videoplayback?sig=secret")
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("err=%v", err)
	}
	if containsSecret(err.Error()) {
		t.Fatalf("leaked secret: %v", err)
	}
}

func containsSecret(message string) bool {
	return strings.Contains(message, "secret") || strings.Contains(message, "sig=secret")
}

func TestConsumeStreamRejectsActiveHeadersAtEOF(t *testing.T) {
	body := encodePart(PartMediaHeader, marshalMediaHeader(MediaHeader{HeaderID: 1, Itag: 137, ContentLength: 3}))
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, nil, 1024)
	reader := NewReader(newByteReader(body), int64(len(body)))
	for {
		part, ok, err := reader.ReadPart()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		if err := assembler.consumePart(part); err != nil {
			t.Fatal(err)
		}
	}
	if err := assembler.finishResponse(); !errors.Is(err, ErrTruncatedStream) {
		t.Fatalf("err=%v", err)
	}
}

func TestEndOfTrackPartRejected(t *testing.T) {
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, nil, 1024)
	err := assembler.consumePart(Part{Type: PartEndOfTrack, Payload: nil})
	if !errors.Is(err, ErrUnsupportedDirective) {
		t.Fatalf("err=%v", err)
	}
}
