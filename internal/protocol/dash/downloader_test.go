package dash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tejasa97/ytdlp-go/internal/events"
	"github.com/tejasa97/ytdlp-go/internal/fragment"
	"github.com/tejasa97/ytdlp-go/internal/network"
)

func FuzzStaticCanonicalizationNeverPersistsURI(f *testing.F) {
	f.Add("https://cdn.example.invalid/base/path?token=bearer-secret")
	f.Fuzz(func(t *testing.T, raw string) {
		identity := FragmentIdentity{
			ProviderTrackIdentity: "provider:track:v1", RepresentationID: raw, PeriodID: raw, AdaptationSetID: raw,
			Addressing: raw, NumberTimeRole: raw, TemplateGrammar: raw, TimelineGrammar: raw, InitializationIdentity: raw,
			CanonicalComplete: true,
		}
		scale := NewDownloader(nil, Config{}).staticScale(identity)
		encoded, err := json.Marshal(scale)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "https://") || strings.Contains(string(encoded), "token=") || (len(raw) > 8 && strings.Contains(string(encoded), raw)) {
			t.Fatalf("canonical scale leaked fuzz input: %q", encoded)
		}
		if len(scale.Key) > 256 || len(scale.Scope) > 256 {
			t.Fatalf("unbounded scale: %#v", scale)
		}
	})
}

func TestStaticCheckpointProofControlsSignedBaseURLReuse(t *testing.T) {
	var generation atomic.Int32
	var firstSegmentCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.mpd":
			_, _ = fmt.Fprintf(writer, `<MPD type="static" mediaPresentationDuration="PT2S"><Period><AdaptationSet id="video" contentType="video"><Representation id="v" bandwidth="1"><SegmentTemplate duration="1" media="segment-$Number$?token=token-%d"/></Representation></AdaptationSet></Period></MPD>`, generation.Load())
		case "/segment-1":
			firstSegmentCalls.Add(1)
			_, _ = writer.Write([]byte("one"))
		case "/segment-2":
			_, _ = writer.Write([]byte("two"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "payload")
	checkpoint := &fragment.Checkpoint{Directory: filepath.Join(root, "checkpoint"), ResumeIdentity: "dash:static:fixture"}
	proof := func(FragmentIdentity) (fragment.Scale, bool) {
		return fragment.Scale{Kind: "content-identity", Value: strings.Repeat("a", 64), Scope: strings.Repeat("b", 64)}, true
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireStaticSingleRepresentation: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1, EquivalenceProof: proof}).Download(ctx, server.URL+"/manifest.mpd?token=manifest-one", root, destination, true, events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindFragmentCompleted && event.Fragment == 1 {
			cancel()
		}
		return nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first run error=%v, want cancellation", err)
	}
	ledger, readErr := os.ReadFile(filepath.Join(checkpoint.Directory, "state.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, secret := range []string{server.URL, "token-0", "manifest-one", "segment-1", "segment-2"} {
		if strings.Contains(string(ledger), secret) {
			t.Fatalf("checkpoint leaked %q: %s", secret, ledger)
		}
	}
	generation.Store(1)
	result, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireStaticSingleRepresentation: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1, EquivalenceProof: proof}).Download(context.Background(), server.URL+"/manifest.mpd?token=manifest-two", root, destination, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Tracks[0].Download.Reused != 1 || firstSegmentCalls.Load() != 1 {
		t.Fatalf("result=%#v first segment calls=%d, want signed URL reuse", result, firstSegmentCalls.Load())
	}
	if body, err := os.ReadFile(destination); err != nil || string(body) != "onetwo" {
		t.Fatalf("output=%q err=%v", body, err)
	}
}

