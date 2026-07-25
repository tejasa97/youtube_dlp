package youtubeump

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/events"
)

func TestResumeAcrossDownloaderInstances(t *testing.T) {
	var calls atomic.Int32
	roundOne := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 4000, payload: []byte("seg0")},
	)
	roundTwo := buildTestUMP(
		137,
		testSegment{headerID: 3, sequence: 1, duration: 3000, payload: []byte("seg1")},
		testSegment{headerID: 4, sequence: 2, duration: 3000, payload: []byte("seg2")},
	)
	root, destination := testRoot(t, "out.bin")
	partPath, statePath := checkpointPaths(destination)

	var sawProgress bool
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		body, _ := io.ReadAll(request.Body)
		switch call {
		case 1:
			return umpResponse(roundOne, request), nil
		default:
			playerTime, bufferedCount, selected, err := decodePlaybackRequestBody(body)
			if err != nil {
				t.Fatal(err)
			}
			if playerTime != 4000 || bufferedCount != 1 || !selected {
				t.Fatalf("resume request playerTime=%d buffered=%d selected=%v", playerTime, bufferedCount, selected)
			}
			if strings.Contains(request.URL.String(), "sig=old") {
				t.Fatalf("resumed with stale signed URL: %s", request.URL)
			}
			if !strings.Contains(request.URL.String(), "sig=fresh%2Btoken") {
				t.Fatalf("expected refreshed signed URL, got %s", request.URL)
			}
			if !strings.HasSuffix(request.URL.String(), "rn=0") {
				t.Fatalf("resume must restart rn, got %s", request.URL)
			}
			return umpResponse(roundTwo, request), nil
		}
	})

	cancelCtx, cancelFirst := context.WithCancel(context.Background())
	sink := events.SinkFunc(func(ctx context.Context, event events.Event) error {
		if event.Kind == events.KindProgress && event.Bytes >= int64(len("INITseg0")) {
			sawProgress = true
			cancelFirst()
		}
		return nil
	})
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=old",
		func(config *Config) {
			config.VideoID = "fixture0001"
			config.MaxRounds = 4
		},
	)).Download(cancelCtx, root, destination, true, sink)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("first err=%v", err)
	}
	if !sawProgress {
		t.Fatal("expected progress before cancel")
	}
	if _, err := os.Stat(partPath); err != nil {
		t.Fatalf("part missing: %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("checkpoint missing: %v", err)
	}
	encoded, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := assertCheckpointHasNoSecrets(encoded); err != nil {
		t.Fatal(err)
	}

	var resumedEvent bool
	resumeSink := events.SinkFunc(func(ctx context.Context, event events.Event) error {
		if event.Resuming {
			resumedEvent = true
		}
		return nil
	})
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fresh%2Btoken",
		func(config *Config) {
			config.VideoID = "fixture0001"
			config.MaxRounds = 4
			config.POToken = []byte("fresh-po-token")
			config.VisitorData = "fresh-visitor"
		},
	)).Download(context.Background(), root, destination, true, resumeSink)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed || !resumedEvent {
		t.Fatalf("resumed=%v event=%v", result.Resumed, resumedEvent)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "INITseg0seg1seg2" {
		t.Fatalf("got=%q", got)
	}
	if _, err := os.Stat(partPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("part remains: %v", err)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint remains: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestResumeRejectsChangedSegmentBytes(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	partPath, statePath := checkpointPaths(destination)
	roundOne := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 5000, payload: []byte("same")},
	)
	var calls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		if call == 1 {
			return umpResponse(roundOne, request), nil
		}
		return umpResponse(buildTestUMP(
			137,
			testSegment{headerID: 2, sequence: 0, duration: 5000, payload: []byte("diff")},
		), request), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	sink := events.SinkFunc(func(ctx context.Context, event events.Event) error {
		if event.Kind == events.KindProgress && event.Bytes >= int64(len("INITsame")) {
			cancel()
		}
		return nil
	})
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.VideoID = "v"; config.MaxRounds = 4 },
	)).Download(ctx, root, destination, true, sink)
	if err == nil {
		t.Fatal("expected cancel")
	}
	if _, err := os.Stat(partPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatal(err)
	}
	_, err = NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fresh",
		func(config *Config) { config.VideoID = "v"; config.MaxRounds = 4 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil || !errors.Is(err, ErrInvalidMediaState) {
		t.Fatalf("err=%v", err)
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestResumeAcceptsIdenticalReplay(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	roundOne := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 5000, payload: []byte("same")},
	)
	roundTwo := buildTestUMP(
		137,
		testSegment{headerID: 2, sequence: 0, duration: 5000, payload: []byte("same")},
		testSegment{headerID: 3, sequence: 1, duration: 5000, payload: []byte("tail")},
	)
	var calls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return umpResponse(roundOne, request), nil
		}
		return umpResponse(roundTwo, request), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	sink := events.SinkFunc(func(ctx context.Context, event events.Event) error {
		if event.Kind == events.KindProgress && event.Bytes >= int64(len("INITsame")) {
			cancel()
		}
		return nil
	})
	_, _ = NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=a",
		func(config *Config) { config.VideoID = "v"; config.MaxRounds = 4 },
	)).Download(ctx, root, destination, true, sink)

	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=b",
		func(config *Config) { config.VideoID = "v"; config.MaxRounds = 4 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed {
		t.Fatal("expected resume")
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "INITsametail" {
		t.Fatalf("got=%q", got)
	}
}

func TestCheckpointCorruptAndMismatchedStartFresh(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	partPath, statePath := checkpointPaths(destination)
	if err := os.WriteFile(partPath, []byte("INITseg0"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("fresh")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.VideoID = "v" },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed {
		t.Fatal("corrupt checkpoint should not resume")
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "INITfresh" {
		t.Fatalf("got=%q", got)
	}
}

func TestCheckpointIdentityMismatchStartsFresh(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	_, statePath := checkpointPaths(destination)
	identity := identityFromConfig(testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.VideoID = "old-video" },
	))
	state := identity.baseCheckpoint()
	state.InitWritten = true
	state.InitDigest = encodeDigest(hashSegment([]byte("INIT")))
	state.InitLength = 4
	state.FormatVerified = true
	state.TotalWritten = 4
	if err := saveCheckpoint(statePath, state); err != nil {
		t.Fatal(err)
	}
	partPath, _ := checkpointPaths(destination)
	if err := os.WriteFile(partPath, []byte("INIT"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("data")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.VideoID = "new-video" },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed {
		t.Fatal("mismatched identity resumed")
	}
}

func TestCheckpointSymlinkRejected(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	_, statePath := checkpointPaths(destination)
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, statePath); err != nil {
		t.Skip("symlink unavailable")
	}
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("data")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil || !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("err=%v", err)
	}
}

