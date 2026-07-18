package operations

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

func TestRecordAndIncidentDocumentsRoundTripAndRejectAmbiguity(t *testing.T) {
	suite := metricsSuite(t)
	records := []Record{metricRecord("public.youtube", OutcomeSuccess, FailureNone, 10), metricRecord("public.other", OutcomeBreakage, FailureExtractor, 20)}
	encoded, err := MarshalRecords(suite, records)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRecords(context.Background(), bytes.NewReader(encoded), 0, suite)
	if err != nil || len(decoded) != 2 {
		t.Fatalf("DecodeRecords() = %+v, %v", decoded, err)
	}

	incident := makeIncident(t, "incident.main", 23)
	encodedIncidents, err := MarshalIncidents(suite, []IncidentEvidence{incident})
	if err != nil {
		t.Fatal(err)
	}
	decodedIncidents, err := DecodeIncidents(context.Background(), bytes.NewReader(encodedIncidents), 0, suite)
	if err != nil || len(decodedIncidents) != 1 || decodedIncidents[0].Status != SLOMet24H {
		t.Fatalf("DecodeIncidents() = %+v, %v", decodedIncidents, err)
	}

	duplicate := []byte(`{"schema_version":1,"schema_version":1,"records":[]}`)
	if _, err := DecodeRecords(context.Background(), bytes.NewReader(duplicate), 0, suite); !errors.Is(err, ErrDecode) {
		t.Fatalf("duplicate error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DecodeIncidents(ctx, bytes.NewReader(encodedIncidents), 0, suite); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}
