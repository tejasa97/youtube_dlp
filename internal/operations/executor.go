package operations

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

type Outcome string

const (
	OutcomeSuccess               Outcome = "success"
	OutcomeBreakage              Outcome = "breakage"
	OutcomeFallback              Outcome = "fallback"
	OutcomeUnsupported           Outcome = "unsupported"
	OutcomeCredentialUnavailable Outcome = "credential_unavailable"
	OutcomeRegionUnavailable     Outcome = "region_unavailable"
	OutcomeCanceled              Outcome = "canceled"
	OutcomeTimeout               Outcome = "timeout"
)

func (outcome Outcome) valid() bool {
	switch outcome {
	case OutcomeSuccess, OutcomeBreakage, OutcomeFallback, OutcomeUnsupported,
		OutcomeCredentialUnavailable, OutcomeRegionUnavailable, OutcomeCanceled, OutcomeTimeout:
		return true
	default:
		return false
	}
}

type FailureClass string

const (
	FailureNone      FailureClass = "none"
	FailureExtractor FailureClass = "extractor"
	FailureNetwork   FailureClass = "network"
	FailureAuth      FailureClass = "auth"
	FailureRegion    FailureClass = "region"
	FailureMedia     FailureClass = "media"
	FailureContract  FailureClass = "contract"
	FailureRunner    FailureClass = "runner"
)

func (failure FailureClass) valid() bool {
	switch failure {
	case FailureNone, FailureExtractor, FailureNetwork, FailureAuth, FailureRegion, FailureMedia, FailureContract, FailureRunner:
		return true
	default:
		return false
	}
}

// Invocation is the bounded input given to a deployment-owned Runner. Secret
// contains a reference only; resolving its value is outside this package.
type Invocation struct {
	ID           string
	Class        CanaryClass
	Extractor    string
	TargetRef    string
	Capabilities []string
	Secret       SecretHandle
	Region       string
}

// Observation is a closed semantic result. Capability, when present, must be
// one of the spec's predeclared capabilities. There is no error-text field.
type Observation struct {
	Outcome    Outcome
	Failure    FailureClass
	Capability string
}

type Runner interface {
	Run(context.Context, Invocation) (Observation, error)
}

type RunnerFunc func(context.Context, Invocation) (Observation, error)

func (function RunnerFunc) Run(ctx context.Context, invocation Invocation) (Observation, error) {
	return function(ctx, invocation)
}

type RunOptions struct {
	OptIn bool
}

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Record intentionally excludes target references, secret handles, regions,
// URLs, error text, and media metadata.
type Record struct {
	CanaryID      string       `json:"canary_id"`
	Class         CanaryClass  `json:"class"`
	Extractor     string       `json:"extractor"`
	Outcome       Outcome      `json:"outcome"`
	Failure       FailureClass `json:"failure_class"`
	Capability    string       `json:"capability,omitempty"`
	StartedUnixMS int64        `json:"started_unix_ms"`
	DurationMS    int64        `json:"duration_ms"`
}

type RecordSet struct {
	SchemaVersion int      `json:"schema_version"`
	Records       []Record `json:"records"`
}

