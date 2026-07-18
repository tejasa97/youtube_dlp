// ytdlp-plugin-rpc-example is a deterministic example plugin for the P1 RPC spike.
package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
)

const maximum = 1 << 20

type message struct {
	Type     string    `json:"type"`
	Versions []uint32  `json:"versions,omitempty"`
	Manifest *manifest `json:"manifest,omitempty"`
	Version  uint32    `json:"version,omitempty"`
	Request  *request  `json:"request,omitempty"`
	Response *response `json:"response,omitempty"`
}
type manifest struct {
	Name     string   `json:"name"`
	Versions []uint32 `json:"versions"`
}
type request struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}
type response struct {
	ID       string         `json:"id"`
	Metadata map[string]any `json:"metadata"`
}

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		os.Exit(2)
	}
}

func run(stdin io.Reader, stdout io.Writer) error {
	var hello message
	if err := read(stdin, &hello); err != nil || hello.Type != "hello" {
		return errors.New("expected hello")
	}
	if err := write(stdout, message{Type: "hello", Manifest: &manifest{Name: "example", Versions: []uint32{1}}}); err != nil {
		return err
	}
	var extract message
	if err := read(stdin, &extract); err != nil || extract.Type != "extract" || extract.Request == nil || extract.Version != 1 {
		return errors.New("expected extract")
	}
	result := &response{ID: extract.Request.ID, Metadata: map[string]any{
		"id": "rpc-example", "title": "RPC example", "webpage_url": extract.Request.URL,
	}}
	return write(stdout, message{Type: "result", Response: result})
}

func read(source io.Reader, destination any) error {
	var header [4]byte
	if _, err := io.ReadFull(source, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maximum {
		return io.ErrUnexpectedEOF
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(source, payload); err != nil {
		return err
	}
	return json.Unmarshal(payload, destination)
}

func write(destination io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := destination.Write(header[:]); err != nil {
		return err
	}
	_, err = destination.Write(payload)
	return err
}
