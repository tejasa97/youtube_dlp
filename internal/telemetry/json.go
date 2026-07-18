package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
)

const (
	DefaultMaxSnapshotBytes int64 = 8 << 20
	HardMaxSnapshotBytes    int64 = 32 << 20
)

// MarshalCanonical serializes a snapshot with stable field and count order.
// It validates the schema and structural fields, but allowlist validation is
// performed by Aggregator.Merge.
func MarshalCanonical(snapshot Snapshot) ([]byte, error) {
	if err := validateStructural(snapshot); err != nil {
		return nil, err
	}
	copySnapshot := snapshot
	copySnapshot.Counts = append(make([]Count, 0, len(snapshot.Counts)), snapshot.Counts...)
	sortCounts(copySnapshot.Counts)
	return json.Marshal(copySnapshot)
}

// WriteCanonical writes the canonical JSON snapshot while honoring context
// cancellation between bounded-size writes.
func WriteCanonical(ctx context.Context, writer io.Writer, snapshot Snapshot) error {
	if writer == nil {
		return ErrInvalidSnapshot
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	data, err := MarshalCanonical(snapshot)
	if err != nil {
		return err
	}
	for len(data) > 0 {
		if err := contextError(ctx); err != nil {
			return err
		}
		chunk := data
		if len(chunk) > 32<<10 {
			chunk = chunk[:32<<10]
		}
		written, err := writer.Write(chunk)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(chunk) {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

// Decode reads one strict JSON snapshot under a byte limit. Unknown fields,
// trailing JSON values, duplicate tuples, invalid names/outcomes, and zero
// counts are rejected with a non-secret-bearing sentinel.
func Decode(ctx context.Context, reader io.Reader, maxBytes int64) (Snapshot, error) {
	if reader == nil {
		return Snapshot{}, ErrInvalidSnapshot
	}
	if maxBytes == 0 {
		maxBytes = DefaultMaxSnapshotBytes
	}
	if maxBytes < 1 || maxBytes > HardMaxSnapshotBytes {
		return Snapshot{}, ErrDecodeLimit
	}
	limited := &io.LimitedReader{R: &contextReader{ctx: ctx, reader: reader}, N: maxBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		if contextError(ctx) != nil {
			return Snapshot{}, contextError(ctx)
		}
		return Snapshot{}, ErrInvalidSnapshot
	}
	if int64(len(data)) > maxBytes {
		return Snapshot{}, ErrDecodeLimit
	}
	if err := contextError(ctx); err != nil {
		return Snapshot{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire struct {
		SchemaVersion *int     `json:"schema_version"`
		Counts        *[]Count `json:"counts"`
		Overflow      *struct {
			CellLimit         *uint64 `json:"cell_limit"`
			CounterSaturation *uint64 `json:"counter_saturation"`
		} `json:"overflow"`
	}
	if err := decoder.Decode(&wire); err != nil {
		return Snapshot{}, ErrInvalidSnapshot
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Snapshot{}, ErrInvalidSnapshot
	}
	if wire.SchemaVersion == nil || wire.Counts == nil || wire.Overflow == nil || wire.Overflow.CellLimit == nil || wire.Overflow.CounterSaturation == nil {
		return Snapshot{}, ErrInvalidSnapshot
	}
	snapshot := Snapshot{
		SchemaVersion: *wire.SchemaVersion,
		Counts:        *wire.Counts,
		Overflow: Overflow{
			CellLimit:         *wire.Overflow.CellLimit,
			CounterSaturation: *wire.Overflow.CounterSaturation,
		},
	}
	if err := validateStructuralContext(ctx, snapshot); err != nil {
		return Snapshot{}, err
	}
	sortCounts(snapshot.Counts)
	return snapshot, nil
}

func validateStructural(snapshot Snapshot) error {
	return validateStructuralContext(nil, snapshot)
}

func validateStructuralContext(ctx context.Context, snapshot Snapshot) error {
	if snapshot.SchemaVersion != SchemaVersion || len(snapshot.Counts) > hardMaxCells {
		return ErrInvalidSnapshot
	}
	seen := make(map[key]struct{}, len(snapshot.Counts))
	for index, entry := range snapshot.Counts {
		if index&255 == 0 {
			if err := contextError(ctx); err != nil {
				return err
			}
		}
		if !validName.MatchString(entry.Extractor) || !validName.MatchString(entry.Capability) || !entry.Outcome.valid() || entry.Count == 0 {
			return ErrInvalidSnapshot
		}
		k := key{extractor: entry.Extractor, capability: entry.Capability, outcome: entry.Outcome}
		if _, duplicate := seen[k]; duplicate {
			return ErrInvalidSnapshot
		}
		seen[k] = struct{}{}
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := contextError(r.ctx); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