func TestCheckpointOversizedRejected(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	_, statePath := checkpointPaths(destination)
	if err := os.WriteFile(statePath, bytes.Repeat([]byte{'a'}, MaxCheckpointBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("data")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil || !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestCheckpointForbiddenContentRejected(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	_, statePath := checkpointPaths(destination)
	malicious := []byte(`{"v":1,"po_token":"secret","client_name":1}`)
	if err := os.WriteFile(statePath, malicious, 0o600); err != nil {
		t.Fatal(err)
	}
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("data")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil || !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("err=%v", err)
	}
}

func TestTruncateUncommittedTailOnResume(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	partPath, statePath := checkpointPaths(destination)
	roundOne := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 4000, payload: []byte("seg0")},
	)
	roundTwo := buildTestUMP(
		137,
		testSegment{headerID: 3, sequence: 1, duration: 6000, payload: []byte("seg1!!")},
	)
	var calls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return umpResponse(roundOne, request), nil
		}
		return umpResponse(roundTwo, request), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	sink := events.SinkFunc(func(ctx context.Context, event events.Event) error {
		if event.Kind == events.KindProgress && event.Bytes >= int64(len("INITseg0")) {
			cancel()
		}
		return nil
	})
	_, _ = NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=a",
		func(config *Config) { config.VideoID = "v"; config.MaxRounds = 4 },
	)).Download(ctx, root, destination, true, sink)

	if err := os.WriteFile(partPath, append([]byte("INITseg0"), []byte("GARBAGE")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatal(err)
	}
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=b",
		func(config *Config) { config.VideoID = "v"; config.MaxRounds = 4 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed {
		t.Fatal("expected resume")
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "INITseg0seg1!!" {
		t.Fatalf("got=%q", got)
	}
}

func TestCheckpointWriteFailureLeavesConsistentState(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	_, statePath := checkpointPaths(destination)
	if err := os.Mkdir(statePath, 0o755); err != nil {
		t.Fatal(err)
	}
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("data")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil {
		t.Fatal("expected checkpoint failure")
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestResumeEndOfTrackFromCheckpoint(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	body := appendEndOfTrackPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("short")},
	))
	var calls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return umpResponse(body, request), nil
	})
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=a",
		func(config *Config) { config.VideoID = "v"; config.DurationSec = 10 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed {
		t.Fatal("first pass should not resume")
	}
	// Simulate crash after publish failure by restoring part+checkpoint manually is hard;
	// instead verify a completed checkpoint with end_of_track can finish without POST.
	partPath, statePath := checkpointPaths(destination)
	_ = os.Remove(destination)
	identity := identityFromConfig(testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=a",
		func(config *Config) { config.VideoID = "v"; config.DurationSec = 10 },
	))
	state := identity.baseCheckpoint()
	state.InitWritten = true
	state.InitDigest = encodeDigest(hashSegment([]byte("INIT")))
	state.InitLength = 4
	state.FormatVerified = true
	state.EndOfTrack = true
	state.HasSequence = true
	state.NextSequence = 1
	state.CumulativeMs = 1000
	state.TotalWritten = int64(len("INITshort"))
	state.Segments = []sabrCheckpointSegment{{
		Sequence: 0, Digest: encodeDigest(hashSegment([]byte("short"))),
		DurationMs: 1000, StartTimeMs: 0, Length: int64(len("short")),
	}}
	if err := saveCheckpoint(statePath, state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partPath, []byte("INITshort"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	result, err = NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=b",
		func(config *Config) { config.VideoID = "v"; config.DurationSec = 10 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed {
		t.Fatal("expected resume")
	}
	if calls.Load() != before {
		t.Fatalf("end-of-track resume issued POST: %d", calls.Load()-before)
	}
}

func TestResumeRaceConcurrentLoadSave(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "race.bin")
	_, statePath := checkpointPaths(destination)
	identity := identityFromConfig(testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.VideoID = "race" },
	))
	state := identity.baseCheckpoint()
	state.InitWritten = true
	state.InitDigest = encodeDigest(hashSegment([]byte("INIT")))
	state.InitLength = 4
	state.FormatVerified = true
	state.TotalWritten = 4
	if err := saveCheckpoint(statePath, state); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := 0; index < 50; index++ {
			_, _, _ = loadCheckpoint(statePath, identity)
			_ = saveCheckpoint(statePath, state)
		}
	}()
	for index := 0; index < 50; index++ {
		_, _, _ = loadCheckpoint(statePath, identity)
		_ = saveCheckpoint(statePath, state)
	}
	<-done
}