func TestStaticScaleRejectsOpaqueCallbackProof(t *testing.T) {
	download := NewDownloader(nil, Config{RepresentationIdentity: "provider:track:v1", EquivalenceProof: func(FragmentIdentity) (fragment.Scale, bool) {
		return fragment.Scale{Kind: "content-identity", Value: strings.Repeat("a", 64), Scope: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0b2tlbiJ9.signature"}, true
	}})
	scale := download.staticScale(FragmentIdentity{CanonicalComplete: true, ProviderTrackIdentity: "provider:track:v1", RepresentationID: "v", PeriodID: "p", AdaptationSetID: "a", Addressing: "template", NumberTimeRole: "number", TemplateGrammar: "Number", TimelineGrammar: "none", InitializationIdentity: "none"})
	if scale.Kind != "" || scale.Value != "" || len(scale.Key) != 64 || len(scale.Scope) != 64 {
		t.Fatalf("opaque callback proof was retained: %#v", scale)
	}
}

func TestCheckpointRequiresStaticSingleRepresentationBeforeOutput(t *testing.T) {
	transport, _ := network.New(network.Config{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprint(writer, `<MPD type="dynamic"><Period><AdaptationSet><Representation id="v"><SegmentTemplate media="seg-$Number$"><SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`)
	}))
	defer server.Close()
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{Checkpoint: &fragment.Checkpoint{Directory: filepath.Join(root, "checkpoint"), ResumeIdentity: "dash:dynamic"}, RequireStaticSingleRepresentation: true}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "payload"), true, nil)
	if !errors.Is(err, ErrStaticResumeUnsupported) {
		t.Fatalf("error=%v, want static resume rejection", err)
	}
}

func TestStaticCanonicalStructuralKeyChangesForEveryResumeFact(t *testing.T) {
	download := NewDownloader(nil, Config{RepresentationIdentity: "provider:track:v1"})
	base := FragmentIdentity{
		RepresentationID: "v1", PeriodID: "period-1", PeriodIndex: 0, PeriodStartNanos: 1, PeriodDurationNanos: 2,
		PeriodTimingKnown: true, AdaptationSetID: "video", ContentType: "video", MimeType: "video/mp4", Codecs: "avc1",
		Language: "en", FrameRate: "30", AudioRate: "48000", Bandwidth: 1000, Width: 1920, Height: 1080, Addressing: "template", NumberTimeRole: "number",
		Timescale: 1000, PresentationTimeOffset: 3, StartNumber: 4, SegmentDuration: 5, TemplateGrammar: "Number", TimelineGrammar: "t0-d1-r0",
		InitializationIdentity: "template:RepresentationID", InitializationRangeStart: 5, InitializationRangeLength: 6,
		IndexRangeStart: 7, IndexRangeLength: 8, Initialize: true, RangeStart: 9, RangeLength: 10, Sequence: 11,
		Timeline: 12, Index: 13, ProviderTrackIdentity: "provider:track:v1", CanonicalComplete: true,
	}
	want := download.staticScale(base).Key
	mutations := []struct {
		name string
		edit func(*FragmentIdentity)
	}{
		{"provider track identity", func(v *FragmentIdentity) { v.ProviderTrackIdentity = "provider:track:v2" }},
		{"period id", func(v *FragmentIdentity) { v.PeriodID = "period-2" }},
		{"period index", func(v *FragmentIdentity) { v.PeriodIndex++ }},
		{"period start", func(v *FragmentIdentity) { v.PeriodStartNanos++ }},
		{"period duration", func(v *FragmentIdentity) { v.PeriodDurationNanos++ }},
		{"period timing", func(v *FragmentIdentity) { v.PeriodTimingKnown = false }},
		{"adaptation set", func(v *FragmentIdentity) { v.AdaptationSetID = "audio" }},
		{"representation", func(v *FragmentIdentity) { v.RepresentationID = "v2" }},
		{"content type", func(v *FragmentIdentity) { v.ContentType = "audio" }},
		{"mime", func(v *FragmentIdentity) { v.MimeType = "audio/mp4" }},
		{"codecs", func(v *FragmentIdentity) { v.Codecs = "hev1" }},
		{"language", func(v *FragmentIdentity) { v.Language = "fr" }},
		{"frame rate", func(v *FragmentIdentity) { v.FrameRate = "60" }},
		{"audio rate", func(v *FragmentIdentity) { v.AudioRate = "44100" }},
		{"bandwidth", func(v *FragmentIdentity) { v.Bandwidth++ }},
		{"width", func(v *FragmentIdentity) { v.Width++ }},
		{"height", func(v *FragmentIdentity) { v.Height++ }},
		{"addressing", func(v *FragmentIdentity) { v.Addressing = "list" }},
		{"number time role", func(v *FragmentIdentity) { v.NumberTimeRole = "time" }},
		{"timescale", func(v *FragmentIdentity) { v.Timescale++ }},
		{"presentation time offset", func(v *FragmentIdentity) { v.PresentationTimeOffset++ }},
		{"start number", func(v *FragmentIdentity) { v.StartNumber++ }},
		{"segment duration", func(v *FragmentIdentity) { v.SegmentDuration++ }},
		{"template grammar", func(v *FragmentIdentity) { v.TemplateGrammar = "Time" }},
		{"timeline grammar", func(v *FragmentIdentity) { v.TimelineGrammar = "t1-d1-r0" }},
		{"initialization identity", func(v *FragmentIdentity) { v.InitializationIdentity = "none" }},
		{"initialization range", func(v *FragmentIdentity) { v.InitializationRangeStart++ }},
		{"initialization range length", func(v *FragmentIdentity) { v.InitializationRangeLength++ }},
		{"index range start", func(v *FragmentIdentity) { v.IndexRangeStart++ }},
		{"index range", func(v *FragmentIdentity) { v.IndexRangeLength++ }},
		{"fragment initialization", func(v *FragmentIdentity) { v.Initialize = false }},
		{"fragment range", func(v *FragmentIdentity) { v.RangeStart++ }},
		{"fragment range length", func(v *FragmentIdentity) { v.RangeLength++ }},
		{"number", func(v *FragmentIdentity) { v.Sequence++ }},
		{"time", func(v *FragmentIdentity) { v.Timeline++ }},
		{"fragment index", func(v *FragmentIdentity) { v.Index++ }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := base
			mutation.edit(&candidate)
			if got := download.staticScale(candidate).Key; got == want {
				t.Fatalf("mutation %q did not change structural key", mutation.name)
			}
		})
	}
}

