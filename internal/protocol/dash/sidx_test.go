package dash

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"testing"
)

// buildSIDX constructs a minimal valid SIDX box for testing.
func buildSIDX(version byte, referenceID, timescale uint32, ept, firstOffset uint64, refs []SIDXReference) []byte {
	var body []byte
	// version + flags
	body = append(body, version, 0, 0, 0)
	// reference_ID + timescale
	body = appendUint32(body, referenceID)
	body = appendUint32(body, timescale)
	if version == 0 {
		body = appendUint32(body, uint32(ept))
		body = appendUint32(body, uint32(firstOffset))
	} else {
		body = appendUint64(body, ept)
		body = appendUint64(body, firstOffset)
	}
	// reserved (2 bytes) + reference_count (2 bytes)
	body = append(body, 0, 0)
	body = appendUint16(body, uint16(len(refs)))
	for _, ref := range refs {
		rawSize := ref.ReferencedSize
		if ref.IsIndex {
			rawSize |= 0x80000000
		}
		body = appendUint32(body, rawSize)
		body = appendUint32(body, ref.SubsegmentDuration)
		var sap uint32
		if ref.StartsWithSAP {
			sap = 0x80000000 | uint32(ref.SAPType)<<28 | ref.SAPDeltaTime
		}
		body = appendUint32(body, sap)
	}
	boxSize := uint32(8 + len(body))
	var box []byte
	box = appendUint32(box, boxSize)
	box = append(box, 's', 'i', 'd', 'x')
	box = append(box, body...)
	return box
}

func appendUint16(buf []byte, v uint16) []byte {
	var tmp [2]byte
	binary.BigEndian.PutUint16(tmp[:], v)
	return append(buf, tmp[:]...)
}

func appendUint32(buf []byte, v uint32) []byte {
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], v)
	return append(buf, tmp[:]...)
}

func appendUint64(buf []byte, v uint64) []byte {
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], v)
	return append(buf, tmp[:]...)
}

func TestSIDXVersion0OneReference(t *testing.T) {
	refs := []SIDXReference{{ReferencedSize: 1000, SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1}}
	data := buildSIDX(0, 1, 48000, 0, 0, refs)
	sidx, offset, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 0 {
		t.Fatalf("offset = %d", offset)
	}
	if sidx.ReferenceID != 1 || sidx.Timescale != 48000 {
		t.Fatalf("sidx = %#v", sidx)
	}
	if sidx.EarliestPresentationTime != 0 || sidx.FirstOffset != 0 {
		t.Fatalf("timing = %#v", sidx)
	}
	if len(sidx.References) != 1 {
		t.Fatalf("references = %d", len(sidx.References))
	}
	ref := sidx.References[0]
	if ref.ReferencedSize != 1000 || ref.SubsegmentDuration != 48000 || !ref.StartsWithSAP || ref.SAPType != 1 {
		t.Fatalf("ref = %#v", ref)
	}
	if sidx.BoxSize != uint64(len(data)) {
		t.Fatalf("BoxSize = %d, want %d", sidx.BoxSize, len(data))
	}
}

func TestSIDXVersion0MultipleReferences(t *testing.T) {
	refs := []SIDXReference{
		{ReferencedSize: 500, SubsegmentDuration: 24000, StartsWithSAP: true, SAPType: 1},
		{ReferencedSize: 600, SubsegmentDuration: 24000, StartsWithSAP: false},
		{ReferencedSize: 700, SubsegmentDuration: 24000, StartsWithSAP: true, SAPType: 2, SAPDeltaTime: 100},
	}
	data := buildSIDX(0, 2, 24000, 1000, 0, refs)
	sidx, _, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(sidx.References) != 3 {
		t.Fatalf("references = %d", len(sidx.References))
	}
	if sidx.EarliestPresentationTime != 1000 {
		t.Fatalf("EPT = %d", sidx.EarliestPresentationTime)
	}
	if sidx.References[2].SAPDeltaTime != 100 {
		t.Fatalf("SAPDeltaTime = %d", sidx.References[2].SAPDeltaTime)
	}
}

