package telemetry

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDecodeStrictRoundTripAndLimits(t *testing.T) {
	canonical := `{"schema_version":1,"counts":[{"extractor":"youtube","capability":"extract","outcome":"success","count":2}],"overflow":{"cell_limit":1,"counter_saturation":0}}`
	snapshot, err := Decode(context.Background(), strings.NewReader(canonical), int64(len(canonical)))
	if err != nil {
		t.Fatal(err)
	}
	data, err := MarshalCanonical(snapshot)
	if err != nil || string(data) != canonical {
		t.Fatalf("round trip = %s, %v", data, err)
	}
	if _, err := Decode(context.Background(), strings.NewReader(canonical), int64(len(canonical)-1)); !errors.Is(err, ErrDecodeLimit) {
		t.Fatalf("byte limit error = %v", err)
	}
	if _, err := Decode(context.Background(), strings.NewReader(canonical), HardMaxSnapshotBytes+1); !errors.Is(err, ErrDecodeLimit) {
		t.Fatalf("hard limit error = %v", err)
	}
}

func TestDecodeRejectsMalformedAmbiguousOrSensitiveDimensions(t *testing.T) {
	tests := []string{
		``,
		`{"schema_version":1,"overflow":{"cell_limit":0,"counter_saturation":0}}`,
		`{"schema_version":1,"counts":[],"overflow":{"cell_limit":0}}`,
		`{"schema_version":1,"counts":null,"overflow":{"cell_limit":0,"counter_saturation":0}}`,
		`{"schema_version":1,"counts":[],"overflow":{"cell_limit":0,"counter_saturation":0},"unknown":1}`,
		`{"schema_version":1,"counts":[],"overflow":{"cell_limit":0,"counter_saturation":0}} {}`,
		`{"schema_version":2,"counts":[],"overflow":{"cell_limit":0,"counter_saturation":0}}`,
		`{"schema_version":1,"counts":[{"extractor":"https://example.test/private?token=x","capability":"extract","outcome":"error","count":1}],"overflow":{"cell_limit":0,"counter_saturation":0}}`,
		`{"schema_version":1,"counts":[{"extractor":"youtube","capability":"extract","outcome":"raw-error-text","count":1}],"overflow":{"cell_limit":0,"counter_saturation":0}}`,
		`{"schema_version":1,"counts":[{"extractor":"youtube","capability":"extract","outcome":"error","count":0}],"overflow":{"cell_limit":0,"counter_saturation":0}}`,
		`{"schema_version":1,"counts":[{"extractor":"youtube","capability":"extract","outcome":"error","count":1},{"extractor":"youtube","capability":"extract","outcome":"error","count":2}],"overflow":{"cell_limit":0,"counter_saturation":0}}`,
	}
	for _, input := range tests {
		if _, err := Decode(context.Background(), strings.NewReader(input), 4096); !errors.Is(err, ErrInvalidSnapshot) {
			t.Fatalf("Decode(%q) error = %v", input, err)
		}
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{"schema_version":1,"counts":[],"overflow":{"cell_limit":0,"counter_saturation":0}}`))
	f.Add([]byte(`{"schema_version":1,"counts":[{"extractor":"youtube","capability":"extract","outcome":"success","count":1}],"overflow":{"cell_limit":0,"counter_saturation":0}}`))
	f.Add([]byte(`not-json`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		snapshot, err := Decode(context.Background(), bytes.NewReader(data), 1<<20)
		if err != nil {
			return
		}
		canonical, err := MarshalCanonical(snapshot)
		if err != nil {
			t.Fatalf("decoded snapshot did not marshal: %v", err)
		}
		roundTrip, err := Decode(context.Background(), bytes.NewReader(canonical), 1<<20)
		if err != nil {
			t.Fatalf("canonical snapshot did not decode: %v", err)
		}
		again, err := MarshalCanonical(roundTrip)
		if err != nil || !bytes.Equal(canonical, again) {
			t.Fatalf("canonical encoding unstable: %v", err)
		}
	})
}

func FuzzMerge(f *testing.F) {
	f.Add([]byte(`{"schema_version":1,"counts":[],"overflow":{"cell_limit":0,"counter_saturation":0}}`))
	f.Add([]byte(`{"schema_version":1,"counts":[{"extractor":"youtube","capability":"extract","outcome":"success","count":1}],"overflow":{"cell_limit":0,"counter_saturation":0}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		snapshot, err := Decode(context.Background(), bytes.NewReader(data), 1<<20)
		if err != nil {
			return
		}
		aggregator := testAggregator(t, 8)
		_ = aggregator.Merge(context.Background(), snapshot)
		if _, err := MarshalCanonical(aggregator.Snapshot()); err != nil {
			t.Fatalf("merged state is invalid: %v", err)
		}
	})
}