func TestCheckpointPathsWindowsSafe(t *testing.T) {
	destination := `C:\Videos\out.mp4`
	partPath, statePath := CheckpointPaths(destination)
	if partPath != destination+".part" || statePath != destination+".part.json" {
		t.Fatalf("part=%q state=%q", partPath, statePath)
	}
}

func TestVerifyCheckpointRejectsTamperedInit(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	partPath, statePath := seedPartialResume(t, root, destination)
	raw, err := os.ReadFile(partPath)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] ^= 0xFF
	if err := os.WriteFile(partPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = statePath
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("fresh")},
	)
	var calls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return umpResponse(body, request), nil
	})
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=b",
		func(config *Config) { config.VideoID = "v" },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed {
		t.Fatal("tampered init must not resume")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "INITfresh" {
		t.Fatalf("got=%q", got)
	}
}

func TestVerifyCheckpointRejectsTamperedMedia(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	partPath, _ := seedPartialResume(t, root, destination)
	raw, err := os.ReadFile(partPath)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a media byte after the 4-byte INIT prefix.
	raw[4] ^= 0xFF
	if err := os.WriteFile(partPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("fresh")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=b",
		func(config *Config) { config.VideoID = "v" },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed {
		t.Fatal("tampered media must not resume")
	}
}

func TestVerifyCheckpointRejectsTruncatedPart(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	partPath, _ := seedPartialResume(t, root, destination)
	raw, err := os.ReadFile(partPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partPath, raw[:len(raw)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("fresh")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=b",
		func(config *Config) { config.VideoID = "v" },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed {
		t.Fatal("truncated part must not resume")
	}
}

func TestVerifyCheckpointTruncatesOversizedTail(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	partPath, _ := seedPartialResume(t, root, destination)
	raw, err := os.ReadFile(partPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partPath, append(raw, []byte("TAIL")...), 0o600); err != nil {
		t.Fatal(err)
	}
	roundTwo := buildTestUMP(
		137,
		testSegment{headerID: 3, sequence: 1, duration: 6000, payload: []byte("seg1!!")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(roundTwo, request), nil
	})
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=b",
		func(config *Config) { config.VideoID = "v"; config.MaxRounds = 4 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed {
		t.Fatal("expected resume after oversized tail truncate")
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "INITseg0seg1!!" {
		t.Fatalf("got=%q", got)
	}
}

func TestResumeAcceptsIdenticalInitReplay(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	_, _ = seedPartialResume(t, root, destination)
	roundTwo := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 3, sequence: 1, duration: 6000, payload: []byte("seg1!!")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(roundTwo, request), nil
	})
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=b",
		func(config *Config) { config.VideoID = "v"; config.MaxRounds = 4 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed {
		t.Fatal("expected resume")
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "INITseg0seg1!!" {
		t.Fatalf("got=%q", got)
	}
}

func TestResumeRejectsChangedInitReplay(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	_, _ = seedPartialResume(t, root, destination)
	roundTwo := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIX")},
		testSegment{headerID: 3, sequence: 1, duration: 6000, payload: []byte("seg1!!")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(roundTwo, request), nil
	})
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=b",
		func(config *Config) { config.VideoID = "v"; config.MaxRounds = 4 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil || !errors.Is(err, ErrInvalidMediaState) {
		t.Fatalf("err=%v", err)
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestCheckpointRejectsNonContiguousSequences(t *testing.T) {
	identity := identityFromConfig(testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.VideoID = "v" },
	))
	state := identity.baseCheckpoint()
	state.InitWritten = true
	state.InitDigest = encodeDigest(hashSegment([]byte("INIT")))
	state.InitLength = 4
	state.FormatVerified = true
	state.HasSequence = true
	state.NextSequence = 2
	state.CumulativeMs = 1000
	state.TotalWritten = 8
	state.Segments = []sabrCheckpointSegment{{
		Sequence: 1, Digest: encodeDigest(hashSegment([]byte("seg0"))),
		DurationMs: 1000, StartTimeMs: 0, Length: 4,
	}}
	if err := validateCheckpoint(state); err == nil {
		t.Fatal("expected non-contiguous rejection")
	}
	state.Segments[0].Sequence = 0
	state.NextSequence = 3
	if err := validateCheckpoint(state); err == nil {
		t.Fatal("expected next-sequence rejection")
	}
}

func seedPartialResume(t *testing.T, root, destination string) (partPath, statePath string) {
	t.Helper()
	roundOne := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 4000, payload: []byte("seg0")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(roundOne, request), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	sink := events.SinkFunc(func(ctx context.Context, event events.Event) error {
		if event.Kind == events.KindProgress && event.Bytes >= int64(len("INITseg0")) {
			cancel()
		}
		return nil
	})
	_, _ = NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=a",
		func(config *Config) { config.VideoID = "v"; config.MaxRounds = 4 },
	)).Download(ctx, root, destination, true, sink)
	partPath, statePath = checkpointPaths(destination)
	if _, err := os.Stat(partPath); err != nil {
		t.Fatalf("part missing: %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("checkpoint missing: %v", err)
	}
	return partPath, statePath
}

func assertCheckpointHasNoSecrets(encoded []byte) error {
	if containsForbiddenCheckpointBytes(encoded) {
		return errors.New("forbidden content")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return err
	}
	for _, key := range []string{"po_token", "playback_cookie", "cookie", "visitor_data", "server_url", "headers", "authorization"} {
		if _, ok := raw[key]; ok {
			return errors.New("secret key present: " + key)
		}
	}
	return nil
}

func TestResumeVideoIDRequired(t *testing.T) {
	if err := ValidateResumeVideoID(""); err == nil || !errors.Is(err, ErrMissingConfig) {
		t.Fatalf("empty: %v", err)
	}
	if err := ValidateResumeVideoID(strings.Repeat("v", maxCheckpointVideoIDBytes+1)); err == nil || !errors.Is(err, ErrMissingConfig) {
		t.Fatalf("oversized: %v", err)
	}
	root, destination := testRoot(t, "out.bin")
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("data")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatal("must not download without video id")
		return umpResponse(body, request), nil
	})
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.VideoID = "" },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil || !errors.Is(err, ErrMissingConfig) {
		t.Fatalf("err=%v", err)
	}
}

func TestEmptyCheckpointVideoIDNeverResumes(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	partPath, statePath := checkpointPaths(destination)
	if err := os.WriteFile(partPath, []byte("INIT"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := identityFromConfig(testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	))
	state := identity.baseCheckpoint()
	state.VideoID = ""
	state.InitWritten = true
	state.InitDigest = encodeDigest(hashSegment([]byte("INIT")))
	state.InitLength = 4
	state.FormatVerified = true
	state.TotalWritten = 4
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("fresh")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed {
		t.Fatal("empty checkpoint video id must not resume")
	}
}

func TestCompletionMarkerLifecycleStandaloneVsRetained(t *testing.T) {
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("data")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})

	root, destination := testRoot(t, "standalone.bin")
	staleMarker := CompletionMarkerPath(destination)
	if err := os.WriteFile(staleMarker, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staleMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("standalone success retained completion marker")
	}
	partPath, statePath := checkpointPaths(destination)
	if _, err := os.Stat(partPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("standalone success left part")
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("standalone success left checkpoint")
	}

	root2, destination2 := testRoot(t, "pair.bin")
	if _, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.RetainCompletionMarker = true },
	)).Download(context.Background(), root2, destination2, true, events.Nop()); err != nil {
		t.Fatal(err)
	}
	marker := CompletionMarkerPath(destination2)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("retained marker missing: %v", err)
	}
	size, ok, err := ValidateCompletedTrack(destination2, IdentityFromConfig(testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.RetainCompletionMarker = true },
	)))
	if err != nil || !ok || size != int64(len("INITdata")) {
		t.Fatalf("completed track ok=%v size=%d err=%v", ok, size, err)
	}
}