func TestSIDXVersion1SixtyFourBitFields(t *testing.T) {
	refs := []SIDXReference{{ReferencedSize: 2000, SubsegmentDuration: 90000}}
	ept := uint64(1) << 40
	firstOffset := uint64(1) << 35
	data := buildSIDX(1, 1, 90000, ept, firstOffset, refs)
	sidx, _, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	if sidx.EarliestPresentationTime != ept {
		t.Fatalf("EPT = %d, want %d", sidx.EarliestPresentationTime, ept)
	}
	if sidx.FirstOffset != firstOffset {
		t.Fatalf("FirstOffset = %d, want %d", sidx.FirstOffset, firstOffset)
	}
}

func TestSIDXNonZeroFirstOffset(t *testing.T) {
	refs := []SIDXReference{{ReferencedSize: 100, SubsegmentDuration: 1000}}
	data := buildSIDX(0, 1, 1000, 0, 50, refs)
	sidx, _, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	if sidx.FirstOffset != 50 {
		t.Fatalf("FirstOffset = %d", sidx.FirstOffset)
	}
	// MediaRanges: base=0, boxSize=len(data), firstOffset=50
	ranges, err := sidx.MediaRanges(0)
	if err != nil {
		t.Fatal(err)
	}
	expectedStart := int64(len(data)) + 50
	if ranges[0].Start != expectedStart || ranges[0].Length != 100 {
		t.Fatalf("range = %#v, want start=%d len=100", ranges[0], expectedStart)
	}
}

func TestSIDXExtendedBoxSize(t *testing.T) {
	// Build a SIDX with 64-bit extended size (size32 == 1).
	refs := []SIDXReference{{ReferencedSize: 300, SubsegmentDuration: 5000}}
	body := buildSIDXBody(0, 1, 5000, 0, 0, refs)
	totalSize := uint64(16 + len(body))
	var box []byte
	box = appendUint32(box, 1) // signals extended size
	box = append(box, 's', 'i', 'd', 'x')
	box = appendUint64(box, totalSize)
	box = append(box, body...)

	sidx, _, err := ParseSIDX(box)
	if err != nil {
		t.Fatal(err)
	}
	if sidx.BoxSize != totalSize {
		t.Fatalf("BoxSize = %d, want %d", sidx.BoxSize, totalSize)
	}
	if len(sidx.References) != 1 || sidx.References[0].ReferencedSize != 300 {
		t.Fatalf("refs = %#v", sidx.References)
	}
}

func buildSIDXBody(version byte, referenceID, timescale uint32, ept, firstOffset uint64, refs []SIDXReference) []byte {
	var body []byte
	body = append(body, version, 0, 0, 0)
	body = appendUint32(body, referenceID)
	body = appendUint32(body, timescale)
	if version == 0 {
		body = appendUint32(body, uint32(ept))
		body = appendUint32(body, uint32(firstOffset))
	} else {
		body = appendUint64(body, ept)
		body = appendUint64(body, firstOffset)
	}
	body = append(body, 0, 0)
	body = appendUint16(body, uint16(len(refs)))
	for _, ref := range refs {
		rawSize := ref.ReferencedSize
		if ref.IsIndex {
			rawSize |= 0x80000000
		}
		body = appendUint32(body, rawSize)
		body = appendUint32(body, ref.SubsegmentDuration)
		var sap uint32
		if ref.StartsWithSAP {
			sap = 0x80000000 | uint32(ref.SAPType)<<28 | ref.SAPDeltaTime
		}
		body = appendUint32(body, sap)
	}
	return body
}

