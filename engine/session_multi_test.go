package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tejasa97/ytdlp-go/engine/value"
	"github.com/tejasa97/ytdlp-go/internal/events"
	mediaformat "github.com/tejasa97/ytdlp-go/internal/format"
	"github.com/tejasa97/ytdlp-go/internal/media/ffmpeg"
	"github.com/tejasa97/ytdlp-go/internal/session"
)

type multiSessionFixture struct {
	mu     sync.Mutex
	media  map[string][]byte
	etags  map[string]string
	calls  map[string]int
	ranges map[string]int
	zeros  map[string]int
}

func (fixture *multiSessionFixture) handler(writer http.ResponseWriter, request *http.Request) {
	fixture.mu.Lock()
	body, ok := fixture.media[request.URL.Path]
	etag := fixture.etags[request.URL.Path]
	fixture.calls[request.URL.Path]++
	if request.Header.Get("Range") == "" {
		fixture.zeros[request.URL.Path]++
	} else {
		fixture.ranges[request.URL.Path]++
	}
	fixture.mu.Unlock()
	if !ok {
		http.NotFound(writer, request)
		return
	}
	if etag != "" {
		writer.Header().Set("ETag", etag)
	}
	http.ServeContent(writer, request, filepath.Base(request.URL.Path), time.Unix(0, 0), bytes.NewReader(body))
}

func (fixture *multiSessionFixture) counts(path string) (calls, ranges, zeros int) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.calls[path], fixture.ranges[path], fixture.zeros[path]
}

func newMultiSessionMediaFixture(t *testing.T) (*multiSessionFixture, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	videoPath := filepath.Join(root, "video.mp4")
	audioPath := filepath.Join(root, "audio.m4a")
	videoOutput, err := exec.Command("ffmpeg", "-nostdin", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=24", "-t", "8", "-an", "-c:v", "mpeg4", "-q:v", "2", videoPath).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg unavailable for session fixture: %v: %s", err, videoOutput)
	}
	audioOutput, err := exec.Command("ffmpeg", "-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=1000:duration=8", "-vn", "-c:a", "aac", "-b:a", "256k", audioPath).CombinedOutput()
	if err != nil {
		t.Skipf("ffmpeg unavailable for session fixture: %v: %s", err, audioOutput)
	}
	video, err := os.ReadFile(videoPath)
	if err != nil {
		t.Fatal(err)
	}
	audio, err := os.ReadFile(audioPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &multiSessionFixture{
		media: map[string][]byte{"/video": video, "/audio": audio},
		etags: map[string]string{"/video": `"video-v1"`, "/audio": `"audio-v1"`},
		calls: make(map[string]int), ranges: make(map[string]int), zeros: make(map[string]int),
	}
	return fixture, httptest.NewServer(http.HandlerFunc(fixture.handler))
}

