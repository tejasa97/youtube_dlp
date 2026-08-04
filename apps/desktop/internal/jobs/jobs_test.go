package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/pkg/ytdlp"
)

func TestHumanErrorYouTubeChallengeTimeout(t *testing.T) {
	typed := &ytdlp.Error{
		Category: ytdlp.ErrorUnsupported,
		Op:       "youtube extraction",
		Err: errors.New(
			"JavaScript challenge solver unavailable: EJS helper timeout: JavaScript execution timed out",
		),
	}
	err := fmt.Errorf("download video: %w", typed)

	if got := humanError(err); got != "YouTube challenge timed out — retry" {
		t.Fatalf("humanError() = %q; want challenge-timeout message", got)
	}
}

func TestHumanErrorOtherUnsupportedIsUnchanged(t *testing.T) {
	err := &ytdlp.Error{
		Category: ytdlp.ErrorUnsupported,
		Op:       "youtube extraction",
		Err:      errors.New("video unavailable"),
	}

	if got := humanError(err); got != "This link is not supported" {
		t.Fatalf("humanError() = %q; want ordinary unsupported message", got)
	}
}

func TestHandleEventUsesPublicDownloadEventKinds(t *testing.T) {
	manager := New(nil, nil)
	state := &jobState{snap: JobSnapshot{Status: StatusActive}}

	manager.handleEvent(state, ytdlp.Event{
		Kind:  ytdlp.EventDownloadProgress,
		Bytes: 25,
		Total: 100,
	})

	if state.snap.Bytes != 25 || state.snap.Total != 100 {
		t.Fatalf("progress bytes = %d/%d; want 25/100", state.snap.Bytes, state.snap.Total)
	}
	if math.Abs(state.snap.Progress-0.25) > 0.0001 {
		t.Fatalf("progress = %f; want 0.25", state.snap.Progress)
	}
	if state.snap.Message != "Downloading" {
		t.Fatalf("message = %q; want Downloading", state.snap.Message)
	}

	state.startBps = time.Now().Add(-time.Second)
	state.startByt = 25
	manager.handleEvent(state, ytdlp.Event{
		Kind:  ytdlp.EventDownloadProgress,
		Bytes: 75,
		Total: 100,
	})
	if state.snap.SpeedBps <= 0 || state.snap.ETASeconds <= 0 {
		t.Fatalf("speed/eta = %f/%f; want positive rolling estimates", state.snap.SpeedBps, state.snap.ETASeconds)
	}
}

func TestHandleEventMapsLifecycleCopy(t *testing.T) {
	manager := New(nil, nil)
	state := &jobState{snap: JobSnapshot{Status: StatusActive}}

	tests := []struct {
		kind string
		want string
	}{
		{ytdlp.EventDownloadStarting, "Starting download"},
		{ytdlp.EventDownloadRetry, "Retrying"},
		{ytdlp.EventPostprocessStarting, "Finalising"},
		{ytdlp.EventDownloadCancelled, "Canceled"},
	}
	for _, test := range tests {
		manager.handleEvent(state, ytdlp.Event{Kind: test.kind})
		if state.snap.Message != test.want {
			t.Fatalf("kind %q message = %q; want %q", test.kind, state.snap.Message, test.want)
		}
	}
}