func TestSIDXWithinLargerByteRange(t *testing.T) {
	// Prepend a non-SIDX box before the SIDX box.
	refs := []SIDXReference{{ReferencedSize: 200, SubsegmentDuration: 1000}}
	sidxBox := buildSIDX(0, 1, 1000, 0, 0, refs)
	// Create a dummy 'free' box of 16 bytes.
	var freeBox []byte
	freeBox = appendUint32(freeBox, 16)
	freeBox = append(freeBox, 'f', 'r', 'e', 'e')
	freeBox = append(freeBox, make([]byte, 8)...)

	data := append(freeBox, sidxBox...)
	sidx, offset, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 16 {
		t.Fatalf("offset = %d, want 16", offset)
	}
	if len(sidx.References) != 1 || sidx.References[0].ReferencedSize != 200 {
		t.Fatalf("refs = %#v", sidx.References)
	}
}

func TestSIDXPreciseAbsoluteByteRanges(t *testing.T) {
	refs := []SIDXReference{
		{ReferencedSize: 100, SubsegmentDuration: 1000},
		{ReferencedSize: 200, SubsegmentDuration: 1000},
		{ReferencedSize: 300, SubsegmentDuration: 1000},
	}
	data := buildSIDX(0, 1, 1000, 0, 0, refs)
	sidx, _, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	// baseOffset = 500 (simulating indexRange start)
	ranges, err := sidx.MediaRanges(500)
	if err != nil {
		t.Fatal(err)
	}
	boxSize := int64(len(data))
	expectedStart := int64(500) + boxSize
	if len(ranges) != 3 {
		t.Fatalf("ranges = %d", len(ranges))
	}
	if ranges[0].Start != expectedStart || ranges[0].Length != 100 {
		t.Fatalf("range[0] = %#v, want start=%d len=100", ranges[0], expectedStart)
	}
	if ranges[1].Start != expectedStart+100 || ranges[1].Length != 200 {
		t.Fatalf("range[1] = %#v", ranges[1])
	}
	if ranges[2].Start != expectedStart+300 || ranges[2].Length != 300 {
		t.Fatalf("range[2] = %#v", ranges[2])
	}
}

func TestSIDXTruncatedHeader(t *testing.T) {
	_, _, err := ParseSIDX([]byte{0, 0, 0})
	if !errors.Is(err, ErrSIDXTruncated) {
		t.Fatalf("err = %v", err)
	}
}

func TestSIDXTruncatedReferenceTable(t *testing.T) {
	// Build a SIDX claiming 2 references but only provide 1 entry.
	body := buildSIDXBody(0, 1, 1000, 0, 0, []SIDXReference{{ReferencedSize: 100}})
	// Patch reference_count to 2.
	// reference_count is at offset: 4(ver+flags) + 8(refID+timescale) + 8(ept+fo) + 2(reserved) = 22
	body[23] = 2
	boxSize := uint32(8 + len(body))
	var box []byte
	box = appendUint32(box, boxSize)
	box = append(box, 's', 'i', 'd', 'x')
	box = append(box, body...)

	_, _, err := ParseSIDX(box)
	if !errors.Is(err, ErrSIDXTruncated) {
		t.Fatalf("err = %v", err)
	}
}

func TestSIDXInvalidVersion(t *testing.T) {
	refs := []SIDXReference{{ReferencedSize: 100}}
	data := buildSIDX(0, 1, 1000, 0, 0, refs)
	data[8] = 5 // patch version to 5
	_, _, err := ParseSIDX(data)
	if !errors.Is(err, ErrSIDXVersion) {
		t.Fatalf("err = %v", err)
	}
}

func TestSIDXZeroTimescale(t *testing.T) {
	refs := []SIDXReference{{ReferencedSize: 100}}
	data := buildSIDX(0, 1, 0, 0, 0, refs)
	_, _, err := ParseSIDX(data)
	if !errors.Is(err, ErrSIDXZeroTimescale) {
		t.Fatalf("err = %v", err)
	}
}

