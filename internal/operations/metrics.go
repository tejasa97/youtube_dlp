package operations

import (
	"sort"
	"sync"
)

const hardMaxRollingRecords = 100_000

type OutcomeCounts struct {
	Total                 uint64 `json:"total"`
	Success               uint64 `json:"success"`
	Breakage              uint64 `json:"breakage"`
	Fallback              uint64 `json:"fallback"`
	Unsupported           uint64 `json:"unsupported"`
	CredentialUnavailable uint64 `json:"credential_unavailable"`
	RegionUnavailable     uint64 `json:"region_unavailable"`
	Canceled              uint64 `json:"canceled"`
	Timeout               uint64 `json:"timeout"`
	SuccessBasisPoints    uint64 `json:"success_basis_points"`
	BreakageBasisPoints   uint64 `json:"breakage_basis_points"`
	FallbackBasisPoints   uint64 `json:"fallback_basis_points"`
}

type CanaryCounts struct {
	CanaryID string        `json:"canary_id"`
	Counts   OutcomeCounts `json:"counts"`
}

type PatchMetrics struct {
	Samples        uint64 `json:"samples"`
	Met24H         uint64 `json:"met_24h"`
	Met48H         uint64 `json:"met_48h"`
	Missed48H      uint64 `json:"missed_48h"`
	TotalLatencyMS uint64 `json:"total_latency_ms"`
	MaxLatencyMS   uint64 `json:"max_latency_ms"`
}

type MetricsSnapshot struct {
	SchemaVersion int            `json:"schema_version"`
	Counts        OutcomeCounts  `json:"counts"`
	ByCanary      []CanaryCounts `json:"by_canary"`
	Patch         PatchMetrics   `json:"patch_latency"`
}

// RollingMetrics retains only the most recent bounded record and incident
// windows. Its allowlist is fixed by a validated Suite.
type RollingMetrics struct {
	mu                       sync.RWMutex
	allowed                  map[string]CanarySpec
	maxRecords, maxIncidents int
	records                  []Record
	incidents                []IncidentEvidence
}

func NewRollingMetrics(suite Suite, maxRecords, maxIncidents int) (*RollingMetrics, error) {
	canonical, err := NewSuite(suite.Canaries)
	if err != nil || suite.SchemaVersion != SchemaVersion || maxRecords < 1 || maxRecords > hardMaxRollingRecords || maxIncidents < 1 || maxIncidents > hardMaxRollingRecords {
		return nil, ErrInvalidSpec
	}
	allowed := make(map[string]CanarySpec, len(canonical.Canaries))
	for _, spec := range canonical.Canaries {
		allowed[spec.ID] = spec
	}
	return &RollingMetrics{allowed: allowed, maxRecords: maxRecords, maxIncidents: maxIncidents}, nil
}

func (metrics *RollingMetrics) AddRecord(record Record) error {
	if !validRecord(record) {
		return ErrInvalidOutcome
	}
	spec, ok := metrics.allowed[record.CanaryID]
	if !ok || spec.Class != record.Class || spec.Extractor != record.Extractor ||
		!validObservation(spec, Observation{Outcome: record.Outcome, Failure: record.Failure, Capability: record.Capability}) {
		return ErrInvalidOutcome
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.records = append(metrics.records, record)
	if len(metrics.records) > metrics.maxRecords {
		copy(metrics.records, metrics.records[len(metrics.records)-metrics.maxRecords:])
		metrics.records = metrics.records[:metrics.maxRecords]
	}
	return nil
}

func (metrics *RollingMetrics) AddIncident(evidence IncidentEvidence) error {
	if !validIncident(evidence) {
		return ErrInvalidDrill
	}
	spec, ok := metrics.allowed[evidence.CanaryID]
	if !ok || spec.Extractor != evidence.Extractor {
		return ErrInvalidDrill
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.incidents = append(metrics.incidents, evidence)
	if len(metrics.incidents) > metrics.maxIncidents {
		copy(metrics.incidents, metrics.incidents[len(metrics.incidents)-metrics.maxIncidents:])
		metrics.incidents = metrics.incidents[:metrics.maxIncidents]
	}
	return nil
}

func (metrics *RollingMetrics) Snapshot() MetricsSnapshot {
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	result := MetricsSnapshot{SchemaVersion: SchemaVersion}
	byCanary := make(map[string]*OutcomeCounts)
	for _, record := range metrics.records {
		addOutcome(&result.Counts, record.Outcome)
		counts := byCanary[record.CanaryID]
		if counts == nil {
			counts = &OutcomeCounts{}
			byCanary[record.CanaryID] = counts
		}
		addOutcome(counts, record.Outcome)
	}
	finalizeCounts(&result.Counts)
	ids := make([]string, 0, len(byCanary))
	for id := range byCanary {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		finalizeCounts(byCanary[id])
		result.ByCanary = append(result.ByCanary, CanaryCounts{CanaryID: id, Counts: *byCanary[id]})
	}
	for _, incident := range metrics.incidents {
		result.Patch.Samples++
		latency := uint64(incident.PatchLatencyMS)
		result.Patch.TotalLatencyMS += latency
		if latency > result.Patch.MaxLatencyMS {
			result.Patch.MaxLatencyMS = latency
		}
		switch incident.Status {
		case SLOMet24H:
			result.Patch.Met24H++
		case SLOMet48H:
			result.Patch.Met48H++
		case SLOMissed48H:
			result.Patch.Missed48H++
		}
	}
	return result
}

func (metrics *RollingMetrics) Reset() MetricsSnapshot {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	// Build from copies without recursively taking the mutex.
	copyMetrics := &RollingMetrics{records: append([]Record(nil), metrics.records...), incidents: append([]IncidentEvidence(nil), metrics.incidents...)}
	metrics.records, metrics.incidents = nil, nil
	return copyMetrics.Snapshot()
}

func addOutcome(counts *OutcomeCounts, outcome Outcome) {
	counts.Total++
	switch outcome {
	case OutcomeSuccess:
		counts.Success++
	case OutcomeBreakage:
		counts.Breakage++
	case OutcomeFallback:
		counts.Fallback++
	case OutcomeUnsupported:
		counts.Unsupported++
	case OutcomeCredentialUnavailable:
		counts.CredentialUnavailable++
	case OutcomeRegionUnavailable:
		counts.RegionUnavailable++
	case OutcomeCanceled:
		counts.Canceled++
	case OutcomeTimeout:
		counts.Timeout++
	}
}

func finalizeCounts(counts *OutcomeCounts) {
	if counts.Total == 0 {
		return
	}
	counts.SuccessBasisPoints = counts.Success * 10_000 / counts.Total
	counts.BreakageBasisPoints = counts.Breakage * 10_000 / counts.Total
	counts.FallbackBasisPoints = counts.Fallback * 10_000 / counts.Total
}
