package youtubeump

import (
	"fmt"
	"math"
)

const (
	wireVarint             = 0
	wireBytes              = 2
	maxProtobufVarintBytes = 10
)

type fieldReader struct {
	data   []byte
	offset int
	fields int
	err    error
}

func (reader *fieldReader) next() (num uint64, wireType int, ok bool) {
	if reader.err != nil || reader.offset >= len(reader.data) {
		return 0, 0, false
	}
	reader.fields++
	if reader.fields > MaxProtobufFields {
		reader.err = ErrInvalidProtobuf
		return 0, 0, false
	}
	key, n, err := readProtobufVarint(reader.data[reader.offset:])
	if err != nil {
		reader.err = err
		return 0, 0, false
	}
	reader.offset += n
	num = key >> 3
	wireType = int(key & 0x7)
	if num == 0 || num > math.MaxUint32 {
		reader.err = ErrInvalidProtobuf
		return 0, 0, false
	}
	return num, wireType, true
}

func (reader *fieldReader) varint() uint64 {
	value, n, err := readProtobufVarint(reader.data[reader.offset:])
	if err != nil {
		reader.err = err
		return 0
	}
	reader.offset += n
	return value
}

func (reader *fieldReader) uint32() uint32 {
	value := reader.varint()
	if reader.err != nil {
		return 0
	}
	if value > math.MaxUint32 {
		reader.err = ErrVarintOverflow
		return 0
	}
	return uint32(value)
}

func (reader *fieldReader) bytes() []byte {
	length, n, err := readProtobufVarint(reader.data[reader.offset:])
	if err != nil {
		reader.err = err
		return nil
	}
	reader.offset += n
	if length > uint64(MaxProtobufFieldBytes) || length > uint64(len(reader.data)-reader.offset) {
		reader.err = ErrInvalidProtobuf
		return nil
	}
	value := reader.data[reader.offset : reader.offset+int(length)]
	reader.offset += int(length)
	return value
}

func (reader *fieldReader) string() string {
	return string(reader.bytes())
}

func (reader *fieldReader) skip(num uint64, wireType int) {
	switch wireType {
	case wireVarint:
		_, n, err := readProtobufVarint(reader.data[reader.offset:])
		if err != nil {
			reader.err = err
			return
		}
		reader.offset += n
	case wireBytes:
		_ = reader.bytes()
	default:
		reader.err = fmt.Errorf("%w: unsupported wire type %d for field %d", ErrInvalidProtobuf, wireType, num)
	}
}

func readProtobufVarint(data []byte) (uint64, int, error) {
	if len(data) == 0 {
		return 0, 0, ErrTruncatedStream
	}
	var value uint64
	var shift uint
	for i := 0; i < maxProtobufVarintBytes; i++ {
		if i >= len(data) {
			return 0, 0, ErrTruncatedStream
		}
		b := data[i]
		if i == maxProtobufVarintBytes-1 && b > 1 {
			return 0, 0, ErrVarintOverflow
		}
		value |= uint64(b&0x7F) << shift
		if b < 0x80 {
			if !isCanonicalProtobufVarint(value, i+1, data[:i+1]) {
				return 0, 0, ErrNonCanonicalVarint
			}
			return value, i + 1, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, 0, ErrVarintOverflow
		}
	}
	return 0, 0, ErrVarintOverflow
}

func isCanonicalProtobufVarint(value uint64, size int, encoded []byte) bool {
	if size == 1 {
		return true
	}
	min := uint64(1) << (7 * (size - 1))
	return value >= min
}

func readProtobufUint32(data []byte) (uint32, error) {
	value, _, err := readProtobufVarint(data)
	if err != nil {
		return 0, err
	}
	if value > math.MaxUint32 {
		return 0, ErrVarintOverflow
	}
	return uint32(value), nil
}

func appendProtobufVarint(buf []byte, field uint64, value uint64) []byte {
	key := field<<3 | wireVarint
	buf = appendKey(buf, key)
	return appendU64(buf, value)
}

func appendProtobufBytes(buf []byte, field uint64, value []byte) []byte {
	key := field<<3 | wireBytes
	buf = appendKey(buf, key)
	buf = appendU64(buf, uint64(len(value)))
	return append(buf, value...)
}

func appendKey(buf []byte, key uint64) []byte {
	return appendU64(buf, key)
}

func appendU64(buf []byte, value uint64) []byte {
	for value >= 0x80 {
		buf = append(buf, byte(value)|0x80)
		value >>= 7
	}
	return append(buf, byte(value))
}
