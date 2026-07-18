package operations

import (
	"bytes"
	"sync"
	"testing"
)

func TestRollingMetricsWindowAndCanonicalSnapshot(t *testing.T) {
	suite := metricsSuite(t)
	metrics, err := NewRollingMetrics(suite, 3, 2)
	if err != nil {
		t.Fatal(err)
	}
	records := []Record{
		metricRecord("public.youtube", OutcomeBreakage, FailureExtractor, 1),
		metricRecord("public.other", OutcomeSuccess, FailureNone, 2),
		metricRecord("public.youtube", OutcomeFallback, FailureNone, 3),
		metricRecord("public.youtube", OutcomeSuccess, FailureNone, 4),
	}
	for _, record := range records {
		if err := metrics.AddRecord(record); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := metrics.Snapshot()
	if snapshot.Counts.Total != 3 || snapshot.Counts.Success != 2 || snapshot.Counts.Fallback != 1 || snapshot.Counts.Breakage != 0 {
		t.Fatalf("rolling counts = %+v", snapshot.Counts)
	}
	if snapshot.Counts.SuccessBasisPoints != 6666 || snapshot.Counts.FallbackBasisPoints != 3333 {
		t.Fatalf("basis points = %+v", snapshot.Counts)
	}
	if len(snapshot.ByCanary) != 2 || snapshot.ByCanary[0].CanaryID != "public.other" || snapshot.ByCanary[1].CanaryID != "public.youtube" {
		t.Fatalf("by-canary order = %+v", snapshot.ByCanary)
	}

	incident := makeIncident(t, "incident.metrics.24", 20)
	if err := metrics.AddIncident(incident); err != nil {
		t.Fatal(err)
	}
	incident = makeIncident(t, "incident.metrics.48", 30)
	if err := metrics.AddIncident(incident); err != nil {
		t.Fatal(err)
	}
	incident = makeIncident(t, "incident.metrics.miss", 50)
	if err := metrics.AddIncident(incident); err != nil {
		t.Fatal(err)
	}
	snapshot = metrics.Snapshot()
	if snapshot.Patch.Samples != 2 || snapshot.Patch.Met48H != 1 || snapshot.Patch.Missed48H != 1 || snapshot.Patch.Met24H != 0 || snapshot.Patch.MaxLatencyMS != uint64(50*hourMS) {
		t.Fatalf("rolling patch metrics = %+v", snapshot.Patch)
	}

	first, err := MarshalMetrics(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.ByCanary[0], snapshot.ByCanary[1] = snapshot.ByCanary[1], snapshot.ByCanary[0]
	second, err := MarshalMetrics(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("non-deterministic metrics:\n%s\n%s", first, second)
	}

	prior := metrics.Reset()
	if prior.Counts.Total != 3 || prior.Patch.Samples != 2 {
		t.Fatalf("reset prior = %+v", prior)
	}
	if after := metrics.Snapshot(); after.Counts.Total != 0 || after.Patch.Samples != 0 {
		t.Fatalf("reset after = %+v", after)
	}
}

func TestRollingMetricsRejectsUnconfiguredData(t *testing.T) {
	suite := metricsSuite(t)
	metrics, err := NewRollingMetrics(suite, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	record := metricRecord("public.youtube", OutcomeSuccess, FailureNone, 1)
	record.Capability = "unconfigured"
	if err := metrics.AddRecord(record); err != ErrInvalidOutcome {
		t.Fatalf("capability error = %v", err)
	}
	record = metricRecord("public.unknown", OutcomeSuccess, FailureNone, 1)
	if err := metrics.AddRecord(record); err != ErrInvalidOutcome {
		t.Fatalf("unknown canary error = %v", err)
	}
	evidence := makeIncident(t, "incident.unknown", 1)
	evidence.CanaryID = "public.unknown"
	if err := metrics.AddIncident(evidence); err != ErrInvalidDrill {
		t.Fatalf("unknown incident error = %v", err)
	}

	duplicate := MetricsSnapshot{SchemaVersion: SchemaVersion, ByCanary: []CanaryCounts{
		{CanaryID: "public.youtube", Counts: OutcomeCounts{Total: 1, Success: 1, SuccessBasisPoints: 10_000}},
		{CanaryID: "public.youtube", Counts: OutcomeCounts{Total: 1, Success: 1, SuccessBasisPoints: 10_000}},
	}}
	if _, err := MarshalMetrics(duplicate); err != ErrInvalidOutcome {
		t.Fatalf("duplicate metrics error = %v", err)
	}
}

func TestRollingMetricsConcurrentWriters(t *testing.T) {
	metrics, err := NewRollingMetrics(metricsSuite(t), 5_000, 10)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for index := 0; index < 100; index++ {
				if err := metrics.AddRecord(metricRecord("public.youtube", OutcomeSuccess, FailureNone, int64(worker*100+index))); err != nil {
					t.Errorf("AddRecord: %v", err)
				}
			}
		}(worker)
	}
	wait.Wait()
	if got := metrics.Snapshot().Counts.Total; got != 3_200 {
		t.Fatalf("total = %d", got)
	}
}

func metricsSuite(t *testing.T) Suite {
	t.Helper()
	suite, err := NewSuite([]CanarySpec{
		{ID: "public.youtube", Class: ClassPublic, Extractor: "youtube", TargetRef: "youtube.smoke", Capabilities: []string{"metadata"}, TimeoutMS: 1_000},
		{ID: "public.other", Class: ClassPublic, Extractor: "other", TargetRef: "other.smoke", Capabilities: []string{"metadata"}, TimeoutMS: 1_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	return suite
}

func metricRecord(id string, outcome Outcome, failure FailureClass, started int64) Record {
	extractor := "youtube"
	if id == "public.other" {
		extractor = "other"
	}
	return Record{CanaryID: id, Class: ClassPublic, Extractor: extractor, Outcome: outcome, Failure: failure, StartedUnixMS: started}
}

func makeIncident(t *testing.T, id string, hours int64) IncidentEvidence {
	t.Helper()
	const detected = int64(100_000)
	drill, err := NewDrill(id, breakageRecord(detected))
	if err != nil {
		t.Fatal(err)
	}
	if err := drill.Diagnose(detected, DiagnosisUnknown); err != nil {
		t.Fatal(err)
	}
	if err := drill.Patch(detected, "abcdef0"); err != nil {
		t.Fatal(err)
	}
	evidence, err := drill.Verify(successRecord(detected + hours*hourMS))
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}
