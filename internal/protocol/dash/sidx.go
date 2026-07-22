package dash

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// SIDX parsing errors.
var (
	ErrInvalidSIDX        = errors.New("invalid SIDX box")
	ErrSIDXTruncated      = errors.New("truncated SIDX data")
	ErrSIDXVersion        = errors.New("unsupported SIDX version")
	ErrSIDXZeroTimescale  = errors.New("SIDX timescale is zero")
	ErrSIDXZeroReference  = errors.New("SIDX reference has zero size")
	ErrSIDXHierarchical   = errors.New("SIDX hierarchical references unsupported")
	ErrSIDXOverflow       = errors.New("SIDX offset or size overflow")
	ErrSIDXTooManyEntries = errors.New("SIDX reference count exceeds limit")
	ErrSIDXBadBoxSize     = errors.New("SIDX box size impossible")
)

// maxSIDXReferences bounds the number of references accepted from a single
// SIDX box. This prevents unbounded allocation from crafted inputs.
const maxSIDXReferences = maxSegmentsPerRepresentation

// SIDXReference is one subsegment reference entry from a SIDX box.
type SIDXReference struct {
	// ReferencedSize is the byte length of the referenced subsegment.
	ReferencedSize uint32
	// SubsegmentDuration is in timescale units.
	SubsegmentDuration uint32
	// StartsWithSAP indicates the subsegment starts with a stream access point.
	StartsWithSAP bool
	// SAPType is the SAP type (0-6), valid only when StartsWithSAP is true.
	SAPType uint8
	// SAPDeltaTime is the SAP delta time in timescale units.
	SAPDeltaTime uint32
	// IsIndex indicates this reference points to a nested SIDX index
	// (reference_type=1) rather than a leaf media subsegment (reference_type=0).
	IsIndex bool
}

// SIDX is a parsed Segment Index box (ISO-BMFF 'sidx').
type SIDX struct {
	// ReferenceID is the track_ID of the referenced stream.
	ReferenceID uint32
	// Timescale is the time unit for durations and presentation times.
	Timescale uint32
	// EarliestPresentationTime is the earliest presentation time of the first
	// referenced subsegment.
	EarliestPresentationTime uint64
	// FirstOffset is the byte distance from the end of the SIDX box to the
	// first referenced byte.
	FirstOffset uint64
	// References holds the ordered subsegment references.
	References []SIDXReference
	// BoxSize is the total size of the enclosing SIDX box in bytes, including
	// header. Needed to compute absolute media byte offsets.
	BoxSize uint64
}

// MediaRange is an absolute byte range within a media resource.
type MediaRange struct {
	Start  int64
	Length int64
}

// HasHierarchicalReferences reports whether any reference in the SIDX box is
// an index reference (reference_type=1) requiring recursive expansion.
func (sidx *SIDX) HasHierarchicalReferences() bool {
	for _, reference := range sidx.References {
		if reference.IsIndex {
			return true
		}
	}
	return false
}

// MediaRanges computes the absolute byte ranges for each referenced subsegment.
// The baseOffset is the absolute position in the media resource where the SIDX
// box starts. Each returned range is an absolute offset into the media resource.
//
// Per ISO-BMFF semantics: the first referenced byte is at
//
//	baseOffset + sidx.BoxSize + sidx.FirstOffset
//
// and subsequent references follow contiguously.
func (sidx *SIDX) MediaRanges(baseOffset int64) ([]MediaRange, error) {
	if len(sidx.References) == 0 {
		return nil, nil
	}
	start, err := addInt64(baseOffset, int64(sidx.BoxSize))
	if err != nil {
		return nil, fmt.Errorf("%w: base + box size", ErrSIDXOverflow)
	}
	start, err = addUint64ToInt64(start, sidx.FirstOffset)
	if err != nil {
		return nil, fmt.Errorf("%w: base + box size + first_offset", ErrSIDXOverflow)
	}
	ranges := make([]MediaRange, 0, len(sidx.References))
	for index, reference := range sidx.References {
		if reference.ReferencedSize == 0 {
			return nil, fmt.Errorf("%w: reference %d", ErrSIDXZeroReference, index)
		}
		length := int64(reference.ReferencedSize)
		if start < 0 || start > math.MaxInt64-length {
			return nil, fmt.Errorf("%w: reference %d range", ErrSIDXOverflow, index)
		}
		ranges = append(ranges, MediaRange{Start: start, Length: length})
		start += length
	}
	return ranges, nil
}

