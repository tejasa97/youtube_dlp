package operations

import (
	"math"
	"regexp"
	"sync"
)

const (
	hourMS               = int64(60 * 60 * 1000)
	maxIncidentLatencyMS = int64(365 * 24 * 60 * 60 * 1000)
)

type Diagnosis string

const (
	DiagnosisExtractorDrift Diagnosis = "extractor_drift"
	DiagnosisAPIChange      Diagnosis = "api_change"
	DiagnosisAuthChange     Diagnosis = "auth_change"
	DiagnosisRegionChange   Diagnosis = "region_change"
	DiagnosisMediaProtocol  Diagnosis = "media_protocol"
	DiagnosisUnknown        Diagnosis = "unknown"
)

func (diagnosis Diagnosis) valid() bool {
	switch diagnosis {
	case DiagnosisExtractorDrift, DiagnosisAPIChange, DiagnosisAuthChange,
		DiagnosisRegionChange, DiagnosisMediaProtocol, DiagnosisUnknown:
		return true
	default:
		return false
	}
}

type SLOStatus string

const (
	SLOMet24H    SLOStatus = "met_24h"
	SLOMet48H    SLOStatus = "met_48h"
	SLOMissed48H SLOStatus = "missed_48h"
)

var patchRefPattern = regexp.MustCompile(`^[a-f0-9]{7,64}$`)

// IncidentEvidence contains only bounded identifiers, classifications, and
// timestamps. It is safe to aggregate and export as operational evidence.
type IncidentEvidence struct {
	IncidentID         string    `json:"incident_id"`
	CanaryID           string    `json:"canary_id"`
	Extractor          string    `json:"extractor"`
	Diagnosis          Diagnosis `json:"diagnosis"`
	PatchRef           string    `json:"patch_ref"`
	DetectedUnixMS     int64     `json:"detected_unix_ms"`
	DiagnosedUnixMS    int64     `json:"diagnosed_unix_ms"`
	PatchedUnixMS      int64     `json:"patched_unix_ms"`
	VerifiedUnixMS     int64     `json:"verified_unix_ms"`
	DiagnosisLatencyMS int64     `json:"diagnosis_latency_ms"`
	PatchLatencyMS     int64     `json:"patch_latency_ms"`
	Status             SLOStatus `json:"slo_status"`
}

// Drill is a deterministic state machine: breakage -> diagnosis -> patch ->
// successful verification. Transitions must use monotonic timestamps.
type Drill struct {
	mu       sync.Mutex
	evidence IncidentEvidence
	stage    uint8
}

func NewDrill(incidentID string, breakage Record) (*Drill, error) {
	if !identifierPattern.MatchString(incidentID) || !validRecord(breakage) || breakage.Outcome != OutcomeBreakage || breakage.Failure == FailureNone {
		return nil, ErrInvalidDrill
	}
	if breakage.StartedUnixMS > math.MaxInt64-breakage.DurationMS {
		return nil, ErrInvalidDrill
	}
	detected := breakage.StartedUnixMS + breakage.DurationMS
	return &Drill{evidence: IncidentEvidence{
		IncidentID: incidentID, CanaryID: breakage.CanaryID, Extractor: breakage.Extractor,
		DetectedUnixMS: detected,
	}, stage: 1}, nil
}

func (drill *Drill) Diagnose(atUnixMS int64, diagnosis Diagnosis) error {
	drill.mu.Lock()
	defer drill.mu.Unlock()
	if drill.stage != 1 || !diagnosis.valid() || atUnixMS < drill.evidence.DetectedUnixMS {
		return ErrInvalidDrill
	}
	drill.evidence.Diagnosis = diagnosis
	drill.evidence.DiagnosedUnixMS = atUnixMS
	drill.stage = 2
	return nil
}

func (drill *Drill) Patch(atUnixMS int64, patchRef string) error {
	drill.mu.Lock()
	defer drill.mu.Unlock()
	if drill.stage != 2 || !patchRefPattern.MatchString(patchRef) || atUnixMS < drill.evidence.DiagnosedUnixMS {
		return ErrInvalidDrill
	}
	drill.evidence.PatchedUnixMS = atUnixMS
	drill.evidence.PatchRef = patchRef
	drill.stage = 3
	return nil
}

func (drill *Drill) Verify(success Record) (IncidentEvidence, error) {
	drill.mu.Lock()
	defer drill.mu.Unlock()
	if !validRecord(success) || success.StartedUnixMS > math.MaxInt64-success.DurationMS {
		return IncidentEvidence{}, ErrInvalidDrill
	}
	verified := success.StartedUnixMS + success.DurationMS
	if drill.stage != 3 || success.Outcome != OutcomeSuccess || success.Failure != FailureNone ||
		success.CanaryID != drill.evidence.CanaryID || success.Extractor != drill.evidence.Extractor ||
		verified < drill.evidence.PatchedUnixMS {
		return IncidentEvidence{}, ErrInvalidDrill
	}
	drill.evidence.VerifiedUnixMS = verified
	drill.evidence.DiagnosisLatencyMS = drill.evidence.DiagnosedUnixMS - drill.evidence.DetectedUnixMS
	drill.evidence.PatchLatencyMS = verified - drill.evidence.DetectedUnixMS
	if drill.evidence.PatchLatencyMS > maxIncidentLatencyMS {
		drill.evidence.VerifiedUnixMS = 0
		drill.evidence.DiagnosisLatencyMS = 0
		drill.evidence.PatchLatencyMS = 0
		return IncidentEvidence{}, ErrInvalidDrill
	}
	switch {
	case drill.evidence.PatchLatencyMS <= 24*hourMS:
		drill.evidence.Status = SLOMet24H
	case drill.evidence.PatchLatencyMS <= 48*hourMS:
		drill.evidence.Status = SLOMet48H
	default:
		drill.evidence.Status = SLOMissed48H
	}
	drill.stage = 4
	return drill.evidence, nil
}

func validIncident(evidence IncidentEvidence) bool {
	if !identifierPattern.MatchString(evidence.IncidentID) || !identifierPattern.MatchString(evidence.CanaryID) ||
		!identifierPattern.MatchString(evidence.Extractor) || !evidence.Diagnosis.valid() ||
		!patchRefPattern.MatchString(evidence.PatchRef) || evidence.DetectedUnixMS < 0 ||
		evidence.DiagnosedUnixMS < evidence.DetectedUnixMS || evidence.PatchedUnixMS < evidence.DiagnosedUnixMS ||
		evidence.VerifiedUnixMS < evidence.PatchedUnixMS || evidence.DiagnosisLatencyMS != evidence.DiagnosedUnixMS-evidence.DetectedUnixMS ||
		evidence.PatchLatencyMS != evidence.VerifiedUnixMS-evidence.DetectedUnixMS || evidence.PatchLatencyMS > maxIncidentLatencyMS {
		return false
	}
	expected := SLOMissed48H
	if evidence.PatchLatencyMS <= 24*hourMS {
		expected = SLOMet24H
	} else if evidence.PatchLatencyMS <= 48*hourMS {
		expected = SLOMet48H
	}
	return evidence.Status == expected
}
