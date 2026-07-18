// ytdlp-plugin-rpc-example is a deterministic Plugin ABI v1 SDK example.
package main

import (
	"errors"
	"io"
	"os"

	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

const maximum = 1 << 20

type message = pluginapi.Envelope
type manifest = pluginapi.Manifest
type request = pluginapi.ExtractRequest
type response = pluginapi.ExtractResponse

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
	pluginManifest := &manifest{
		Schema: "ytdlp-go.plugin/v1", ID: "example.rpc", Name: "RPC example", Release: "1.0.0",
		Runtime: pluginapi.RuntimeNative, Entrypoint: "ytdlp-plugin-rpc-example",
		ABIRange:     pluginapi.VersionRange{Minimum: pluginapi.V1_0, Maximum: pluginapi.V1_1},
		Capabilities: []pluginapi.Capability{pluginapi.CapabilityExtractor},
	}
	if err := write(stdout, message{Type: "hello", Manifest: pluginManifest}); err != nil {
		return err
	}
	var extract message
	if err := read(stdin, &extract); err != nil || extract.Type != "extract" || extract.ExtractRequest == nil ||
		extract.Version < pluginapi.V1_0 || extract.Version > pluginapi.V1_1 {
		return errors.New("expected compatible extract")
	}
	result := &response{ID: extract.ExtractRequest.ID, Metadata: map[string]any{
		"id": "rpc-example", "title": "RPC example", "webpage_url": extract.ExtractRequest.URL,
	}}
	return write(stdout, message{Type: "result", ExtractResponse: result})
}

func read(source io.Reader, destination *message) error {
	value, err := (pluginapi.Codec{Maximum: maximum}).Read(source)
	if err != nil {
		return err
	}
	*destination = value
	return nil
}

func write(destination io.Writer, value message) error {
	return (pluginapi.Codec{Maximum: maximum}).Write(destination, value)
}