func TestStaticCheckpointChangedProofRestartsWholeScope(t *testing.T) {
	var generation atomic.Int32
	var firstCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.mpd":
			_, _ = fmt.Fprint(writer, `<MPD type="static" mediaPresentationDuration="PT2S"><Period><AdaptationSet id="video" contentType="video"><Representation id="v" bandwidth="1"><SegmentTemplate duration="1" media="segment-$Number$?token=rotated"/></Representation></AdaptationSet></Period></MPD>`)
		case "/segment-1":
			firstCalls.Add(1)
			if generation.Load() == 0 {
				_, _ = writer.Write([]byte("old"))
			} else {
				_, _ = writer.Write([]byte("new"))
			}
		case "/segment-2":
			_, _ = writer.Write([]byte("two"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "payload")
	checkpoint := &fragment.Checkpoint{Directory: filepath.Join(root, "checkpoint"), ResumeIdentity: "dash:proof:restart"}
	proofValue := strings.Repeat("a", 64)
	proof := func(FragmentIdentity) (fragment.Scale, bool) {
		return fragment.Scale{Kind: "content-identity", Value: proofValue, Scope: strings.Repeat("b", 64)}, true
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireStaticSingleRepresentation: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1, EquivalenceProof: proof}).Download(ctx, server.URL+"/manifest.mpd?token=one", root, destination, true, events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindFragmentCompleted && event.Fragment == 1 {
			cancel()
		}
		return nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first run error=%v, want cancellation", err)
	}
	generation.Store(1)
	proofValue = strings.Repeat("c", 64)
	result, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireStaticSingleRepresentation: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1, EquivalenceProof: proof}).Download(context.Background(), server.URL+"/manifest.mpd?token=two", root, destination, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Tracks[0].Download.Reused != 0 || firstCalls.Load() != 2 {
		t.Fatalf("result=%#v first calls=%d, want complete restart", result, firstCalls.Load())
	}
	if body, err := os.ReadFile(destination); err != nil || string(body) != "newtwo" {
		t.Fatalf("output=%q err=%v", body, err)
	}
}

