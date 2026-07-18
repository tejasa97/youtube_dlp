package pluginapi

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var (
	ErrMalformedFrame = errors.New("malformed plugin ABI frame")
	ErrFrameTooLarge  = errors.New("plugin ABI frame exceeds limit")
)

// Envelope is the v1 stdio wire union. Exactly one request or response field
// is present for an operation message.
type Envelope struct {
	Type                string               `json:"type"`
	Versions            []uint32             `json:"versions,omitempty"`
	ABI                 *VersionRange        `json:"abi,omitempty"`
	Version             uint32               `json:"version,omitempty"`
	Manifest            *Manifest            `json:"manifest,omitempty"`
	ExtractRequest      *ExtractRequest      `json:"request,omitempty"`
	ExtractResponse     *ExtractResponse     `json:"response,omitempty"`
	PostprocessRequest  *PostprocessRequest  `json:"postprocess_request,omitempty"`
	PostprocessResponse *PostprocessResponse `json:"postprocess_response,omitempty"`
	ProviderRequest     *ProviderRequest     `json:"provider_request,omitempty"`
	ProviderResponse    *ProviderResponse    `json:"provider_response,omitempty"`
	RequestID           string               `json:"request_id,omitempty"`
}

type Codec struct {
	Maximum uint32
}

func (codec Codec) maximum() uint32 {
	if codec.Maximum == 0 {
		return 1 << 20
	}
	return codec.Maximum
}

func (codec Codec) Write(writer io.Writer, value Envelope) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: encode: %v", ErrMalformedFrame, err)
	}
	maximum := codec.maximum()
	if len(payload) == 0 || uint64(len(payload)) > uint64(maximum) {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(payload))
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeAll(writer, header[:]); err != nil {
		return err
	}
	return writeAll(writer, payload)
}

func (codec Codec) Read(reader io.Reader) (Envelope, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return Envelope{}, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 {
		return Envelope{}, fmt.Errorf("%w: empty", ErrMalformedFrame)
	}
	if size > codec.maximum() {
		return Envelope{}, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, size)
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return Envelope{}, fmt.Errorf("%w: truncated", ErrMalformedFrame)
	}
	var result Envelope
	decoder := json.NewDecoder(bytesReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return Envelope{}, fmt.Errorf("%w: decode: %v", ErrMalformedFrame, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Envelope{}, fmt.Errorf("%w: trailing JSON", ErrMalformedFrame)
	}
	return result, nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) != 0 {
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

type sliceReader struct {
	data []byte
	off  int
}

func bytesReader(data []byte) *sliceReader { return &sliceReader{data: data} }

func (reader *sliceReader) Read(target []byte) (int, error) {
	if reader.off == len(reader.data) {
		return 0, io.EOF
	}
	written := copy(target, reader.data[reader.off:])
	reader.off += written
	return written, nil
}