func TestCancelActiveKeepsFIFOSingleActive(t *testing.T) {
	manager := New(nil, nil)
	started := make(chan ytdlp.Request, 2)
	release := make(chan struct{})
	manager.runDownload = func(ctx context.Context, req ytdlp.Request, _ ytdlp.EventHandler) (ytdlp.Result, error) {
		started <- req
		<-ctx.Done()
		<-release
		return ytdlp.Result{}, ctx.Err()
	}

	first, err := manager.Submit(Request{URL: "https://example.invalid/first", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first job did not start")
	}
	second, err := manager.Submit(Request{URL: "https://example.invalid/second", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	manager.Cancel(first)
	select {
	case <-started:
		t.Fatal("next FIFO job started before canceled worker exited")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("next FIFO job did not start after canceled worker exited")
	}
	manager.Cancel(second)
}

func TestDownloadRequestUsesConfiguredFFmpegLocation(t *testing.T) {
	manager := New(nil, nil)
	configured := filepath.Join(t.TempDir(), "ffmpeg")
	manager.SetFFmpegLocation(configured)
	started := make(chan ytdlp.Request, 1)
	manager.runDownload = func(ctx context.Context, req ytdlp.Request, _ ytdlp.EventHandler) (ytdlp.Result, error) {
		started <- req
		<-ctx.Done()
		return ytdlp.Result{}, ctx.Err()
	}

	id, err := manager.Submit(Request{URL: "https://example.invalid/video", OutputDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-started:
		if req.Filesystem.FfmpegLocation != configured {
			t.Fatalf("ffmpeg location = %q; want %q", req.Filesystem.FfmpegLocation, configured)
		}
	case <-time.After(time.Second):
		t.Fatal("download runner did not receive a request")
	}
	manager.Cancel(id)
}

func TestDownloadRequestsUseExactV0SelectorsAndDistinctOutputTemplates(t *testing.T) {
	tests := []struct {
		quality  Quality
		selector string
		template string
	}{
		{QualityBest, "bv*+ba/b", "%(title)s [%(id)s] [Best].%(ext)s"},
		{Quality4K, "bv*[height<=2160]+ba/b[height<=2160]", "%(title)s [%(id)s] [4K].%(ext)s"},
		{Quality1440p, "bv*[height<=1440]+ba/b[height<=1440]", "%(title)s [%(id)s] [1440p].%(ext)s"},
		{Quality1080p, "bv*[height<=1080]+ba/b[height<=1080]", "%(title)s [%(id)s] [1080p].%(ext)s"},
		{Quality720p, "bv*[height<=720]+ba/b[height<=720]", "%(title)s [%(id)s] [720p].%(ext)s"},
		{QualityAudioOnly, "ba/b", "%(title)s [%(id)s] [Audio only].%(ext)s"},
	}

	for _, test := range tests {
		t.Run(test.quality.Label(), func(t *testing.T) {
			manager := New(nil, nil)
			started := make(chan ytdlp.Request, 1)
			manager.runDownload = func(_ context.Context, req ytdlp.Request, _ ytdlp.EventHandler) (ytdlp.Result, error) {
				started <- req
				return ytdlp.Result{}, nil
			}

			if _, err := manager.Submit(Request{URL: "https://example.invalid/video", OutputDir: t.TempDir(), Quality: test.quality}); err != nil {
				t.Fatal(err)
			}
			select {
			case req := <-started:
				if req.Format != test.selector {
					t.Fatalf("format = %q; want %q", req.Format, test.selector)
				}
				if req.OutputTemplate != test.template {
					t.Fatalf("output template = %q; want %q", req.OutputTemplate, test.template)
				}
			case <-time.After(time.Second):
				t.Fatal("download runner did not receive a request")
			}
		})
	}

	if QualityBest.outputTemplate() == QualityAudioOnly.outputTemplate() {
		t.Fatal("Best and Audio only must not resolve to the same output template")
	}
}

func TestSubmitRejectsUnknownQuality(t *testing.T) {
	manager := New(nil, nil)
	if _, err := manager.Submit(Request{
		URL: "https://example.invalid/video", OutputDir: t.TempDir(), Quality: Quality("8k"),
	}); err == nil || !strings.Contains(err.Error(), "unsupported quality") {
		t.Fatalf("Submit() error = %v; want unsupported quality", err)
	}
}

func TestEventsAreDeliveredInEmissionOrder(t *testing.T) {
	received := make(chan string, 3)
	manager := New(nil, func(event Event) { received <- event.Job.ID })

	manager.mu.Lock()
	manager.emitLocked(Event{Name: EventJobUpdate, Job: JobSnapshot{ID: "first"}})
	manager.emitLocked(Event{Name: EventJobUpdate, Job: JobSnapshot{ID: "second"}})
	manager.emitLocked(Event{Name: EventJobUpdate, Job: JobSnapshot{ID: "third"}})
	manager.mu.Unlock()

	for _, want := range []string{"first", "second", "third"} {
		select {
		case got := <-received:
			if got != want {
				t.Fatalf("event = %q; want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %q", want)
		}
	}
}

func TestRemoveDoesNotDropActiveOrPendingJobs(t *testing.T) {
	manager := New(nil, nil)
	manager.all["active"] = &jobState{snap: JobSnapshot{ID: "active", Status: StatusActive}}
	manager.all["pending"] = &jobState{snap: JobSnapshot{ID: "pending", Status: StatusPending}}

	manager.Remove("active")
	manager.Remove("pending")

	if _, ok := manager.Find("active"); !ok {
		t.Fatal("Remove dropped an active job")
	}
	if _, ok := manager.Find("pending"); !ok {
		t.Fatal("Remove dropped a pending job")
	}
}

func TestClearTerminalRemovesCanceledJobAndEmitsEmptyQueue(t *testing.T) {
	events := make(chan Event, 1)
	manager := New(nil, func(event Event) { events <- event })
	manager.all["canceled-job"] = &jobState{snap: JobSnapshot{ID: "canceled-job", Status: StatusCanceled}}
	manager.ClearTerminal()
	if got := manager.List(); len(got) != 0 {
		t.Fatalf("jobs after ClearTerminal = %#v; want empty", got)
	}
	select {
	case event := <-events:
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if event.Name != EventQueue || !strings.Contains(string(encoded), `"queue":[]`) {
			t.Fatalf("event = %s; want empty queue", encoded)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for queue update")
	}
}

func TestCancelActiveTransitionsToCanceledThenClearTerminalRemovesIt(t *testing.T) {
	manager := New(nil, nil)
	started := make(chan struct{}, 1)
	manager.runDownload = func(ctx context.Context, _ ytdlp.Request, _ ytdlp.EventHandler) (ytdlp.Result, error) {
		started <- struct{}{}
		<-ctx.Done()
		return ytdlp.Result{}, ctx.Err()
	}
	id, err := manager.Submit(Request{URL: "https://example.invalid/active", OutputDir: t.TempDir(), Quality: Quality4K})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("active download did not start")
	}
	manager.mu.Lock()
	done := manager.all[id].done
	manager.mu.Unlock()
	manager.Cancel(id)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled download worker did not exit")
	}
	job, ok := manager.Find(id)
	if !ok {
		t.Fatal("canceled job disappeared before ClearTerminal")
	}
	if job.Status != StatusCanceled {
		t.Fatalf("status after Cancel = %q; want %q", job.Status, StatusCanceled)
	}
	manager.ClearTerminal()
	if _, ok := manager.Find(id); ok {
		t.Fatal("canceled job remains after ClearTerminal")
	}
}