func TestCompletionMarkerFailurePreservesRecoverableState(t *testing.T) {
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("data")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "pair.bin")
	partPath, statePath := checkpointPaths(destination)
	markerPath := CompletionMarkerPath(destination)

	old := completionMarkerWriter
	t.Cleanup(func() { completionMarkerWriter = old })
	var writes atomic.Int32
	completionMarkerWriter = func(mediaPath, dest string, identity ResumeIdentity, totalBytes int64) error {
		writes.Add(1)
		return fmt.Errorf("%w: injected marker failure", ErrDownloadFailed)
	}

	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.RetainCompletionMarker = true },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil {
		t.Fatal("expected marker failure")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("destination published despite marker failure")
	}
	if _, err := os.Stat(partPath); err != nil {
		t.Fatalf("part deleted after marker failure: %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("checkpoint deleted after marker failure: %v", err)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial marker left after failure")
	}

	completionMarkerWriter = old
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.RetainCompletionMarker = true },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatalf("retry after marker failure: %v", err)
	}
	if !result.Resumed {
		t.Fatal("expected resume from preserved part/checkpoint")
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("destination missing after retry: %v", err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("marker missing after retry: %v", err)
	}
	if _, err := os.Stat(partPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("part remains after successful retry")
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("checkpoint remains after successful retry")
	}
}

