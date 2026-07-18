package telemetry

import (
	"bytes"
	"context"
	"errors"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/quick"
)

func testAggregator(t *testing.T, maxCells int) *Aggregator {
	t.Helper()
	aggregator, err := New(Config{
		Extractors:   []string{"generic", "twitch", "youtube"},
		Capabilities: []string{"download", "extract", "live"},
		MaxCells:     maxCells,
	})
	if err != nil {
		t.Fatal(err)
	}
	return aggregator
}

func TestObserveSnapshotResetAndCanonicalJSON(t *testing.T) {
	aggregator := testAggregator(t, 12)
	observations := []struct {
		extractor, capability string
		outcome               Outcome
	}{
		{"youtube", "extract", OutcomeSuccess},
		{"generic", "download", OutcomeFallback},
		{"youtube", "extract", OutcomeSuccess},
		{"twitch", "live", OutcomeUnsupported},
		{"twitch", "extract", OutcomeError},
	}
	for _, observation := range observations {
		if err := aggregator.Observe(context.Background(), observation.extractor, observation.capability, observation.outcome); err != nil {
			t.Fatal(err)
		}
	}

	snapshot := aggregator.Snapshot()
	data, err := MarshalCanonical(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schema_version":1,"counts":[{"extractor":"generic","capability":"download","outcome":"fallback","count":1},{"extractor":"twitch","capability":"extract","outcome":"error","count":1},{"extractor":"twitch","capability":"live","outcome":"unsupported","count":1},{"extractor":"youtube","capability":"extract","outcome":"success","count":2}],"overflow":{"cell_limit":0,"counter_saturation":0}}`
	if string(data) != want {
		t.Fatalf("canonical snapshot:\n got %s\nwant %s", data, want)
	}
	if reset := aggregator.Reset(); !reflect.DeepEqual(reset, snapshot) {
		t.Fatalf("reset snapshot = %#v, want %#v", reset, snapshot)
	}
	if after := aggregator.Snapshot(); len(after.Counts) != 0 || after.Overflow != (Overflow{}) {
		t.Fatalf("snapshot after reset = %#v", after)
	}
}

func TestEveryOutcomeIsExplicit(t *testing.T) {
	aggregator := testAggregator(t, 4)
	for _, outcome := range []Outcome{OutcomeSuccess, OutcomeError, OutcomeFallback, OutcomeUnsupported} {
		if err := aggregator.Observe(context.Background(), "youtube", "extract", outcome); err != nil {
			t.Fatal(err)
		}
	}
	if got := aggregator.Snapshot().Counts; len(got) != 4 {
		t.Fatalf("counts = %#v", got)
	}
}

func TestConfigurationAndObservationsRejectUnboundedLabelsWithoutLeaks(t *testing.T) {
	secret := "https://user:password@example.test/private/title?token=secret"
	badConfigs := []Config{
		{},
		{Extractors: []string{"youtube", "youtube"}, Capabilities: []string{"extract"}},
		{Extractors: []string{secret}, Capabilities: []string{"extract"}},
		{Extractors: []string{"youtube"}, Capabilities: []string{"user supplied label"}},
		{Extractors: []string{"youtube"}, Capabilities: []string{"extract"}, MaxCells: hardMaxCells + 1},
	}
	for _, config := range badConfigs {
		_, err := New(config)
		if !errors.Is(err, ErrInvalidConfig) || strings.Contains(err.Error(), secret) {
			t.Fatalf("New() error = %q", err)
		}
	}

	aggregator := testAggregator(t, 4)
	for _, test := range []struct {
		err  error
		call func() error
	}{
		{ErrUnknownExtractor, func() error { return aggregator.Observe(context.Background(), secret, "extract", OutcomeError) }},
		{ErrUnknownCapability, func() error { return aggregator.Observe(context.Background(), "youtube", secret, OutcomeError) }},
		{ErrInvalidOutcome, func() error { return aggregator.Observe(context.Background(), "youtube", "extract", Outcome(secret)) }},
	} {
		err := test.call()
		if !errors.Is(err, test.err) || strings.Contains(err.Error(), secret) {
			t.Fatalf("Observe() error = %q", err)
		}
	}
	data, err := MarshalCanonical(aggregator.Snapshot())
	if err != nil || bytes.Contains(data, []byte(secret)) {
		t.Fatalf("state leaked rejected value: %s (%v)", data, err)
	}
}

func TestBoundsAndSaturationAreAccounted(t *testing.T) {
	aggregator := testAggregator(t, 1)
	if err := aggregator.Observe(context.Background(), "youtube", "extract", OutcomeSuccess); err != nil {
		t.Fatal(err)
	}
	if err := aggregator.Observe(context.Background(), "twitch", "live", OutcomeSuccess); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
	if got := aggregator.Snapshot().Overflow.CellLimit; got != 1 {
		t.Fatalf("cell overflow = %d", got)
	}

	aggregator.Reset()
	err := aggregator.Merge(context.Background(), Snapshot{
		SchemaVersion: SchemaVersion,
		Counts:        []Count{{Extractor: "youtube", Capability: "extract", Outcome: OutcomeSuccess, Count: math.MaxUint64}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := aggregator.Observe(context.Background(), "youtube", "extract", OutcomeSuccess); !errors.Is(err, ErrCounterSaturated) {
		t.Fatalf("saturation error = %v", err)
	}
	if got := aggregator.Snapshot().Overflow.CounterSaturation; got != 1 {
		t.Fatalf("counter overflow = %d", got)
	}
}

func TestMergeIsDeterministicBoundedAndAtomicOnFailure(t *testing.T) {
	incoming := Snapshot{SchemaVersion: SchemaVersion, Counts: []Count{
		{Extractor: "youtube", Capability: "live", Outcome: OutcomeSuccess, Count: 7},
		{Extractor: "generic", Capability: "download", Outcome: OutcomeError, Count: 3},
	}, Overflow: Overflow{CellLimit: 2}}
	left := testAggregator(t, 1)
	right := testAggregator(t, 1)
	if err := left.Merge(context.Background(), incoming); err != nil {
		t.Fatal(err)
	}
	incoming.Counts[0], incoming.Counts[1] = incoming.Counts[1], incoming.Counts[0]
	if err := right.Merge(context.Background(), incoming); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left.Snapshot(), right.Snapshot()) {
		t.Fatalf("merge order changed result: %#v != %#v", left.Snapshot(), right.Snapshot())
	}
	if got := left.Snapshot().Overflow.CellLimit; got != 9 {
		t.Fatalf("merged overflow = %d, want 9", got)
	}

	before := left.Snapshot()
	invalid := Snapshot{SchemaVersion: SchemaVersion, Counts: []Count{{Extractor: "secret-title", Capability: "extract", Outcome: OutcomeSuccess, Count: 1}}}
	if err := left.Merge(context.Background(), invalid); !errors.Is(err, ErrUnknownExtractor) {
		t.Fatalf("invalid merge error = %v", err)
	}
	if !reflect.DeepEqual(left.Snapshot(), before) {
		t.Fatal("invalid merge mutated aggregator")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := left.Merge(canceled, incoming); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled merge error = %v", err)
	}
	if !reflect.DeepEqual(left.Snapshot(), before) {
		t.Fatal("canceled merge mutated aggregator")
	}
}

func TestConcurrentObservation(t *testing.T) {
	aggregator := testAggregator(t, 8)
	const goroutines = 32
	const observations = 500
	var wait sync.WaitGroup
	wait.Add(goroutines)
	for range goroutines {
		go func() {
			defer wait.Done()
			for range observations {
				if err := aggregator.Observe(context.Background(), "youtube", "extract", OutcomeSuccess); err != nil {
					t.Errorf("Observe() error = %v", err)
					return
				}
			}
		}()
	}
	wait.Wait()
	if got := aggregator.Snapshot().Counts[0].Count; got != goroutines*observations {
		t.Fatalf("concurrent count = %d", got)
	}
}

func TestCancellation(t *testing.T) {
	aggregator := testAggregator(t, 4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := aggregator.Observe(ctx, "youtube", "extract", OutcomeSuccess); !errors.Is(err, context.Canceled) {
		t.Fatalf("Observe() error = %v", err)
	}
	var output bytes.Buffer
	if err := WriteCanonical(ctx, &output, aggregator.Snapshot()); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteCanonical() error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatal("canceled write emitted data")
	}
}

func TestCanonicalOrderingProperty(t *testing.T) {
	property := func(seed uint64) bool {
		observations := []struct {
			extractor, capability string
			outcome               Outcome
		}{
			{"youtube", "extract", OutcomeSuccess},
			{"generic", "download", OutcomeFallback},
			{"twitch", "live", OutcomeError},
			{"youtube", "download", OutcomeUnsupported},
		}
		random := rand.New(rand.NewSource(int64(seed)))
		random.Shuffle(len(observations), func(i, j int) { observations[i], observations[j] = observations[j], observations[i] })
		aggregator := testAggregator(t, 8)
		for _, observation := range observations {
			if aggregator.Observe(context.Background(), observation.extractor, observation.capability, observation.outcome) != nil {
				return false
			}
		}
		data, err := MarshalCanonical(aggregator.Snapshot())
		if err != nil {
			return false
		}
		if seed == 0 {
			return len(data) > 0
		}
		baseline := testAggregator(t, 8)
		for _, observation := range []struct {
			extractor, capability string
			outcome               Outcome
		}{
			{"youtube", "extract", OutcomeSuccess}, {"generic", "download", OutcomeFallback},
			{"twitch", "live", OutcomeError}, {"youtube", "download", OutcomeUnsupported},
		} {
			_ = baseline.Observe(context.Background(), observation.extractor, observation.capability, observation.outcome)
		}
		baselineData, _ := MarshalCanonical(baseline.Snapshot())
		return bytes.Equal(data, baselineData)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestConformanceFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "conformance", "telemetry", "snapshot_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Decode(context.Background(), bytes.NewReader(data), 4096)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := MarshalCanonical(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != string(canonical) {
		t.Fatalf("fixture is not canonical:\n%s", canonical)
	}
}
