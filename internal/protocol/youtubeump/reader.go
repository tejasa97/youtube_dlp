package youtubeump

import (
	"io"
)

const readerChunkSize = 32 * 1024

// Part is one decoded UMP part. Payload is copied and does not alias the reader buffer.
type Part struct {
	Type    int
	Payload []byte
}

// Reader incrementally parses UMP framing from a bounded stream.
type Reader struct {
	source      io.Reader
	buffer      []byte
	scratch     []byte
	partCount   int
	totalBytes  int64
	maxBytes    int64
	maxParts    int
	maxPartSize int
	limitProbe  limitProbeState
}

type limitProbeState uint8

const (
	limitNotProbed limitProbeState = iota
	limitCleanEOF
	limitOversize
)

func NewReader(source io.Reader, maxBytes int64) *Reader {
	if maxBytes <= 0 || maxBytes > MaxRoundBytes {
		maxBytes = MaxRoundBytes
	}
	return &Reader{
		source:      source,
		scratch:     make([]byte, readerChunkSize),
		maxBytes:    maxBytes,
		maxParts:    MaxParts,
		maxPartSize: MaxPartPayload,
	}
}

func (reader *Reader) ReadPart() (Part, bool, error) {
	typeValue, err := reader.readVarint()
	if err != nil {
		if err == io.EOF {
			return Part{}, false, nil
		}
		if err == ErrTruncatedStream && len(reader.buffer) == 0 {
			return Part{}, false, nil
		}
		return Part{}, false, err
	}
	partType, err := int32FromUint64(typeValue)
	if err != nil {
		return Part{}, false, err
	}
	sizeValue, err := reader.readVarint()
	if err != nil {
		return Part{}, false, err
	}
	if sizeValue > uint64(reader.maxPartSize) {
		return Part{}, false, ErrOversizedPart
	}
	partSize := int(sizeValue)
	if err := reader.ensure(partSize); err != nil {
		if err == io.EOF {
			return Part{}, false, ErrTruncatedStream
		}
		return Part{}, false, err
	}
	payload := append([]byte(nil), reader.buffer[:partSize]...)
	reader.buffer = reader.buffer[partSize:]
	reader.partCount++
	if reader.partCount > reader.maxParts {
		return Part{}, false, ErrTooManyParts
	}
	return Part{Type: int(partType), Payload: payload}, true, nil
}

func (reader *Reader) readVarint() (uint64, error) {
	if err := reader.ensure(1); err != nil {
		return 0, err
	}
	size := umpVarintSize(reader.buffer[0])
	if err := reader.ensure(size); err != nil {
		if err == io.EOF {
			return 0, ErrTruncatedStream
		}
		return 0, err
	}
	value, consumed, err := readUMPVarint(reader.buffer[:size])
	if err != nil {
		return 0, err
	}
	reader.buffer = reader.buffer[consumed:]
	return value, nil
}

func (reader *Reader) ensure(need int) error {
	for len(reader.buffer) < need {
		remaining := reader.maxBytes - reader.totalBytes
		if remaining <= 0 {
			if len(reader.buffer) >= need {
				return nil
			}
			return reader.needBeyondLimit(need)
		}
		chunk := int64(len(reader.scratch))
		if chunk > remaining {
			chunk = remaining
		}
		n, err := reader.source.Read(reader.scratch[:chunk])
		if n > 0 {
			reader.totalBytes += int64(n)
			reader.buffer = append(reader.buffer, reader.scratch[:n]...)
		}
		if err != nil {
			if err == io.EOF && len(reader.buffer) >= need {
				return nil
			}
			return err
		}
		if n == 0 {
			return io.EOF
		}
	}
	return nil
}

func (reader *Reader) needBeyondLimit(need int) error {
	if len(reader.buffer) >= need {
		return nil
	}
	if err := reader.probeAtLimit(); err != nil {
		return err
	}
	if len(reader.buffer) > 0 {
		return ErrTruncatedStream
	}
	return io.EOF
}

func (reader *Reader) probeAtLimit() error {
	switch reader.limitProbe {
	case limitCleanEOF:
		return io.EOF
	case limitOversize:
		return ErrResponseTooLarge
	default:
		var sentinel [1]byte
		n, err := reader.source.Read(sentinel[:])
		if n > 0 {
			reader.limitProbe = limitOversize
			return ErrResponseTooLarge
		}
		reader.limitProbe = limitCleanEOF
		if err != nil && err != io.EOF {
			return err
		}
		return io.EOF
	}
}

// ParseAll decodes a fully-buffered UMP body. Tests and offline fixtures use it.
func ParseAll(body []byte) ([]Part, error) {
	reader := NewReader(newByteReader(body), MaxRoundBytes)
	var parts []Part
	for {
		part, ok, err := reader.ReadPart()
		if err != nil {
			return nil, err
		}
		if !ok {
			return parts, nil
		}
		parts = append(parts, part)
	}
}

type byteReader struct {
	data []byte
}

func newByteReader(data []byte) *byteReader {
	return &byteReader{data: data}
}

func (reader *byteReader) Read(p []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, reader.data)
	reader.data = reader.data[n:]
	return n, nil
}
