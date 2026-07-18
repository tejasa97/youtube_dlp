package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
)

const ReplaySchemaVersion = 1

// ReplayItem is a privacy-preserving semantic observation. Raw requests,
// responses, URLs, credential handles, regions, errors, and timestamps are
// deliberately excluded.
type ReplayItem struct {
	CanaryID   string       `json:"canary_id"`
	Outcome    Outcome      `json:"outcome"`
	Failure    FailureClass `json:"failure_class"`
	Capability string       `json:"capability,omitempty"`
}

// ReplayCapture is bound to the exact canonical Suite through SHA-256.
type ReplayCapture struct {
	SchemaVersion int          `json:"schema_version"`
	SuiteSHA256   string       `json:"suite_sha256"`
	Items         []ReplayItem `json:"items"`
}

// CaptureReplay produces a deterministic semantic replay from one suite run.
func CaptureReplay(suite Suite, records []Record) (ReplayCapture, error) {
	canonical, digest, err := suiteDigest(suite)
	if err != nil || len(records) == 0 || len(records) > len(canonical.Canaries) {
		return ReplayCapture{}, ErrInvalidReplay
	}
	allowed := make(map[string]CanarySpec, len(canonical.Canaries))
	for _, spec := range canonical.Canaries {
		allowed[spec.ID] = spec
	}
	items := make([]ReplayItem, 0, len(records))
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		spec, ok := allowed[record.CanaryID]
		observation := Observation{Outcome: record.Outcome, Failure: record.Failure, Capability: record.Capability}
		if !ok || seen[record.CanaryID] || record.Class != spec.Class || record.Extractor != spec.Extractor || !validRecord(record) || !validObservation(spec, observation) {
			return ReplayCapture{}, ErrInvalidReplay
		}
		seen[record.CanaryID] = true
		items = append(items, ReplayItem{CanaryID: record.CanaryID, Outcome: record.Outcome, Failure: record.Failure, Capability: record.Capability})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CanaryID < items[j].CanaryID })
	return ReplayCapture{SchemaVersion: ReplaySchemaVersion, SuiteSHA256: digest, Items: items}, nil
}

func MarshalReplay(capture ReplayCapture, suite Suite) ([]byte, error) {
	canonical, err := validateReplay(capture, suite)
	if err != nil {
		return nil, err
	}
	return json.Marshal(canonical)
}

func DecodeReplay(ctx context.Context, reader io.Reader, maxBytes int64, suite Suite) (ReplayCapture, error) {
	data, err := readDocument(ctx, reader, maxBytes)
	if err != nil {
		return ReplayCapture{}, err
	}
	var capture ReplayCapture
	if err := decodeStrict(data, &capture); err != nil {
		return ReplayCapture{}, ErrDecode
	}
	return validateReplay(capture, suite)
}

func validateReplay(capture ReplayCapture, suite Suite) (ReplayCapture, error) {
	canonicalSuite, digest, err := suiteDigest(suite)
	if err != nil || capture.SchemaVersion != ReplaySchemaVersion || capture.SuiteSHA256 != digest || len(capture.Items) == 0 || len(capture.Items) > len(canonicalSuite.Canaries) {
		return ReplayCapture{}, ErrInvalidReplay
	}
	allowed := make(map[string]CanarySpec, len(canonicalSuite.Canaries))
	for _, spec := range canonicalSuite.Canaries {
		allowed[spec.ID] = spec
	}
	result := capture
	result.Items = append([]ReplayItem(nil), capture.Items...)
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].CanaryID < result.Items[j].CanaryID })
	for index, item := range result.Items {
		spec, ok := allowed[item.CanaryID]
		if !ok || index > 0 && result.Items[index-1].CanaryID == item.CanaryID || !validObservation(spec, Observation{Outcome: item.Outcome, Failure: item.Failure, Capability: item.Capability}) {
			return ReplayCapture{}, ErrInvalidReplay
		}
	}
	return result, nil
}

// ReplayRunner returns captured observations and rejects uncaptured canaries.
func ReplayRunner(capture ReplayCapture, suite Suite) (Runner, error) {
	canonical, err := validateReplay(capture, suite)
	if err != nil {
		return nil, err
	}
	items := make(map[string]Observation, len(canonical.Items))
	for _, item := range canonical.Items {
		items[item.CanaryID] = Observation{Outcome: item.Outcome, Failure: item.Failure, Capability: item.Capability}
	}
	return RunnerFunc(func(_ context.Context, invocation Invocation) (Observation, error) {
		observation, ok := items[invocation.ID]
		if !ok {
			return Observation{}, ErrInvalidReplay
		}
		return observation, nil
	}), nil
}

func suiteDigest(suite Suite) (Suite, string, error) {
	canonical, err := NewSuite(suite.Canaries)
	if err != nil || suite.SchemaVersion != SchemaVersion {
		return Suite{}, "", ErrInvalidSpec
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return Suite{}, "", ErrInvalidSpec
	}
	digest := sha256.Sum256(data)
	return canonical, hex.EncodeToString(digest[:]), nil
}
