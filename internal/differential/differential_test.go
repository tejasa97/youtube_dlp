package differential

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

const documentPrefix = `{
  "schema_version": 1,
  "metadata": %s,
  "formats": %s,
  "playlists": %s,
  "events": %s,
  "selection": %s,
  "outputs": %s
}`

func parseFixture(t *testing.T, metadata, formats, playlists, events, selection, outputs string) Document {
	t.Helper()
	input := strings.NewReader(fmt.Sprintf(documentPrefix, metadata, formats, playlists, events, selection, outputs))
	document, err := ParseDocument(input)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestCompareDetectsMissingVersusNull(t *testing.T) {
	expected := parseFixture(t, `{"title":"x","description":null}`, `[]`, `[]`, `[]`, `{}`, `[]`)
	actual := parseFixture(t, `{"title":"x"}`, `[]`, `[]`, `[]`, `{}`, `[]`)
	report := Compare(expected, actual, DefaultPolicy())
	assertDifference(t, report, "$.metadata.description", "missing_actual")
}

func TestCompareDetectsObjectAndListOrder(t *testing.T) {
	expected := parseFixture(t, `{"id":"x","title":"y"}`, `[]`, `["a","b"]`, `[]`, `{}`, `[]`)
	actual := parseFixture(t, `{"title":"y","id":"x"}`, `[]`, `["b","a"]`, `[]`, `{}`, `[]`)
	report := Compare(expected, actual, DefaultPolicy())
	assertDifference(t, report, "$.metadata", "object_order_mismatch")
	assertDifference(t, report, "$.playlists[0]", "value_mismatch")
}

func TestCompareAppliesToleranceSetIgnoreAndURLRedaction(t *testing.T) {
	expected := parseFixture(t,
		`{"duration":12.5}`,
		`[{"url":"https://cdn.example/video?token=one&visible=yes"}]`,
		`["a","b"]`, `[{"timestamp":100}]`, `{}`, `[]`)
	actual := parseFixture(t,
		`{"duration":12.5004}`,
		`[{"url":"https://cdn.example/video?visible=yes&token=two"}]`,
		`["b","a"]`, `[{"timestamp":200}]`, `{}`, `[]`)
	policy := Policy{Version: 1, Rules: []Rule{
		{Path: "$.metadata.duration", Mode: ModeTolerance, Tolerance: 0.001, Reason: "fixture clock precision", Owner: "core"},
		{Path: "$.formats[*].url", Mode: ModeRedactedURL, Reason: "signed token", Owner: "network"},
		{Path: "$.playlists", Mode: ModeSet, Reason: "source is unordered", Owner: "extractors"},
		{Path: "$.events[*].timestamp", Mode: ModeIgnore, Reason: "wall clock", Owner: "events"},
	}}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	report := Compare(expected, actual, policy)
	if !report.Equal {
		t.Fatalf("Compare() differences = %#v", report.Differences)
	}
}

func TestCompareDetectsFormatSelectionDifference(t *testing.T) {
	expected := parseFixture(t, `{}`, `[]`, `[]`, `[]`, `{"format_id":"137+140"}`, `[]`)
	actual := parseFixture(t, `{}`, `[]`, `[]`, `[]`, `{"format_id":"22"}`, `[]`)
	report := Compare(expected, actual, DefaultPolicy())
	assertDifference(t, report, "$.selection.format_id", "value_mismatch")
}

func TestPolicyRequiresAttributionForExceptions(t *testing.T) {
	policy := Policy{Version: 1, Rules: []Rule{{Path: "$.metadata.duration", Mode: ModeTolerance, Tolerance: 1}}}
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestIgnoreCoversAbsentFieldsAndObjectOrder(t *testing.T) {
	expected := parseFixture(t, `{"id":"x","volatile":1,"title":"y"}`, `[]`, `[]`, `[]`, `{}`, `[]`)
	actual := parseFixture(t, `{"id":"x","title":"y"}`, `[]`, `[]`, `[]`, `{}`, `[]`)
	policy := Policy{Version: 1, Rules: []Rule{{
		Path: "$.metadata.volatile", Mode: ModeIgnore, Reason: "volatile source field", Owner: "extractors",
	}}}
	report := Compare(expected, actual, policy)
	if !report.Equal {
		t.Fatalf("Compare() differences = %#v", report.Differences)
	}
}

func TestWriteReports(t *testing.T) {
	expected := parseFixture(t, `{}`, `[]`, `[]`, `[]`, `{}`, `[]`)
	actual := parseFixture(t, `{"extra":true}`, `[]`, `[]`, `[]`, `{}`, `[]`)
	report := Compare(expected, actual, DefaultPolicy())
	var jsonOutput, markdownOutput bytes.Buffer
	if err := WriteJSON(&jsonOutput, report); err != nil {
		t.Fatal(err)
	}
	if err := WriteMarkdown(&markdownOutput, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOutput.String(), `"difference_count": 1`) ||
		!strings.Contains(markdownOutput.String(), "$.metadata.extra") {
		t.Fatalf("reports missing difference:\n%s\n%s", jsonOutput.String(), markdownOutput.String())
	}
}

func TestParseDocumentRejectsInvalidEnvelope(t *testing.T) {
	for _, input := range []string{`[]`, `{"schema_version":2}`, `{"schema_version":1,"metadata":[]}`} {
		if _, err := ParseDocument(strings.NewReader(input)); err == nil {
			t.Fatalf("ParseDocument(%s) succeeded", input)
		}
	}
}

func assertDifference(t *testing.T, report Report, path, reason string) {
	t.Helper()
	for _, difference := range report.Differences {
		if difference.Path == path && difference.Reason == reason {
			return
		}
	}
	t.Fatalf("difference %s/%s not found in %#v", path, reason, report.Differences)
}
