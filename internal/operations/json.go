package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

const (
	DefaultMaxDocumentBytes int64 = 4 << 20
	HardMaxDocumentBytes    int64 = 16 << 20
)

func MarshalSuite(suite Suite) ([]byte, error) {
	canonical, err := NewSuite(suite.Canaries)
	if err != nil || suite.SchemaVersion != SchemaVersion {
		return nil, ErrInvalidSpec
	}
	return json.Marshal(canonical)
}

func DecodeSuite(ctx context.Context, reader io.Reader, maxBytes int64) (Suite, error) {
	data, err := readDocument(ctx, reader, maxBytes)
	if err != nil {
		return Suite{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire struct {
		SchemaVersion *int          `json:"schema_version"`
		Canaries      *[]CanarySpec `json:"canaries"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return Suite{}, ErrDecode
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Suite{}, ErrDecode
	}
	if wire.SchemaVersion == nil || *wire.SchemaVersion != SchemaVersion || wire.Canaries == nil {
		return Suite{}, ErrDecode
	}
	suite, err := NewSuite(*wire.Canaries)
	if err != nil {
		return Suite{}, err
	}
	return suite, nil
}

type IncidentSet struct {
	SchemaVersion int                `json:"schema_version"`
	Incidents     []IncidentEvidence `json:"incidents"`
}

func DecodeRecords(ctx context.Context, reader io.Reader, maxBytes int64, suite Suite) ([]Record, error) {
	data, err := readDocument(ctx, reader, maxBytes)
	if err != nil {
		return nil, err
	}
	var set RecordSet
	if err := decodeStrict(data, &set); err != nil || set.SchemaVersion != SchemaVersion || len(set.Records) > hardMaxRollingRecords {
		return nil, ErrDecode
	}
	if _, err := MarshalRecords(suite, set.Records); err != nil {
		return nil, err
	}
	return append([]Record(nil), set.Records...), nil
}

func MarshalIncidents(suite Suite, incidents []IncidentEvidence) ([]byte, error) {
	canonical, err := NewSuite(suite.Canaries)
	if err != nil || suite.SchemaVersion != SchemaVersion || len(incidents) > hardMaxRollingRecords {
		return nil, ErrInvalidDrill
	}
	allowed := make(map[string]string, len(canonical.Canaries))
	for _, spec := range canonical.Canaries {
		allowed[spec.ID] = spec.Extractor
	}
	copyIncidents := append([]IncidentEvidence(nil), incidents...)
	sort.Slice(copyIncidents, func(i, j int) bool {
		if copyIncidents[i].DetectedUnixMS != copyIncidents[j].DetectedUnixMS {
			return copyIncidents[i].DetectedUnixMS < copyIncidents[j].DetectedUnixMS
		}
		return copyIncidents[i].IncidentID < copyIncidents[j].IncidentID
	})
	for _, incident := range copyIncidents {
		if !validIncident(incident) || allowed[incident.CanaryID] != incident.Extractor {
			return nil, ErrInvalidDrill
		}
	}
	return json.Marshal(IncidentSet{SchemaVersion: SchemaVersion, Incidents: copyIncidents})
}

func DecodeIncidents(ctx context.Context, reader io.Reader, maxBytes int64, suite Suite) ([]IncidentEvidence, error) {
	data, err := readDocument(ctx, reader, maxBytes)
	if err != nil {
		return nil, err
	}
	var set IncidentSet
	if err := decodeStrict(data, &set); err != nil || set.SchemaVersion != SchemaVersion || len(set.Incidents) > hardMaxRollingRecords {
		return nil, ErrDecode
	}
	if _, err := MarshalIncidents(suite, set.Incidents); err != nil {
		return nil, err
	}
	return append([]IncidentEvidence(nil), set.Incidents...), nil
}

func readDocument(ctx context.Context, reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil || ctx == nil {
		return nil, ErrDecode
	}
	if maxBytes == 0 {
		maxBytes = DefaultMaxDocumentBytes
	}
	if maxBytes < 1 || maxBytes > HardMaxDocumentBytes {
		return nil, ErrResourceLimit
	}
	limited := &io.LimitedReader{R: &contextReader{ctx: ctx, reader: reader}, N: maxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		if contextError(ctx) != nil {
			return nil, contextError(ctx)
		}
		return nil, ErrDecode
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrResourceLimit
	}
	if err := pluginapi.ValidateJSONFrame(data); err != nil {
		return nil, ErrDecode
	}
	return data, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrDecode
	}
	return nil
}

func MarshalIncident(evidence IncidentEvidence) ([]byte, error) {
	if !validIncident(evidence) {
		return nil, ErrInvalidDrill
	}
	return json.Marshal(evidence)
}

func MarshalMetrics(snapshot MetricsSnapshot) ([]byte, error) {
	if snapshot.SchemaVersion != SchemaVersion {
		return nil, ErrInvalidOutcome
	}
	copySnapshot := snapshot
	copySnapshot.ByCanary = append(make([]CanaryCounts, 0, len(snapshot.ByCanary)), snapshot.ByCanary...)
	sort.Slice(copySnapshot.ByCanary, func(i, j int) bool { return copySnapshot.ByCanary[i].CanaryID < copySnapshot.ByCanary[j].CanaryID })
	if !validOutcomeCounts(copySnapshot.Counts) || !validFailureCounts(copySnapshot.Failures) || copySnapshot.Failures.Total != copySnapshot.Counts.Total || !validPatchMetrics(copySnapshot.Patch) {
		return nil, ErrInvalidOutcome
	}
	for _, item := range copySnapshot.ByCanary {
		if !identifierPattern.MatchString(item.CanaryID) || item.Counts.Total == 0 || !validOutcomeCounts(item.Counts) {
			return nil, ErrInvalidOutcome
		}
	}
	for index := 1; index < len(copySnapshot.ByCanary); index++ {
		if copySnapshot.ByCanary[index-1].CanaryID == copySnapshot.ByCanary[index].CanaryID {
			return nil, ErrInvalidOutcome
		}
	}
	return json.Marshal(copySnapshot)
}

func validFailureCounts(counts FailureCounts) bool {
	if counts.Total > hardMaxRollingRecords {
		return false
	}
	return counts.None+counts.Extractor+counts.Network+counts.Auth+counts.Region+
		counts.Media+counts.Contract+counts.Runner == counts.Total
}

func validOutcomeCounts(counts OutcomeCounts) bool {
	if counts.Total > hardMaxRollingRecords {
		return false
	}
	values := [...]uint64{counts.Success, counts.Breakage, counts.Fallback, counts.Unsupported,
		counts.CredentialUnavailable, counts.RegionUnavailable, counts.Canceled, counts.Timeout}
	for _, value := range values {
		if value > counts.Total {
			return false
		}
	}
	sum := counts.Success + counts.Breakage + counts.Fallback + counts.Unsupported +
		counts.CredentialUnavailable + counts.RegionUnavailable + counts.Canceled + counts.Timeout
	if sum != counts.Total {
		return false
	}
	if counts.Total == 0 {
		return counts.SuccessBasisPoints == 0 && counts.BreakageBasisPoints == 0 && counts.FallbackBasisPoints == 0
	}
	return counts.SuccessBasisPoints == counts.Success*10_000/counts.Total &&
		counts.BreakageBasisPoints == counts.Breakage*10_000/counts.Total &&
		counts.FallbackBasisPoints == counts.Fallback*10_000/counts.Total
}

func validPatchMetrics(metrics PatchMetrics) bool {
	if metrics.Samples > hardMaxRollingRecords || metrics.Met24H > metrics.Samples ||
		metrics.Met48H > metrics.Samples || metrics.Missed48H > metrics.Samples ||
		metrics.Met24H+metrics.Met48H+metrics.Missed48H != metrics.Samples {
		return false
	}
	if metrics.MaxLatencyMS > uint64(maxIncidentLatencyMS) ||
		metrics.TotalLatencyMS > metrics.Samples*uint64(maxIncidentLatencyMS) {
		return false
	}
	if metrics.Samples == 0 {
		return metrics.TotalLatencyMS == 0 && metrics.MaxLatencyMS == 0
	}
	return metrics.TotalLatencyMS >= metrics.MaxLatencyMS
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := contextError(reader.ctx); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
