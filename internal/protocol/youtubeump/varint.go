package youtubeump

// UMP variable-length integers use a prefix scheme documented in
// https://github.com/davidzeng0/innertube/blob/main/googlevideo/ump.md and
// implemented in LuanRT/GoogleVideo UmpReader.ts (commit d2fa40d761034a286cf60ee033653307a1295b0c).

func umpVarintSize(prefix byte) int {
	size := 1
	for i := 7; i >= 1; i-- {
		if prefix&(1<<uint(i)) == 0 {
			break
		}
		size++
	}
	if size > 5 {
		size = 5
	}
	return size
}

func readUMPVarint(data []byte) (value uint64, consumed int, err error) {
	if len(data) == 0 {
		return 0, 0, ErrTruncatedStream
	}
	prefix := data[0]
	size := umpVarintSize(prefix)
	if len(data) < size {
		return 0, 0, ErrTruncatedStream
	}
	if size == 5 {
		if prefix&0x07 != 0 {
			return 0, 0, ErrNonCanonicalVarint
		}
		value = uint64(data[1]) | uint64(data[2])<<8 | uint64(data[3])<<16 | uint64(data[4])<<24
	} else {
		mask := byte(1<<(8-uint(size))) - 1
		value = uint64(prefix & mask)
		shift := uint(8 - size)
		for i := 1; i < size; i++ {
			value |= uint64(data[i]) << shift
			shift += 8
		}
	}
	encoded, encSize := encodeUMPVarint(value)
	if encSize != size {
		return 0, 0, ErrNonCanonicalVarint
	}
	for i := 0; i < size; i++ {
		if data[i] != encoded[i] {
			return 0, 0, ErrNonCanonicalVarint
		}
	}
	return value, size, nil
}

func encodeUMPVarint(value uint64) ([]byte, int) {
	switch {
	case value <= 0x7F:
		return []byte{byte(value)}, 1
	case value <= 0x3FFF:
		return []byte{byte(0x80 | (value & 0x3F)), byte(value >> 6)}, 2
	case value <= 0x1FFFFF:
		return []byte{byte(0xC0 | (value & 0x1F)), byte((value >> 5) & 0xFF), byte(value >> 13)}, 3
	case value <= 0x0FFFFFFF:
		return []byte{
			byte(0xE0 | (value & 0x0F)),
			byte((value >> 4) & 0xFF),
			byte((value >> 12) & 0xFF),
			byte(value >> 20),
		}, 4
	default:
		return []byte{0xF0, byte(value), byte(value >> 8), byte(value >> 16), byte(value >> 24)}, 5
	}
}
