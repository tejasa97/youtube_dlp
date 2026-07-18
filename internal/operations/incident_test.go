package operations

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMajorSiteDrillMatchesConformanceFixture(t *testing.T) {
	const (
		detected = int64(1_784_407_497_000) // 60fccbd author timestamp
		fixed    = int64(1_784_407_550_000) // 9ad13de author timestamp
	)
	drill, err := NewDrill("twitch-llhls-sequence-overflow-001", Record{
		CanaryID: "public.twitch", Class: ClassPublic, Extractor: "twitch",
		Outcome: OutcomeBreakage, Failure: FailureMedia,
		StartedUnixMS: detected,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := drill.Diagnose(fixed, DiagnosisMediaProtocol); err != nil {
		t.Fatal(err)
	}
	if err := drill.Patch(fixed, "9ad13deac4a7f81fe2ece83d94a53300e926bdaa"); err != nil {
		t.Fatal(err)
	}
	evidence, err := drill.Verify(Record{
		CanaryID: "public.twitch", Class: ClassPublic, Extractor: "twitch",
		Outcome: OutcomeSuccess, Failure: FailureNone, StartedUnixMS: fixed,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := MarshalIncident(evidence)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "conformance", "operations", "major_site_drill_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(got, '\n'), want) {
		t.Fatalf("fixture drift:\n got: %s\nwant: %s", got, want)
	}
	if _, err := MarshalIncidents(fixtureSuite(t), []IncidentEvidence{evidence}); err != nil {
		t.Fatalf("attributable incident is not bound to the canary suite: %v", err)
	}
}

func TestDrillSLOBoundaries(t *testing.T) {
	for _, test := range []struct {
		name   string
		hours  int64
		status SLOStatus
	}{
		{"24 hours", 24, SLOMet24H},
		{"30 hours", 30, SLOMet48H},
		{"48 hours", 48, SLOMet48H},
		{"49 hours", 49, SLOMissed48H},
	} {
		t.Run(test.name, func(t *testing.T) {
			const detected = int64(10_000)
			drill, err := NewDrill("incident.boundary", breakageRecord(detected))
			if err != nil {
				t.Fatal(err)
			}
			if err := drill.Diagnose(detected, DiagnosisUnknown); err != nil {
				t.Fatal(err)
			}
			if err := drill.Patch(detected, "abcdef0"); err != nil {
				t.Fatal(err)
			}
			evidence, err := drill.Verify(successRecord(detected + test.hours*hourMS))
			if err != nil {
				t.Fatal(err)
			}
			if evidence.Status != test.status {
				t.Fatalf("status = %q, want %q", evidence.Status, test.status)
			}
		})
	}
}

func TestDrillRejectsInvalidTransitions(t *testing.T) {
	const detected = int64(100_000)
	drill, err := NewDrill("incident.transitions", breakageRecord(detected))
	if err != nil {
		t.Fatal(err)
	}
	if err := drill.Patch(detected, "abcdef0"); err != ErrInvalidDrill {
		t.Fatalf("Patch before diagnosis = %v", err)
	}
	if err := drill.Diagnose(detected-1, DiagnosisAPIChange); err != ErrInvalidDrill {
		t.Fatalf("non-monotonic diagnosis = %v", err)
	}
	if err := drill.Diagnose(detected, DiagnosisAPIChange); err != nil {
		t.Fatal(err)
	}
	if err := drill.Patch(detected, "not-a-hash"); err != ErrInvalidDrill {
		t.Fatalf("malformed patch = %v", err)
	}
	if err := drill.Patch(detected+1, "abcdef0"); err != nil {
		t.Fatal(err)
	}
	wrong := successRecord(detected + 2)
	wrong.CanaryID = "public.other"
	if _, err := drill.Verify(wrong); err != ErrInvalidDrill {
		t.Fatalf("wrong canary = %v", err)
	}
	if _, err := drill.Verify(successRecord(detected)); err != ErrInvalidDrill {
		t.Fatalf("verification before patch = %v", err)
	}
}

func breakageRecord(detected int64) Record {
	return Record{CanaryID: "public.youtube", Class: ClassPublic, Extractor: "youtube", Outcome: OutcomeBreakage, Failure: FailureExtractor, StartedUnixMS: detected}
}

func successRecord(started int64) Record {
	return Record{CanaryID: "public.youtube", Class: ClassPublic, Extractor: "youtube", Outcome: OutcomeSuccess, Failure: FailureNone, StartedUnixMS: started}
}

func FuzzDrillTransitions(f *testing.F) {
	f.Add(int64(0), int64(0), int64(0))
	f.Add(int64(2*hourMS), int64(10*hourMS), int64(23*hourMS))
	f.Fuzz(func(t *testing.T, diagnosisOffset, patchOffset, verifyOffset int64) {
		const detected = int64(1_000_000)
		drill, err := NewDrill("incident.fuzz", breakageRecord(detected))
		if err != nil {
			t.Fatal(err)
		}
		if diagnosisOffset < 0 || diagnosisOffset > maxIncidentLatencyMS {
			return
		}
		if err := drill.Diagnose(detected+diagnosisOffset, DiagnosisUnknown); err != nil {
			t.Fatal(err)
		}
		if patchOffset < diagnosisOffset || patchOffset > maxIncidentLatencyMS {
			return
		}
		if err := drill.Patch(detected+patchOffset, "abcdef0"); err != nil {
			t.Fatal(err)
		}
		if verifyOffset < patchOffset || verifyOffset > maxIncidentLatencyMS {
			return
		}
		evidence, err := drill.Verify(successRecord(detected + verifyOffset))
		if err != nil {
			t.Fatal(err)
		}
		if !validIncident(evidence) {
			t.Fatal("valid transition produced invalid evidence")
		}
	})
}
