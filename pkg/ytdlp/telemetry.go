package ytdlp

import (
	"context"
	"io"

	"github.com/ytdlp-go/ytdlp/internal/telemetry"
)

const (
	// TelemetryUnknownExtractor is the bounded bucket used when an operation
	// fails before routing or selects an extractor outside the configured set.
	TelemetryUnknownExtractor  = "unknown"
	TelemetryCapabilityExtract = "extract"
)

type TelemetryOutcome = telemetry.Outcome

const (
	TelemetryOutcomeSuccess     = telemetry.OutcomeSuccess
	TelemetryOutcomeError       = telemetry.OutcomeError
	TelemetryOutcomeFallback    = telemetry.OutcomeFallback
	TelemetryOutcomeUnsupported = telemetry.OutcomeUnsupported
)

type TelemetryCount = telemetry.Count
type TelemetryOverflow = telemetry.Overflow
type TelemetrySnapshot = telemetry.Snapshot
type TelemetryCoverage = telemetry.Coverage

// TelemetryConfig declares the only extractor identifiers that may become
// dimensions. Unknown and plugin-specific identifiers are aggregated into the
// fixed "unknown" bucket unless explicitly included here.
type TelemetryConfig struct {
	Extractors []string
	MaxCells   int
}

// TelemetryCollector is an opt-in, privacy-safe aggregate counter. It cannot
// receive URLs, metadata, credentials, headers, paths, or arbitrary errors.
type TelemetryCollector struct {
	aggregator *telemetry.Aggregator
	extractors map[string]struct{}
}

func NewTelemetryCollector(config TelemetryConfig) (*TelemetryCollector, error) {
	extractors := make([]string, 0, len(config.Extractors)+1)
	allowed := make(map[string]struct{}, len(config.Extractors)+1)
	for _, extractor := range config.Extractors {
		if _, exists := allowed[extractor]; exists {
			continue
		}
		allowed[extractor] = struct{}{}
		extractors = append(extractors, extractor)
	}
	if _, exists := allowed[TelemetryUnknownExtractor]; !exists {
		allowed[TelemetryUnknownExtractor] = struct{}{}
		extractors = append(extractors, TelemetryUnknownExtractor)
	}
	aggregator, err := telemetry.New(telemetry.Config{
		Extractors: extractors, Capabilities: []string{TelemetryCapabilityExtract}, MaxCells: config.MaxCells,
	})
	if err != nil {
		return nil, err
	}
	return &TelemetryCollector{aggregator: aggregator, extractors: allowed}, nil
}

func (collector *TelemetryCollector) observe(extractor string, outcome TelemetryOutcome) {
	if collector == nil || collector.aggregator == nil {
		return
	}
	if _, approved := collector.extractors[extractor]; !approved {
		extractor = TelemetryUnknownExtractor
	}
	// Recording is a bounded in-memory increment and intentionally outlives a
	// canceled media operation so cancellation still remains in the denominator.
	_ = collector.aggregator.Observe(context.Background(), extractor, TelemetryCapabilityExtract, outcome)
}

func (collector *TelemetryCollector) Snapshot() TelemetrySnapshot {
	if collector == nil || collector.aggregator == nil {
		return TelemetrySnapshot{}
	}
	return collector.aggregator.Snapshot()
}

func (collector *TelemetryCollector) Reset() TelemetrySnapshot {
	if collector == nil || collector.aggregator == nil {
		return TelemetrySnapshot{}
	}
	return collector.aggregator.Reset()
}

func (collector *TelemetryCollector) Merge(ctx context.Context, snapshot TelemetrySnapshot) error {
	if collector == nil || collector.aggregator == nil {
		return telemetry.ErrInvalidConfig
	}
	return collector.aggregator.Merge(ctx, snapshot)
}

func (collector *TelemetryCollector) WriteCanonical(ctx context.Context, writer io.Writer) error {
	if collector == nil || collector.aggregator == nil {
		return telemetry.ErrInvalidConfig
	}
	return telemetry.WriteCanonical(ctx, writer, collector.Snapshot())
}

func (collector *TelemetryCollector) Coverage() (TelemetryCoverage, error) {
	if collector == nil || collector.aggregator == nil {
		return TelemetryCoverage{}, telemetry.ErrInvalidConfig
	}
	return telemetry.CalculateCoverage(collector.Snapshot())
}

func DecodeTelemetrySnapshot(ctx context.Context, reader io.Reader, maxBytes int64) (TelemetrySnapshot, error) {
	return telemetry.Decode(ctx, reader, maxBytes)
}
