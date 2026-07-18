package sdk

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

type exampleExtractor struct{}

func (exampleExtractor) Extract(_ context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
	return pluginapi.ExtractResponse{ID: request.ID, Metadata: map[string]any{"title": "Example"}}, nil
}

func ExampleServer() {
	codec := pluginapi.Codec{Maximum: 1 << 20}
	var input bytes.Buffer
	_ = codec.Write(&input, pluginapi.Envelope{Type: "hello", Versions: []uint32{pluginapi.V1_1, pluginapi.V1_0}})
	_ = codec.Write(&input, pluginapi.Envelope{Type: "extract", Version: pluginapi.V1_1, ExtractRequest: &pluginapi.ExtractRequest{ID: "example-1", URL: "https://example.invalid/video"}})

	server := Server{
		Manifest: pluginapi.Manifest{
			Schema: manifestSchema, ID: "example.extractor", Name: "Example extractor", Release: "1.0.0",
			Runtime: pluginapi.RuntimeNative, Entrypoint: "example-extractor",
			ABIRange:     pluginapi.VersionRange{Minimum: pluginapi.V1_0, Maximum: pluginapi.V1_1},
			Capabilities: []pluginapi.Capability{pluginapi.CapabilityExtractor},
		},
		Extractor: exampleExtractor{}, Codec: codec,
	}
	var output bytes.Buffer
	if err := server.Serve(context.Background(), io.NopCloser(&input), &output); err != nil {
		fmt.Println("error")
		return
	}
	hello, _ := codec.Read(&output)
	result, _ := codec.Read(&output)
	fmt.Println(hello.Manifest.ID, result.ExtractResponse.ID, result.ExtractResponse.Metadata["title"])
	// Output: example.extractor example-1 Example
}