func writeMultiSessionInfo(t *testing.T, path, baseURL, videoToken, audioToken string) {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"id": "multi-session-fixture", "title": "multi session fixture", "webpage_url": "https://fixture.example/watch",
		"ext": "mp4", "formats": []map[string]any{
			{"format_id": "video", "url": baseURL + "/video?token=" + videoToken, "ext": "mp4", "protocol": "http", "vcodec": "mpeg4", "acodec": "none", "height": 240},
			{"format_id": "audio", "url": baseURL + "/audio?token=" + audioToken, "ext": "m4a", "protocol": "http", "vcodec": "none", "acodec": "aac", "abr": 256},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func multiSessionRequest(root, infoPath, sessionID string, arbiter *PublicationArbiter, basename string) Request {
	return Request{
		LoadInfoJSON: infoPath, OutputDir: root, OutputTemplate: "output.%(ext)s", Format: "video+audio",
		Filesystem: FilesystemOptions{Resume: ResumeOptions{
			SessionID: sessionID, PublicationArbiter: arbiter,
			CommitTargets: []CommitTarget{{Kind: ArtifactKindPrimary, Identity: "primary", Basename: basename}},
		}},
	}
}

func multiSessionAudioProgress(event Event) bool {
	return event.Kind == EventDownloadProgress && filepath.Base(event.Path) == "audio" && event.Bytes >= 64<<10
}

func TestMultiTrackSessionPauseRestartRotatesTokensAndReusesCompletedTracks(t *testing.T) {
	fixture, server := newMultiSessionMediaFixture(t)
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeMultiSessionInfo(t, infoPath, server.URL, "one", "one")
	const sessionID = "11111111111111111111111111111111"

	ctx, cancel := context.WithCancelCause(context.Background())
	var paused atomic.Bool
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		if multiSessionAudioProgress(event) && paused.CompareAndSwap(false, true) {
			cancel(ErrPauseRequested)
		}
		return nil
	}))
	pausedResult, pausedErr := client.Run(ctx, multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	if pausedErr == nil || !errors.Is(pausedErr, ErrPauseRequested) || pausedResult.Session.Disposition != SessionRetained {
		t.Fatalf("pause err=%v result=%#v", pausedErr, pausedResult)
	}
	videoCalls, _, _ := fixture.counts("/video")
	audioCalls, _, _ := fixture.counts("/audio")
	rootRef, err := ValidateOutputRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := InspectResumeState(context.Background(), rootRef, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Components) != 2 || summary.Status != SessionStatus("paused") {
		t.Fatalf("paused summary=%#v", summary)
	}
	writeMultiSessionInfo(t, infoPath, server.URL, "rotated-video", "rotated-audio")
	resumed, resumeErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	if resumeErr != nil || !resumed.Downloaded || resumed.Session.Disposition != SessionPublished {
		t.Fatalf("resume err=%v result=%#v", resumeErr, resumed)
	}
	videoCallsAfter, videoRanges, _ := fixture.counts("/video")
	audioCallsAfter, audioRanges, _ := fixture.counts("/audio")
	if videoCallsAfter < videoCalls || audioCallsAfter < audioCalls || videoRanges+audioRanges == 0 {
		t.Fatalf("resume did not use independent range checkpoints: video %d->%d ranges=%d audio %d->%d ranges=%d", videoCalls, videoCallsAfter, videoRanges, audioCalls, audioCallsAfter, audioRanges)
	}
	if _, err := os.Stat(filepath.Join(root, "output.mp4")); err != nil {
		t.Fatal(err)
	}

	callsAfterPublishVideo, _, _ := fixture.counts("/video")
	callsAfterPublishAudio, _, _ := fixture.counts("/audio")
	writeMultiSessionInfo(t, infoPath, server.URL, "third-video", "third-audio")
	reused, reuseErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	if reuseErr != nil || !reused.Downloaded {
		t.Fatalf("completed reuse err=%v result=%#v", reuseErr, reused)
	}
	if got, _, _ := fixture.counts("/video"); got != callsAfterPublishVideo {
		t.Fatalf("completed video track retransferred: %d -> %d", callsAfterPublishVideo, got)
	}
	if got, _, _ := fixture.counts("/audio"); got != callsAfterPublishAudio {
		t.Fatalf("completed audio track retransferred: %d -> %d", callsAfterPublishAudio, got)
	}
}