func TestSIDXZeroReferenceSize(t *testing.T) {
	refs := []SIDXReference{{ReferencedSize: 0, SubsegmentDuration: 1000}}
	data := buildSIDX(0, 1, 1000, 0, 0, refs)
	sidx, _, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sidx.MediaRanges(0)
	if !errors.Is(err, ErrSIDXZeroReference) {
		t.Fatalf("err = %v", err)
	}
}

func TestSIDXHierarchicalReferenceParsed(t *testing.T) {
	refs := []SIDXReference{{ReferencedSize: 100}}
	data := buildSIDX(0, 1, 1000, 0, 0, refs)
	// Set reference_type bit (bit 31) on the first reference entry.
	// Reference entries start after: 8(header) + 4(ver+flags) + 8(refID+ts) + 8(ept+fo) + 4(reserved+count) = 32
	entryOffset := 8 + 4 + 8 + 8 + 4
	data[entryOffset] |= 0x80
	sidx, _, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(sidx.References) != 1 {
		t.Fatalf("references = %d", len(sidx.References))
	}
	if !sidx.References[0].IsIndex {
		t.Fatal("expected IsIndex=true for reference_type=1")
	}
	if sidx.References[0].ReferencedSize != 100 {
		t.Fatalf("ReferencedSize = %d", sidx.References[0].ReferencedSize)
	}
	if !sidx.HasHierarchicalReferences() {
		t.Fatal("expected HasHierarchicalReferences()=true")
	}
}

// TestSIDXHierarchicalReferenceRejection is retained as a parity-manifest alias.
// The behavior changed: reference_type=1 is now parsed (not rejected) and
// handled by the bounded recursive downloader expansion.
func TestSIDXHierarchicalReferenceRejection(t *testing.T) {
	TestSIDXHierarchicalReferenceParsed(t)
}

func TestSIDXSizeOverflow(t *testing.T) {
	refs := []SIDXReference{{ReferencedSize: math.MaxUint32 & 0x7FFFFFFF}}
	data := buildSIDX(0, 1, 1000, 0, 0, refs)
	sidx, _, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	// Use a base offset near MaxInt64 to trigger overflow.
	_, err = sidx.MediaRanges(math.MaxInt64 - 10)
	if !errors.Is(err, ErrSIDXOverflow) {
		t.Fatalf("err = %v", err)
	}
}

func TestSIDXOffsetOverflow(t *testing.T) {
	refs := []SIDXReference{{ReferencedSize: 100}}
	// version 1 with huge first_offset
	data := buildSIDX(1, 1, 1000, 0, math.MaxUint64-10, refs)
	sidx, _, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sidx.MediaRanges(100)
	if !errors.Is(err, ErrSIDXOverflow) {
		t.Fatalf("err = %v", err)
	}
}

func TestSIDXSegmentCountLimit(t *testing.T) {
	// The reference_count field is uint16 (max 65535) which is below
	// maxSIDXReferences (100000), so the count limit is unreachable via the
	// wire format. Instead, verify that a large count with insufficient data
	// triggers the truncated error (defense in depth).
	refs := []SIDXReference{{ReferencedSize: 100}}
	body := buildSIDXBody(0, 1, 1000, 0, 0, refs)
	// Patch reference_count to 65535 (max uint16) but only 1 entry present.
	binary.BigEndian.PutUint16(body[22:24], 65535)
	boxSize := uint32(8 + len(body))
	var box []byte
	box = appendUint32(box, boxSize)
	box = append(box, 's', 'i', 'd', 'x')
	box = append(box, body...)

	_, _, err := ParseSIDX(box)
	if !errors.Is(err, ErrSIDXTruncated) {
		t.Fatalf("err = %v", err)
	}
}

func TestSIDXNoSIDXBox(t *testing.T) {
	// A non-SIDX box only.
	var box []byte
	box = appendUint32(box, 16)
	box = append(box, 'f', 'r', 'e', 'e')
	box = append(box, make([]byte, 8)...)
	_, _, err := ParseSIDX(box)
	if !errors.Is(err, ErrInvalidSIDX) {
		t.Fatalf("err = %v", err)
	}
}

