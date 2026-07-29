package hds

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// Bootstrap bounds. These are deliberately tighter than the manifest budget
// because bootstraps are typically a few hundred bytes; a runaway byte budget
// hides real corruption.
const (
	maxBootstrapBytes = 1 << 20 // 1 MiB hard cap.
	maxBootstrapASRT  = 1024    // ASRT box count.
	maxBootstrapAFRT  = 1024    // AFRT box count.
	maxBootstrapRuns  = 4096    // Combined ASRT/AFRT run totals.
)

// SegmentRun is one entry of an ASRT (Segment Run Table) box.
type SegmentRun struct {
	FirstSegment        uint32
	FragmentsPerSegment uint32
}

// FragmentRun is one entry of an AFRT (Fragment Run Table) box.
type FragmentRun struct {
	First                  uint32
	Timestamp              uint64
	Duration               uint32
	DiscontinuityIndicator *byte // Only set when Duration == 0.
}

// Bootstrap is the parsed ABST payload. Only the first ASRT and the first
// AFRT are honored to mirror f4m.py (segments[0], fragments[0]); additional
// boxes are tolerated but not consulted, preventing arbitrary growth.
type Bootstrap struct {
	Live      bool
	Segments  []SegmentRun
	Fragments []FragmentRun
}

// ParseBootstrap decodes the ABST bootstrap payload referenced from an F4M
// <bootstrapInfo> element. The payload is wrapped in a single outer 'abst' box
// that itself wraps the ASRT and AFRT runs.
//
// Live bootstraps are rejected: this package implements only VOD. The
// detection mirrors f4m.py's `flags & 0x20` bit (Live).
func ParseBootstrap(payload []byte) (Bootstrap, error) {
	if len(payload) == 0 {
		return Bootstrap{}, fmt.Errorf("%w: empty payload", ErrInvalidBootstrap)
	}
	if len(payload) > maxBootstrapBytes {
		return Bootstrap{}, fmt.Errorf("%w: payload exceeds %d bytes", ErrInvalidBootstrap, maxBootstrapBytes)
	}
	outer, outerType, body, err := readBox(bytes.NewReader(payload))
	if err != nil {
		return Bootstrap{}, fmt.Errorf("%w: outer box", ErrInvalidBootstrap)
	}
	if !bytes.Equal(outerType, boxTypeBootstrap) {
		return Bootstrap{}, fmt.Errorf("%w: expected abst, got %q", ErrInvalidBootstrap, string(outerType))
	}
	if int64(len(payload)) != outer {
		return Bootstrap{}, fmt.Errorf("%w: outer box size %d != payload %d", ErrInvalidBootstrap, outer, len(payload))
	}
	return parseABST(body)
}

