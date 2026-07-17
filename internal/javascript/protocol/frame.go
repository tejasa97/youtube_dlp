package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	HeaderBytes   = 4
	MaxFrameBytes = 16 << 20
)

var ErrFrameTooLarge = errors.New("JavaScript helper frame exceeds limit")

// WriteFrame writes one big-endian length-prefixed message.
func WriteFrame(writer io.Writer, payload []byte) error {
	if len(payload) > MaxFrameBytes {
		return fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, len(payload), MaxFrameBytes)
	}
	var header [HeaderBytes]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeAll(writer, header[:]); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if err := writeAll(writer, payload); err != nil {
		return fmt.Errorf("write frame payload: %w", err)
	}
	return nil
}

// ReadFrame reads one bounded big-endian length-prefixed message.
func ReadFrame(reader io.Reader, maximum int) ([]byte, error) {
	if maximum <= 0 || maximum > MaxFrameBytes {
		return nil, fmt.Errorf("invalid frame maximum %d", maximum)
	}
	var header [HeaderBytes]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, fmt.Errorf("read frame header: %w", err)
	}
	length := int(binary.BigEndian.Uint32(header[:]))
	if length > maximum {
		return nil, fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, length, maximum)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, fmt.Errorf("read frame payload: %w", err)
	}
	return payload, nil
}

func writeAll(writer io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return nil
}
