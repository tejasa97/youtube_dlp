package youtubeump

import (
	"bytes"
	"errors"
	"io"
	"math"
	"testing"
)

type countingReader struct {
	data  []byte
	calls int
}

func (reader *countingReader) Read(p []byte) (int, error) {
	reader.calls++
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, reader.data)
	reader.data = reader.data[n:]
	return n, nil
}

func TestReaderUsesChunkedReads(t *testing.T) {
	body := make([]byte, 1<<20)
	for index := range body {
		body[index] = byte(index)
	}
	source := &countingReader{data: body}
	reader := NewReader(source, int64(len(body)))
	if err := reader.ensure(len(body)); err != nil {
		t.Fatal(err)
	}
	if source.calls > len(body)/readerChunkSize+1 {
		t.Fatalf("read calls=%d want O(size/chunk)", source.calls)
	}
	if source.calls < len(body)/readerChunkSize {
		t.Fatalf("read calls=%d too few", source.calls)
	}
}

func TestReaderReadPartExactBoundEOF(t *testing.T) {
	body := encodePart(PartMediaEnd, mustTestVarint(1))
	bound := int64(len(body))
	reader := NewReader(newByteReader(body), bound)
	part, ok, err := reader.ReadPart()
	if err != nil || !ok || part.Type != PartMediaEnd {
		t.Fatalf("part=%+v ok=%v err=%v", part, ok, err)
	}
	_, ok, err = reader.ReadPart()
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestReaderReadPartBoundPlusOneRejected(t *testing.T) {
	body := encodePart(PartMediaEnd, mustTestVarint(1))
	bound := int64(len(body))
	oversize := append(append([]byte(nil), body...), 0x00)
	reader := NewReader(newByteReader(oversize), bound)
	if _, ok, err := reader.ReadPart(); err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	_, ok, err := reader.ReadPart()
	if ok || !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestReaderReadPartTruncatedAtBound(t *testing.T) {
	full := encodePart(PartMediaEnd, mustTestVarint(1))
	body := full[:len(full)-1]
	reader := NewReader(newByteReader(body), int64(len(body)))
	_, ok, err := reader.ReadPart()
	if ok || !errors.Is(err, ErrTruncatedStream) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestReaderReadPartExactBoundMultipleParts(t *testing.T) {
	body := append(
		encodePart(PartMediaEnd, mustTestVarint(1)),
		encodePart(PartMediaEnd, mustTestVarint(2))...,
	)
	bound := int64(len(body))
	reader := NewReader(newByteReader(body), bound)
	for index := 0; index < 2; index++ {
		_, ok, err := reader.ReadPart()
		if err != nil || !ok {
			t.Fatalf("part=%d ok=%v err=%v", index, ok, err)
		}
	}
	_, ok, err := reader.ReadPart()
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestReaderReadPartBoundPlusOneAfterMultipleParts(t *testing.T) {
	body := append(
		encodePart(PartMediaEnd, mustTestVarint(1)),
		encodePart(PartMediaEnd, mustTestVarint(2))...,
	)
	bound := int64(len(body))
	oversize := append(append([]byte(nil), body...), 0xff)
	reader := NewReader(newByteReader(oversize), bound)
	for index := 0; index < 2; index++ {
		if _, ok, err := reader.ReadPart(); err != nil || !ok {
			t.Fatalf("part=%d ok=%v err=%v", index, ok, err)
		}
	}
	_, ok, err := reader.ReadPart()
	if ok || !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestReaderReadPartTruncatedMidFrameAtBound(t *testing.T) {
	payload := bytes.Repeat([]byte{'x'}, 50)
	full := encodePart(PartMediaEnd, payload)
	const bound = 30
	prefix := full[:bound]
	reader := NewReader(newByteReader(prefix), bound)
	_, ok, err := reader.ReadPart()
	if ok || !errors.Is(err, ErrTruncatedStream) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestReaderReadPartOversizeMidFrameAtBound(t *testing.T) {
	payload := bytes.Repeat([]byte{'x'}, 50)
	full := encodePart(PartMediaEnd, payload)
	const bound = 30
	oversize := append(append([]byte(nil), full[:bound]...), 0xaa)
	reader := NewReader(newByteReader(oversize), bound)
	_, ok, err := reader.ReadPart()
	if ok || !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestReadPartRejectsOversizedPartType(t *testing.T) {
	typeBytes, _ := encodeUMPVarint(uint64(math.MaxInt32) + 1)
	sizeBytes, _ := encodeUMPVarint(0)
	body := append(typeBytes, sizeBytes...)
	_, ok, err := NewReader(newByteReader(body), int64(len(body))).ReadPart()
	if ok || !errors.Is(err, ErrVarintOverflow) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