// parseABST reads the ABST body. The layout matches yt-dlp's read_abst:
//
//	version (1)
//	flags   (3)
//	BootstrapinfoVersion (4)
//	Profile/Live/Update/Reserved (1, Live = bit 0x20)
//	timescale (4)
//	CurrentMediaTime (8)
//	SmpteTimeCodeOffset (8)
//	MovieIdentifier (zero-terminated)
//	server_count (1) + server strings
//	quality_count (1) + quality strings
//	DrmData (zero-terminated)
//	MetaData (zero-terminated)
//	segments_count (1) + ASRT boxes
//	fragments_count (1) + AFRT boxes
func parseABST(body []byte) (Bootstrap, error) {
	reader := bytes.NewReader(body)
	if _, err := discard(reader, 4); err != nil { // version + flags
		return Bootstrap{}, fmt.Errorf("%w: version+flags", ErrInvalidBootstrap)
	}
	if _, err := discard(reader, 4); err != nil { // BootstrapinfoVersion
		return Bootstrap{}, fmt.Errorf("%w: bootstrapinfo version", ErrInvalidBootstrap)
	}
	flagsByte, err := readByte(reader)
	if err != nil {
		return Bootstrap{}, fmt.Errorf("%w: profile flags", ErrInvalidBootstrap)
	}
	if flagsByte&0x20 != 0 {
		return Bootstrap{}, fmt.Errorf("%w: live ABST rejected", ErrUnsupportedLive)
	}
	if _, err := discard(reader, 4); err != nil { // timescale
		return Bootstrap{}, fmt.Errorf("%w: timescale", ErrInvalidBootstrap)
	}
	if _, err := discard(reader, 8); err != nil { // CurrentMediaTime
		return Bootstrap{}, fmt.Errorf("%w: current media time", ErrInvalidBootstrap)
	}
	if _, err := discard(reader, 8); err != nil { // SmpteTimeCodeOffset
		return Bootstrap{}, fmt.Errorf("%w: smpte offset", ErrInvalidBootstrap)
	}
	if _, err := readCString(reader, maxBootstrapBytes); err != nil { // MovieIdentifier
		return Bootstrap{}, fmt.Errorf("%w: movie id", ErrInvalidBootstrap)
	}
	serverCount, err := readByte(reader)
	if err != nil {
		return Bootstrap{}, fmt.Errorf("%w: server count", ErrInvalidBootstrap)
	}
	if int(serverCount) > maxBootstrapRuns {
		return Bootstrap{}, fmt.Errorf("%w: server count %d > %d", ErrInvalidBootstrap, serverCount, maxBootstrapRuns)
	}
	if err := readCStringTable(reader, int(serverCount), maxBootstrapBytes); err != nil {
		return Bootstrap{}, fmt.Errorf("%w: server entries", ErrInvalidBootstrap)
	}
	qualityCount, err := readByte(reader)
	if err != nil {
		return Bootstrap{}, fmt.Errorf("%w: quality count", ErrInvalidBootstrap)
	}
	if int(qualityCount) > maxBootstrapRuns {
		return Bootstrap{}, fmt.Errorf("%w: quality count %d > %d", ErrInvalidBootstrap, qualityCount, maxBootstrapRuns)
	}
	if err := readCStringTable(reader, int(qualityCount), maxBootstrapBytes); err != nil {
		return Bootstrap{}, fmt.Errorf("%w: quality entries", ErrInvalidBootstrap)
	}
	if _, err := readCString(reader, maxBootstrapBytes); err != nil { // DrmData
		return Bootstrap{}, fmt.Errorf("%w: drm data", ErrInvalidBootstrap)
	}
	if _, err := readCString(reader, maxBootstrapBytes); err != nil { // MetaData
		return Bootstrap{}, fmt.Errorf("%w: metadata", ErrInvalidBootstrap)
	}
	segmentsCount, err := readByte(reader)
	if err != nil {
		return Bootstrap{}, fmt.Errorf("%w: segments count", ErrInvalidBootstrap)
	}
	if int(segmentsCount) > maxBootstrapASRT {
		return Bootstrap{}, fmt.Errorf("%w: %d ASRT runs", ErrInvalidBootstrap, segmentsCount)
	}
	var segments []SegmentRun
	for i := uint8(0); i < segmentsCount; i++ {
		boxSize, boxType, segBody, err := readBox(reader)
		if err != nil {
			return Bootstrap{}, fmt.Errorf("%w: asrt[%d]", ErrInvalidBootstrap, i)
		}
		if !bytes.Equal(boxType, []byte("asrt")) {
			return Bootstrap{}, fmt.Errorf("%w: segments box %q is not asrt", ErrInvalidBootstrap, string(boxType))
		}
		asrt, err := parseASRTBody(segBody)
		if err != nil {
			return Bootstrap{}, fmt.Errorf("%w: asrt[%d]: %v", ErrInvalidBootstrap, i, err)
		}
		_ = boxSize
		if i == 0 {
			segments = asrt
		}
	}
	fragmentsCount, err := readByte(reader)
	if err != nil {
		return Bootstrap{}, fmt.Errorf("%w: fragments count", ErrInvalidBootstrap)
	}
	if int(fragmentsCount) > maxBootstrapAFRT {
		return Bootstrap{}, fmt.Errorf("%w: %d AFRT runs", ErrInvalidBootstrap, fragmentsCount)
	}
	var fragments []FragmentRun
	for i := uint8(0); i < fragmentsCount; i++ {
		boxSize, boxType, fragBody, err := readBox(reader)
		if err != nil {
			return Bootstrap{}, fmt.Errorf("%w: afrt[%d]", ErrInvalidBootstrap, i)
		}
		if !bytes.Equal(boxType, boxTypeFragment) {
			return Bootstrap{}, fmt.Errorf("%w: fragments box %q is not afrt", ErrInvalidBootstrap, string(boxType))
		}
		afrt, err := parseAFRTBody(fragBody)
		if err != nil {
			return Bootstrap{}, fmt.Errorf("%w: afrt[%d]: %v", ErrInvalidBootstrap, i, err)
		}
		_ = boxSize
		if i == 0 {
			fragments = afrt
		}
	}
	// Trailing bytes are explicitly rejected: they suggest an unrecognized
	// appendix or a truncated parser; yt-dlp tolerates them, but we cannot
	// promise safety without enumerating every field.
	if reader.Len() != 0 {
		return Bootstrap{}, fmt.Errorf("%w: %d trailing bytes after ABST", ErrInvalidBootstrap, reader.Len())
	}
	return Bootstrap{Live: false, Segments: segments, Fragments: fragments}, nil
}

