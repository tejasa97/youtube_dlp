package operations

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReplayCaptureIsDeterministicRedactedAndExecutable(t *testing.T) {
	suite := fixtureSuite(t)
	records := []Record{
		{CanaryID: "region.bbc", Class: ClassRegion, Extractor: "bbciplayer", Outcome: OutcomeFallback, Failure: FailureNone, Capability: "formats", StartedUnixMS: 999, DurationMS: 12},
		{CanaryID: "auth.youtube", Class: ClassCredential, Extractor: "youtube", Outcome: OutcomeCredentialUnavailable, Failure: FailureAuth, Capability: "extract", StartedUnixMS: 1, DurationMS: 500},
		{CanaryID: "public.twitch", Class: ClassPublic, Extractor: "twitch", Outcome: OutcomeSuccess, Failure: FailureNone, StartedUnixMS: 3, DurationMS: 2},
		{CanaryID: "public.youtube", Class: ClassPublic, Extractor: "youtube", Outcome: OutcomeSuccess, Failure: FailureNone, StartedUnixMS: 2, DurationMS: 2},
	}
	capture, err := CaptureReplay(suite, records)
	if err != nil {
		t.Fatal(err)
	}
	data, err := MarshalReplay(capture, suite)
	if err != nil {
		t.Fatal(err)
	}
	if want := bytes.TrimSpace(fixture(t, "replay_capture_v1.json")); !bytes.Equal(data, want) {
		t.Fatalf("capture fixture mismatch:\n%s\n%s", data, want)
	}
	for _, prohibited := range []string{"keychain", "youtube.fixture", "youtube.members.fixture", `"region":"GB"`, "started_unix_ms", "duration_ms"} {
		if strings.Contains(string(data), prohibited) {
			t.Fatalf("capture leaked %q: %s", prohibited, data)
		}
	}
	decoded, err := DecodeReplay(context.Background(), bytes.NewReader(data), 4096, suite)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := ReplayRunner(decoded, suite)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Execute(context.Background(), suite, RunOptions{OptIn: true}, runner, &fakeClock{})
	if err != nil || len(got) != 4 || got[0].Outcome != OutcomeCredentialUnavailable || got[1].Outcome != OutcomeSuccess || got[2].Outcome != OutcomeSuccess || got[3].Outcome != OutcomeFallback {
		t.Fatalf("replayed records=%+v err=%v", got, err)
	}
	reversed := append([]Record(nil), records...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	second, err := CaptureReplay(suite, reversed)
	if err != nil {
		t.Fatal(err)
	}
	secondData, _ := MarshalReplay(second, suite)
	if !bytes.Equal(data, secondData) {
		t.Fatalf("capture is not deterministic:\n%s\n%s", data, secondData)
	}
}

func TestReplayCaptureRejectsWrongSuiteDuplicatesAndUncapturedInvocation(t *testing.T) {
	suite := fixtureSuite(t)
	record := Record{CanaryID: "public.youtube", Class: ClassPublic, Extractor: "youtube", Outcome: OutcomeSuccess, Failure: FailureNone, StartedUnixMS: 1}
	if _, err := CaptureReplay(suite, []Record{record, record}); !errors.Is(err, ErrInvalidReplay) {
		t.Fatalf("duplicate capture error = %v", err)
	}
	capture, err := CaptureReplay(suite, []Record{record})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := ReplayRunner(capture, suite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Invocation{ID: "auth.youtube"}); !errors.Is(err, ErrInvalidReplay) {
		t.Fatalf("uncaptured invocation error = %v", err)
	}
	other, err := NewSuite([]CanarySpec{{ID: "public.other", Class: ClassPublic, Extractor: "other", TargetRef: "other.fixture", Capabilities: []string{"extract"}, TimeoutMS: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarshalReplay(capture, other); !errors.Is(err, ErrInvalidReplay) {
		t.Fatalf("wrong suite error = %v", err)
	}
}

func FuzzDecodeReplay(f *testing.F) {
	suite := fixtureSuite(f)
	record := Record{CanaryID: "public.youtube", Class: ClassPublic, Extractor: "youtube", Outcome: OutcomeSuccess, Failure: FailureNone, StartedUnixMS: 1}
	capture, _ := CaptureReplay(suite, []Record{record})
	seed, _ := MarshalReplay(capture, suite)
	f.Add(seed)
	f.Add([]byte(`{"schema_version":1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		capture, err := DecodeReplay(context.Background(), bytes.NewReader(data), 1<<20, suite)
		if err != nil {
			return
		}
		canonical, err := MarshalReplay(capture, suite)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeReplay(context.Background(), bytes.NewReader(canonical), 1<<20, suite); err != nil {
			t.Fatal(err)
		}
	})
}
