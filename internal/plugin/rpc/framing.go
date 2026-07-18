package rpc

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ytdlp-go/ytdlp/internal/plugin"
	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

type envelope struct {
	Type                string                      `json:"type"`
	Versions            []uint32                    `json:"versions,omitempty"`
	ABIRange            *plugin.VersionRange        `json:"abi,omitempty"`
	Version             uint32                      `json:"version,omitempty"`
	Manifest            *plugin.Manifest            `json:"manifest,omitempty"`
	Request             *plugin.ExtractRequest      `json:"request,omitempty"`
	Response            *plugin.ExtractResponse     `json:"response,omitempty"`
	PostprocessRequest  *plugin.PostprocessRequest  `json:"postprocess_request,omitempty"`
	PostprocessResponse *plugin.PostprocessResponse `json:"postprocess_response,omitempty"`
	ProviderRequest     *plugin.ProviderRequest     `json:"provider_request,omitempty"`
	ProviderResponse    *plugin.ProviderResponse    `json:"provider_response,omitempty"`
	RequestID           string                      `json:"request_id,omitempty"`
}

func validatePluginHello(value envelope) error {
	if value.Type != "hello" || value.Manifest == nil || len(value.Versions) != 0 || value.ABIRange != nil ||
		value.Version != 0 || value.RequestID != "" ||
		value.Request != nil || value.Response != nil || value.PostprocessRequest != nil ||
		value.PostprocessResponse != nil || value.ProviderRequest != nil || value.ProviderResponse != nil {
		return fmt.Errorf("%w: invalid plugin hello union", plugin.ErrMalformedMessage)
	}
	return nil
}

func validateResultEnvelope(value envelope, expected string) error {
	if value.Type != expected || value.Manifest != nil || len(value.Versions) != 0 || value.ABIRange != nil ||
		value.Version != 0 || value.RequestID != "" || value.Request != nil ||
		value.PostprocessRequest != nil || value.ProviderRequest != nil {
		return fmt.Errorf("%w: invalid result union", plugin.ErrMalformedMessage)
	}
	present := 0
	for _, exists := range []bool{value.Response != nil, value.PostprocessResponse != nil, value.ProviderResponse != nil} {
		if exists {
			present++
		}
	}
	if present != 1 || expected == "result" && value.Response == nil ||
		expected == "postprocess_result" && value.PostprocessResponse == nil ||
		expected == "provider_result" && value.ProviderResponse == nil {
		return fmt.Errorf("%w: mismatched result union", plugin.ErrMalformedMessage)
	}
	return nil
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
	if err := pluginapi.ValidateJSONFrame(payload); err != nil {
		return fmt.Errorf("%w: %v", plugin.ErrMalformedMessage, err)
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
