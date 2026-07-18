// Package upgrade provides deterministic conformance evidence for compatible
// minor revisions of the out-of-process plugin ABI. It is an audit harness,
// not a second runtime or transport implementation.
package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/plugin"
	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

const (
	fixtureSchema       = "ytdlp-go.plugin-upgrade/v1"
	defaultMaximumBytes = 1 << 20
	maximumBytes        = 4 << 20
)

var (
	ErrMalformedFixture = errors.New("malformed plugin upgrade fixture")
	ErrFixtureTooLarge  = errors.New("plugin upgrade fixture exceeds limit")
	ErrIncompatible     = errors.New("incompatible plugin upgrade")
	ErrInvariant        = errors.New("plugin upgrade invariant violated")
	ErrPythonRuntime    = errors.New("Python plugin runtime prohibited by upgrade contract")
)

// Fixture captures both sides of one proposed minor ABI revision. Transcript
// messages are real pluginapi envelopes; additive data is carried inside the
// ABI's existing options and metadata extension maps.
type Fixture struct {
	Schema                   string                 `json:"schema"`
	CaseID                   string                 `json:"case_id"`
	Host                     pluginapi.VersionRange `json:"host"`
	Baseline                 Contract               `json:"baseline"`
	Candidate                Contract               `json:"candidate"`
	ExpectedTranscriptSHA256 string                 `json:"expected_transcript_sha256"`
}

type Contract struct {
	Manifest   pluginapi.Manifest `json:"manifest"`
	Transcript []json.RawMessage  `json:"transcript"`
}

type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// Report is intentionally map-free so its JSON encoding is a deterministic,
// reviewable compatibility artifact.
type Report struct {
	Schema                   string   `json:"schema"`
	CaseID                   string   `json:"case_id"`
	Status                   string   `json:"status"`
	SelectedABI              uint32   `json:"selected_abi,omitempty"`
	IgnoredCandidateOptions  []string `json:"ignored_candidate_options,omitempty"`
	PreservedCandidateFields []string `json:"preserved_candidate_fields,omitempty"`
	TranscriptSHA256         string   `json:"transcript_sha256,omitempty"`
	Checks                   []Check  `json:"checks"`
}

func Decode(ctx context.Context, reader io.Reader, maximum int64) (Fixture, error) {
	if err := ctx.Err(); err != nil {
		return Fixture{}, err
	}
	if maximum <= 0 {
		maximum = defaultMaximumBytes
	}
	if maximum > maximumBytes {
		maximum = maximumBytes
	}
	var payload bytes.Buffer
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return Fixture{}, err
		}
		read, err := reader.Read(buffer)
		if read > 0 {
			if int64(payload.Len()+read) > maximum {
				return Fixture{}, fmt.Errorf("%w: maximum %d", ErrFixtureTooLarge, maximum)
			}
			_, _ = payload.Write(buffer[:read])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Fixture{}, fmt.Errorf("%w: read: %v", ErrMalformedFixture, err)
		}
		if read == 0 {
			return Fixture{}, fmt.Errorf("%w: reader made no progress", ErrMalformedFixture)
		}
	}
	if err := pluginapi.ValidateJSONFrame(payload.Bytes()); err != nil {
		return Fixture{}, fmt.Errorf("%w: %v", ErrMalformedFixture, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload.Bytes()))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var fixture Fixture
	if err := decoder.Decode(&fixture); err != nil {
		return Fixture{}, fmt.Errorf("%w: decode: %v", ErrMalformedFixture, err)
	}
	if fixture.Schema != fixtureSchema || fixture.CaseID == "" || len(fixture.CaseID) > 128 {
		return Fixture{}, fmt.Errorf("%w: invalid identity", ErrMalformedFixture)
	}
	return fixture, nil
}