// ParseSIDX parses a SIDX box from raw bytes. The input may contain the SIDX
// box at any offset; the parser scans for the 'sidx' box type. Leading
// non-SIDX boxes are skipped. The returned SIDX includes the box size so
// callers can compute absolute media offsets.
func ParseSIDX(data []byte) (*SIDX, int, error) {
	offset := 0
	for offset < len(data) {
		remaining := data[offset:]
		boxSize, headerSize, boxType, err := parseBoxHeader(remaining)
		if err != nil {
			return nil, 0, err
		}
		if boxType == "sidx" {
			if boxSize > uint64(len(remaining)) {
				return nil, 0, fmt.Errorf("%w: SIDX box extends beyond data", ErrSIDXTruncated)
			}
			bodyEnd := int(boxSize) - headerSize
			if bodyEnd < 0 {
				return nil, 0, fmt.Errorf("%w: SIDX box extends beyond data", ErrSIDXTruncated)
			}
			sidx, err := parseSIDXBody(remaining[headerSize:headerSize+bodyEnd], boxSize)
			if err != nil {
				return nil, 0, err
			}
			return sidx, offset, nil
		}
		// Skip non-SIDX boxes.
		if boxSize == 0 {
			break
		}
		if boxSize > uint64(len(remaining)) {
			break
		}
		offset += int(boxSize)
	}
	return nil, 0, fmt.Errorf("%w: no sidx box found", ErrInvalidSIDX)
}

// parseBoxHeader reads an ISO-BMFF box header and returns the total box size,
// header size (8 or 16 bytes), and the 4-character box type.
func parseBoxHeader(data []byte) (boxSize uint64, headerSize int, boxType string, err error) {
	if len(data) < 8 {
		return 0, 0, "", fmt.Errorf("%w: need 8 bytes for box header, got %d", ErrSIDXTruncated, len(data))
	}
	size32 := binary.BigEndian.Uint32(data[0:4])
	boxType = string(data[4:8])
	headerSize = 8
	switch {
	case size32 == 1:
		// 64-bit extended size.
		if len(data) < 16 {
			return 0, 0, "", fmt.Errorf("%w: need 16 bytes for extended box header, got %d", ErrSIDXTruncated, len(data))
		}
		boxSize = binary.BigEndian.Uint64(data[8:16])
		headerSize = 16
		if boxSize < 16 {
			return 0, 0, "", fmt.Errorf("%w: extended size %d < 16", ErrSIDXBadBoxSize, boxSize)
		}
	case size32 == 0:
		// Box extends to end of data.
		boxSize = uint64(len(data))
	default:
		boxSize = uint64(size32)
		if boxSize < 8 {
			return 0, 0, "", fmt.Errorf("%w: size %d < 8", ErrSIDXBadBoxSize, boxSize)
		}
	}
	return boxSize, headerSize, boxType, nil
}