// Execute runs canaries sequentially in canonical suite order. It enforces a
// per-canary deadline even if a buggy runner fails to return. Such a runner may
// leave its own goroutine alive, so production runners must also honor context.
func Execute(ctx context.Context, suite Suite, options RunOptions, runner Runner, clock Clock) ([]Record, error) {
	if !options.OptIn {
		return nil, ErrOptInRequired
	}
	canonical, err := NewSuite(suite.Canaries)
	if err != nil || suite.SchemaVersion != SchemaVersion || runner == nil || ctx == nil {
		return nil, ErrInvalidSpec
	}
	if clock == nil {
		clock = realClock{}
	}
	records := make([]Record, 0, len(canonical.Canaries))
	for _, spec := range canonical.Canaries {
		if err := contextError(ctx); err != nil {
			return records, err
		}
		started := clock.Now().UTC()
		child, cancel := context.WithTimeout(ctx, time.Duration(spec.TimeoutMS)*time.Millisecond)
		resultChannel := make(chan runnerResult, 1)
		invocation := Invocation{
			ID: spec.ID, Class: spec.Class, Extractor: spec.Extractor, TargetRef: spec.TargetRef,
			Capabilities: append([]string(nil), spec.Capabilities...), Secret: spec.Secret, Region: spec.Region,
		}
		go func() {
			resultChannel <- runSafely(child, runner, invocation)
		}()
		record := Record{
			CanaryID: spec.ID, Class: spec.Class, Extractor: spec.Extractor,
			StartedUnixMS: started.UnixMilli(), Failure: FailureNone,
		}
		select {
		case result := <-resultChannel:
			cancel()
			record.DurationMS = elapsedMilliseconds(started, clock.Now())
			if record.DurationMS > spec.TimeoutMS {
				record.DurationMS = spec.TimeoutMS
			}
			if result.err != nil {
				record.Outcome, record.Failure = OutcomeBreakage, FailureRunner
			} else if !validObservation(spec, result.observation) {
				record.Outcome, record.Failure = OutcomeBreakage, FailureContract
			} else {
				record.Outcome, record.Failure, record.Capability = result.observation.Outcome, result.observation.Failure, result.observation.Capability
			}
		case <-child.Done():
			doneErr := child.Err()
			cancel()
			record.DurationMS = elapsedMilliseconds(started, clock.Now())
			if errors.Is(doneErr, context.DeadlineExceeded) {
				record.Outcome, record.Failure = OutcomeTimeout, FailureRunner
				if record.DurationMS < spec.TimeoutMS {
					record.DurationMS = spec.TimeoutMS
				}
			} else {
				record.Outcome, record.Failure = OutcomeCanceled, FailureRunner
			}
			records = append(records, record)
			if contextError(ctx) != nil {
				return records, contextError(ctx)
			}
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

type runnerResult struct {
	observation Observation
	err         error
}

func runSafely(ctx context.Context, runner Runner, invocation Invocation) (result runnerResult) {
	defer func() {
		if recover() != nil {
			// Panic values can contain credentials, URLs, or arbitrary text. Do
			// not preserve them; the closed runner failure class is sufficient.
			result = runnerResult{err: ErrInvalidOutcome}
		}
	}()
	result.observation, result.err = runner.Run(ctx, invocation)
	return result
}

func validObservation(spec CanarySpec, observation Observation) bool {
	if !observation.Outcome.valid() || !observation.Failure.valid() {
		return false
	}
	switch observation.Outcome {
	case OutcomeSuccess, OutcomeFallback, OutcomeUnsupported:
		if observation.Failure != FailureNone {
			return false
		}
	case OutcomeBreakage:
		if observation.Failure == FailureNone {
			return false
		}
	case OutcomeCredentialUnavailable:
		if observation.Failure != FailureAuth {
			return false
		}
	case OutcomeRegionUnavailable:
		if observation.Failure != FailureRegion {
			return false
		}
	case OutcomeCanceled, OutcomeTimeout:
		if observation.Failure != FailureRunner {
			return false
		}
	}
	if observation.Capability != "" {
		index := sort.SearchStrings(spec.Capabilities, observation.Capability)
		if index >= len(spec.Capabilities) || spec.Capabilities[index] != observation.Capability {
			return false
		}
	}
	return true
}

func elapsedMilliseconds(start, end time.Time) int64 {
	if end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

func MarshalRecords(suite Suite, records []Record) ([]byte, error) {
	canonical, err := NewSuite(suite.Canaries)
	if err != nil || suite.SchemaVersion != SchemaVersion {
		return nil, ErrInvalidSpec
	}
	allowed := make(map[string]CanarySpec, len(canonical.Canaries))
	for _, spec := range canonical.Canaries {
		allowed[spec.ID] = spec
	}
	copyRecords := append([]Record(nil), records...)
	sort.SliceStable(copyRecords, func(i, j int) bool {
		if copyRecords[i].StartedUnixMS != copyRecords[j].StartedUnixMS {
			return copyRecords[i].StartedUnixMS < copyRecords[j].StartedUnixMS
		}
		return copyRecords[i].CanaryID < copyRecords[j].CanaryID
	})
	for _, record := range copyRecords {
		spec, ok := allowed[record.CanaryID]
		if !ok || spec.Class != record.Class || spec.Extractor != record.Extractor || !validRecord(record) ||
			!validObservation(spec, Observation{Outcome: record.Outcome, Failure: record.Failure, Capability: record.Capability}) {
			return nil, ErrInvalidOutcome
		}
	}
	return json.Marshal(RecordSet{SchemaVersion: SchemaVersion, Records: copyRecords})
}

func validRecord(record Record) bool {
	return identifierPattern.MatchString(record.CanaryID) && identifierPattern.MatchString(record.Extractor) &&
		record.Class.valid() && record.Outcome.valid() && record.Failure.valid() &&
		(record.Capability == "" || identifierPattern.MatchString(record.Capability)) &&
		record.StartedUnixMS >= 0 && record.DurationMS >= 0 && record.DurationMS <= MaxTimeoutMS
}