func TestCompletionMarkerCrashAfterDurableMarkerBeforePublish(t *testing.T) {
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("data")},
	)
	var posts atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		posts.Add(1)
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "crash.bin")
	partPath, statePath := checkpointPaths(destination)
	markerPath := CompletionMarkerPath(destination)

	old := completionMarkerWriter
	t.Cleanup(func() { completionMarkerWriter = old })
	completionMarkerWriter = func(mediaPath, dest string, identity ResumeIdentity, totalBytes int64) error {
		if err := writeCompletionMarkerDurable(mediaPath, dest, identity, totalBytes); err != nil {
			return err
		}
		return fmt.Errorf("%w: injected crash after durable marker", ErrDownloadFailed)
	}

	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.RetainCompletionMarker = true },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil {
		t.Fatal("expected post-marker crash")
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("destination published despite post-marker crash")
	}
	if _, err := os.Stat(partPath); err != nil {
		t.Fatalf("part missing after post-marker crash: %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("checkpoint missing after post-marker crash: %v", err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("durable marker missing after crash: %v", err)
	}
	before := posts.Load()

	completionMarkerWriter = old
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.RetainCompletionMarker = true },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatalf("retry after post-marker crash: %v", err)
	}
	if !result.Resumed {
		t.Fatal("expected resume without re-download")
	}
	if posts.Load() != before {
		t.Fatalf("retry issued POST: before=%d after=%d", before, posts.Load())
	}
	size, ok, err := ValidateCompletedTrack(destination, IdentityFromConfig(testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.RetainCompletionMarker = true },
	)))
	if err != nil || !ok || size != int64(len("INITdata")) {
		t.Fatalf("completed ok=%v size=%d err=%v", ok, size, err)
	}
	if _, err := os.Stat(partPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("part remains after publish")
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("checkpoint remains after publish")
	}
}