// parseSIDXBody parses the SIDX box payload (after the box header).
func parseSIDXBody(payload []byte, boxSize uint64) (*SIDX, error) {
	if len(payload) < 4 {
		return nil, fmt.Errorf("%w: SIDX body needs at least 4 bytes", ErrSIDXTruncated)
	}
	version := payload[0]
	if version > 1 {
		return nil, fmt.Errorf("%w: version %d", ErrSIDXVersion, version)
	}
	// flags are payload[1:4], reserved.
	offset := 4
	if len(payload) < offset+8 {
		return nil, fmt.Errorf("%w: SIDX needs reference_ID and timescale", ErrSIDXTruncated)
	}
	referenceID := binary.BigEndian.Uint32(payload[offset : offset+4])
	timescale := binary.BigEndian.Uint32(payload[offset+4 : offset+8])
	offset += 8
	if timescale == 0 {
		return nil, ErrSIDXZeroTimescale
	}

	var earliestPresentationTime uint64
	var firstOffset uint64
	if version == 0 {
		if len(payload) < offset+8 {
			return nil, fmt.Errorf("%w: SIDX v0 needs EPT and first_offset", ErrSIDXTruncated)
		}
		earliestPresentationTime = uint64(binary.BigEndian.Uint32(payload[offset : offset+4]))
		firstOffset = uint64(binary.BigEndian.Uint32(payload[offset+4 : offset+8]))
		offset += 8
	} else {
		if len(payload) < offset+16 {
			return nil, fmt.Errorf("%w: SIDX v1 needs 64-bit EPT and first_offset", ErrSIDXTruncated)
		}
		earliestPresentationTime = binary.BigEndian.Uint64(payload[offset : offset+8])
		firstOffset = binary.BigEndian.Uint64(payload[offset+8 : offset+16])
		offset += 16
	}

	// reserved (2 bytes) + reference_count (2 bytes)
	if len(payload) < offset+4 {
		return nil, fmt.Errorf("%w: SIDX needs reserved and reference_count", ErrSIDXTruncated)
	}
	referenceCount := int(binary.BigEndian.Uint16(payload[offset+2 : offset+4]))
	offset += 4

	if referenceCount == 0 {
		return &SIDX{
			ReferenceID:              referenceID,
			Timescale:                timescale,
			EarliestPresentationTime: earliestPresentationTime,
			FirstOffset:              firstOffset,
			BoxSize:                  boxSize,
		}, nil
	}
	if referenceCount > maxSIDXReferences {
		return nil, fmt.Errorf("%w: %d > %d", ErrSIDXTooManyEntries, referenceCount, maxSIDXReferences)
	}

	// Each reference entry is 12 bytes.
	entryBytes := referenceCount * 12
	if len(payload) < offset+entryBytes {
		return nil, fmt.Errorf("%w: need %d bytes for %d references, got %d", ErrSIDXTruncated, entryBytes, referenceCount, len(payload)-offset)
	}

	references := make([]SIDXReference, 0, referenceCount)
	for index := 0; index < referenceCount; index++ {
		entry := payload[offset : offset+12]
		offset += 12

		rawSize := binary.BigEndian.Uint32(entry[0:4])
		referenceType := rawSize >> 31
		referencedSize := rawSize & 0x7FFFFFFF
		subsegmentDuration := binary.BigEndian.Uint32(entry[4:8])
		rawSAP := binary.BigEndian.Uint32(entry[8:12])
		startsWithSAP := rawSAP>>31 == 1
		sapType := uint8((rawSAP >> 28) & 0x07)
		sapDeltaTime := rawSAP & 0x0FFFFFFF

		references = append(references, SIDXReference{
			ReferencedSize:     referencedSize,
			SubsegmentDuration: subsegmentDuration,
			StartsWithSAP:      startsWithSAP,
			SAPType:            sapType,
			SAPDeltaTime:       sapDeltaTime,
			IsIndex:            referenceType == 1,
		})
	}

	return &SIDX{
		ReferenceID:              referenceID,
		Timescale:                timescale,
		EarliestPresentationTime: earliestPresentationTime,
		FirstOffset:              firstOffset,
		References:               references,
		BoxSize:                  boxSize,
	}, nil
}

// addInt64 adds two int64 values with overflow detection.
func addInt64(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, ErrSIDXOverflow
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, ErrSIDXOverflow
	}
	return a + b, nil
}

// addUint64ToInt64 adds a uint64 to an int64 with overflow detection.
func addUint64ToInt64(a int64, b uint64) (int64, error) {
	if b > uint64(math.MaxInt64) || a > math.MaxInt64-int64(b) {
		return 0, ErrSIDXOverflow
	}
	return a + int64(b), nil
}