func TestStaticCheckpointIncompleteCanonicalMetadataDownloadsAndRestarts(t *testing.T) {
	var generation atomic.Int32
	var firstCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.mpd":
			_, _ = fmt.Fprint(writer, `<MPD type="static" mediaPresentationDuration="PT2S"><Period><AdaptationSet contentType="video"><Representation id="v"><SegmentTemplate duration="1" media="segment-$Number$"/></Representation></AdaptationSet></Period></MPD>`)
		case "/segment-1":
			firstCalls.Add(1)
			if generation.Load() == 0 {
				_, _ = writer.Write([]byte("old"))
			} else {
				_, _ = writer.Write([]byte("new"))
			}
		case "/segment-2":
			_, _ = writer.Write([]byte("two"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "payload")
	checkpoint := &fragment.Checkpoint{Directory: filepath.Join(root, "checkpoint"), ResumeIdentity: "dash:incomplete:restart"}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireStaticSingleRepresentation: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1}).Download(ctx, server.URL+"/manifest.mpd", root, destination, true, events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindFragmentCompleted && event.Fragment == 1 {
			cancel()
		}
		return nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first run error=%v, want cancellation", err)
	}
	generation.Store(1)
	result, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireStaticSingleRepresentation: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1}).Download(context.Background(), server.URL+"/manifest.mpd", root, destination, true, nil)
	if err != nil {
		t.Fatalf("incomplete canonical static DASH must download: %v", err)
	}
	if result.Tracks[0].Download.Reused != 0 || firstCalls.Load() != 2 {
		t.Fatalf("result=%#v first calls=%d, want full restart", result, firstCalls.Load())
	}
	if body, err := os.ReadFile(destination); err != nil || string(body) != "newtwo" {
		t.Fatalf("output=%q err=%v", body, err)
	}
}

func TestDownloadSeparateAudioVideoTracks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.mpd":
			_, _ = fmt.Fprint(writer, `<MPD type="static" mediaPresentationDuration="PT2S"><Period>
<AdaptationSet contentType="video" mimeType="video/mp4"><SegmentTemplate duration="1" initialization="v-init" media="v-$Number$"/><Representation id="v" bandwidth="1000"/></AdaptationSet>
<AdaptationSet contentType="audio" mimeType="audio/mp4"><SegmentTemplate duration="1" initialization="a-init" media="a-$Number$"/><Representation id="a" bandwidth="128"/></AdaptationSet>
</Period></MPD>`)
		case "/v-init":
			_, _ = writer.Write([]byte("VI"))
		case "/v-1", "/v-2":
			_, _ = writer.Write([]byte(request.URL.Path[3:]))
		case "/a-init":
			_, _ = writer.Write([]byte("AI"))
		case "/a-1", "/a-2":
			_, _ = writer.Write([]byte(request.URL.Path[3:]))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "dash.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MergeRequired || len(result.Tracks) != 2 {
		t.Fatalf("result = %#v", result)
	}
	for _, track := range result.Tracks {
		contents, _ := os.ReadFile(track.Download.Path)
		if len(contents) != 4 {
			t.Fatalf("track %s contents = %q", track.Representation.ID, contents)
		}
	}
}

func TestDownloadDynamicMPDPollsAndDeduplicates(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.mpd" {
			repeat := 0
			if polls.Add(1) > 1 {
				repeat = 1
			}
			_, _ = fmt.Fprintf(writer, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video"><Representation id="v" bandwidth="1"><SegmentTemplate media="$Time$.m4s"><SegmentTimeline><S t="0" d="1" r="%d"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`, repeat)
			return
		}
		_, _ = writer.Write([]byte(request.URL.Path))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "live.bin"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	if string(contents) != "/0.m4s/1.m4s" || polls.Load() != 2 {
		t.Fatalf("contents = %q, polls = %d", contents, polls.Load())
	}
}