// Evaluate checks one minor upgrade and returns a structured report even when
// compatibility is rejected.
func Evaluate(ctx context.Context, fixture Fixture) (Report, error) {
	report := Report{Schema: fixtureSchema, CaseID: fixture.CaseID, Status: "rejected", Checks: []Check{}}
	check := func(name string, err error) error {
		if err != nil {
			report.Checks = append(report.Checks, Check{Name: name, Status: "failed", Detail: err.Error()})
			return err
		}
		report.Checks = append(report.Checks, Check{Name: name, Status: "passed"})
		return nil
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := check("baseline_manifest", validateManifest(fixture.Baseline.Manifest)); err != nil {
		return report, err
	}
	if err := check("candidate_manifest", validateManifest(fixture.Candidate.Manifest)); err != nil {
		return report, err
	}
	if err := check("identity_runtime_capability_permission_invariants", validateInvariants(fixture.Baseline.Manifest, fixture.Candidate.Manifest)); err != nil {
		return report, err
	}
	if err := check("minor_range_is_non_downgrading", validateNonDowngrade(fixture.Baseline.Manifest.ABIRange, fixture.Candidate.Manifest.ABIRange)); err != nil {
		return report, err
	}
	selected, err := plugin.NegotiateRange(fixture.Host, fixture.Candidate.Manifest.ABIRange)
	if err != nil {
		err = fmt.Errorf("%w: %v", ErrIncompatible, err)
	}
	if err := check("host_negotiation", err); err != nil {
		return report, err
	}
	report.SelectedABI = selected
	baseline, err := decodeTranscript(fixture.Baseline.Transcript, fixture.Baseline.Manifest.ABIRange.Maximum)
	if err := check("baseline_transcript", err); err != nil {
		return report, err
	}
	candidate, err := decodeTranscript(fixture.Candidate.Transcript, selected)
	if err := check("candidate_transcript", err); err != nil {
		return report, err
	}
	ignored, preserved, err := compatibleProjection(baseline, candidate)
	if err := check("v1_0_optional_field_projection", err); err != nil {
		return report, err
	}
	report.IgnoredCandidateOptions = ignored
	report.PreservedCandidateFields = preserved
	canonical, err := CanonicalTranscript(fixture)
	if err := check("canonical_transcript", err); err != nil {
		return report, err
	}
	digest := sha256.Sum256(canonical)
	report.TranscriptSHA256 = hex.EncodeToString(digest[:])
	if len(fixture.ExpectedTranscriptSHA256) != sha256.Size*2 || fixture.ExpectedTranscriptSHA256 != report.TranscriptSHA256 {
		err = fmt.Errorf("%w: transcript digest got %s want %s", ErrInvariant, report.TranscriptSHA256, fixture.ExpectedTranscriptSHA256)
	}
	if err := check("pinned_transcript_digest", err); err != nil {
		return report, err
	}
	report.Status = "compatible"
	return report, nil
}

func validateManifest(manifest pluginapi.Manifest) error {
	err := plugin.ValidateManifest(manifest)
	switch {
	case errors.Is(err, plugin.ErrPythonRuntime):
		return fmt.Errorf("%w: %v", ErrPythonRuntime, err)
	case errors.Is(err, plugin.ErrIncompatibleVersion):
		return fmt.Errorf("%w: %v", ErrIncompatible, err)
	case err != nil:
		return fmt.Errorf("%w: %v", ErrMalformedFixture, err)
	default:
		return nil
	}
}

func validateInvariants(baseline, candidate pluginapi.Manifest) error {
	if baseline.Schema != candidate.Schema || baseline.ID != candidate.ID || baseline.Runtime != candidate.Runtime || baseline.Entrypoint != candidate.Entrypoint {
		return fmt.Errorf("%w: identity, schema, runtime, or entrypoint changed", ErrInvariant)
	}
	if !equalStrings(capabilities(baseline.Capabilities), capabilities(candidate.Capabilities)) {
		return fmt.Errorf("%w: capabilities changed without a new reviewed contract", ErrInvariant)
	}
	if !equalStrings(permissions(baseline.Permissions), permissions(candidate.Permissions)) {
		return fmt.Errorf("%w: permissions changed without a new approval", ErrInvariant)
	}
	return nil
}

func validateNonDowngrade(baseline, candidate pluginapi.VersionRange) error {
	if !pluginapi.Compatible(baseline.Minimum, candidate.Minimum) ||
		pluginapi.CompareVersions(candidate.Minimum, baseline.Minimum) > 0 ||
		pluginapi.CompareVersions(candidate.Maximum, baseline.Maximum) < 0 {
		return fmt.Errorf("%w: candidate %v does not retain baseline %v", ErrIncompatible, candidate, baseline)
	}
	return nil
}

type transcript struct {
	request  pluginapi.ExtractRequest
	response pluginapi.ExtractResponse
}

func decodeTranscript(messages []json.RawMessage, expectedVersion uint32) (transcript, error) {
	if len(messages) != 2 {
		return transcript{}, fmt.Errorf("%w: transcript must contain request and result", ErrMalformedFixture)
	}
	decoded := make([]pluginapi.Envelope, 2)
	for index, raw := range messages {
		if len(raw) == 0 || len(raw) > defaultMaximumBytes {
			return transcript{}, fmt.Errorf("%w: transcript message size", ErrMalformedFixture)
		}
		if err := pluginapi.ValidateJSONFrame(raw); err != nil {
			return transcript{}, fmt.Errorf("%w: message %d: %v", ErrMalformedFixture, index, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if err := decoder.Decode(&decoded[index]); err != nil {
			return transcript{}, fmt.Errorf("%w: message %d: %v", ErrMalformedFixture, index, err)
		}
	}
	request, response := decoded[0], decoded[1]
	if request.Type != "extract" || request.Version != expectedVersion || request.ExtractRequest == nil ||
		request.ExtractResponse != nil || request.PostprocessRequest != nil || request.ProviderRequest != nil {
		return transcript{}, fmt.Errorf("%w: invalid extract request envelope", ErrMalformedFixture)
	}
	if response.Type != "result" || response.ExtractResponse == nil || response.ExtractRequest != nil ||
		response.PostprocessResponse != nil || response.ProviderResponse != nil || response.Version != 0 {
		return transcript{}, fmt.Errorf("%w: invalid extract result envelope", ErrMalformedFixture)
	}
	if request.ExtractRequest.ID == "" || request.ExtractRequest.ID != response.ExtractResponse.ID || request.ExtractRequest.URL == "" {
		return transcript{}, fmt.Errorf("%w: request/response identity mismatch", ErrMalformedFixture)
	}
	return transcript{request: *request.ExtractRequest, response: *response.ExtractResponse}, nil
}

func compatibleProjection(baseline, candidate transcript) ([]string, []string, error) {
	if baseline.request.ID != candidate.request.ID || baseline.request.URL != candidate.request.URL || baseline.response.ID != candidate.response.ID {
		return nil, nil, fmt.Errorf("%w: required request fields changed", ErrInvariant)
	}
	if err := subset(baseline.request.Options, candidate.request.Options); err != nil {
		return nil, nil, fmt.Errorf("%w: baseline option %v", ErrInvariant, err)
	}
	if err := subset(baseline.response.Metadata, candidate.response.Metadata); err != nil {
		return nil, nil, fmt.Errorf("%w: baseline metadata %v", ErrInvariant, err)
	}
	ignored := addedKeys(baseline.request.Options, candidate.request.Options)
	preserved := addedKeys(baseline.response.Metadata, candidate.response.Metadata)
	for index := range ignored {
		ignored[index] = "request.options." + ignored[index]
	}
	for index := range preserved {
		preserved[index] = "response.metadata." + preserved[index]
	}
	return ignored, preserved, nil
}

func subset(baseline, candidate map[string]any) error {
	for key, value := range baseline {
		other, ok := candidate[key]
		if !ok || !jsonEqual(value, other) {
			return fmt.Errorf("%q was removed or changed", key)
		}
	}
	return nil
}

func jsonEqual(left, right any) bool {
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	return bytes.Equal(a, b)
}

func addedKeys(baseline, candidate map[string]any) []string {
	result := make([]string, 0)
	for key := range candidate {
		if _, ok := baseline[key]; !ok {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

func capabilities(values []pluginapi.Capability) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func permissions(values []pluginapi.Permission) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func equalStrings(left, right []string) bool {
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

// CanonicalTranscript returns deterministic JSON Lines ordered baseline then
// candidate. Object keys are sorted by encoding/json; duplicate keys and
// non-UTF-8 input are rejected before normalization.
func CanonicalTranscript(fixture Fixture) ([]byte, error) {
	var output bytes.Buffer
	for _, contract := range []Contract{fixture.Baseline, fixture.Candidate} {
		for _, raw := range contract.Transcript {
			if err := pluginapi.ValidateJSONFrame(raw); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrMalformedFixture, err)
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			var value any
			if err := decoder.Decode(&value); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrMalformedFixture, err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, fmt.Errorf("%w: canonical encode: %v", ErrMalformedFixture, err)
			}
			output.Write(encoded)
			output.WriteByte('\n')
		}
	}
	return output.Bytes(), nil
}

func MarshalReport(report Report) ([]byte, error) {
	if report.Schema != fixtureSchema || strings.TrimSpace(report.CaseID) == "" {
		return nil, fmt.Errorf("%w: incomplete report", ErrMalformedFixture)
	}
	return json.Marshal(report)
}
