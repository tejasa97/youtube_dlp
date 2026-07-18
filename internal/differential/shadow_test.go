package differential

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPhase3CorpusCanonicalComparisonAndRedaction(t *testing.T) {
	reference := loadObservationFixture(t, "reference.json")
	goObservation := loadObservationFixture(t, "go.json")
	report, err := CompareObservations(context.Background(), reference, goObservation, DefaultShadowPolicy())
	if err != nil || !report.Equal {
		t.Fatalf("CompareObservations() = %#v, %v", report, err)
	}
	first, firstHash, err := CanonicalObservation(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := CanonicalObservation(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || firstHash != secondHash || len(firstHash) != 64 {
		t.Fatalf("non-deterministic canonical observation: %q / %q", firstHash, secondHash)
	}
	for _, secret := range []string{"reference-secret", "reference-cookie", "credential-reference"} {
		if bytes.Contains(first, []byte(secret)) {
			t.Fatalf("canonical observation leaked %q", secret)
		}
	}
	if !bytes.Contains(first, []byte(`"credential_count":1`)) || !bytes.Contains(first, []byte("REDACTED")) {
		t.Fatalf("canonical observation lacks redaction evidence: %s", first)
	}
	if firstHash != "a4f852c6145cb3b09c04085125f6b3e38c201022de20ec47314cc83bded9aaa4" {
		t.Fatalf("canonical reference hash = %s", firstHash)
	}
}

func TestSemanticMismatchClassificationSeverityAndMissingNull(t *testing.T) {
	left := fixtureObservation()
	right := fixtureObservation()
	right.Routing.Extractor = "other"
	right.Metadata = map[string]json.RawMessage{"title": json.RawMessage(`"changed"`)}
	right.Formats[0].Usable = false
	right.Playlist.Entries = append(right.Playlist.Entries, PlaylistEntryObservation{ID: "extra"})
	right.Protocols[0].Usable = false
	policy := DefaultShadowPolicy()
	policy.MaxMismatches = 20
	report, err := CompareObservations(context.Background(), left, right, policy)
	if err != nil {
		t.Fatal(err)
	}
	assertSemanticMismatch(t, report, ClassRouting, SeverityCritical, "$.routing.extractor")
	assertSemanticMismatch(t, report, ClassMetadata, SeverityMedium, "$.metadata.title")
	assertSemanticMismatch(t, report, ClassMetadata, SeverityMedium, "$.metadata.description")
	assertSemanticMismatch(t, report, ClassFormat, SeverityCritical, "$.formats[0].usable")
	assertSemanticMismatch(t, report, ClassPlaylist, SeverityLow, "$.playlist.entries")
	assertSemanticMismatch(t, report, ClassProtocol, SeverityCritical, "$.protocols[0].usable")
	policy.MissingNull = MissingNullEquivalent
	report, err = CompareObservations(context.Background(), left, right, policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, mismatch := range report.Mismatches {
		if mismatch.Path == "$.metadata.description" {
			t.Fatal("missing/null equivalence was ignored")
		}
	}
}

func TestOrderPoliciesAndBoundedMismatchCollection(t *testing.T) {
	left := fixtureObservation()
	right := fixtureObservation()
	right.Formats = []FormatObservation{left.Formats[1], left.Formats[0]}
	policy := DefaultShadowPolicy()
	report, err := CompareObservations(context.Background(), left, right, policy)
	if err != nil || !report.Equal {
		t.Fatalf("identity order = %#v, %v", report, err)
	}
	policy.FormatOrder = OrderSignificant
	report, err = CompareObservations(context.Background(), left, right, policy)
	if err != nil || report.Equal {
		t.Fatalf("significant order = %#v, %v", report, err)
	}
	right.Routing.Extractor = "different"
	right.Metadata["title"] = json.RawMessage(`"different"`)
	right.Protocols[0].Usable = false
	policy.MaxMismatches = 2
	report, err = CompareObservations(context.Background(), left, right, policy)
	if err != nil || !report.Truncated || report.StoredCount != 2 || report.MismatchCount <= report.StoredCount {
		t.Fatalf("bounded report = %#v, %v", report, err)
	}
	if report.Mismatches[0].Severity != SeverityCritical {
		t.Fatalf("bounded report did not retain highest severity: %#v", report.Mismatches)
	}
}

func TestMalformedOversizedAndCancellation(t *testing.T) {
	if _, err := ParseObservation(context.Background(), strings.NewReader(`{"unknown":true}`)); !errors.Is(err, ErrInvalidObservation) {
		t.Fatalf("unknown field error = %v", err)
	}
	if _, err := ParseObservation(context.Background(), strings.NewReader(`{"schema_version":1,"schema_version":1}`)); !errors.Is(err, ErrInvalidObservation) {
		t.Fatalf("duplicate field error = %v", err)
	}
	if _, err := ParseObservation(context.Background(), strings.NewReader(strings.Repeat(" ", MaxShadowBytes+1))); !errors.Is(err, ErrObservationLimit) {
		t.Fatalf("oversize error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ParseObservation(canceled, strings.NewReader(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("parse cancellation = %v", err)
	}
	if _, err := CompareObservations(canceled, fixtureObservation(), fixtureObservation(), DefaultShadowPolicy()); !errors.Is(err, context.Canceled) {
		t.Fatalf("compare cancellation = %v", err)
	}
	overLimit := fixtureObservation()
	overLimit.Formats = make([]FormatObservation, MaxShadowItems+1)
	if _, err := SanitizeObservation(overLimit); !errors.Is(err, ErrObservationLimit) {
		t.Fatalf("item limit = %v", err)
	}
}

func TestMismatchValuesAreBounded(t *testing.T) {
	left, right := fixtureObservation(), fixtureObservation()
	left.Metadata["large"] = json.RawMessage(`"` + strings.Repeat("a", maxMismatchValueBytes*2) + `"`)
	right.Metadata["large"] = json.RawMessage(`"` + strings.Repeat("b", maxMismatchValueBytes*2) + `"`)
	report, err := CompareObservations(context.Background(), left, right, DefaultShadowPolicy())
	if err != nil || len(report.Mismatches) != 1 {
		t.Fatalf("report = %#v, %v", report, err)
	}
	if len(report.Mismatches[0].Expected) > 256 || !strings.Contains(report.Mismatches[0].Expected, "truncated sha256") {
		t.Fatalf("unbounded mismatch value: %q", report.Mismatches[0].Expected)
	}
}

func TestCanonicalReportsAndAggregation(t *testing.T) {
	left, right := fixtureObservation(), fixtureObservation()
	right.Request.Method = "POST"
	report, err := CompareObservations(context.Background(), left, right, DefaultShadowPolicy())
	if err != nil {
		t.Fatal(err)
	}
	first, firstHash, err := CanonicalShadowReport(context.Background(), report)
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := CanonicalShadowReport(context.Background(), report)
	if err != nil || !bytes.Equal(first, second) || firstHash != secondHash {
		t.Fatal("report serialization is not canonical")
	}
	summary, err := AggregateShadowReports(context.Background(), []ShadowReport{report, {SchemaVersion: 1, Equal: true}})
	if err != nil || summary.Reports != 2 || summary.EqualReports != 1 || summary.ByClass[ClassRequest] == 0 || summary.BySeverity[SeverityHigh] == 0 {
		t.Fatalf("summary = %#v, %v", summary, err)
	}
	if _, err := AggregateShadowReports(context.Background(), make([]ShadowReport, MaxAggregateReports+1)); !errors.Is(err, ErrObservationLimit) {
		t.Fatalf("aggregate bound = %v", err)
	}
}

func TestMetadataAndWarningSecretRedaction(t *testing.T) {
	input := fixtureObservation()
	input.Request.Headers["Referer"] = []string{"https://media.example/watch?token=referer-secret"}
	input.Metadata["http_headers"] = json.RawMessage(`{"Authorization":"Bearer metadata-secret","X-Safe":"yes"}`)
	input.Metadata["media_url"] = json.RawMessage(`"https://media.example/video?access_token=metadata-token&visible=yes"`)
	input.Warnings = []WarningObservation{{Code: "fixture", Message: "failed https://media.example/v?token=warning-secret token=inline-secret"}}
	encoded, _, err := CanonicalObservation(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"referer-secret", "metadata-secret", "metadata-token", "warning-secret", "inline-secret"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("serialized observation leaked %q: %s", secret, encoded)
		}
	}
}

func FuzzParseObservation(f *testing.F) {
	seed, err := os.ReadFile(filepath.Join("..", "..", "conformance", "differential", "phase3", "reference.json"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(`{"schema_version":1}`))
	f.Fuzz(func(t *testing.T, input []byte) {
		observation, err := ParseObservation(context.Background(), bytes.NewReader(input))
		if err != nil {
			return
		}
		encoded, _, err := CanonicalObservation(context.Background(), observation)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip, err := ParseObservation(context.Background(), bytes.NewReader(encoded))
		if err != nil {
			t.Fatal(err)
		}
		first, _, _ := CanonicalObservation(context.Background(), observation)
		second, _, _ := CanonicalObservation(context.Background(), roundTrip)
		if !reflect.DeepEqual(first, second) {
			t.Fatal("canonical round-trip changed bytes")
		}
	})
}

func fixtureObservation() ObservationEnvelope {
	bitrate := 128.0
	return ObservationEnvelope{
		SchemaVersion: 1, Producer: "fixture", Routing: RoutingObservation{InputURL: "https://media.example/watch?id=1", Extractor: "fixture"},
		Request:  RequestObservation{Method: "GET", URL: "https://api.example/item?id=1", Headers: map[string][]string{"Accept": {"application/json"}}},
		Metadata: map[string]json.RawMessage{"title": json.RawMessage(`"title"`), "description": json.RawMessage(`null`)},
		Formats:  []FormatObservation{{ID: "audio", Protocol: "https", Bitrate: &bitrate, Usable: true}, {ID: "video", Protocol: "hls", Usable: true}},
		Playlist: PlaylistObservation{ID: "one", Entries: []PlaylistEntryObservation{{ID: "entry"}}},
		Warnings: []WarningObservation{}, Protocols: []ProtocolObservation{{Name: "hls", Usable: true}},
	}
}

func loadObservationFixture(t *testing.T, name string) ObservationEnvelope {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "conformance", "differential", "phase3", name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result, err := ParseObservation(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertSemanticMismatch(t *testing.T, report ShadowReport, class MismatchClass, severity Severity, path string) {
	t.Helper()
	for _, mismatch := range report.Mismatches {
		if mismatch.Class == class && mismatch.Severity == severity && mismatch.Path == path {
			return
		}
	}
	t.Fatalf("mismatch %s/%s/%s not found in %#v", class, severity, path, report.Mismatches)
}