func TestDownloadRetriesTransientMPDWithoutDuplicateSegments(t *testing.T) {
	var manifestAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.mpd" {
			if manifestAttempts.Add(1) == 1 {
				writer.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = fmt.Fprint(writer, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video"><Representation id="v"><SegmentTemplate media="segment-$Number$"><SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`)
			return
		}
		_, _ = writer.Write([]byte(request.URL.Path))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "retry.bin")
	result, err := NewDownloader(transport, Config{DynamicPolls: 1, Attempts: 3, RetryBaseDelay: time.Millisecond}).Download(
		context.Background(), server.URL+"/live.mpd", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manifestAttempts.Load() != 2 {
		t.Fatalf("manifest attempts = %d, want 2", manifestAttempts.Load())
	}
	contents, readErr := os.ReadFile(result.Tracks[0].Download.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got, want := string(contents), "/segment-1"; got != want {
		t.Fatalf("contents = %q, want %q", got, want)
	}
}

func TestDynamicMPDPolicyDefaultsAllow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.mpd" {
			_, _ = fmt.Fprint(writer, `<MPD type="dynamic"><Period><AdaptationSet contentType="video"><Representation id="v"><SegmentTemplate media="segment.m4s"><SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`)
			return
		}
		_, _ = writer.Write([]byte("dynamic-allowed"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.bin"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, readErr := os.ReadFile(result.Tracks[0].Download.Path)
	if readErr != nil || string(contents) != "dynamic-allowed" {
		t.Fatalf("contents = %q, error = %v", contents, readErr)
	}
}

func TestDynamicMPDPolicyDenyRunsParseAndURLValidationBeforeTransfer(t *testing.T) {
	var nonManifestRequests atomic.Int32
	var validatedURLs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.mpd" {
			_, _ = fmt.Fprint(writer, `<MPD type="dynamic"><Period><AdaptationSet contentType="video"><Representation id="v"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="0-7"/></Representation></AdaptationSet></Period></MPD>`)
			return
		}
		nonManifestRequests.Add(1)
		http.Error(writer, "dynamic policy should stop before index/media", http.StatusInternalServerError)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{
		DynamicMPDPolicy: DynamicMPDPolicyDeny,
		URLValidator: func(string) error {
			validatedURLs.Add(1)
			return nil
		},
	}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.bin"), false, nil)
	if !errors.Is(err, ErrDynamicMPDUnsupported) {
		t.Fatalf("error = %v, want ErrDynamicMPDUnsupported", err)
	}
	if validatedURLs.Load() < 2 {
		t.Fatalf("validated URLs = %d, want manifest and representation URL validation", validatedURLs.Load())
	}
	if nonManifestRequests.Load() != 0 {
		t.Fatalf("index/media requests = %d, want 0", nonManifestRequests.Load())
	}
}

func TestDynamicMPDPolicyDenyLeavesStaticMPDUnaffected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/manifest.mpd" {
			_, _ = fmt.Fprint(writer, `<MPD mediaPresentationDuration="PT1S"><Period><AdaptationSet contentType="video"><Representation id="v"><SegmentTemplate media="segment.m4s" duration="1"/></Representation></AdaptationSet></Period></MPD>`)
			return
		}
		_, _ = writer.Write([]byte("static-allowed"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicMPDPolicy: DynamicMPDPolicyDeny}).Download(
		context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.bin"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, readErr := os.ReadFile(result.Tracks[0].Download.Path)
	if readErr != nil || string(contents) != "static-allowed" {
		t.Fatalf("contents = %q, error = %v", contents, readErr)
	}
}

func TestDownloadCancellationDuringMPDFetchDoesNotRetryOrCreateOutput(t *testing.T) {
	var manifestAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		manifestAttempts.Add(1)
		<-request.Context().Done()
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "cancel.bin")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := NewDownloader(transport, Config{Attempts: 5, RetryBaseDelay: time.Millisecond}).Download(
		ctx, server.URL+"/live.mpd", root, destination, false, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if manifestAttempts.Load() != 1 {
		t.Fatalf("manifest attempts = %d, want 1", manifestAttempts.Load())
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not exist: %v", statErr)
	}
}

