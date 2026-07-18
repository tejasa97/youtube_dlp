package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

func fixtureBytes(t *testing.T) []byte {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "plugin", "abi-v1.1", "compatible-extractor-upgrade.json"))
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func fixtureValue(t *testing.T) Fixture {
	t.Helper()
	fixture, err := Decode(context.Background(), bytes.NewReader(fixtureBytes(t)), 0)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestCompatibleMinorUpgradeReport(t *testing.T) {
	fixture := fixtureValue(t)
	report, err := Evaluate(context.Background(), fixture)
	if err != nil {
		t.Fatalf("Evaluate() report=%+v error=%v", report, err)
	}
	if report.Status != "compatible" || report.SelectedABI != pluginapi.V1_1 {
		t.Fatalf("report=%+v", report)
	}
	if !reflect.DeepEqual(report.IgnoredCandidateOptions, []string{"request.options.client_hints"}) {
		t.Fatalf("ignored options=%v", report.IgnoredCandidateOptions)
	}
	if !reflect.DeepEqual(report.PreservedCandidateFields, []string{"response.metadata.availability", "response.metadata.provenance"}) {
		t.Fatalf("preserved fields=%v", report.PreservedCandidateFields)
	}
	if len(report.Checks) != 10 {
		t.Fatalf("checks=%+v", report.Checks)
	}
	encoded, err := MarshalReport(report)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Report
	if err := json.Unmarshal(encoded, &roundTrip); err != nil || !reflect.DeepEqual(roundTrip, report) {
		t.Fatalf("report round trip=%+v error=%v", roundTrip, err)
	}
	second, err := Evaluate(context.Background(), fixture)
	if err != nil || !reflect.DeepEqual(second, report) {
		t.Fatalf("non-deterministic report=%+v error=%v", second, err)
	}
}

func TestRejectsPythonMajorDowngradeAndInvariantChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Fixture)
		target error
	}{
		{
			name: "Python runtime",
			mutate: func(value *Fixture) {
				value.Candidate.Manifest.Runtime = "python"
			},
			target: ErrPythonRuntime,
		},
		{
			name: "new major",
			mutate: func(value *Fixture) {
				value.Candidate.Manifest.ABIRange = pluginapi.VersionRange{Minimum: pluginapi.Version(2, 0), Maximum: pluginapi.Version(2, 0)}
			},
			target: ErrIncompatible,
		},
		{
			name: "downgrade",
			mutate: func(value *Fixture) {
				value.Baseline.Manifest.ABIRange.Maximum = pluginapi.V1_1
				value.Candidate.Manifest.ABIRange.Maximum = pluginapi.V1_0
			},
			target: ErrIncompatible,
		},
		{
			name: "permission escalation",
			mutate: func(value *Fixture) {
				value.Candidate.Manifest.Permissions = append(value.Candidate.Manifest.Permissions, pluginapi.PermissionCookies)
			},
			target: ErrInvariant,
		},
		{
			name: "capability expansion",
			mutate: func(value *Fixture) {
				value.Candidate.Manifest.Capabilities = append(value.Candidate.Manifest.Capabilities, pluginapi.CapabilityProvider)
			},
			target: ErrInvariant,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := fixtureValue(t)
			test.mutate(&fixture)
			report, err := Evaluate(context.Background(), fixture)
			if !errors.Is(err, test.target) || report.Status != "rejected" || len(report.Checks) == 0 || report.Checks[len(report.Checks)-1].Status != "failed" {
				t.Fatalf("report=%+v error=%v", report, err)
			}
		})
	}
}

func TestRejectsChangedRequiredAndBaselineFields(t *testing.T) {
	tests := []func(*Fixture){
		func(value *Fixture) { rewriteCandidate(t, value, "request-1", "https://changed.invalid/watch/1", nil) },
		func(value *Fixture) { rewriteCandidate(t, value, "request-1", "", map[string]any{"title": "changed"}) },
	}
	for index, mutate := range tests {
		fixture := fixtureValue(t)
		mutate(&fixture)
		report, err := Evaluate(context.Background(), fixture)
		if !errors.Is(err, ErrInvariant) || report.Status != "rejected" {
			t.Fatalf("case %d report=%+v error=%v", index, report, err)
		}
	}
}

