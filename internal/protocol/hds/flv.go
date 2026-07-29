package hds

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// FLV constants mirror f4m.py's write_flv_header and write_metadata_tag.
const (
	flvHeader           = "FLV\x01\x05\x00\x00\x00\x09\x00\x00\x00\x00"
	flvScriptTag        = byte(0x12)
	flvTagHeaderLen     = 11
	maxFLVMetadataBytes = 1 << 20
	maxFLVFragmentBytes = 64 << 20
	maxFLVOutputBytes   = 8 << 30
	maxUint24           = 0xffffff
)

// writeFLVHeader writes the FLV signature, audio/video flags, and DataOffset.
func writeFLVHeader(w io.Writer) error {
	if _, err := io.WriteString(w, flvHeader); err != nil {
		return fmt.Errorf("flv header: %w", err)
	}
	return nil
}

// writeUint24 writes a big-endian 24-bit value. Panics if v exceeds 0xffffff.
func writeUint24(buf []byte, offset int, v uint32) error {
	if v > maxUint24 {
		return fmt.Errorf("uint24 overflow: %d", v)
	}
	buf[offset] = byte(v >> 16)
	buf[offset+1] = byte(v >> 8)
	buf[offset+2] = byte(v)
	return nil
}

// writeFLVMetadataTag writes the optional onMetaData script tag preceding the
// media payload. Passing a nil/empty payload skips emission entirely.
func writeFLVMetadataTag(w io.Writer, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	if len(payload) > maxFLVMetadataBytes {
		return fmt.Errorf("%w: metadata payload", ErrFragmentTooLarge)
	}
	if uint32(len(payload)) > maxUint24 {
		// FLV tag size is a 24-bit field; the script tag body cannot exceed it.
		return fmt.Errorf("%w: metadata uint24 overflow", ErrFragmentTooLarge)
	}
	header := make([]byte, flvTagHeaderLen)
	header[0] = flvScriptTag
	if err := writeUint24(header, 1, uint32(len(payload))); err != nil {
		return fmt.Errorf("flv metadata tag: %w", err)
	}
	// Timestamp (3 bytes) + TimestampExtended (1 byte) zeroed.
	// StreamID zeroed.
	footer := make([]byte, 4)
	binary.BigEndian.PutUint32(footer, uint32(len(payload)+flvTagHeaderLen))
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("flv metadata header: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("flv metadata body: %w", err)
	}
	if _, err := w.Write(footer); err != nil {
		return fmt.Errorf("flv metadata footer: %w", err)
	}
	return nil
}

// extractMDAT scans a downloaded F4F fragment and returns the first mdat
// payload. Subsequent boxes are ignored, exactly matching f4m.py's loop which
// appends the first mdat and breaks. A fragment with no mdat is rejected with
// ErrFragmentFetch because it cannot be safely assembled.
func extractMDAT(fragment []byte) ([]byte, error) {
	if len(fragment) == 0 {
		return nil, fmt.Errorf("%w: empty fragment", ErrFragmentFetch)
	}
	if len(fragment) > maxFLVFragmentBytes {
		return nil, fmt.Errorf("%w: fragment size", ErrFragmentTooLarge)
	}
	offset := 0
	for offset < len(fragment) {
		size, kind, payload, err := readBox(bytesReaderAt(fragment, offset))
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, fmt.Errorf("%w: truncated fragment box at %d", ErrFragmentFetch, offset)
			}
			return nil, err
		}
		if int64(offset)+size > int64(len(fragment)) {
			return nil, fmt.Errorf("%w: box overflows fragment", ErrFragmentFetch)
		}
		if bytesEqual(kind, boxTypeMedia) {
			return payload, nil
		}
		offset += int(size)
	}
	return nil, fmt.Errorf("%w: no mdat in fragment", ErrFragmentFetch)
}