func TestMultiTrackSessionChangedValidatorRestartsOnlyChangedTrack(t *testing.T) {
	fixture, server := newMultiSessionMediaFixture(t)
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeMultiSessionInfo(t, infoPath, server.URL, "before", "before")
	const sessionID = "22222222222222222222222222222222"
	ctx, cancel := context.WithCancelCause(context.Background())
	var paused atomic.Bool
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		if multiSessionAudioProgress(event) && paused.CompareAndSwap(false, true) {
			cancel(ErrPauseRequested)
		}
		return nil
	}))
	if _, err := client.Run(ctx, multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4")); err == nil || !errors.Is(err, ErrPauseRequested) {
		t.Fatalf("pause err=%v", err)
	}
	fixture.mu.Lock()
	fixture.etags["/audio"] = `"audio-v2"`
	fixture.mu.Unlock()
	writeMultiSessionInfo(t, infoPath, server.URL, "after-video", "after-audio")
	if _, err := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4")); err != nil {
		t.Fatal(err)
	}
	if _, ranges, _ := fixture.counts("/audio"); ranges == 0 {
		t.Fatal("changed audio track did not first probe its old checkpoint with Range")
	}
	if _, _, zeros := fixture.counts("/audio"); zeros < 2 {
		t.Fatalf("changed audio track was not conservatively restarted: zero requests=%d", zeros)
	}
}

func TestMultiTrackSessionAmbiguousValidatorRestartsConservatively(t *testing.T) {
	fixture, server := newMultiSessionMediaFixture(t)
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeMultiSessionInfo(t, infoPath, server.URL, "before", "before")
	const sessionID = "33333333333333333333333333333333"
	ctx, cancel := context.WithCancelCause(context.Background())
	var paused atomic.Bool
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		if multiSessionAudioProgress(event) && paused.CompareAndSwap(false, true) {
			cancel(ErrPauseRequested)
		}
		return nil
	}))
	if _, err := client.Run(ctx, multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4")); err == nil || !errors.Is(err, ErrPauseRequested) {
		t.Fatalf("pause err=%v", err)
	}
	fixture.mu.Lock()
	fixture.etags["/audio"] = ""
	fixture.mu.Unlock()
	writeMultiSessionInfo(t, infoPath, server.URL, "after-video", "without-validator")
	if _, err := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4")); err != nil {
		t.Fatal(err)
	}
	if _, ranges, zeros := fixture.counts("/audio"); ranges == 0 || zeros < 2 {
		t.Fatalf("ambiguous audio validator was not conservatively restarted: ranges=%d zeros=%d", ranges, zeros)
	}
}