// parseASRTBody reads the ASRT payload. Layout:
//
//	version (1) + flags (3)
//	quality_entry_count (1)
//	per entry: quality string (zero-terminated)
//	segment_run_count (4)
//	per run: first_segment (4) + fragments_per_segment (4)
func parseASRTBody(body []byte) ([]SegmentRun, error) {
	reader := bytes.NewReader(body)
	if _, err := discard(reader, 4); err != nil {
		return nil, fmt.Errorf("version+flags: %w", err)
	}
	qualityCount, err := readByte(reader)
	if err != nil {
		return nil, fmt.Errorf("quality count: %w", err)
	}
	if int(qualityCount) > maxBootstrapRuns {
		return nil, fmt.Errorf("quality count %d > %d", qualityCount, maxBootstrapRuns)
	}
	if err := readCStringTable(reader, int(qualityCount), maxBootstrapBytes); err != nil {
		return nil, fmt.Errorf("quality entries: %w", err)
	}
	var count uint32
	if err := binary.Read(reader, binary.BigEndian, &count); err != nil {
		return nil, fmt.Errorf("segment run count: %w", err)
	}
	if int(count) > maxBootstrapRuns {
		return nil, fmt.Errorf("segment run count %d > %d", count, maxBootstrapRuns)
	}
	runs := make([]SegmentRun, 0, count)
	for i := uint32(0); i < count; i++ {
		var run SegmentRun
		if err := binary.Read(reader, binary.BigEndian, &run.FirstSegment); err != nil {
			return nil, fmt.Errorf("first segment: %w", err)
		}
		if err := binary.Read(reader, binary.BigEndian, &run.FragmentsPerSegment); err != nil {
			return nil, fmt.Errorf("fragments per segment: %w", err)
		}
		if uint64(run.FirstSegment)+uint64(run.FragmentsPerSegment) > math.MaxUint32 {
			return nil, fmt.Errorf("segment run overflow")
		}
		runs = append(runs, run)
	}
	if reader.Len() != 0 {
		return nil, fmt.Errorf("trailing bytes after ASRT runs: %d", reader.Len())
	}
	return runs, nil
}

