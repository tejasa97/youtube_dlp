package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/testserver"
)

func TestIsCategory(t *testing.T) {
	err := &Error{Category: ErrorNetwork, Op: "fetch", Err: errors.New("offline")}
	if !IsCategory(err, ErrorNetwork) {
		t.Fatal("IsCategory() = false, want true")
	}
	if IsCategory(err, ErrorInvalidInput) {
		t.Fatal("IsCategory() matched the wrong category")
	}
	if !errors.Is(err, err.Err) {
		t.Fatal("Error does not unwrap its cause")
	}
}

func TestClientCancellationReachesTransport(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := NewClient().Run(ctx, Request{URL: server.URL + "/slow?delay=1s", SkipDownload: true})
	if !IsCategory(err, ErrorCancelled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestClientWalkingSkeleton(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	var events []Event
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	}))
	result, err := client.Run(context.Background(), Request{URL: server.URL + "/page", OutputDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || result.Extractor != "fixture" {
		t.Fatalf("result = %#v", result)
	}
	if !json.Valid(result.InfoJSON) {
		t.Fatalf("invalid metadata JSON: %s", result.InfoJSON)
	}
	downloaded, err := os.ReadFile(result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(downloaded) != string(server.Media()) {
		t.Fatal("downloaded media mismatch")
	}
	if len(events) < 4 || events[0].Kind != "extracting" || events[len(events)-1].Kind != "download_completed" {
		t.Fatalf("events = %#v", events)
	}
}

func TestClientConcurrentOperationsDoNotShareState(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	client := NewClient()
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := client.Run(context.Background(), Request{
				URL: server.URL + "/page", OutputDir: filepath.Join(t.TempDir(), "operation"),
			})
			errorsSeen <- err
		}(index)
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
}