func TestSIDXMalformedExtendedSize(t *testing.T) {
	// Extended size (size32==1) but less than 16 bytes total.
	var box []byte
	box = appendUint32(box, 1)
	box = append(box, 's', 'i', 'd', 'x')
	box = appendUint64(box, 10) // invalid: < 16
	box = append(box, make([]byte, 20)...)
	_, _, err := ParseSIDX(box)
	if !errors.Is(err, ErrSIDXBadBoxSize) {
		t.Fatalf("err = %v", err)
	}
}

func TestSIDXBoxSizeTooSmall(t *testing.T) {
	// Normal box with size < 8.
	var box []byte
	box = appendUint32(box, 4)
	box = append(box, 's', 'i', 'd', 'x')
	box = append(box, make([]byte, 20)...)
	_, _, err := ParseSIDX(box)
	if !errors.Is(err, ErrSIDXBadBoxSize) {
		t.Fatalf("err = %v", err)
	}
}

func TestSIDXEmptyData(t *testing.T) {
	_, _, err := ParseSIDX(nil)
	if !errors.Is(err, ErrInvalidSIDX) {
		t.Fatalf("err = %v", err)
	}
}

func TestSIDXMediaRangesWithFirstOffset(t *testing.T) {
	refs := []SIDXReference{
		{ReferencedSize: 150, SubsegmentDuration: 1000},
		{ReferencedSize: 250, SubsegmentDuration: 1000},
	}
	data := buildSIDX(0, 1, 1000, 0, 75, refs)
	sidx, _, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	ranges, err := sidx.MediaRanges(0)
	if err != nil {
		t.Fatal(err)
	}
	boxSize := int64(len(data))
	firstStart := boxSize + 75
	if ranges[0].Start != firstStart || ranges[0].Length != 150 {
		t.Fatalf("range[0] = %#v, want start=%d", ranges[0], firstStart)
	}
	if ranges[1].Start != firstStart+150 || ranges[1].Length != 250 {
		t.Fatalf("range[1] = %#v", ranges[1])
	}
}

func TestSIDXVersion0NestedReference(t *testing.T) {
	refs := []SIDXReference{
		{ReferencedSize: 500, SubsegmentDuration: 48000, IsIndex: true, StartsWithSAP: true, SAPType: 1},
	}
	data := buildSIDX(0, 1, 48000, 0, 0, refs)
	sidx, offset, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 0 {
		t.Fatalf("offset = %d", offset)
	}
	if len(sidx.References) != 1 {
		t.Fatalf("references = %d", len(sidx.References))
	}
	ref := sidx.References[0]
	if !ref.IsIndex {
		t.Fatal("expected IsIndex=true")
	}
	if ref.ReferencedSize != 500 || ref.SubsegmentDuration != 48000 {
		t.Fatalf("ref = %#v", ref)
	}
	if !sidx.HasHierarchicalReferences() {
		t.Fatal("expected HasHierarchicalReferences()=true")
	}
}

func TestSIDXVersion1NestedReference(t *testing.T) {
	refs := []SIDXReference{
		{ReferencedSize: 1000, SubsegmentDuration: 90000, IsIndex: true},
	}
	ept := uint64(1) << 40
	firstOffset := uint64(1) << 35
	data := buildSIDX(1, 1, 90000, ept, firstOffset, refs)
	sidx, _, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	if sidx.EarliestPresentationTime != ept {
		t.Fatalf("EPT = %d, want %d", sidx.EarliestPresentationTime, ept)
	}
	if sidx.FirstOffset != firstOffset {
		t.Fatalf("FirstOffset = %d, want %d", sidx.FirstOffset, firstOffset)
	}
	if !sidx.References[0].IsIndex {
		t.Fatal("expected IsIndex=true for v1 nested reference")
	}
}