func TestMultiTrackSessionProcessingPauseRestartRetainsInputs(t *testing.T) {
	fixture, server := newMultiSessionMediaFixture(t)
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeMultiSessionInfo(t, infoPath, server.URL, "one", "one")
	const sessionID = "44444444444444444444444444444444"

	originalMerge := multiSessionMergeTracks
	defer func() { multiSessionMergeTracks = originalMerge }()
	started := make(chan struct{})
	var progressOnce sync.Once
	multiSessionMergeTracks = func(ctx context.Context, tools *ffmpeg.Toolset, inputs []ffmpeg.MergeInput, destination string, sink events.Sink, workspace ffmpeg.ProcessingWorkspace) error {
		gatedSink := events.SinkFunc(func(eventCtx context.Context, event events.Event) error {
			if event.Kind == events.KindPostprocessProgress {
				progressOnce.Do(func() { close(started) })
				<-ctx.Done()
				return ctx.Err()
			}
			return sink.Emit(eventCtx, event)
		})
		return originalMerge(ctx, tools, inputs, destination, gatedSink, workspace)
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	var pausedResult Result
	var pausedErr error
	go func() {
		pausedResult, pausedErr = newBroadTestClient().Run(ctx, multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("multi-track processing did not start")
	}
	videoCalls, _, _ := fixture.counts("/video")
	audioCalls, _, _ := fixture.counts("/audio")
	cancel(ErrPauseRequested)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("paused processing did not return")
	}
	if pausedErr == nil || !errors.Is(pausedErr, ErrPauseRequested) || pausedResult.Session.Disposition != SessionRetained {
		t.Fatalf("processing pause err=%v result=%#v", pausedErr, pausedResult)
	}

	multiSessionMergeTracks = originalMerge
	resumed, resumeErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	if resumeErr != nil || !resumed.Downloaded {
		t.Fatalf("processing resume err=%v result=%#v", resumeErr, resumed)
	}
	if got, _, _ := fixture.counts("/video"); got != videoCalls {
		t.Fatalf("processing restart retransferred video: %d -> %d", videoCalls, got)
	}
	if got, _, _ := fixture.counts("/audio"); got != audioCalls {
		t.Fatalf("processing restart retransferred audio: %d -> %d", audioCalls, got)
	}
}

func TestMultiTrackSessionCollisionSupportsPublishOnlyRetry(t *testing.T) {
	fixture, server := newMultiSessionMediaFixture(t)
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeMultiSessionInfo(t, infoPath, server.URL, "one", "one")
	const sessionID = "55555555555555555555555555555555"
	if err := os.WriteFile(filepath.Join(root, "output.mp4"), []byte("user-owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	if err == nil || !errors.Is(err, ErrDestinationCollision) || result.Session.Disposition != SessionCollision {
		t.Fatalf("collision err=%v result=%#v", err, result)
	}
	data, err := os.ReadFile(filepath.Join(root, "output.mp4"))
	if err != nil || string(data) != "user-owned" {
		t.Fatalf("collision changed target: %q err=%v", data, err)
	}
	videoCalls, _, _ := fixture.counts("/video")
	audioCalls, _, _ := fixture.counts("/audio")
	if err := os.Remove(filepath.Join(root, "output.mp4")); err != nil {
		t.Fatal(err)
	}
	retry, retryErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	if retryErr != nil || !retry.Downloaded {
		t.Fatalf("publish-only retry err=%v result=%#v", retryErr, retry)
	}
	if got, _, _ := fixture.counts("/video"); got != videoCalls {
		t.Fatalf("publish-only retry retransferred video: %d -> %d", videoCalls, got)
	}
	if got, _, _ := fixture.counts("/audio"); got != audioCalls {
		t.Fatalf("publish-only retry retransferred audio: %d -> %d", audioCalls, got)
	}
}

func TestMultiTrackSessionProcessingFaultRetainsInputs(t *testing.T) {
	fixture, server := newMultiSessionMediaFixture(t)
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeMultiSessionInfo(t, infoPath, server.URL, "one", "one")
	const sessionID = "66666666666666666666666666666666"

	originalMerge := multiSessionMergeTracks
	multiSessionMergeTracks = func(context.Context, *ffmpeg.Toolset, []ffmpeg.MergeInput, string, events.Sink, ffmpeg.ProcessingWorkspace) error {
		return errors.New("injected multi-track processing fault")
	}
	result, runErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	multiSessionMergeTracks = originalMerge
	if runErr == nil || result.Downloaded || result.Session.Disposition != SessionRetained {
		t.Fatalf("processing fault err=%v result=%#v", runErr, result)
	}
	videoCalls, _, _ := fixture.counts("/video")
	audioCalls, _, _ := fixture.counts("/audio")
	resumed, resumeErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	if resumeErr != nil || !resumed.Downloaded {
		t.Fatalf("processing fault retry err=%v result=%#v", resumeErr, resumed)
	}
	if got, _, _ := fixture.counts("/video"); got != videoCalls {
		t.Fatalf("processing retry retransferred video: %d -> %d", videoCalls, got)
	}
	if got, _, _ := fixture.counts("/audio"); got != audioCalls {
		t.Fatalf("processing retry retransferred audio: %d -> %d", audioCalls, got)
	}
}

func TestMultiTrackSessionPrePublicationFaultRetriesWithoutTransfer(t *testing.T) {
	fixture, server := newMultiSessionMediaFixture(t)
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeMultiSessionInfo(t, infoPath, server.URL, "one", "one")
	const sessionID = "77777777777777777777777777777777"

	originalJournalWrite := multiSessionJournalWrite
	multiSessionJournalWrite = func(path string, journal directPublicationJournal) error {
		if journal.State == directJournalPublishing {
			return errors.New("injected multi-track pre-publication journal fault")
		}
		return originalJournalWrite(path, journal)
	}
	result, runErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	multiSessionJournalWrite = originalJournalWrite
	if runErr == nil || result.Downloaded || result.Session.Disposition != SessionRetained || result.Session.Publication != PublicationReady {
		t.Fatalf("pre-publication fault err=%v result=%#v", runErr, result)
	}
	if _, err := os.Stat(filepath.Join(root, "output.mp4")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-publication fault changed destination: %v", err)
	}
	videoCalls, _, _ := fixture.counts("/video")
	audioCalls, _, _ := fixture.counts("/audio")
	retry, retryErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	if retryErr != nil || !retry.Downloaded {
		t.Fatalf("pre-publication retry err=%v result=%#v", retryErr, retry)
	}
	if got, _, _ := fixture.counts("/video"); got != videoCalls {
		t.Fatalf("pre-publication retry retransferred video: %d -> %d", videoCalls, got)
	}
	if got, _, _ := fixture.counts("/audio"); got != audioCalls {
		t.Fatalf("pre-publication retry retransferred audio: %d -> %d", audioCalls, got)
	}
}

func TestMultiTrackSessionPostReplaceJournalFaultRecoversPublishOnly(t *testing.T) {
	fixture, server := newMultiSessionMediaFixture(t)
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeMultiSessionInfo(t, infoPath, server.URL, "one", "one")
	const sessionID = "88888888888888888888888888888888"

	originalJournalWrite := multiSessionJournalWrite
	multiSessionJournalWrite = func(path string, journal directPublicationJournal) error {
		if journal.State == directJournalPublished {
			return errors.New("injected multi-track post-replace journal fault")
		}
		return originalJournalWrite(path, journal)
	}
	result, runErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	multiSessionJournalWrite = originalJournalWrite
	if runErr == nil || !errors.Is(runErr, ErrSessionNeedsReconciliation) || result.Downloaded || result.Session.Disposition != SessionRecoveryRequired {
		t.Fatalf("post-replace fault err=%v result=%#v", runErr, result)
	}
	if _, err := os.Stat(filepath.Join(root, "output.mp4")); err != nil {
		t.Fatalf("destination was not committed before journal fault: %v", err)
	}
	videoCalls, _, _ := fixture.counts("/video")
	audioCalls, _, _ := fixture.counts("/audio")
	recovered, recoveryErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	if recoveryErr != nil || !recovered.Downloaded {
		t.Fatalf("post-replace recovery err=%v result=%#v", recoveryErr, recovered)
	}
	if got, _, _ := fixture.counts("/video"); got != videoCalls {
		t.Fatalf("post-replace recovery retransferred video: %d -> %d", videoCalls, got)
	}
	if got, _, _ := fixture.counts("/audio"); got != audioCalls {
		t.Fatalf("post-replace recovery retransferred audio: %d -> %d", audioCalls, got)
	}
}

func TestMultiTrackSessionIndeterminatePublicationPersistsAndRetries(t *testing.T) {
	fixture, server := newMultiSessionMediaFixture(t)
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeMultiSessionInfo(t, infoPath, server.URL, "one", "one")
	const sessionID = "99999999999999999999999999999999"

	originalNoReplace := multiSessionNoReplace
	multiSessionNoReplace = func(string, string, string) (directNoReplaceResult, error) {
		return directNoReplaceIndeterminate, errors.New("injected multi-track indeterminate replacement")
	}
	result, runErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	multiSessionNoReplace = originalNoReplace
	if runErr == nil || !errors.Is(runErr, ErrPublicationIndeterminate) || result.Downloaded || result.Session.Disposition != SessionRecoveryRequired {
		t.Fatalf("indeterminate publication err=%v result=%#v", runErr, result)
	}
	rootRef, err := ValidateOutputRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := InspectResumeState(context.Background(), rootRef, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != SessionStatus(session.StatusNeedsReconciliation) || summary.Publication != SessionPublicationState(session.PublicationIndeterminate) {
		t.Fatalf("indeterminate publication was not durable: %#v", summary)
	}
	if _, err := os.Stat(filepath.Join(root, "output.mp4")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("indeterminate publication changed destination: %v", err)
	}
	videoCalls, _, _ := fixture.counts("/video")
	audioCalls, _, _ := fixture.counts("/audio")
	journalPath := filepath.Join(root, session.SessionsDirectoryName, sessionID, directSessionPublishDir, directSessionJournalName)
	journal, err := readDirectPublicationJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	journal.TargetIdentity = "mismatched-target"
	if err := writeDirectPublicationJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	mismatch, mismatchErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	if mismatchErr == nil || !errors.Is(mismatchErr, ErrSessionNeedsReconciliation) || mismatch.Downloaded {
		t.Fatalf("mismatched publication journal was accepted: err=%v result=%#v", mismatchErr, mismatch)
	}
	if got, _, _ := fixture.counts("/video"); got != videoCalls {
		t.Fatalf("journal mismatch retransferred video: %d -> %d", videoCalls, got)
	}
	if got, _, _ := fixture.counts("/audio"); got != audioCalls {
		t.Fatalf("journal mismatch retransferred audio: %d -> %d", audioCalls, got)
	}
	journal.TargetIdentity = "primary"
	if err := writeDirectPublicationJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	recovered, recoveryErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	if recoveryErr != nil || !recovered.Downloaded {
		t.Fatalf("indeterminate recovery err=%v result=%#v", recoveryErr, recovered)
	}
	if got, _, _ := fixture.counts("/video"); got != videoCalls {
		t.Fatalf("indeterminate recovery retransferred video: %d -> %d", videoCalls, got)
	}
	if got, _, _ := fixture.counts("/audio"); got != audioCalls {
		t.Fatalf("indeterminate recovery retransferred audio: %d -> %d", audioCalls, got)
	}
}

func TestMultiTrackSessionLeaseContentionAndRecovery(t *testing.T) {
	fixture, server := newMultiSessionMediaFixture(t)
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeMultiSessionInfo(t, infoPath, server.URL, "one", "one")
	const sessionID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once atomic.Bool
	firstDone := make(chan struct{})
	var firstResult Result
	var firstErr error
	go func() {
		client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
			if multiSessionAudioProgress(event) && once.CompareAndSwap(false, true) {
				close(entered)
				<-release
				cancel(ErrPauseRequested)
			}
			return nil
		}))
		firstResult, firstErr = client.Run(ctx, multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
		close(firstDone)
	}()
	select {
	case <-entered:
	case <-time.After(30 * time.Second):
		t.Fatal("multi-track lease holder did not reach the transfer")
	}
	if _, err := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4")); err == nil || !errors.Is(err, ErrSessionInUse) {
		t.Fatalf("contending multi-track session err=%v", err)
	}
	close(release)
	select {
	case <-firstDone:
	case <-time.After(30 * time.Second):
		t.Fatal("multi-track lease holder did not recover from pause")
	}
	if firstErr == nil || !errors.Is(firstErr, ErrPauseRequested) || firstResult.Session.Disposition != SessionRetained {
		t.Fatalf("lease holder pause err=%v result=%#v", firstErr, firstResult)
	}
	resumed, resumeErr := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	if resumeErr != nil || !resumed.Downloaded {
		t.Fatalf("lease recovery resume err=%v result=%#v", resumeErr, resumed)
	}
	if videoCalls, _, _ := fixture.counts("/video"); videoCalls == 0 {
		t.Fatal("lease recovery did not transfer video")
	}
}

