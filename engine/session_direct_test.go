package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/session"
)

type directSessionFixture struct {
	body       []byte
	etag       atomic.Value
	calls      atomic.Int64
	rangeCalls atomic.Int64
	zeroCalls  atomic.Int64
}

func (fixture *directSessionFixture) handler(writer http.ResponseWriter, request *http.Request) {
	fixture.calls.Add(1)
	body := fixture.body
	etag, _ := fixture.etag.Load().(string)
	offset := int64(0)
	if raw := request.Header.Get("Range"); raw != "" {
		prefix := "bytes="
		if strings.HasPrefix(raw, prefix) {
			parsed, parseErr := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(raw, prefix), "-"), 10, 64)
			if parseErr == nil && parsed >= 0 && parsed < int64(len(body)) {
				offset = parsed
				fixture.rangeCalls.Add(1)
			}
		}
	}
	if offset == 0 {
		fixture.zeroCalls.Add(1)
	}
	if offset > 0 {
		writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, len(body)-1, len(body)))
		writer.Header().Set("ETag", etag)
		writer.Header().Set("Content-Length", strconv.FormatInt(int64(len(body))-offset, 10))
		writer.WriteHeader(http.StatusPartialContent)
	} else {
		writer.Header().Set("ETag", etag)
		writer.Header().Set("Content-Length", strconv.FormatInt(int64(len(body))-offset, 10))
		writer.WriteHeader(http.StatusOK)
	}
	flusher, _ := writer.(http.Flusher)
	for position := offset; position < int64(len(body)); {
		end := position + 32<<10
		if end > int64(len(body)) {
			end = int64(len(body))
		}
		if _, err := writer.Write(body[position:end]); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		position = end
		if request.Context().Err() != nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func writeDirectSessionInfo(t *testing.T, path, mediaURL string) {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"id": "direct-fixture", "title": "direct fixture", "webpage_url": "https://fixture.example/watch",
		"formats": []map[string]any{{
			"format_id": "direct", "url": mediaURL, "ext": "bin", "protocol": "http",
			"vcodec": "fixture", "acodec": "none",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func directSessionRequest(root, infoPath, sessionID string, arbiter *PublicationArbiter) Request {
	return Request{
		LoadInfoJSON: infoPath, OutputDir: root, OutputTemplate: "output.bin", Format: "direct",
		Filesystem: FilesystemOptions{Resume: ResumeOptions{
			SessionID: sessionID, PublicationArbiter: arbiter,
			CommitTargets: []CommitTarget{{Kind: ArtifactKindPrimary, Identity: "primary", Basename: "output.bin"}},
		}},
	}
}

func TestDirectSessionPauseRestartTokenRotationAndCompletedReuse(t *testing.T) {
	t.Parallel()
	fixture := &directSessionFixture{body: []byte(strings.Repeat("direct-payload-", 32<<10))}
	fixture.etag.Store(`"strong-v1"`)
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeDirectSessionInfo(t, infoPath, server.URL+"/media?token=one")
	const sessionID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	ctx, cancel := context.WithCancelCause(context.Background())
	var paused atomic.Bool
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventDownloadProgress && event.Bytes >= 64<<10 && paused.CompareAndSwap(false, true) {
			cancel(ErrPauseRequested)
		}
		return nil
	}))
	pausedResult, pausedErr := client.Run(ctx, directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if pausedErr == nil || !errors.Is(pausedErr, ErrPauseRequested) {
		t.Fatalf("paused Run() err=%v result=%#v", pausedErr, pausedResult)
	}
	if pausedResult.Session.Disposition != SessionRetained {
		t.Fatalf("paused result session=%#v", pausedResult.Session)
	}
	rootRef, err := ValidateOutputRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := InspectResumeState(context.Background(), rootRef, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != SessionStatus(session.StatusPaused) || len(summary.Components) != 1 || summary.Components[0].CommittedBytes == 0 {
		t.Fatalf("paused summary=%#v", summary)
	}
	if _, err := os.Stat(filepath.Join(root, "output.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination exists after pause: %v", err)
	}
	rangeBefore := fixture.rangeCalls.Load()
	writeDirectSessionInfo(t, infoPath, server.URL+"/media?token=two")
	resumed, resumeErr := newBroadTestClient().Run(context.Background(), directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if resumeErr != nil {
		t.Fatalf("resume Run() err=%v result=%#v", resumeErr, resumed)
	}
	if !resumed.Downloaded || resumed.Session.Disposition != SessionPublished {
		t.Fatalf("resume result=%#v", resumed)
	}
	if fixture.rangeCalls.Load() <= rangeBefore {
		t.Fatalf("resume did not issue a Range request: before=%d after=%d", rangeBefore, fixture.rangeCalls.Load())
	}
	output, err := os.ReadFile(filepath.Join(root, "output.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != string(fixture.body) {
		t.Fatalf("published payload mismatch: got %d bytes want %d", len(output), len(fixture.body))
	}
	callsAfterPublish := fixture.calls.Load()
	writeDirectSessionInfo(t, infoPath, server.URL+"/media?token=rotated-again")
	reused, reuseErr := newBroadTestClient().Run(context.Background(), directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if reuseErr != nil || !reused.Downloaded || fixture.calls.Load() != callsAfterPublish {
		t.Fatalf("completed payload was not reused: err=%v result=%#v calls=%d want=%d", reuseErr, reused, fixture.calls.Load(), callsAfterPublish)
	}
}

func TestDirectSessionRejectsUnknownProtocol(t *testing.T) {
	for _, protocol := range []string{"m3u8", "http_dash_segments_generator", "custom-doer"} {
		if directSessionSupportedSelection(mediaformat.Selection{URL: "https://media.example/file", Protocol: protocol}) {
			t.Fatalf("protocol %q unexpectedly admitted to direct session mode", protocol)
		}
	}
	for _, protocol := range []string{"", "http", "https"} {
		if !directSessionSupportedSelection(mediaformat.Selection{URL: "https://media.example/file", Protocol: protocol}) {
			t.Fatalf("protocol %q unexpectedly rejected from direct session mode", protocol)
		}
	}
}

func TestDirectSessionChangedValidatorRestartsWithoutAppending(t *testing.T) {
	t.Parallel()
	fixture := &directSessionFixture{body: []byte(strings.Repeat("old-payload-", 32<<10))}
	fixture.etag.Store(`"old"`)
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeDirectSessionInfo(t, infoPath, server.URL+"/media?token=old")
	const sessionID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	ctx, cancel := context.WithCancelCause(context.Background())
	var paused atomic.Bool
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventDownloadProgress && event.Bytes >= 64<<10 && paused.CompareAndSwap(false, true) {
			cancel(ErrPauseRequested)
		}
		return nil
	}))
	_, err := client.Run(ctx, directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if err == nil || !errors.Is(err, ErrPauseRequested) {
		t.Fatalf("pause err=%v", err)
	}
	fixture.body = []byte(strings.Repeat("new-payload-", 32<<10))
	fixture.etag.Store(`"new"`)
	writeDirectSessionInfo(t, infoPath, server.URL+"/media?token=new")
	beforeRange := fixture.rangeCalls.Load()
	result, err := newBroadTestClient().Run(context.Background(), directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if err != nil {
		t.Fatalf("changed-validator resume err=%v result=%#v", err, result)
	}
	if fixture.rangeCalls.Load() <= beforeRange || fixture.zeroCalls.Load() < 2 {
		t.Fatalf("changed-validator run did not show conservative range-then-restart behavior: ranges=%d zeros=%d", fixture.rangeCalls.Load(), fixture.zeroCalls.Load())
	}
	output, err := os.ReadFile(filepath.Join(root, "output.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != string(fixture.body) {
		t.Fatalf("changed-validator output mismatch")
	}
}

func TestDirectSessionAmbiguousValidatorRestartsConservatively(t *testing.T) {
	fixture := &directSessionFixture{body: []byte(strings.Repeat("ambiguous-payload-", 32<<10))}
	fixture.etag.Store(`"strong-before"`)
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeDirectSessionInfo(t, infoPath, server.URL+"/media?token=before")
	const sessionID = "abababababababababababababababab"
	ctx, cancel := context.WithCancelCause(context.Background())
	var paused atomic.Bool
	client := newBroadTestClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventDownloadProgress && event.Bytes >= 64<<10 && paused.CompareAndSwap(false, true) {
			cancel(ErrPauseRequested)
		}
		return nil
	}))
	_, err := client.Run(ctx, directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if err == nil || !errors.Is(err, ErrPauseRequested) {
		t.Fatalf("pause err=%v", err)
	}

	// The representation is unchanged, but the refreshed response has no
	// strong validator. A paused prefix is therefore ambiguous and must not be
	// appended to; the downloader may probe with Range, then restart from zero.
	fixture.etag.Store("")
	writeDirectSessionInfo(t, infoPath, server.URL+"/media?token=without-validator")
	rangesBefore := fixture.rangeCalls.Load()
	zerosBefore := fixture.zeroCalls.Load()
	result, err := newBroadTestClient().Run(context.Background(), directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if err != nil || !result.Downloaded {
		t.Fatalf("ambiguous-validator resume err=%v result=%#v", err, result)
	}
	if fixture.rangeCalls.Load() <= rangesBefore || fixture.zeroCalls.Load() <= zerosBefore {
		t.Fatalf("ambiguous-validator run did not restart conservatively: ranges=%d zeros=%d", fixture.rangeCalls.Load(), fixture.zeroCalls.Load())
	}
	output, err := os.ReadFile(filepath.Join(root, "output.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != string(fixture.body) {
		t.Fatalf("ambiguous-validator output mismatch")
	}
}

func TestDirectSessionCollisionPreservesTargetAndSupportsPublishOnlyRetry(t *testing.T) {
	t.Parallel()
	fixture := &directSessionFixture{body: []byte(strings.Repeat("collision-payload-", 8<<10))}
	fixture.etag.Store(`"collision"`)
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeDirectSessionInfo(t, infoPath, server.URL+"/media?token=collision")
	const sessionID = "cccccccccccccccccccccccccccccccc"
	if err := os.WriteFile(filepath.Join(root, "output.bin"), []byte("user-owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := newBroadTestClient().Run(context.Background(), directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if err == nil || !errors.Is(err, ErrDestinationCollision) || result.Session.Disposition != SessionCollision {
		t.Fatalf("collision Run() err=%v result=%#v", err, result)
	}
	owned, err := os.ReadFile(filepath.Join(root, "output.bin"))
	if err != nil || string(owned) != "user-owned" {
		t.Fatalf("collision changed target: %q err=%v", owned, err)
	}
	calls := fixture.calls.Load()
	if err := os.Remove(filepath.Join(root, "output.bin")); err != nil {
		t.Fatal(err)
	}
	retry, retryErr := newBroadTestClient().Run(context.Background(), directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if retryErr != nil || !retry.Downloaded || fixture.calls.Load() != calls {
		t.Fatalf("publish-only retry transferred or failed: err=%v result=%#v calls=%d want=%d", retryErr, retry, fixture.calls.Load(), calls)
	}
}

func TestDirectSessionPostReplaceJournalFailureIsRecoveryRequired(t *testing.T) {
	fixture := &directSessionFixture{body: []byte(strings.Repeat("journal-fault-", 8<<10))}
	fixture.etag.Store(`"journal-fault"`)
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeDirectSessionInfo(t, infoPath, server.URL+"/media?token=journal-fault")
	const sessionID = "ffffffffffffffffffffffffffffffff"
	originalJournalWrite := directSessionJournalWrite
	directSessionJournalWrite = func(path string, journal directPublicationJournal) error {
		if journal.State == directJournalPublished {
			return errors.New("injected post-replace journal failure")
		}
		return originalJournalWrite(path, journal)
	}
	result, err := newBroadTestClient().Run(context.Background(), directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	directSessionJournalWrite = originalJournalWrite
	if err == nil || !errors.Is(err, ErrSessionNeedsReconciliation) || result.Downloaded || result.Session.Disposition != SessionRecoveryRequired {
		t.Fatalf("post-replace journal failure err=%v result=%#v", err, result)
	}
	if _, err := os.Stat(filepath.Join(root, "output.bin")); err != nil {
		t.Fatalf("destination was not committed before journal fault: %v", err)
	}
	calls := fixture.calls.Load()
	recovered, recoveryErr := newBroadTestClient().Run(context.Background(), directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if recoveryErr != nil || !recovered.Downloaded || fixture.calls.Load() != calls {
		t.Fatalf("recovery did not complete publish-only: err=%v result=%#v calls=%d want=%d", recoveryErr, recovered, fixture.calls.Load(), calls)
	}
}

func TestDirectSessionIndeterminatePublicationPersistsAndRetries(t *testing.T) {
	fixture := &directSessionFixture{body: []byte(strings.Repeat("indeterminate-payload-", 8<<10))}
	fixture.etag.Store(`"indeterminate"`)
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeDirectSessionInfo(t, infoPath, server.URL+"/media?token=indeterminate")
	const sessionID = "12121212121212121212121212121212"
	originalNoReplace := directSessionNoReplace
	directSessionNoReplace = func(string, string, string) (directNoReplaceResult, error) {
		return directNoReplaceIndeterminate, errors.New("injected indeterminate replacement")
	}
	defer func() { directSessionNoReplace = originalNoReplace }()
	result, err := newBroadTestClient().Run(context.Background(), directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if err == nil || !errors.Is(err, ErrPublicationIndeterminate) || result.Downloaded || result.Session.Disposition != SessionRecoveryRequired {
		t.Fatalf("indeterminate publication err=%v result=%#v", err, result)
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
	if _, err := os.Stat(filepath.Join(root, "output.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("indeterminate publication changed destination: %v", err)
	}
	journalPath := filepath.Join(root, session.SessionsDirectoryName, sessionID, directSessionPublishDir, directSessionJournalName)
	journal, err := readDirectPublicationJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	journal.TargetIdentity = "mismatched-target"
	if err := writeDirectPublicationJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	calls := fixture.calls.Load()
	mismatch, mismatchErr := newBroadTestClient().Run(context.Background(), directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if mismatchErr == nil || !errors.Is(mismatchErr, ErrSessionNeedsReconciliation) || mismatch.Downloaded || fixture.calls.Load() != calls {
		t.Fatalf("mismatched publication journal was accepted: err=%v result=%#v calls=%d want=%d", mismatchErr, mismatch, fixture.calls.Load(), calls)
	}
	journal.TargetIdentity = "primary"
	if err := writeDirectPublicationJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	directSessionNoReplace = originalNoReplace
	recovered, recoveryErr := newBroadTestClient().Run(context.Background(), directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if recoveryErr != nil || !recovered.Downloaded || fixture.calls.Load() != calls {
		t.Fatalf("indeterminate retry did not publish without transfer: err=%v result=%#v calls=%d want=%d", recoveryErr, recovered, fixture.calls.Load(), calls)
	}
}

func TestDirectSessionPrePublicationFaultRemainsRetryable(t *testing.T) {
	fixture := &directSessionFixture{body: []byte(strings.Repeat("pre-publication-payload-", 8<<10))}
	fixture.etag.Store(`"pre-publication"`)
	server := httptest.NewServer(http.HandlerFunc(fixture.handler))
	defer server.Close()
	root := t.TempDir()
	infoPath := filepath.Join(t.TempDir(), "info.json")
	writeDirectSessionInfo(t, infoPath, server.URL+"/media?token=pre-publication")
	const sessionID = "34343434343434343434343434343434"
	originalJournalWrite := directSessionJournalWrite
	directSessionJournalWrite = func(path string, journal directPublicationJournal) error {
		if journal.State == directJournalPublishing {
			return errors.New("injected pre-publication journal failure")
		}
		return originalJournalWrite(path, journal)
	}
	result, err := newBroadTestClient().Run(context.Background(), directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	directSessionJournalWrite = originalJournalWrite
	if err == nil || result.Downloaded || result.Session.Disposition != SessionRetained || result.Session.Publication != PublicationReady {
		t.Fatalf("pre-publication fault err=%v result=%#v", err, result)
	}
	if _, err := os.Stat(filepath.Join(root, "output.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-publication fault changed destination: %v", err)
	}
	calls := fixture.calls.Load()
	recovered, recoveryErr := newBroadTestClient().Run(context.Background(), directSessionRequest(root, infoPath, sessionID, NewPublicationArbiter()))
	if recoveryErr != nil || !recovered.Downloaded || fixture.calls.Load() != calls {
		t.Fatalf("pre-publication retry did not publish without transfer: err=%v result=%#v calls=%d want=%d", recoveryErr, recovered, fixture.calls.Load(), calls)
	}
}

func TestDirectSessionCancellationAtPublicationEntry(t *testing.T) {
	cases := []struct {
		name  string
		cause error
		id    string
	}{
		{name: "pause", cause: ErrPauseRequested, id: "56565656565656565656565656565656"},
		{name: "cancel", cause: context.Canceled, id: "78787878787878787878787878787878"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := &directSessionFixture{body: []byte(strings.Repeat("publish-cancel-payload-", 8<<10))}
			fixture.etag.Store(`"publish-cancel"`)
			server := httptest.NewServer(http.HandlerFunc(fixture.handler))
			defer server.Close()
			root := t.TempDir()
			infoPath := filepath.Join(t.TempDir(), "info.json")
			writeDirectSessionInfo(t, infoPath, server.URL+"/media?token=publish-cancel")
			ctx, cancel := context.WithCancelCause(context.Background())
			originalJournalWrite := directSessionJournalWrite
			directSessionJournalWrite = func(path string, journal directPublicationJournal) error {
				err := originalJournalWrite(path, journal)
				if err == nil && journal.State == directJournalReady {
					cancel(test.cause)
				}
				return err
			}
			result, runErr := newBroadTestClient().Run(ctx, directSessionRequest(root, infoPath, test.id, NewPublicationArbiter()))
			directSessionJournalWrite = originalJournalWrite
			if runErr == nil || !errors.Is(runErr, test.cause) || result.Downloaded {
				t.Fatalf("publication-entry cancellation err=%v result=%#v", runErr, result)
			}
			rootRef, err := ValidateOutputRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			summary, err := InspectResumeState(context.Background(), rootRef, test.id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(root, "output.bin")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("publication-entry cancellation changed destination: %v", err)
			}
			if test.name == "pause" {
				if result.Session.Disposition != SessionRetained || summary.Status != SessionStatus(session.StatusPaused) ||
					summary.Phase != SessionPhase(session.PhaseReadyToPublish) || summary.Publication != SessionPublicationState(session.PublicationPending) {
					t.Fatalf("pause-at-publication state result=%#v summary=%#v", result.Session, summary)
				}
				return
			}
			if (result.Session.Disposition != SessionDiscarded && result.Session.Disposition != SessionCleanupPending) ||
				(result.Session.Cleanup != CleanupComplete && result.Session.Cleanup != CleanupPendingOutcome) || summary.HasManifest {
				t.Fatalf("cancel-at-publication state err=%v result=%#v summary=%#v", runErr, result.Session, summary)
			}
		})
	}
}

func TestDirectSessionLeaseContentionRecovers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ref, err := ValidateOutputRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := session.CreateWithID(session.CreateOptions{
		OutputRoot: root, OutputRootIdentity: ref.Identity,
		Source:              session.SourceIntent{Provider: "fixture", ID: "fixture", Kind: "video"},
		Output:              session.OutputIntent{Container: "bin", Extension: "bin", PlanIdentity: "direct-v1:placeholder:bin"},
		RelativeDestination: "output.bin",
		Components:          []session.Component{{ID: directSessionComponentID, Kind: "direct", Checkpoint: session.CheckpointMetadata{RelativePath: directSessionCheckpoint}}},
	}, "dddddddddddddddddddddddddddddddd")
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	blocked, err := session.Open(session.WorkspaceRef{OutputRoot: root, OutputRootIdentity: ref.Identity, SessionID: "dddddddddddddddddddddddddddddddd"})
	if err == nil || blocked != nil || !errors.Is(err, session.ErrLeaseContended) {
		t.Fatalf("second lease acquisition = workspace=%v err=%v", blocked, err)
	}
	if err := workspace.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := session.Open(session.WorkspaceRef{OutputRoot: root, OutputRootIdentity: ref.Identity, SessionID: "dddddddddddddddddddddddddddddddd"})
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDirectPublicationJournalRejectsCredentialMaterial(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "publish", directSessionJournalName)
	err := writeDirectPublicationJournal(path, directPublicationJournal{
		Version: directSessionJournalVersion, SessionID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", State: directJournalReady,
		Fingerprint: strings.Repeat("a", 64), TargetKind: "primary", TargetIdentity: "primary", TargetBasename: "out.bin", UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "http") || strings.Contains(string(encoded), "token") || strings.Contains(string(encoded), "cookie") {
		t.Fatalf("journal contains unexpected request material: %s", encoded)
	}
}
