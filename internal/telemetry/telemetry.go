// Package telemetry provides privacy-preserving, bounded-cardinality counters
// for measuring extractor and capability outcomes.
//
// The package intentionally accepts only constructor-approved extractor and
// capability identifiers and a closed set of outcomes. It has no API for raw
// URLs, media metadata, arbitrary labels, credentials, or error messages.
package telemetry

import (
	"context"
	"errors"
	"math"
	"regexp"
	"sort"
	"sync"
)

const (
	SchemaVersion = 1

	defaultMaxCells = 4096
	hardMaxNames    = 4096
	hardMaxCells    = 1_000_000
)

var (
	ErrInvalidConfig     = errors.New("invalid telemetry configuration")
	ErrUnknownExtractor  = errors.New("unknown telemetry extractor")
	ErrUnknownCapability = errors.New("unknown telemetry capability")
	ErrInvalidOutcome    = errors.New("invalid telemetry outcome")
	ErrCapacity          = errors.New("telemetry capacity exhausted")
	ErrCounterSaturated  = errors.New("telemetry counter saturated")
	ErrInvalidSnapshot   = errors.New("invalid telemetry snapshot")
	ErrDecodeLimit       = errors.New("telemetry snapshot exceeds decode limit")
)

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Outcome is a closed, non-user-controlled result category.
type Outcome string

const (
	OutcomeSuccess     Outcome = "success"
	OutcomeError       Outcome = "error"
	OutcomeFallback    Outcome = "fallback"
	OutcomeUnsupported Outcome = "unsupported"
)

func (o Outcome) valid() bool {
	switch o {
	case OutcomeSuccess, OutcomeError, OutcomeFallback, OutcomeUnsupported:
		return true
	default:
		return false
	}
}

// Config fixes every permitted string dimension before observations begin.
// MaxCells bounds the number of distinct extractor/capability/outcome tuples.
type Config struct {
	Extractors   []string
	Capabilities []string
	MaxCells     int
}

type key struct {
	extractor  string
	capability string
	outcome    Outcome
}

// Aggregator is safe for concurrent use.
type Aggregator struct {
	mu           sync.RWMutex
	extractors   map[string]struct{}
	capabilities map[string]struct{}
	maxCells     int
	counts       map[key]uint64
	overflow     Overflow
}

// Count is one bounded-cardinality aggregate. It never contains event data.
type Count struct {
	Extractor  string  `json:"extractor"`
	Capability string  `json:"capability"`
	Outcome    Outcome `json:"outcome"`
	Count      uint64  `json:"count"`
}

// Overflow accounts for observations not represented exactly. CellLimit is
// the number of observations dropped when MaxCells was full. CounterSaturation
// is the amount that could not be added because a uint64 counter was full.
type Overflow struct {
	CellLimit         uint64 `json:"cell_limit"`
	CounterSaturation uint64 `json:"counter_saturation"`
}

// Snapshot is a deterministic, timestamp-free interchange representation.
// Counts are always sorted by extractor, capability, then outcome.
type Snapshot struct {
	SchemaVersion int      `json:"schema_version"`
	Counts        []Count  `json:"counts"`
	Overflow      Overflow `json:"overflow"`
}

// New validates and copies configuration. At least one extractor and one
// capability are required; duplicate names and unsafe identifiers are rejected.
func New(config Config) (*Aggregator, error) {
	maxCells := config.MaxCells
	if maxCells == 0 {
		maxCells = defaultMaxCells
	}
	if maxCells < 1 || maxCells > hardMaxCells || len(config.Extractors) == 0 || len(config.Extractors) > hardMaxNames || len(config.Capabilities) == 0 || len(config.Capabilities) > hardMaxNames {
		return nil, ErrInvalidConfig
	}
	extractors, ok := nameSet(config.Extractors)
	if !ok {
		return nil, ErrInvalidConfig
	}
	capabilities, ok := nameSet(config.Capabilities)
	if !ok {
		return nil, ErrInvalidConfig
	}
	return &Aggregator{
		extractors:   extractors,
		capabilities: capabilities,
		maxCells:     maxCells,
		counts:       make(map[key]uint64),
	}, nil
}

func nameSet(names []string) (map[string]struct{}, bool) {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !validName.MatchString(name) {
			return nil, false
		}
		if _, duplicate := set[name]; duplicate {
			return nil, false
		}
		set[name] = struct{}{}
	}
	return set, true
}

// Observe increments one approved tuple. Returned errors are fixed sentinels
// and never include the rejected value.
func (a *Aggregator) Observe(ctx context.Context, extractor, capability string, outcome Outcome) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if _, ok := a.extractors[extractor]; !ok {
		return ErrUnknownExtractor
	}
	if _, ok := a.capabilities[capability]; !ok {
		return ErrUnknownCapability
	}
	if !outcome.valid() {
		return ErrInvalidOutcome
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return err
	}
	k := key{extractor: extractor, capability: capability, outcome: outcome}
	current, exists := a.counts[k]
	if !exists && len(a.counts) >= a.maxCells {
		saturatingAdd(&a.overflow.CellLimit, 1)
		return ErrCapacity
	}
	if current == math.MaxUint64 {
		saturatingAdd(&a.overflow.CounterSaturation, 1)
		return ErrCounterSaturated
	}
	a.counts[k] = current + 1
	return nil
}

// Snapshot returns a stable point-in-time copy.
func (a *Aggregator) Snapshot() Snapshot {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return snapshotOf(a.counts, a.overflow)
}

// Reset atomically returns the prior snapshot and clears all counters.
func (a *Aggregator) Reset() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	previous := snapshotOf(a.counts, a.overflow)
	a.counts = make(map[key]uint64)
	a.overflow = Overflow{}
	return previous
}

func snapshotOf(counts map[key]uint64, overflow Overflow) Snapshot {
	result := Snapshot{SchemaVersion: SchemaVersion, Counts: make([]Count, 0, len(counts)), Overflow: overflow}
	for k, count := range counts {
		result.Counts = append(result.Counts, Count{Extractor: k.extractor, Capability: k.capability, Outcome: k.outcome, Count: count})
	}
	sortCounts(result.Counts)
	return result
}

func sortCounts(counts []Count) {
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].Extractor != counts[j].Extractor {
			return counts[i].Extractor < counts[j].Extractor
		}
		if counts[i].Capability != counts[j].Capability {
			return counts[i].Capability < counts[j].Capability
		}
		return counts[i].Outcome < counts[j].Outcome
	})
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func saturatingAdd(destination *uint64, amount uint64) (lost uint64) {
	if math.MaxUint64-*destination < amount {
		lost = amount - (math.MaxUint64 - *destination)
		*destination = math.MaxUint64
		return lost
	}
	*destination += amount
	return 0
}
