package telemetry

import (
	"math"
	"testing"
)

func TestCalculateCoverageKeepsFailuresFallbackAndOverflowInDenominator(t *testing.T) {
	coverage, err := CalculateCoverage(Snapshot{
		SchemaVersion: SchemaVersion,
		Counts: []Count{
			{Extractor: "fixture", Capability: "extract", Outcome: OutcomeSuccess, Count: 95},
			{Extractor: "fixture", Capability: "extract", Outcome: OutcomeError, Count: 1},
			{Extractor: "fixture", Capability: "extract", Outcome: OutcomeUnsupported, Count: 1},
			{Extractor: "fixture", Capability: "extract", Outcome: OutcomeFallback, Count: 1},
		},
		Overflow: Overflow{CellLimit: 1, CounterSaturation: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !coverage.Exact || coverage.Denominator != 100 || coverage.Successful != 95 || coverage.BasisPoints != 9500 || coverage.Overflow != 2 {
		t.Fatalf("coverage=%+v", coverage)
	}
}

func TestCalculateCoverageMarksSaturatedTotalsInexact(t *testing.T) {
	coverage, err := CalculateCoverage(Snapshot{
		SchemaVersion: SchemaVersion,
		Counts: []Count{
			{Extractor: "a", Capability: "extract", Outcome: OutcomeSuccess, Count: math.MaxUint64},
			{Extractor: "b", Capability: "extract", Outcome: OutcomeSuccess, Count: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if coverage.Exact || coverage.BasisPoints != 0 || coverage.Denominator != math.MaxUint64 {
		t.Fatalf("coverage=%+v", coverage)
	}
}

func TestCalculateCoverageRejectsInvalidSnapshot(t *testing.T) {
	if _, err := CalculateCoverage(Snapshot{}); err != ErrInvalidSnapshot {
		t.Fatalf("error=%v", err)
	}
}
