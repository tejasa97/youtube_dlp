package differential

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

// Difference is one deterministic semantic mismatch.
type Difference struct {
	Path     string `json:"path"`
	Mode     Mode   `json:"mode"`
	Reason   string `json:"reason"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

// Report is the machine-readable comparison result.
type Report struct {
	SchemaVersion   int          `json:"schema_version"`
	Equal           bool         `json:"equal"`
	DifferenceCount int          `json:"difference_count"`
	Differences     []Difference `json:"differences"`
	AppliedRules    []Rule       `json:"applied_rules,omitempty"`
}

// Compare performs a deterministic comparison under policy.
func Compare(expected, actual Document, policy Policy) Report {
	report := Report{
		SchemaVersion: SchemaVersion,
		Differences:   make([]Difference, 0),
		AppliedRules:  append([]Rule(nil), policy.Rules...),
	}
	comparator := comparator{policy: policy, differences: &report.Differences}
	comparator.compare("$", expected.rootValue(), actual.rootValue())
	report.DifferenceCount = len(report.Differences)
	report.Equal = report.DifferenceCount == 0
	return report
}

type comparator struct {
	policy      Policy
	differences *[]Difference
}

func (comparator comparator) compare(path string, expected, actual value.Value) {
	rule := comparator.policy.ruleFor(path)
	switch rule.Mode {
	case ModeIgnore:
		return
	case ModeTolerance:
		comparator.compareTolerance(path, rule, expected, actual)
		return
	case ModeRedactedURL:
		comparator.compareRedactedURL(path, rule, expected, actual)
		return
	case ModeSet:
		comparator.compareSet(path, rule, expected, actual)
		return
	}

	if expected.Kind() != actual.Kind() {
		comparator.add(path, rule.Mode, "kind_mismatch", expected, actual)
		return
	}
	switch expected.Kind() {
	case value.KindMissing, value.KindNull:
		return
	case value.KindBool:
		expectedValue, _ := expected.Bool()
		actualValue, _ := actual.Bool()
		if expectedValue != actualValue {
			comparator.add(path, rule.Mode, "value_mismatch", expected, actual)
		}
	case value.KindInt:
		expectedValue, _ := expected.Int()
		actualValue, _ := actual.Int()
		if expectedValue != actualValue {
			comparator.add(path, rule.Mode, "value_mismatch", expected, actual)
		}
	case value.KindFloat:
		expectedValue, _ := expected.Float()
		actualValue, _ := actual.Float()
		if expectedValue != actualValue {
			comparator.add(path, rule.Mode, "value_mismatch", expected, actual)
		}
	case value.KindString:
		expectedValue, _ := expected.StringValue()
		actualValue, _ := actual.StringValue()
		if expectedValue != actualValue {
			comparator.add(path, rule.Mode, "value_mismatch", expected, actual)
		}
	case value.KindBytes:
		expectedValue, _ := expected.BytesValue()
		actualValue, _ := actual.BytesValue()
		if string(expectedValue) != string(actualValue) {
			comparator.add(path, rule.Mode, "value_mismatch", expected, actual)
		}
	case value.KindList:
		comparator.compareList(path, expected, actual)
	case value.KindObject:
		comparator.compareObject(path, expected, actual)
	}
}

func (comparator comparator) compareList(path string, expected, actual value.Value) {
	expectedItems, _ := expected.ListValue()
	actualItems, _ := actual.ListValue()
	if len(expectedItems) != len(actualItems) {
		comparator.add(path, ModeOrdered, "length_mismatch", expected, actual)
	}
	length := min(len(expectedItems), len(actualItems))
	for index := 0; index < length; index++ {
		comparator.compare(path+"["+strconv.Itoa(index)+"]", expectedItems[index], actualItems[index])
	}
}

func (comparator comparator) compareObject(path string, expected, actual value.Value) {
	expectedObject, _ := expected.Object()
	actualObject, _ := actual.Object()
	expectedFields := expectedObject.Fields()
	actualFields := actualObject.Fields()
	expectedKeys := comparator.comparableFieldKeys(path, expectedFields)
	actualKeys := comparator.comparableFieldKeys(path, actualFields)
	if sameStringSet(expectedKeys, actualKeys) && strings.Join(expectedKeys, "\x00") != strings.Join(actualKeys, "\x00") {
		comparator.append(Difference{
			Path: path, Mode: ModeOrdered, Reason: "object_order_mismatch",
			Expected: encodeStringList(expectedKeys), Actual: encodeStringList(actualKeys),
		})
	}
	for _, field := range expectedFields {
		childPath := objectPath(path, field.Key)
		childRule := comparator.policy.ruleFor(childPath)
		if childRule.Mode == ModeIgnore {
			continue
		}
		actualValue, exists := actualObject.Get(field.Key)
		if !exists {
			comparator.add(childPath, childRule.Mode, "missing_actual", field.Value, value.Missing())
			continue
		}
		comparator.compare(childPath, field.Value, actualValue)
	}
	for _, field := range actualFields {
		childPath := objectPath(path, field.Key)
		childRule := comparator.policy.ruleFor(childPath)
		if childRule.Mode == ModeIgnore {
			continue
		}
		if _, exists := expectedObject.Get(field.Key); exists {
			continue
		}
		comparator.add(childPath, childRule.Mode, "unexpected_actual", value.Missing(), field.Value)
	}
}

func (comparator comparator) comparableFieldKeys(parent string, fields []value.Field) []string {
	keys := make([]string, 0, len(fields))
	for _, field := range fields {
		if comparator.policy.ruleFor(objectPath(parent, field.Key)).Mode != ModeIgnore {
			keys = append(keys, field.Key)
		}
	}
	return keys
}

func (comparator comparator) compareTolerance(path string, rule Rule, expected, actual value.Value) {
	expectedNumber, expectedOK := numeric(expected)
	actualNumber, actualOK := numeric(actual)
	if !expectedOK || !actualOK {
		comparator.add(path, rule.Mode, "tolerance_requires_numbers", expected, actual)
		return
	}
	if math.Abs(expectedNumber-actualNumber) > rule.Tolerance {
		comparator.add(path, rule.Mode, "outside_tolerance", expected, actual)
	}
}

func (comparator comparator) compareRedactedURL(path string, rule Rule, expected, actual value.Value) {
	expectedRaw, expectedOK := expected.StringValue()
	actualRaw, actualOK := actual.StringValue()
	if !expectedOK || !actualOK {
		comparator.add(path, rule.Mode, "redacted_url_requires_strings", expected, actual)
		return
	}
	expectedURL, expectedErr := url.Parse(expectedRaw)
	actualURL, actualErr := url.Parse(actualRaw)
	if expectedErr != nil || actualErr != nil {
		comparator.add(path, rule.Mode, "invalid_url", expected, actual)
		return
	}
	expectedRedacted := network.RedactURL(expectedURL)
	actualRedacted := network.RedactURL(actualURL)
	if expectedRedacted != actualRedacted {
		comparator.append(Difference{
			Path: path, Mode: rule.Mode, Reason: "redacted_url_mismatch",
			Expected: quote(expectedRedacted), Actual: quote(actualRedacted),
		})
	}
}

func (comparator comparator) compareSet(path string, rule Rule, expected, actual value.Value) {
	expectedItems, expectedOK := expected.ListValue()
	actualItems, actualOK := actual.ListValue()
	if !expectedOK || !actualOK {
		comparator.add(path, rule.Mode, "set_requires_lists", expected, actual)
		return
	}
	expectedEncoded := encodeAndSort(expectedItems)
	actualEncoded := encodeAndSort(actualItems)
	if strings.Join(expectedEncoded, "\x00") != strings.Join(actualEncoded, "\x00") {
		comparator.append(Difference{
			Path: path, Mode: rule.Mode, Reason: "set_mismatch",
			Expected: encodeStringList(expectedEncoded), Actual: encodeStringList(actualEncoded),
		})
	}
}

func (comparator comparator) add(path string, mode Mode, reason string, expected, actual value.Value) {
	comparator.append(Difference{
		Path: path, Mode: mode, Reason: reason,
		Expected: renderValue(expected), Actual: renderValue(actual),
	})
}

func (comparator comparator) append(difference Difference) {
	*comparator.differences = append(*comparator.differences, difference)
}

func numeric(input value.Value) (float64, bool) {
	if integer, ok := input.Int(); ok {
		return float64(integer), true
	}
	return input.Float()
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, item := range left {
		counts[item]++
	}
	for _, item := range right {
		counts[item]--
		if counts[item] < 0 {
			return false
		}
	}
	return true
}

func objectPath(parent, key string) string {
	return parent + "." + key
}

func renderValue(input value.Value) string {
	if input.IsMissing() {
		return "<missing>"
	}
	encoded, err := input.MarshalJSON()
	if err != nil {
		return fmt.Sprintf("<encode error: %v>", err)
	}
	return string(encoded)
}

func encodeAndSort(items []value.Value) []string {
	encoded := make([]string, len(items))
	for index, item := range items {
		encoded[index] = renderValue(item)
	}
	sort.Strings(encoded)
	return encoded
}

func encodeStringList(items []string) string {
	encoded, _ := json.Marshal(items)
	return string(encoded)
}

func quote(input string) string {
	encoded, _ := json.Marshal(input)
	return string(encoded)
}
