package rpc

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ytdlp-go/ytdlp/internal/plugin"
)

type envelope struct {
	Type      string                  `json:"type"`
	Versions  []uint32                `json:"versions,omitempty"`
	Version   uint32                  `json:"version,omitempty"`
	Manifest  *plugin.Manifest        `json:"manifest,omitempty"`
	Request   *plugin.ExtractRequest  `json:"request,omitempty"`
	Response  *plugin.ExtractResponse `json:"response,omitempty"`
	RequestID string                  `json:"request_id,omitempty"`
}

func writeFrame(w io.Writer, value any, maximum uint32) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", plugin.ErrMalformedMessage, err)
	}
	if uint64(len(payload)) > uint64(maximum) {
		return fmt.Errorf("%w: message is %d bytes", plugin.ErrResourceLimit, len(payload))
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeAll(w, header[:]); err != nil {
		return err
	}
	return writeAll(w, payload)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func readFrame(r io.Reader, maximum uint32, destination any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 {
		return fmt.Errorf("%w: empty frame", plugin.ErrMalformedMessage)
	}
	if size > maximum {
		return fmt.Errorf("%w: message declares %d bytes", plugin.ErrResourceLimit, size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return fmt.Errorf("%w: truncated frame", plugin.ErrMalformedMessage)
	}
	decoder := json.NewDecoder(bytesReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: %v", plugin.ErrMalformedMessage, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON", plugin.ErrMalformedMessage)
	}
	return nil
}

type byteReader struct {
	data []byte
	off  int
}

func bytesReader(data []byte) *byteReader { return &byteReader{data: data} }

func (r *byteReader) Read(target []byte) (int, error) {
	if r.off == len(r.data) {
		return 0, io.EOF
	}
	n := copy(target, r.data[r.off:])
	r.off += n
	return n, nil
}
