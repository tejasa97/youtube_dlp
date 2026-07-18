package telemetry

import (
	"context"
	"math"
)

// Merge atomically incorporates a snapshot. Invalid or canceled merges leave
// the aggregator unchanged. Valid counts beyond MaxCells are reflected in
// Overflow.CellLimit instead of creating unbounded cardinality.
func (a *Aggregator) Merge(ctx context.Context, incoming Snapshot) error {
	entries, err := a.validateSnapshot(ctx, incoming)
	if err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return err
	}

	counts := make(map[key]uint64, len(a.counts))
	for k, count := range a.counts {
		counts[k] = count
	}
	overflow := a.overflow
	saturatingAdd(&overflow.CellLimit, incoming.Overflow.CellLimit)
	saturatingAdd(&overflow.CounterSaturation, incoming.Overflow.CounterSaturation)

	for index, entry := range entries {
		if index&255 == 0 {
			if err := contextError(ctx); err != nil {
				return err
			}
		}
		k := key{extractor: entry.Extractor, capability: entry.Capability, outcome: entry.Outcome}
		current, exists := counts[k]
		if !exists && len(counts) >= a.maxCells {
			saturatingAdd(&overflow.CellLimit, entry.Count)
			continue
		}
		if math.MaxUint64-current < entry.Count {
			lost := entry.Count - (math.MaxUint64 - current)
			counts[k] = math.MaxUint64
			saturatingAdd(&overflow.CounterSaturation, lost)
			continue
		}
		counts[k] = current + entry.Count
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	a.counts = counts
	a.overflow = overflow
	return nil
}

func (a *Aggregator) validateSnapshot(ctx context.Context, snapshot Snapshot) ([]Count, error) {
	if snapshot.SchemaVersion != SchemaVersion || len(snapshot.Counts) > hardMaxCells {
		return nil, ErrInvalidSnapshot
	}
	entries := append([]Count(nil), snapshot.Counts...)
	sortCounts(entries)
	var previous key
	for index, entry := range entries {
		if index&255 == 0 {
			if err := contextError(ctx); err != nil {
				return nil, err
			}
		}
		if entry.Count == 0 || !entry.Outcome.valid() {
			return nil, ErrInvalidSnapshot
		}
		if _, ok := a.extractors[entry.Extractor]; !ok {
			return nil, ErrUnknownExtractor
		}
		if _, ok := a.capabilities[entry.Capability]; !ok {
			return nil, ErrUnknownCapability
		}
		current := key{extractor: entry.Extractor, capability: entry.Capability, outcome: entry.Outcome}
		if index > 0 && current == previous {
			return nil, ErrInvalidSnapshot
		}
		previous = current
	}
	return entries, nil
}