func TestMultiTrackPlanIdentityBindsTrackIdentityAndContainer(t *testing.T) {
	infoPath := filepath.Join(t.TempDir(), "info.json")
	server := httptest.NewServer(nil)
	defer server.Close()
	writeMultiSessionInfo(t, infoPath, server.URL, "t1", "t1")

	videoSelection := mediaformat.Selection{
		ID: "video", Ext: "mp4", Protocol: "http", VCodec: "mpeg4", ACodec: "none",
	}
	audioSelection := mediaformat.Selection{
		ID: "audio", Ext: "m4a", Protocol: "http", VCodec: "none", ACodec: "aac",
	}
	videoIdentity, err := directSessionTrackIdentity(value.Info{}, videoSelection)
	if err != nil {
		t.Fatal(err)
	}
	audioIdentity, err := directSessionTrackIdentity(value.Info{}, audioSelection)
	if err != nil {
		t.Fatal(err)
	}
	tracks := []multiTrackSessionTrack{
		{id: "video", role: "video", identity: videoIdentity, identitySum: multiTrackIdentityDigest(videoIdentity), selection: videoSelection},
		{id: "audio", role: "audio", identity: audioIdentity, identitySum: multiTrackIdentityDigest(audioIdentity), selection: audioSelection},
	}

	mp4 := multiTrackPlanIdentity(tracks, "output.mp4")
	mkv := multiTrackPlanIdentity(tracks, "output.mkv")
	if mp4 == mkv {
		t.Fatalf("plan identity does not distinguish output container: mp4=%q mkv=%q", mp4, mkv)
	}

	otherTracks := []multiTrackSessionTrack{
		{id: "video", role: "video", identity: videoIdentity, identitySum: multiTrackIdentityDigest(videoIdentity), selection: videoSelection},
		{id: "audio", role: "audio", identity: audioIdentity, identitySum: multiTrackIdentityDigest(audioIdentity), selection: audioSelection},
	}
	otherTracks[0].identitySum = multiTrackIdentityDigest("different-video-identity")
	if mp4 == multiTrackPlanIdentity(otherTracks, "output.mp4") {
		t.Fatal("plan identity does not bind track identity digest")
	}
}

