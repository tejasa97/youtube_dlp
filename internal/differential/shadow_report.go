package differential

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const MaxAggregateReports = 10_000

type ShadowSummary struct {
	SchemaVersion int                   `json:"schema_version"`
	Reports       int                   `json:"reports"`
	EqualReports  int                   `json:"equal_reports"`
	Mismatches    int                   `json:"mismatches"`
	Truncated     bool                  `json:"truncated"`
	ByClass       map[MismatchClass]int `json:"by_class"`
	BySeverity    map[Severity]int      `json:"by_severity"`
}

// AggregateShadowReports reduces already-redacted reports without retaining
// their values. The report bound prevents untrusted batch input from growing
// aggregation work without limit.
func AggregateShadowReports(ctx context.Context, reports []ShadowReport) (ShadowSummary, error) {
	if len(reports) > MaxAggregateReports {
		return ShadowSummary{}, ErrObservationLimit
	}
	summary := ShadowSummary{SchemaVersion: ShadowSchemaVersion, Reports: len(reports), ByClass: make(map[MismatchClass]int), BySeverity: make(map[Severity]int)}
	for index, report := range reports {
		if index&63 == 0 {
			if err := ctx.Err(); err != nil {
				return ShadowSummary{}, err
			}
		}
		if report.Equal {
			summary.EqualReports++
		}
		summary.Mismatches += report.MismatchCount
		summary.Truncated = summary.Truncated || report.Truncated
		for _, mismatch := range report.Mismatches {
			summary.ByClass[mismatch.Class]++
			summary.BySeverity[mismatch.Severity]++
		}
	}
	return summary, nil
}

// CanonicalShadowReport emits stable compact JSON and a content identity.
func CanonicalShadowReport(ctx context.Context, report ShadowReport) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(report); err != nil {
		return nil, "", err
	}
	if output.Len() > MaxShadowBytes {
		return nil, "", ErrObservationLimit
	}
	digest := sha256.Sum256(output.Bytes())
	return output.Bytes(), hex.EncodeToString(digest[:]), nil
}
