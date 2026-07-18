package differential

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const maxMismatchValueBytes = 2048

type MissingNullPolicy string
type OrderPolicy string
type MismatchClass string
type Severity string

const (
	MissingNullDistinct   MissingNullPolicy = "distinct"
	MissingNullEquivalent MissingNullPolicy = "equivalent"
	OrderSignificant      OrderPolicy       = "significant"
	OrderByIdentity       OrderPolicy       = "by_identity"
	OrderAsSet            OrderPolicy       = "set"

	ClassRouting  MismatchClass = "routing"
	ClassRequest  MismatchClass = "request"
	ClassMetadata MismatchClass = "metadata"
	ClassFormat   MismatchClass = "format"
	ClassPlaylist MismatchClass = "playlist"
	ClassWarning  MismatchClass = "warning"
	ClassProtocol MismatchClass = "protocol"

	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

type ShadowPolicy struct {
	MissingNull   MissingNullPolicy `json:"missing_null"`
	FormatOrder   OrderPolicy       `json:"format_order"`
	PlaylistOrder OrderPolicy       `json:"playlist_order"`
	WarningOrder  OrderPolicy       `json:"warning_order"`
	ProtocolOrder OrderPolicy       `json:"protocol_order"`
	MaxMismatches int               `json:"max_mismatches"`
}

func DefaultShadowPolicy() ShadowPolicy {
	return ShadowPolicy{MissingNull: MissingNullDistinct, FormatOrder: OrderByIdentity, PlaylistOrder: OrderSignificant, WarningOrder: OrderAsSet, ProtocolOrder: OrderByIdentity, MaxMismatches: DefaultMaxMismatches}
}

func (policy ShadowPolicy) validate() error {
	if policy.MissingNull != MissingNullDistinct && policy.MissingNull != MissingNullEquivalent {
		return fmt.Errorf("%w: missing/null policy", ErrInvalidObservation)
	}
	for _, order := range []OrderPolicy{policy.FormatOrder, policy.PlaylistOrder, policy.WarningOrder, policy.ProtocolOrder} {
		if order != OrderSignificant && order != OrderByIdentity && order != OrderAsSet {
			return fmt.Errorf("%w: order policy", ErrInvalidObservation)
		}
	}
	if policy.MaxMismatches <= 0 || policy.MaxMismatches > MaxShadowItems {
		return ErrObservationLimit
	}
	return nil
}

type SemanticMismatch struct {
	Class    MismatchClass `json:"class"`
	Severity Severity      `json:"severity"`
	Path     string        `json:"path"`
	Reason   string        `json:"reason"`
	Expected string        `json:"expected"`
	Actual   string        `json:"actual"`
}

type ShadowReport struct {
	SchemaVersion int                `json:"schema_version"`
	Equal         bool               `json:"equal"`
	MismatchCount int                `json:"mismatch_count"`
	StoredCount   int                `json:"stored_count"`
	Truncated     bool               `json:"truncated"`
	Mismatches    []SemanticMismatch `json:"mismatches"`
}

// CompareObservations performs a deterministic, bounded semantic comparison.
// Inputs are sanitized before any value can be copied into the report.
func CompareObservations(ctx context.Context, expected, actual ObservationEnvelope, policy ShadowPolicy) (ShadowReport, error) {
	if err := ctx.Err(); err != nil {
		return ShadowReport{}, err
	}
	if err := policy.validate(); err != nil {
		return ShadowReport{}, err
	}
	left, err := SanitizeObservation(expected)
	if err != nil {
		return ShadowReport{}, err
	}
	if err := ctx.Err(); err != nil {
		return ShadowReport{}, err
	}
	right, err := SanitizeObservation(actual)
	if err != nil {
		return ShadowReport{}, err
	}
	collector := mismatchCollector{ctx: ctx, policy: policy, mismatches: make([]SemanticMismatch, 0, min(policy.MaxMismatches, 32))}
	collector.section(ClassRouting, "$.routing", left.Routing, right.Routing)
	collector.section(ClassRequest, "$.request", left.Request, right.Request)
	collector.section(ClassMetadata, "$.metadata", left.Metadata, right.Metadata)
	compareCollection(&collector, ClassFormat, "$.formats", left.Formats, right.Formats, policy.FormatOrder, func(item FormatObservation) string { return item.ID })
	compareCollection(&collector, ClassPlaylist, "$.playlist.entries", left.Playlist.Entries, right.Playlist.Entries, policy.PlaylistOrder, func(item PlaylistEntryObservation) string { return item.ID })
	compareCollection(&collector, ClassWarning, "$.warnings", left.Warnings, right.Warnings, policy.WarningOrder, func(item WarningObservation) string { return item.Code + "\x00" + item.Message })
	compareCollection(&collector, ClassProtocol, "$.protocols", left.Protocols, right.Protocols, policy.ProtocolOrder, func(item ProtocolObservation) string { return item.Name })
	if err := ctx.Err(); err != nil {
		return ShadowReport{}, err
	}
	return ShadowReport{SchemaVersion: ShadowSchemaVersion, Equal: collector.total == 0, MismatchCount: collector.total, StoredCount: len(collector.mismatches), Truncated: collector.total > len(collector.mismatches), Mismatches: collector.mismatches}, nil
}

type mismatchCollector struct {
	ctx        context.Context
	policy     ShadowPolicy
	total      int
	mismatches []SemanticMismatch
}

func (collector *mismatchCollector) section(class MismatchClass, path string, expected, actual any) {
	left, leftErr := toSemantic(expected)
	right, rightErr := toSemantic(actual)
	if leftErr != nil || rightErr != nil {
		collector.add(class, path, "invalid_section", left, right)
		return
	}
	collector.compare(class, path, left, right, true, true)
}

func compareCollection[T any](collector *mismatchCollector, class MismatchClass, path string, expected, actual []T, order OrderPolicy, identity func(T) string) {
	left := append([]T(nil), expected...)
	right := append([]T(nil), actual...)
	if order == OrderByIdentity {
		left = sortedCopy(left, identity)
		right = sortedCopy(right, identity)
	} else if order == OrderAsSet {
		left = sortedCopy(left, func(item T) string { encoded, _ := json.Marshal(item); return string(encoded) })
		right = sortedCopy(right, func(item T) string { encoded, _ := json.Marshal(item); return string(encoded) })
	}
	collector.section(class, path, left, right)
}

func (collector *mismatchCollector) compare(class MismatchClass, path string, expected, actual any, expectedPresent, actualPresent bool) {
	if collector.ctx.Err() != nil {
		return
	}
	if !expectedPresent || !actualPresent {
		if collector.policy.MissingNull == MissingNullEquivalent && ((!expectedPresent && actual == nil) || (!actualPresent && expected == nil)) {
			return
		}
		reason := "missing_actual"
		if !expectedPresent {
			reason = "unexpected_actual"
		}
		collector.add(class, path, reason, expected, actual)
		return
	}
	leftObject, leftOK := expected.(map[string]any)
	rightObject, rightOK := actual.(map[string]any)
	if leftOK && rightOK {
		keys := make(map[string]struct{}, len(leftObject)+len(rightObject))
		for key := range leftObject {
			keys[key] = struct{}{}
		}
		for key := range rightObject {
			keys[key] = struct{}{}
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		for _, key := range ordered {
			left, leftPresent := leftObject[key]
			right, rightPresent := rightObject[key]
			collector.compare(class, path+"."+key, left, right, leftPresent, rightPresent)
		}
		return
	}
	leftList, leftOK := expected.([]any)
	rightList, rightOK := actual.([]any)
	if leftOK && rightOK {
		if len(leftList) != len(rightList) {
			collector.add(class, path, "length_mismatch", len(leftList), len(rightList))
		}
		for index := 0; index < min(len(leftList), len(rightList)); index++ {
			collector.compare(class, path+"["+strconv.Itoa(index)+"]", leftList[index], rightList[index], true, true)
		}
		return
	}
	if canonicalValue(expected) != canonicalValue(actual) {
		reason := "value_mismatch"
		if fmt.Sprintf("%T", expected) != fmt.Sprintf("%T", actual) {
			reason = "kind_mismatch"
		}
		collector.add(class, path, reason, expected, actual)
	}
}

func (collector *mismatchCollector) add(class MismatchClass, path, reason string, expected, actual any) {
	collector.total++
	candidate := SemanticMismatch{Class: class, Severity: severityFor(class, path), Path: path, Reason: reason, Expected: canonicalValue(expected), Actual: canonicalValue(actual)}
	index := sort.Search(len(collector.mismatches), func(index int) bool {
		return !mismatchLess(collector.mismatches[index], candidate)
	})
	if len(collector.mismatches) >= collector.policy.MaxMismatches && index == len(collector.mismatches) {
		return
	}
	collector.mismatches = append(collector.mismatches, SemanticMismatch{})
	copy(collector.mismatches[index+1:], collector.mismatches[index:])
	collector.mismatches[index] = candidate
	if len(collector.mismatches) > collector.policy.MaxMismatches {
		collector.mismatches = collector.mismatches[:collector.policy.MaxMismatches]
	}
}

func toSemantic(input any) (any, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var output any
	if err := decoder.Decode(&output); err != nil {
		return nil, err
	}
	return output, nil
}

func canonicalValue(input any) string {
	if input == nil {
		return "null"
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return `"<unavailable>"`
	}
	if len(encoded) > maxMismatchValueBytes {
		digest := sha256.Sum256(encoded)
		description, _ := json.Marshal(fmt.Sprintf("<truncated sha256:%s bytes:%d>", hex.EncodeToString(digest[:]), len(encoded)))
		return string(description)
	}
	return string(encoded)
}

func severityFor(class MismatchClass, path string) Severity {
	switch class {
	case ClassRouting:
		return SeverityCritical
	case ClassRequest:
		return SeverityHigh
	case ClassProtocol:
		if strings.HasSuffix(path, ".usable") {
			return SeverityCritical
		}
		return SeverityHigh
	case ClassFormat:
		if strings.HasSuffix(path, ".usable") {
			return SeverityCritical
		}
		return SeverityMedium
	case ClassMetadata:
		return SeverityMedium
	default:
		return SeverityLow
	}
}

func classRank(class MismatchClass) int {
	for index, candidate := range []MismatchClass{ClassRouting, ClassRequest, ClassMetadata, ClassFormat, ClassPlaylist, ClassWarning, ClassProtocol} {
		if candidate == class {
			return index
		}
	}
	return 99
}

func severityRank(severity Severity) int {
	for index, candidate := range []Severity{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow} {
		if candidate == severity {
			return index
		}
	}
	return 99
}

func mismatchLess(left, right SemanticMismatch) bool {
	if severityRank(left.Severity) != severityRank(right.Severity) {
		return severityRank(left.Severity) < severityRank(right.Severity)
	}
	if classRank(left.Class) != classRank(right.Class) {
		return classRank(left.Class) < classRank(right.Class)
	}
	if left.Path != right.Path {
		return left.Path < right.Path
	}
	if left.Reason != right.Reason {
		return left.Reason < right.Reason
	}
	if left.Expected != right.Expected {
		return left.Expected < right.Expected
	}
	return left.Actual < right.Actual
}
