package youtubeump

import (
	"errors"
	"os"
	"testing"
)

func TestMediaEndRejectsTrailingPayload(t *testing.T) {
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, nil, 1024)
	err := assembler.consumePart(Part{
		Type:    PartMediaEnd,
		Payload: append(mustTestVarint(1), 0xFF),
	})
	if !errors.Is(err, ErrInvalidMediaState) {
		t.Fatalf("err=%v", err)
	}
}

func TestMaxActiveHeadersBoundary(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "active-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
	if err := assembler.consumePart(Part{Type: PartFormatInitializationMetadata, Payload: encodeFormatInitialization(137)}); err != nil {
		t.Fatal(err)
	}
	for id := uint32(1); id <= MaxActiveHeaders; id++ {
		header := marshalMediaHeader(MediaHeader{
			HeaderID: id, Itag: 140, SequenceNumber: uint64(id), DurationMs: 1, ContentLength: 1,
		})
		if err := assembler.consumePart(Part{Type: PartMediaHeader, Payload: header}); err != nil {
			t.Fatalf("id=%d err=%v", id, err)
		}
		if err := assembler.consumePart(Part{Type: PartMedia, Payload: append(mustTestVarint(uint64(id)), 'x')}); err != nil {
			t.Fatalf("media id=%d err=%v", id, err)
		}
	}
	extra := marshalMediaHeader(MediaHeader{
		HeaderID: 100, Itag: 140, SequenceNumber: 100, DurationMs: 1, ContentLength: 1,
	})
	if err := assembler.consumePart(Part{Type: PartMediaHeader, Payload: extra}); !errors.Is(err, ErrTooManyActiveHeaders) {
		t.Fatalf("err=%v", err)
	}
}

func TestManySequentialFinalizedHeadersAllowed(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "seq-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
	if err := assembler.consumePart(Part{Type: PartFormatInitializationMetadata, Payload: encodeFormatInitialization(137)}); err != nil {
		t.Fatal(err)
	}
	initHeader := marshalMediaHeader(MediaHeader{HeaderID: 1, Itag: 137, IsInitSeg: true, ContentLength: 4})
	for _, part := range []struct {
		partType int
		payload  []byte
	}{
		{PartMediaHeader, initHeader},
		{PartMedia, append(mustTestVarint(1), []byte("INIT")...)},
		{PartMediaEnd, mustTestVarint(1)},
	} {
		if err := assembler.consumePart(Part{Type: part.partType, Payload: part.payload}); err != nil {
			t.Fatal(err)
		}
	}
	for id := uint32(2); id <= MaxActiveHeaders+4; id++ {
		header := marshalMediaHeader(MediaHeader{
			HeaderID: id, Itag: 137, SequenceNumber: uint64(id - 2), DurationMs: 1000, ContentLength: 1,
		})
		for _, part := range []struct {
			partType int
			payload  []byte
		}{
			{PartMediaHeader, header},
			{PartMedia, append(mustTestVarint(uint64(id)), 'x')},
			{PartMediaEnd, mustTestVarint(uint64(id))},
		} {
			if err := assembler.consumePart(Part{Type: part.partType, Payload: part.payload}); err != nil {
				t.Fatalf("id=%d err=%v", id, err)
			}
		}
	}
}

func TestFormatInitRejectsConflictingLastModified(t *testing.T) {
	assembler := newTrackAssembler(FormatID{Itag: 137, LastModified: 100}, 10000, nil, 1024)
	meta := appendProtobufBytes(nil, fFormatInitFormatID, FormatID{Itag: 137, LastModified: 200}.marshal())
	err := assembler.consumePart(Part{Type: PartFormatInitializationMetadata, Payload: meta})
	if !errors.Is(err, ErrInvalidMediaState) {
		t.Fatalf("err=%v", err)
	}
}

func TestFormatInitAcceptsAbsentOptionalIdentityFields(t *testing.T) {
	assembler := newTrackAssembler(FormatID{Itag: 137, LastModified: 100}, 10000, nil, 1024)
	if err := assembler.consumePart(Part{Type: PartFormatInitializationMetadata, Payload: encodeFormatInitialization(137)}); err != nil {
		t.Fatal(err)
	}
	if !assembler.formatVerified {
		t.Fatal("expected format verification")
	}
}

func TestSelectedHeaderRejectsConflictingFormatID(t *testing.T) {
	assembler := newTrackAssembler(FormatID{Itag: 137, XTags: "foo"}, 10000, nil, 1024)
	if err := assembler.consumePart(Part{Type: PartFormatInitializationMetadata, Payload: encodeFormatInitialization(137)}); err != nil {
		t.Fatal(err)
	}
	header := marshalMediaHeader(MediaHeader{
		HeaderID:  1,
		Itag:      137,
		FormatID:  FormatID{Itag: 137, XTags: "bar"},
		IsInitSeg: true, ContentLength: 4,
	})
	err := assembler.consumePart(Part{Type: PartMediaHeader, Payload: header})
	if !errors.Is(err, ErrInvalidMediaState) {
		t.Fatalf("err=%v", err)
	}
}

func TestSelectedHeaderAcceptsItagOnlyIdentity(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "itag-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
	if err := assembler.consumePart(Part{Type: PartFormatInitializationMetadata, Payload: encodeFormatInitialization(137)}); err != nil {
		t.Fatal(err)
	}
	header := marshalMediaHeader(MediaHeader{HeaderID: 1, Itag: 137, IsInitSeg: true, ContentLength: 4})
	for _, part := range []struct {
		partType int
		payload  []byte
	}{
		{PartMediaHeader, header},
		{PartMedia, append(mustTestVarint(1), []byte("INIT")...)},
		{PartMediaEnd, mustTestVarint(1)},
	} {
		if err := assembler.consumePart(Part{Type: part.partType, Payload: part.payload}); err != nil {
			t.Fatalf("err=%v", err)
		}
	}
}