func TestCompletionLeftoverCheckpointAfterPublishIsComplete(t *testing.T) {
	_, destination := testRoot(t, "leftover.bin")
	payload := []byte("INITdata")
	if err := os.WriteFile(destination, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	identity := IdentityFromConfig(testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.RetainCompletionMarker = true },
	))
	if err := WriteCompletionMarker(destination, identity, int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	_, statePath := checkpointPaths(destination)
	state := identityFromConfig(testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).baseCheckpoint()
	state.InitWritten = true
	state.InitDigest = encodeDigest(hashSegment([]byte("INIT")))
	state.InitLength = 4
	state.FormatVerified = true
	state.EndOfTrack = true
	state.HasSequence = true
	state.NextSequence = 1
	state.CumulativeMs = 10000
	state.TotalWritten = int64(len(payload))
	state.Segments = []sabrCheckpointSegment{{
		Sequence: 0, Digest: encodeDigest(hashSegment([]byte("data"))),
		DurationMs: 10000, StartTimeMs: 0, Length: int64(len("data")),
	}}
	if err := saveCheckpoint(statePath, state); err != nil {
		t.Fatal(err)
	}
	size, ok, err := ValidateCompletedTrack(destination, identity)
	if err != nil || !ok || size != int64(len(payload)) {
		t.Fatalf("ok=%v size=%d err=%v", ok, size, err)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("leftover checkpoint not cleaned after durable marker validation")
	}
}

func TestStrictJSONRejectsTrailingValues(t *testing.T) {
	validCheckpoint := []byte(`{"v":1,"video_id":"fixture0001","client_name":1,"client_version":"fixture","track_kind":"video","itag":137,"duration_sec":10,"ustreamer_sha256":"` + hashUstreamerConfig([]byte("fixture-ustreamer")) + `","init_written":false,"format_verified":false,"next_sequence":0,"has_sequence":false,"cumulative_ms":0,"total_written":0,"segments":[]}`)
	var state sabrCheckpoint
	if err := decodeStrictJSON(append(append([]byte(nil), validCheckpoint...), '\n', ' '), &state); err != nil {
		t.Fatalf("whitespace after object should be accepted: %v", err)
	}
	if err := decodeStrictJSON(append(append([]byte(nil), validCheckpoint...), []byte(`{"x":1}`)...), &state); err == nil || !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("trailing object: %v", err)
	}
	if err := decodeStrictJSON(append(append([]byte(nil), validCheckpoint...), []byte(` null`)...), &state); err == nil || !errors.Is(err, ErrCheckpointInvalid) {
		t.Fatalf("trailing null: %v", err)
	}

	root, destination := testRoot(t, "trail.bin")
	_, statePath := checkpointPaths(destination)
	if err := os.WriteFile(statePath, append(append([]byte(nil), validCheckpoint...), []byte("\n{}")...), 0o600); err != nil {
		t.Fatal(err)
	}
	partPath, _ := checkpointPaths(destination)
	if err := os.WriteFile(partPath, []byte("INIT"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("fresh")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if result.Resumed {
		t.Fatal("trailing JSON checkpoint must not resume")
	}

	if err := os.WriteFile(destination, []byte("INITdata"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := IdentityFromConfig(testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	))
	if err := WriteCompletionMarker(destination, identity, int64(len("INITdata"))); err != nil {
		t.Fatal(err)
	}
	markerPath := CompletionMarkerPath(destination)
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, append(append([]byte(nil), raw...), []byte("\n1")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := ValidateCompletedTrack(destination, identity); err != nil || ok {
		t.Fatalf("trailing marker JSON accepted ok=%v err=%v", ok, err)
	}
}
