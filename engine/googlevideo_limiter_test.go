package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGoogleVideoTransferLimiterSerializesAndPreservesCancellation(t *testing.T) {
	client := NewClient(Composition{})
	release, err := client.acquireGoogleVideoTransfer(context.Background(), "https://rr1---sn.example.googlevideo.com/videoplayback")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := client.acquireGoogleVideoTransfer(ctx, "https://rr2---sn.example.googlevideo.com/videoplayback"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued acquisition error = %v", err)
	}

	release()
	releaseNext, err := client.acquireGoogleVideoTransfer(context.Background(), "https://rr2---sn.example.googlevideo.com/videoplayback")
	if err != nil {
		t.Fatal(err)
	}
	releaseNext()
}

func TestGoogleVideoTransferLimiterDoesNotSerializeOtherHosts(t *testing.T) {
	client := NewClient(Composition{})
	releaseFirst, err := client.acquireGoogleVideoTransfer(context.Background(), "https://media.example/first")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFirst()
	releaseSecond, err := client.acquireGoogleVideoTransfer(context.Background(), "https://media.example/second")
	if err != nil {
		t.Fatal(err)
	}
	releaseSecond()
}