func TestMultiTrackSessionCompletedFormatMismatchIsRejected(t *testing.T) {
	fixture, server := newMultiSessionMediaFixture(t)
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeMultiSessionInfo(t, infoPath, server.URL, "one", "one")
	const sessionID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	if _, err := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4")); err != nil {
		t.Fatalf("initial run err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "output.mp4")); err != nil {
		t.Fatalf("completed output missing: %v", err)
	}
	videoCalls, _, _ := fixture.counts("/video")
	audioCalls, _, _ := fixture.counts("/audio")

	// A resumed run with a different format identity (same selectable pair,
	// different codec) for the same session must not fast-path to the
	// previously completed artifact.
	encoded, err := json.Marshal(map[string]any{
		"id": "multi-session-fixture", "title": "multi session fixture", "webpage_url": "https://fixture.example/watch",
		"ext": "mp4", "formats": []map[string]any{
			{"format_id": "video", "url": server.URL + "/video?token=two", "ext": "mp4", "protocol": "http", "vcodec": "avc1", "acodec": "none", "height": 240},
			{"format_id": "audio", "url": server.URL + "/audio?token=two", "ext": "m4a", "protocol": "http", "vcodec": "none", "acodec": "aac", "abr": 256},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(infoPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := newBroadTestClient().Run(context.Background(), multiSessionRequest(root, infoPath, sessionID, NewPublicationArbiter(), "output.mp4"))
	if err == nil || !errors.Is(err, ErrResumeIdentityMismatch) {
		t.Fatalf("completed format mismatch err=%v result=%#v", err, result)
	}
	if got, _, _ := fixture.counts("/video"); got != videoCalls {
		t.Fatalf("format mismatch retransferred video: %d -> %d", videoCalls, got)
	}
	if got, _, _ := fixture.counts("/audio"); got != audioCalls {
		t.Fatalf("format mismatch retransferred audio: %d -> %d", audioCalls, got)
	}
}