func TestDownloadMPDRetryRedactsURLAndPreservesHeaderIsolation(t *testing.T) {
	var seenAuthorization atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seenAuthorization.Store(request.Header.Get("Authorization"))
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	header := http.Header{"Authorization": {"Bearer dash-secret"}}
	downloader := NewDownloader(transport, Config{Headers: header, Attempts: 2, RetryBaseDelay: time.Millisecond})
	header.Set("Authorization", "Bearer mutated-after-construction")
	root := t.TempDir()
	_, err := downloader.Download(context.Background(), server.URL+"/live.mpd?token=manifest-secret", root, filepath.Join(root, "out.bin"), false, nil)
	if err == nil {
		t.Fatal("expected transient MPD failure")
	}
	if got := seenAuthorization.Load().(string); got != "Bearer dash-secret" {
		t.Fatalf("Authorization = %q, want cloned header", got)
	}
	if strings.Contains(err.Error(), "manifest-secret") || strings.Contains(err.Error(), "dash-secret") || strings.Contains(err.Error(), "mutated-after-construction") {
		t.Fatalf("error leaked sensitive data: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP status 503") || !strings.Contains(err.Error(), "REDACTED") {
		t.Fatalf("error = %v, want redacted status evidence", err)
	}
}

func TestDownloadRejectsUnboundedProtocolRetryConfiguration(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	for name, test := range map[string]struct {
		config Config
		want   error
	}{
		"attempts":          {config: Config{Attempts: maxDASHAttempts + 1}, want: fragment.ErrTooManyAttempts},
		"negative attempts": {config: Config{Attempts: -1}, want: fragment.ErrTooManyAttempts},
		"delay":             {config: Config{RetryBaseDelay: maxDASHRetryDelay + time.Nanosecond}, want: fragment.ErrTooManyAttempts},
		"polls":             {config: Config{DynamicPolls: maxDASHPolls + 1}, want: ErrUnsupportedAddressing},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewDownloader(transport, test.config).Download(context.Background(), server.URL+"/live.mpd", t.TempDir(), filepath.Join(t.TempDir(), "out.bin"), false, nil)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want 0", requests.Load())
	}
}

func TestDownloadDynamicUsesManifestPollIntervalAndCancels(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		polls.Add(1)
		_, _ = fmt.Fprint(writer, `<MPD type="dynamic" minimumUpdatePeriod="PT5S"><Period><AdaptationSet contentType="video"><Representation id="v"><SegmentTemplate media="$Time$.m4s"><SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	destination := filepath.Join(root, "cancelled.bin")
	_, err := NewDownloader(transport, Config{DynamicPolls: 2}).Download(ctx, server.URL+"/live.mpd", root, destination, false, nil)
	if err != context.DeadlineExceeded {
		t.Fatalf("Download() error = %v", err)
	}
	if polls.Load() != 1 {
		t.Fatalf("polls = %d", polls.Load())
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not be finalized: %v", statErr)
	}
}

func TestDownloadDynamicDoesNotCollideSameRepresentationID(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.mpd" {
			t := polls.Add(1) - 1
			_, _ = fmt.Fprintf(writer, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period>
<AdaptationSet contentType="video"><Representation id="same" bandwidth="1000"><SegmentTemplate media="v-$Time$"><SegmentTimeline><S t="%d" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet>
<AdaptationSet contentType="audio"><Representation id="same" bandwidth="128"><SegmentTemplate media="a-$Time$"><SegmentTimeline><S t="%d" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet>
</Period></MPD>`, t, t)
			return
		}
		_, _ = writer.Write([]byte(request.URL.Path))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 2}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "same.bin"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, track := range result.Tracks {
		contents, readErr := os.ReadFile(track.Download.Path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		prefix := "/v-"
		if track.Representation.ContentType == "audio" {
			prefix = "/a-"
		}
		if got, want := string(contents), prefix+"0"+prefix+"1"; got != want {
			t.Fatalf("%s contents = %q, want %q", track.Representation.ContentType, got, want)
		}
	}
}
