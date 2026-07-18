package ytdlp

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/testserver"
)

func TestTelemetryCollectorProductIntegrationAndCanonicalRoundTrip(t *testing.T) {
	collector, err := NewTelemetryCollector(TelemetryConfig{Extractors: []string{"fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(WithTelemetryCollector(collector))
	_, err = client.Run(context.Background(), Request{URL: "not-a-url"})
	if err == nil {
		t.Fatal("invalid URL succeeded")
	}
	server := testserver.New()
	defer server.Close()
	_, err = client.Run(context.Background(), Request{URL: server.URL + "/page", SkipDownload: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := collector.Snapshot()
	if len(snapshot.Counts) != 2 || snapshot.Counts[0].Extractor != "fixture" || snapshot.Counts[0].Outcome != TelemetryOutcomeSuccess || snapshot.Counts[1].Extractor != TelemetryUnknownExtractor || snapshot.Counts[1].Outcome != TelemetryOutcomeUnsupported {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	var encoded bytes.Buffer
	if err := collector.WriteCanonical(context.Background(), &encoded); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded.String(), server.URL) || strings.Contains(encoded.String(), "not-a-url") {
		t.Fatalf("telemetry exposed operation input: %s", encoded.String())
	}
	decoded, err := DecodeTelemetrySnapshot(context.Background(), &encoded, 0)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := NewTelemetryCollector(TelemetryConfig{Extractors: []string{"fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := merged.Merge(context.Background(), decoded); err != nil {
		t.Fatal(err)
	}
	if got := merged.Snapshot(); len(got.Counts) != 2 {
		t.Fatalf("merged=%+v", got)
	}
	coverage, err := merged.Coverage()
	if err != nil {
		t.Fatal(err)
	}
	if !coverage.Exact || coverage.Denominator != 2 || coverage.Successful != 1 || coverage.BasisPoints != 5000 {
		t.Fatalf("coverage=%+v", coverage)
	}
}

func TestTelemetryIsDisabledByDefaultAndUnknownExtractorIsBounded(t *testing.T) {
	client := NewClient()
	_, _ = client.Run(context.Background(), Request{URL: "not-a-url"})

	collector, err := NewTelemetryCollector(TelemetryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	collector.observe("plugin-secret-shaped-name", TelemetryOutcomeError)
	snapshot := collector.Snapshot()
	if len(snapshot.Counts) != 1 || snapshot.Counts[0].Extractor != TelemetryUnknownExtractor {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}