func rewriteCandidate(t *testing.T, fixture *Fixture, id, rawURL string, metadata map[string]any) {
	t.Helper()
	var request pluginapi.Envelope
	if err := json.Unmarshal(fixture.Candidate.Transcript[0], &request); err != nil {
		t.Fatal(err)
	}
	request.ExtractRequest.ID = id
	if rawURL != "" {
		request.ExtractRequest.URL = rawURL
	}
	fixture.Candidate.Transcript[0], _ = json.Marshal(request)
	if metadata != nil {
		var response pluginapi.Envelope
		_ = json.Unmarshal(fixture.Candidate.Transcript[1], &response)
		for key, value := range metadata {
			response.ExtractResponse.Metadata[key] = value
		}
		fixture.Candidate.Transcript[1], _ = json.Marshal(response)
	}
}

func TestDecodeBoundsMalformedAndCancellation(t *testing.T) {
	if _, err := Decode(context.Background(), strings.NewReader(`{"schema":"x","unknown":true}`), 0); !errors.Is(err, ErrMalformedFixture) {
		t.Fatalf("unknown field error=%v", err)
	}
	duplicate := bytes.Replace(fixtureBytes(t), []byte(`"case_id":`), []byte(`"case_id":"duplicate","case_id":`), 1)
	if _, err := Decode(context.Background(), bytes.NewReader(duplicate), 0); !errors.Is(err, ErrMalformedFixture) {
		t.Fatalf("duplicate field error=%v", err)
	}
	if _, err := Decode(context.Background(), bytes.NewReader(fixtureBytes(t)), 64); !errors.Is(err, ErrFixtureTooLarge) {
		t.Fatalf("oversize error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Decode(ctx, bytes.NewReader(fixtureBytes(t)), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled decode error=%v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	reader := &cancellingReader{payload: fixtureBytes(t), cancel: cancel}
	if _, err := Decode(ctx, reader, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-read cancellation error=%v", err)
	}
	if _, err := Decode(context.Background(), zeroReader{}, 0); !errors.Is(err, ErrMalformedFixture) {
		t.Fatalf("zero reader error=%v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read([]byte) (int, error) { return 0, nil }

type cancellingReader struct {
	payload []byte
	cancel  context.CancelFunc
	done    bool
}

func (reader *cancellingReader) Read(target []byte) (int, error) {
	if reader.done {
		return 0, io.EOF
	}
	reader.done = true
	written := copy(target, reader.payload[:min(len(reader.payload), 32)])
	reader.cancel()
	return written, nil
}

func TestEvaluateCancellationAndMalformedTranscripts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Evaluate(ctx, fixtureValue(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled evaluate error=%v", err)
	}
	tests := []json.RawMessage{
		json.RawMessage(`{"type":"extract","type":"result"}`),
		json.RawMessage(`{"type":"extract","extension":true}`),
		json.RawMessage(`{"type":"extract"}`),
	}
	for _, malformed := range tests {
		fixture := fixtureValue(t)
		fixture.Candidate.Transcript[0] = malformed
		if _, err := Evaluate(context.Background(), fixture); !errors.Is(err, ErrMalformedFixture) {
			t.Fatalf("message=%s error=%v", malformed, err)
		}
	}
}

func TestCanonicalTranscriptRejectsDuplicateAndIsStable(t *testing.T) {
	fixture := fixtureValue(t)
	first, err := CanonicalTranscript(fixture)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalTranscript(fixture)
	if err != nil || !bytes.Equal(first, second) || !bytes.HasSuffix(first, []byte("\n")) {
		t.Fatalf("canonical transcript unstable: %v", err)
	}
	fixture.Candidate.Transcript[0] = json.RawMessage(`{"type":"extract","type":"result"}`)
	if _, err := CanonicalTranscript(fixture); !errors.Is(err, ErrMalformedFixture) {
		t.Fatalf("duplicate key error=%v", err)
	}
}

func FuzzDecodeFixture(f *testing.F) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "..", "conformance", "plugin", "abi-v1.1", "compatible-extractor-upgrade.json"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(payload)
	f.Add([]byte(`{"runtime":"python"}`))
	f.Fuzz(func(t *testing.T, input []byte) {
		fixture, err := Decode(context.Background(), bytes.NewReader(input), 256<<10)
		if err != nil {
			return
		}
		_, _ = Evaluate(context.Background(), fixture)
		_, _ = CanonicalTranscript(fixture)
	})
}

var _ io.Reader = zeroReader{}
