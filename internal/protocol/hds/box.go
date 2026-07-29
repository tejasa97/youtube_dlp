package hds

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// Box kinds used by the F4M bootstrap payload (ABST, ASRT, AFRT).
var (
	boxTypeBootstrap = []byte("abst")
	boxTypeFragment  = []byte("afrt")
	boxTypeMedia     = []byte("mdat")
)

// maxBox64Bytes is the largest box we are willing to materialize as an int64.
// yt-dlp accepts arbitrarily large boxes; we cap at 16 MiB which is more than
// any legitimate HDS bootstrap. A larger box is rejected as malformed.
const maxBox64Bytes int64 = 16 << 20

// readBox parses an MP4-style ISO box from r and returns the inner payload.
//
// A normal box carries a 32-bit size and 32-bit four-cc. When size==1 the real
// size follows as a 64-bit value; size==0 means "extends to EOF" which we
// reject because HDS bootstraps are explicitly bounded.
//
// The returned payload is exactly (boxSize - headerEnd) bytes; the function
// errors if the box claims more than the source can supply, or if the size is
// larger than int64's practical representable range (after which we cannot
// safely allocate the payload).
func readBox(r io.Reader) (size int64, boxType []byte, payload []byte, err error) {
	var header [8]byte
	if _, readErr := io.ReadFull(r, header[:]); readErr != nil {
		if errors.Is(readErr, io.ErrUnexpectedEOF) || errors.Is(readErr, io.EOF) {
			return 0, nil, nil, io.ErrUnexpectedEOF
		}
		return 0, nil, nil, readErr
	}
	boxSize := int64(binary.BigEndian.Uint32(header[:4]))
	boxType = append([]byte(nil), header[4:8]...)
	headerEnd := int64(8)
	switch boxSize {
	case 0:
		return 0, nil, nil, fmt.Errorf("%w: size==0 boxes are unbounded", ErrInvalidBootstrap)
	case 1:
		var big [8]byte
		if _, readErr := io.ReadFull(r, big[:]); readErr != nil {
			return 0, nil, nil, readErr
		}
		raw := binary.BigEndian.Uint64(big[:])
		if raw > math.MaxInt64 {
			return 0, nil, nil, fmt.Errorf("%w: 64-bit size exceeds int64 range", ErrInvalidBootstrap)
		}
		boxSize = int64(raw)
		headerEnd = 16
		if boxSize < headerEnd {
			return 0, nil, nil, fmt.Errorf("%w: undersized 64-bit box", ErrInvalidBootstrap)
		}
	}
	if boxSize < headerEnd {
		return 0, nil, nil, fmt.Errorf("%w: undersized box", ErrInvalidBootstrap)
	}
	if boxSize > maxBox64Bytes {
		return 0, nil, nil, fmt.Errorf("%w: box exceeds %d bytes", ErrInvalidBootstrap, maxBox64Bytes)
	}
	bodyLen := boxSize - headerEnd
	payload = make([]byte, bodyLen)
	if _, readErr := io.ReadFull(r, payload); readErr != nil {
		return 0, nil, nil, readErr
	}
	return boxSize, boxType, payload, nil
}

// readBoxes parses a sequence of framed boxes that exactly fill body. Each
// returned payload is the inner bytes of one box, in order. Trailing bytes
// or short reads are reported as malformed.
func readBoxes(body []byte, allowedTypes ...[]byte) ([]Box, error) {
	offset := 0
	var out []Box
	for offset < len(body) {
		size, kind, payload, err := readBox(bytesReaderAt(body, offset))
		if err != nil {
			return nil, err
		}
		if int64(offset)+size > int64(len(body)) {
			return nil, fmt.Errorf("%w: box overflows container at offset %d", ErrInvalidBootstrap, offset)
		}
		if !boxKindAllowed(kind, allowedTypes) {
			return nil, fmt.Errorf("%w: unexpected box kind %q", ErrInvalidBootstrap, string(kind))
		}
		out = append(out, Box{Type: append([]byte(nil), kind...), Payload: payload})
		offset += int(size)
	}
	if offset != len(body) {
		return nil, fmt.Errorf("%w: %d trailing bytes after box run", ErrInvalidBootstrap, len(body)-offset)
	}
	return out, nil
}

// Box is one parsed ISO box.
type Box struct {
	Type    []byte
	Payload []byte
}

func boxKindAllowed(kind []byte, allowed [][]byte) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if bytesEqual(kind, candidate) {
			return true
		}
	}
	return false
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// bytesReaderAt returns a fresh reader positioned at offset within data. Used
// because readBox takes an io.Reader and the bootstrap walks sub-boxes from
// arbitrary offsets inside a parent body.
func bytesReaderAt(data []byte, offset int) io.Reader {
	return &sliceReader{data: data[offset:], pos: 0}
}

// sliceReader is a non-Seeker io.Reader over a byte slice, used by readBox.
type sliceReader struct {
	data []byte
	pos  int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