// parseAFRTBody reads the AFRT payload. Layout:
//
//	version (1) + flags (3)
//	timescale (4)
//	quality_entry_count (1)
//	per entry: quality string (zero-terminated)
//	fragment_count (4)
//	per fragment: first (4), first_ts (8), duration (4)
//	  if duration == 0: discontinuity_indicator (1)
func parseAFRTBody(body []byte) ([]FragmentRun, error) {
	reader := bytes.NewReader(body)
	if _, err := discard(reader, 4); err != nil {
		return nil, fmt.Errorf("version+flags: %w", err)
	}
	if _, err := discard(reader, 4); err != nil {
		return nil, fmt.Errorf("timescale: %w", err)
	}
	qualityCount, err := readByte(reader)
	if err != nil {
		return nil, fmt.Errorf("quality count: %w", err)
	}
	if int(qualityCount) > maxBootstrapRuns {
		return nil, fmt.Errorf("quality count %d > %d", qualityCount, maxBootstrapRuns)
	}
	if err := readCStringTable(reader, int(qualityCount), maxBootstrapBytes); err != nil {
		return nil, fmt.Errorf("quality entries: %w", err)
	}
	var count uint32
	if err := binary.Read(reader, binary.BigEndian, &count); err != nil {
		return nil, fmt.Errorf("fragment count: %w", err)
	}
	if int(count) > maxBootstrapRuns {
		return nil, fmt.Errorf("fragment count %d > %d", count, maxBootstrapRuns)
	}
	runs := make([]FragmentRun, 0, count)
	for i := uint32(0); i < count; i++ {
		var run FragmentRun
		if err := binary.Read(reader, binary.BigEndian, &run.First); err != nil {
			return nil, fmt.Errorf("first: %w", err)
		}
		if err := binary.Read(reader, binary.BigEndian, &run.Timestamp); err != nil {
			return nil, fmt.Errorf("timestamp: %w", err)
		}
		if err := binary.Read(reader, binary.BigEndian, &run.Duration); err != nil {
			return nil, fmt.Errorf("duration: %w", err)
		}
		if run.Duration == 0 {
			indicator, err := readByte(reader)
			if err != nil {
				return nil, fmt.Errorf("discontinuity indicator: %w", err)
			}
			run.DiscontinuityIndicator = &indicator
		}
		runs = append(runs, run)
	}
	if reader.Len() != 0 {
		return nil, fmt.Errorf("trailing bytes after AFRT runs: %d", reader.Len())
	}
	return runs, nil
}

// readByte reads one byte or returns a wrapped error.
func readByte(r io.Reader) (byte, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}
	return b[0], nil
}

// readCString reads a NUL-terminated ASCII string of bounded length. The max
// parameter bounds the entire string including the terminator so a malformed
// payload can't allocate unbounded memory.
func readCString(r io.Reader, max int) (string, error) {
	if seeker, ok := r.(io.Seeker); ok {
		start, _ := seeker.Seek(0, io.SeekCurrent)
		buf := make([]byte, 0, 64)
		chunk := make([]byte, 256)
		for bufLen := 0; bufLen <= max; {
			n, err := r.Read(chunk)
			if err != nil {
				if errors.Is(err, io.EOF) {
					return "", io.ErrUnexpectedEOF
				}
				return "", err
			}
			for i := 0; i < n; i++ {
				if chunk[i] == 0 {
					consumed := int64(i+1) - int64(n)
					if consumed != 0 {
						if _, seekErr := seeker.Seek(consumed, io.SeekCurrent); seekErr != nil {
							return "", fmt.Errorf("cstring rewind: %w", seekErr)
						}
					}
					_ = start
					return string(buf), nil
				}
				buf = append(buf, chunk[i])
				if len(buf) > max {
					return "", fmt.Errorf("cstring exceeds %d bytes", max)
				}
			}
		}
	}
	return "", fmt.Errorf("cstring reader is not seekable: %T", r)
}

func readCStringTable(r io.Reader, count, totalMax int) error {
	for i := 0; i < count; i++ {
		if _, err := readCString(r, totalMax); err != nil {
			return err
		}
	}
	return nil
}

func discard(r io.Reader, n int) (int, error) {
	if seeker, ok := r.(io.Seeker); ok {
		if _, err := seeker.Seek(int64(n), io.SeekCurrent); err != nil {
			return 0, err
		}
		return n, nil
	}
	_, err := io.CopyN(io.Discard, r, int64(n))
	if err != nil {
		return 0, err
	}
	return n, nil
}