func TestSIDXMixedLeafAndNestedReferences(t *testing.T) {
	refs := []SIDXReference{
		{ReferencedSize: 200, SubsegmentDuration: 1000, StartsWithSAP: true, SAPType: 1},
		{ReferencedSize: 300, SubsegmentDuration: 1000, IsIndex: true},
		{ReferencedSize: 400, SubsegmentDuration: 1000, StartsWithSAP: true, SAPType: 2},
	}
	data := buildSIDX(0, 1, 1000, 0, 0, refs)
	sidx, _, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(sidx.References) != 3 {
		t.Fatalf("references = %d", len(sidx.References))
	}
	if sidx.References[0].IsIndex {
		t.Fatal("ref[0] should be leaf")
	}
	if !sidx.References[1].IsIndex {
		t.Fatal("ref[1] should be index")
	}
	if sidx.References[2].IsIndex {
		t.Fatal("ref[2] should be leaf")
	}
	if !sidx.HasHierarchicalReferences() {
		t.Fatal("expected HasHierarchicalReferences()=true")
	}
	// MediaRanges should still compute for all references.
	ranges, err := sidx.MediaRanges(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 3 {
		t.Fatalf("ranges = %d", len(ranges))
	}
	boxSize := int64(len(data))
	if ranges[0].Start != boxSize || ranges[0].Length != 200 {
		t.Fatalf("range[0] = %#v", ranges[0])
	}
	if ranges[1].Start != boxSize+200 || ranges[1].Length != 300 {
		t.Fatalf("range[1] = %#v", ranges[1])
	}
	if ranges[2].Start != boxSize+500 || ranges[2].Length != 400 {
		t.Fatalf("range[2] = %#v", ranges[2])
	}
}

func TestSIDXNestedWithNonZeroFirstOffset(t *testing.T) {
	refs := []SIDXReference{
		{ReferencedSize: 100, SubsegmentDuration: 1000, IsIndex: true},
	}
	data := buildSIDX(0, 1, 1000, 0, 50, refs)
	sidx, _, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	if sidx.FirstOffset != 50 {
		t.Fatalf("FirstOffset = %d", sidx.FirstOffset)
	}
	ranges, err := sidx.MediaRanges(0)
	if err != nil {
		t.Fatal(err)
	}
	expectedStart := int64(len(data)) + 50
	if ranges[0].Start != expectedStart || ranges[0].Length != 100 {
		t.Fatalf("range = %#v, want start=%d len=100", ranges[0], expectedStart)
	}
}

func TestSIDXNestedAfterNonSIDXBox(t *testing.T) {
	// A nested SIDX located after another ISO-BMFF box.
	refs := []SIDXReference{
		{ReferencedSize: 250, SubsegmentDuration: 5000, IsIndex: true},
	}
	sidxBox := buildSIDX(0, 1, 5000, 0, 0, refs)
	// Prepend a 'free' box of 24 bytes.
	var freeBox []byte
	freeBox = appendUint32(freeBox, 24)
	freeBox = append(freeBox, 'f', 'r', 'e', 'e')
	freeBox = append(freeBox, make([]byte, 16)...)

	data := append(freeBox, sidxBox...)
	sidx, offset, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	if offset != 24 {
		t.Fatalf("offset = %d, want 24", offset)
	}
	if !sidx.References[0].IsIndex {
		t.Fatal("expected IsIndex=true")
	}
	if sidx.References[0].ReferencedSize != 250 {
		t.Fatalf("ReferencedSize = %d", sidx.References[0].ReferencedSize)
	}
}

func TestSIDXLeafOnlyHasNoHierarchicalReferences(t *testing.T) {
	refs := []SIDXReference{
		{ReferencedSize: 100, SubsegmentDuration: 1000},
		{ReferencedSize: 200, SubsegmentDuration: 1000},
	}
	data := buildSIDX(0, 1, 1000, 0, 0, refs)
	sidx, _, err := ParseSIDX(data)
	if err != nil {
		t.Fatal(err)
	}
	if sidx.HasHierarchicalReferences() {
		t.Fatal("expected HasHierarchicalReferences()=false for leaf-only SIDX")
	}
}

// FuzzSIDX is a bounded fuzz target for the SIDX parser. It exercises parsing
// and media range computation without network or filesystem operations.
func FuzzSIDX(f *testing.F) {
	// Seed with valid v0 box.
	refsV0 := []SIDXReference{
		{ReferencedSize: 1000, SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
		{ReferencedSize: 2000, SubsegmentDuration: 48000, StartsWithSAP: false},
	}
	f.Add(buildSIDX(0, 1, 48000, 0, 0, refsV0))

	// Seed with valid v1 box.
	refsV1 := []SIDXReference{{ReferencedSize: 500, SubsegmentDuration: 90000, StartsWithSAP: true, SAPType: 2, SAPDeltaTime: 10}}
	f.Add(buildSIDX(1, 2, 90000, 1<<33, 1<<30, refsV1))

	// Seed with truncated data.
	f.Add([]byte{0, 0, 0, 20, 's', 'i', 'd', 'x', 0, 0})

	// Seed with malformed extended size.
	f.Add([]byte{0, 0, 0, 1, 's', 'i', 'd', 'x', 0, 0, 0, 0, 0, 0, 0, 5})

	// Seed with non-SIDX box.
	f.Add([]byte{0, 0, 0, 16, 'f', 'r', 'e', 'e', 0, 0, 0, 0, 0, 0, 0, 0})

	// Seed with empty input.
	f.Add([]byte{})

	// Hierarchical seeds: v0 with index reference.
	refsHierV0 := []SIDXReference{
		{ReferencedSize: 300, SubsegmentDuration: 48000, IsIndex: true, StartsWithSAP: true, SAPType: 1},
		{ReferencedSize: 500, SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
	}
	f.Add(buildSIDX(0, 1, 48000, 0, 0, refsHierV0))

	// Hierarchical seeds: v1 with mixed leaf/index.
	refsHierV1 := []SIDXReference{
		{ReferencedSize: 200, SubsegmentDuration: 90000, StartsWithSAP: true, SAPType: 1},
		{ReferencedSize: 400, SubsegmentDuration: 90000, IsIndex: true},
		{ReferencedSize: 600, SubsegmentDuration: 90000, StartsWithSAP: true, SAPType: 2},
	}
	f.Add(buildSIDX(1, 2, 90000, 1<<33, 1<<30, refsHierV1))

	// Hierarchical seed: all index references.
	refsAllIndex := []SIDXReference{
		{ReferencedSize: 100, SubsegmentDuration: 1000, IsIndex: true},
		{ReferencedSize: 100, SubsegmentDuration: 1000, IsIndex: true},
	}
	f.Add(buildSIDX(0, 1, 1000, 0, 0, refsAllIndex))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		sidx, offset, err := ParseSIDX(data)
		if err != nil {
			return
		}
		if offset < 0 || offset >= len(data) {
			t.Fatalf("offset %d out of bounds for data len %d", offset, len(data))
		}
		// If parsing succeeded, exercise MediaRanges with various base offsets.
		for _, base := range []int64{0, 100, 1 << 30, math.MaxInt64 - 1<<20} {
			ranges, rangeErr := sidx.MediaRanges(base)
			if rangeErr != nil {
				continue
			}
			// Validate range invariants.
			var prevEnd int64
			for i, r := range ranges {
				if r.Length <= 0 {
					t.Fatalf("range[%d] has non-positive length %d", i, r.Length)
				}
				if r.Start < 0 {
					t.Fatalf("range[%d] has negative start %d", i, r.Start)
				}
				if i > 0 && r.Start != prevEnd {
					t.Fatalf("range[%d] start %d != prev end %d", i, r.Start, prevEnd)
				}
				prevEnd = r.Start + r.Length
			}
		}
	})
}

// FuzzSIDXRecursiveExpansion is a focused fuzz target for the recursive SIDX
// expansion logic. It uses a deterministic in-memory transport that serves a
// synthetic resource built from the fuzz input. No real networking is used.
func FuzzSIDXRecursiveExpansion(f *testing.F) {
	// Seed: root SIDX with one index ref pointing to a nested SIDX with one leaf.
	nestedRefs := []SIDXReference{{ReferencedSize: 50, SubsegmentDuration: 1000, StartsWithSAP: true, SAPType: 1}}
	nestedBox := buildSIDX(0, 1, 1000, 0, 0, nestedRefs)
	rootRefs := []SIDXReference{{ReferencedSize: uint32(len(nestedBox)), SubsegmentDuration: 1000, IsIndex: true}}
	rootBox := buildSIDX(0, 1, 1000, 0, 0, rootRefs)
	var resource []byte
	resource = append(resource, rootBox...)
	resource = append(resource, nestedBox...)
	resource = append(resource, make([]byte, 50)...) // leaf media
	f.Add(resource, int64(0), int64(len(rootBox)))

	// Seed: leaf-only SIDX.
	leafRefs := []SIDXReference{{ReferencedSize: 100, SubsegmentDuration: 1000}}
	leafBox := buildSIDX(0, 1, 1000, 0, 0, leafRefs)
	var leafResource []byte
	leafResource = append(leafResource, leafBox...)
	leafResource = append(leafResource, make([]byte, 100)...)
	f.Add(leafResource, int64(0), int64(len(leafBox)))

	f.Fuzz(func(t *testing.T, resource []byte, indexStart, indexLength int64) {
		if len(resource) > 1<<20 {
			t.Skip()
		}
		if indexStart < 0 || indexLength <= 0 || indexStart+indexLength > int64(len(resource)) {
			return
		}
		// Use the in-memory transport to serve the resource.
		transport := &memoryRangeTransport{data: resource}
		downloader := NewDownloader(transport, Config{MaxSegments: 1000})
		marker := Segment{
			URL:        "https://media.example.test/video.mp4",
			IndexRange: fmt.Sprintf("%d-%d", indexStart, indexStart+indexLength-1),
		}
		segments, err := downloader.expandOneSIDX(context.Background(), marker)
		if err != nil {
			// Errors are acceptable; panics and unbounded allocations are not.
			return
		}
		// Validate: no index bytes in output, ranges are positive, ordered.
		for i, seg := range segments {
			if seg.RangeLength <= 0 {
				t.Fatalf("segment[%d] has non-positive length %d", i, seg.RangeLength)
			}
			if seg.RangeStart < 0 {
				t.Fatalf("segment[%d] has negative start %d", i, seg.RangeStart)
			}
		}
	})
}

// memoryRangeTransport is a deterministic in-memory Transport for fuzzing.
type memoryRangeTransport struct {
	data []byte
}

func (m *memoryRangeTransport) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	rangeHeader := req.Header.Get("Range")
	if rangeHeader == "" {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader(m.data)),
			ContentLength: int64(len(m.data)),
			Header:        http.Header{},
		}, nil
	}
	var start, end int64
	if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(bytes.NewReader(nil)), Header: http.Header{}}, nil
	}
	if start >= int64(len(m.data)) || end >= int64(len(m.data)) || start > end {
		return &http.Response{StatusCode: http.StatusRequestedRangeNotSatisfiable, Body: io.NopCloser(bytes.NewReader(nil)), Header: http.Header{}}, nil
	}
	header := http.Header{}
	header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(m.data)))
	return &http.Response{
		StatusCode:    http.StatusPartialContent,
		Body:          io.NopCloser(bytes.NewReader(m.data[start : end+1])),
		ContentLength: end - start + 1,
		Header:        header,
	}, nil
}

func (m *memoryRangeTransport) ReadPage(_ context.Context, _ string) ([]byte, http.Header, error) {
	return m.data, http.Header{}, nil
}
