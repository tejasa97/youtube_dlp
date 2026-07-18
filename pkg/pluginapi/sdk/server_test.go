package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

type extractorFunc func(context.Context, pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error)

func (function extractorFunc) Extract(ctx context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
	return function(ctx, request)
}

type postprocessorFunc func(context.Context, pluginapi.PostprocessRequest) (pluginapi.PostprocessResponse, error)

func (function postprocessorFunc) Postprocess(ctx context.Context, request pluginapi.PostprocessRequest) (pluginapi.PostprocessResponse, error) {
	return function(ctx, request)
}

type providerFunc func(context.Context, pluginapi.ProviderRequest) (pluginapi.ProviderResponse, error)

func (function providerFunc) Provide(ctx context.Context, request pluginapi.ProviderRequest) (pluginapi.ProviderResponse, error) {
	return function(ctx, request)
}

func TestDeterministicV11TranscriptFixture(t *testing.T) {
	hostHello := pluginapi.Envelope{Type: "hello", Versions: []uint32{pluginapi.V1_1, pluginapi.V1_0}, ABI: &pluginapi.VersionRange{Minimum: pluginapi.V1_0, Maximum: pluginapi.V1_1}}
	request := pluginapi.Envelope{Type: "extract", Version: pluginapi.V1_1, ExtractRequest: &pluginapi.ExtractRequest{ID: "request-1", URL: "https://fixture.invalid/watch/1"}}
	assertTranscriptFixture(t, "transcript-v1.1.json", hostHello, request)
}

func TestDeterministicV10TranscriptFixture(t *testing.T) {
	hostHello := pluginapi.Envelope{Type: "hello", Versions: []uint32{pluginapi.V1_0}}
	request := pluginapi.Envelope{Type: "extract", Version: pluginapi.V1_0, ExtractRequest: &pluginapi.ExtractRequest{ID: "request-1", URL: "https://fixture.invalid/watch/1"}}
	assertTranscriptFixture(t, "transcript-v1.0.json", hostHello, request)
}

func assertTranscriptFixture(t *testing.T, name string, hostHello, request pluginapi.Envelope) {
	t.Helper()
	server := extractorServer(extractorFunc(func(_ context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
		return pluginapi.ExtractResponse{ID: request.ID, Metadata: map[string]any{"id": "fixture-1", "title": "SDK fixture", "webpage_url": request.URL}}, nil
	}))
	output, err := exchangeBuffer(server, hostHello, request)
	if err != nil {
		t.Fatal(err)
	}
	responses := readAll(t, output)
	if len(responses) != 2 || responses[0].Manifest == nil || responses[1].ExtractResponse == nil {
		t.Fatalf("responses = %#v", responses)
	}
	transcript := []transcriptFrame{
		{Direction: "host_to_plugin", Envelope: hostHello},
		{Direction: "plugin_to_host", Envelope: responses[0]},
		{Direction: "host_to_plugin", Envelope: request},
		{Direction: "plugin_to_host", Envelope: responses[1]},
	}
	actual, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	actual = append(actual, '\n')
	expected, err := os.ReadFile(fixturePath(name))
	if err != nil {
		t.Fatal(err)
	}
	expected = bytes.ReplaceAll(expected, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(actual, expected) {
		t.Fatalf("replace transcript fixture with:\n%s", actual)
	}
}

func TestHelloNegotiationV10V11AndPermutationProperty(t *testing.T) {
	rangeValue := pluginapi.VersionRange{Minimum: pluginapi.V1_0, Maximum: pluginapi.V1_1}
	for _, versions := range [][]uint32{{pluginapi.V1_0}, {pluginapi.V1_1, pluginapi.V1_0}, {pluginapi.V1_0, pluginapi.V1_1}} {
		hello := pluginapi.Envelope{Type: "hello", Versions: versions, ABI: &rangeValue}
		selected, err := negotiateHello(hello, rangeValue)
		expected := pluginapi.V1_0
		if len(versions) > 1 || versions[0] == pluginapi.V1_1 {
			expected = pluginapi.V1_1
		}
		if err != nil || selected != expected {
			t.Fatalf("versions %v selected %d, %v", versions, selected, err)
		}
	}
	if selected, err := negotiateHello(pluginapi.Envelope{Type: "hello", ABI: &rangeValue}, pluginapi.VersionRange{Minimum: pluginapi.V1_0, Maximum: pluginapi.V1_0}); err != nil || selected != pluginapi.V1_0 {
		t.Fatalf("range-only v1.0 selected %d, %v", selected, err)
	}
	unknown := pluginapi.Envelope{Type: "hello", Versions: []uint32{pluginapi.Version(2, 0)}}
	if _, err := negotiateHello(unknown, rangeValue); !errors.Is(err, ErrIncompatibleVersion) {
		t.Fatalf("unknown major error = %v", err)
	}
}

func TestExactCapabilityDispatchForAllInterfaces(t *testing.T) {
	tests := []struct {
		name     string
		server   Server
		request  pluginapi.Envelope
		response func(pluginapi.Envelope) bool
	}{
		{
			name: "extractor", server: extractorServer(extractorFunc(func(_ context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
				return pluginapi.ExtractResponse{ID: request.ID}, nil
			})), request: pluginapi.Envelope{Type: "extract", Version: pluginapi.V1_1, ExtractRequest: &pluginapi.ExtractRequest{ID: "e1", URL: "https://fixture.invalid/e"}},
			response: func(value pluginapi.Envelope) bool {
				return value.Type == "result" && value.ExtractResponse != nil && value.ExtractResponse.ID == "e1"
			},
		},
		{
			name: "postprocessor", server: serverFor(pluginapi.CapabilityPostprocessor), request: pluginapi.Envelope{Type: "postprocess", Version: pluginapi.V1_1, PostprocessRequest: &pluginapi.PostprocessRequest{ID: "p1", Operation: "tag", Input: pluginapi.Artifact{Handle: "artifact-1"}}},
			response: func(value pluginapi.Envelope) bool {
				return value.Type == "postprocess_result" && value.PostprocessResponse != nil && value.PostprocessResponse.ID == "p1"
			},
		},
		{
			name: "provider", server: serverFor(pluginapi.CapabilityProvider), request: pluginapi.Envelope{Type: "provide", Version: pluginapi.V1_1, ProviderRequest: &pluginapi.ProviderRequest{ID: "v1", Provider: "fixture", Action: "lookup", Secrets: []pluginapi.SecretHandle{{ID: "handle-1", Purpose: "login"}}}},
			response: func(value pluginapi.Envelope) bool {
				return value.Type == "provider_result" && value.ProviderResponse != nil && value.ProviderResponse.ID == "v1"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := exchangeBuffer(test.server, hostHello(), test.request)
			if err != nil {
				t.Fatal(err)
			}
			responses := readAll(t, output)
			if len(responses) != 2 || !test.response(responses[1]) {
				t.Fatalf("responses = %#v", responses)
			}
		})
	}
}

func TestRejectsHandlerDeclarationMismatchPythonAndInterpreter(t *testing.T) {
	server := extractorServer(extractorFunc(func(_ context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
		return pluginapi.ExtractResponse{ID: request.ID}, nil
	}))
	server.Extractor = nil
	if err := server.Serve(context.Background(), newReadCloser(nil), io.Discard); !errors.Is(err, ErrCapability) {
		t.Fatalf("missing handler error = %v", err)
	}
	server = extractorServer(extractorFunc(func(_ context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
		return pluginapi.ExtractResponse{ID: request.ID}, nil
	}))
	server.Manifest.Capabilities = []pluginapi.Capability{pluginapi.CapabilityProvider}
	if err := server.Serve(context.Background(), newReadCloser(nil), io.Discard); !errors.Is(err, ErrCapability) {
		t.Fatalf("undeclared handler error = %v", err)
	}
	for _, entrypoint := range []string{"plugin.py", "python3", "run.sh", "node"} {
		server = extractorServer(extractorFunc(func(_ context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
			return pluginapi.ExtractResponse{ID: request.ID}, nil
		}))
		server.Manifest.Entrypoint = entrypoint
		if err := server.Serve(context.Background(), newReadCloser(nil), io.Discard); !errors.Is(err, ErrPythonRuntime) {
			t.Fatalf("entrypoint %q error = %v", entrypoint, err)
		}
	}
	server.Manifest.Entrypoint = "fixture-plugin"
	server.Manifest.Runtime = pluginapi.Runtime("python")
	if err := server.Serve(context.Background(), newReadCloser(nil), io.Discard); !errors.Is(err, ErrPythonRuntime) {
		t.Fatalf("runtime error = %v", err)
	}
	server.Manifest.Runtime = pluginapi.RuntimeWASM
	if err := server.Serve(context.Background(), newReadCloser(nil), io.Discard); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("WASM RPC runtime error = %v", err)
	}
	server.Manifest.Runtime = pluginapi.RuntimeNative
	for _, entrypoint := range []string{"nested/plugin", `nested\plugin`, `C:\plugin.exe`} {
		server.Manifest.Entrypoint = entrypoint
		if err := server.Serve(context.Background(), newReadCloser(nil), io.Discard); !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("non-portable entrypoint %q error = %v", entrypoint, err)
		}
	}
}

func TestRequestVersionIDCapabilityAndOneOperationPolicy(t *testing.T) {
	server := extractorServer(extractorFunc(func(_ context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
		return pluginapi.ExtractResponse{ID: request.ID}, nil
	}))
	invalid := []pluginapi.Envelope{
		{Type: "extract", Version: pluginapi.V1_0, ExtractRequest: &pluginapi.ExtractRequest{ID: "id", URL: "https://fixture.invalid"}},
		{Type: "extract", Version: pluginapi.V1_1, ExtractRequest: &pluginapi.ExtractRequest{URL: "https://fixture.invalid"}},
		{Type: "provide", Version: pluginapi.V1_1, ProviderRequest: &pluginapi.ProviderRequest{ID: "id", Provider: "p", Action: "a"}},
	}
	for index, request := range invalid {
		if _, err := exchangeBuffer(server, hostHello(), request); err == nil || !(errors.Is(err, ErrProtocol) || errors.Is(err, ErrCapability)) {
			t.Fatalf("invalid request %d error = %v", index, err)
		}
	}

	started := make(chan struct{})
	server = extractorServer(extractorFunc(func(ctx context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
		close(started)
		<-ctx.Done()
		return pluginapi.ExtractResponse{}, ctx.Err()
	}))
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), inputReader, outputWriter) }()
	writeEnvelope(t, inputWriter, hostHello())
	readEnvelope(t, outputReader)
	first := pluginapi.Envelope{Type: "extract", Version: pluginapi.V1_1, ExtractRequest: &pluginapi.ExtractRequest{ID: "first", URL: "https://fixture.invalid/1"}}
	writeEnvelope(t, inputWriter, first)
	<-started
	writeEnvelope(t, inputWriter, first)
	select {
	case err := <-done:
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("second operation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second operation leaked or deadlocked")
	}
	_ = inputWriter.Close()
	_ = outputReader.Close()
}

func TestCancellationMessageStopsHandlerWithoutLeak(t *testing.T) {
	started := make(chan struct{})
	returned := make(chan struct{})
	server := extractorServer(extractorFunc(func(ctx context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
		close(started)
		<-ctx.Done()
		close(returned)
		return pluginapi.ExtractResponse{}, ctx.Err()
	}))
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), inputReader, outputWriter) }()
	writeEnvelope(t, inputWriter, hostHello())
	readEnvelope(t, outputReader)
	writeEnvelope(t, inputWriter, pluginapi.Envelope{Type: "extract", Version: pluginapi.V1_1, ExtractRequest: &pluginapi.ExtractRequest{ID: "cancel-1", URL: "https://fixture.invalid"}})
	<-started
	writeEnvelope(t, inputWriter, pluginapi.Envelope{Type: "cancel", RequestID: "cancel-1"})
	response := readEnvelope(t, outputReader)
	if response.ExtractResponse == nil || response.ExtractResponse.Error == nil || response.ExtractResponse.Error.Code != "canceled" {
		t.Fatalf("cancel response = %#v", response)
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("handler goroutine did not return")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = inputWriter.Close()
	_ = outputReader.Close()
}

func TestParentContextCancellationStopsHandlerWithoutLeak(t *testing.T) {
	started := make(chan struct{})
	returned := make(chan struct{})
	server := extractorServer(extractorFunc(func(ctx context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
		close(started)
		<-ctx.Done()
		close(returned)
		return pluginapi.ExtractResponse{}, ctx.Err()
	}))
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, inputReader, outputWriter) }()
	writeEnvelope(t, inputWriter, hostHello())
	readEnvelope(t, outputReader)
	writeEnvelope(t, inputWriter, pluginapi.Envelope{Type: "extract", Version: pluginapi.V1_1, ExtractRequest: &pluginapi.ExtractRequest{ID: "context-1", URL: "https://fixture.invalid"}})
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("context error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("context cancellation leaked or deadlocked")
	}
	select {
	case <-returned:
	default:
		t.Fatal("handler did not return before Serve")
	}
	_ = inputWriter.Close()
	_ = outputReader.Close()
}

func TestCategorizedFailuresAreSecretSafe(t *testing.T) {
	tests := []struct {
		name      string
		handler   extractorFunc
		category  pluginapi.RemoteCategory
		code      string
		forbidden string
	}{
		{"declared", func(context.Context, pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
			return pluginapi.ExtractResponse{}, &Failure{Category: pluginapi.RemoteNetwork, Code: "rate_limited", Message: "service rate limited", Retryable: true}
		}, pluginapi.RemoteNetwork, "rate_limited", ""},
		{"plain error", func(context.Context, pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
			return pluginapi.ExtractResponse{}, errors.New("token=raw-secret")
		}, pluginapi.RemoteInternal, "handler_error", "raw-secret"},
		{"invalid declared", func(context.Context, pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
			return pluginapi.ExtractResponse{}, &Failure{Category: pluginapi.RemoteNetwork, Code: "network", Message: "authorization=raw-secret"}
		}, pluginapi.RemoteInternal, "handler_error", "raw-secret"},
		{"secret response", func(_ context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
			return pluginapi.ExtractResponse{ID: request.ID, Metadata: map[string]any{"token": "raw-secret"}}, nil
		}, pluginapi.RemoteInternal, "invalid_response", "raw-secret"},
		{"panic", func(context.Context, pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
			panic("token=raw-secret")
		}, pluginapi.RemoteInternal, "handler_panic", "raw-secret"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := exchangeBuffer(extractorServer(test.handler), hostHello(), pluginapi.Envelope{Type: "extract", Version: pluginapi.V1_1, ExtractRequest: &pluginapi.ExtractRequest{ID: "failure-1", URL: "https://fixture.invalid"}})
			if err != nil {
				t.Fatal(err)
			}
			responses := readAll(t, output)
			remote := responses[1].ExtractResponse.Error
			if remote == nil || remote.Category != test.category || remote.Code != test.code {
				t.Fatalf("remote = %#v", remote)
			}
			encoded, _ := json.Marshal(responses[1])
			if test.forbidden != "" && strings.Contains(string(encoded), test.forbidden) {
				t.Fatalf("secret exposed in %s", encoded)
			}
		})
	}
}

func TestMalformedEOFFailuresAndWriteFailures(t *testing.T) {
	server := extractorServer(extractorFunc(func(_ context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
		return pluginapi.ExtractResponse{ID: request.ID}, nil
	}))
	duplicate := []byte(`{"type":"hello","type":"hello"}`)
	framed := append([]byte{0, 0, 0, byte(len(duplicate))}, duplicate...)
	if err := server.Serve(context.Background(), newReadCloser(framed), io.Discard); !errors.Is(err, ErrProtocol) {
		t.Fatalf("malformed error = %v", err)
	}
	if err := server.Serve(context.Background(), newReadCloser(nil), io.Discard); !errors.Is(err, ErrRead) {
		t.Fatalf("EOF error = %v", err)
	}
	var helloOnly bytes.Buffer
	writeEnvelope(t, &helloOnly, hostHello())
	if err := server.Serve(context.Background(), newReadCloser(helloOnly.Bytes()), io.Discard); !errors.Is(err, ErrRead) {
		t.Fatalf("operation EOF error = %v", err)
	}
	if err := server.Serve(context.Background(), newReadCloser(helloOnly.Bytes()), failingWriter{}); !errors.Is(err, ErrWrite) {
		t.Fatalf("hello write error = %v", err)
	}
	input := framedEnvelopes(t, hostHello(), pluginapi.Envelope{Type: "extract", Version: pluginapi.V1_1, ExtractRequest: &pluginapi.ExtractRequest{ID: "write-1", URL: "https://fixture.invalid"}})
	writer := &failAfterWriter{allowed: 2}
	if err := server.Serve(context.Background(), newReadCloser(input), writer); !errors.Is(err, ErrWrite) {
		t.Fatalf("result write error = %v", err)
	}
}

func TestPayloadResourceAndSecretBounds(t *testing.T) {
	server := extractorServer(extractorFunc(func(_ context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
		return pluginapi.ExtractResponse{ID: request.ID}, nil
	}))
	request := pluginapi.Envelope{Type: "extract", Version: pluginapi.V1_1, ExtractRequest: &pluginapi.ExtractRequest{ID: "bounded-1", URL: "https://fixture.invalid", Options: map[string]any{"access_token": "raw-secret"}}}
	if _, err := exchangeBuffer(server, hostHello(), request); !errors.Is(err, ErrSecretExposure) {
		t.Fatalf("secret payload error = %v", err)
	}
	deep := map[string]any{}
	cursor := deep
	for depth := 0; depth < 70; depth++ {
		next := map[string]any{}
		cursor["nested"] = next
		cursor = next
	}
	request.ExtractRequest.Options = deep
	if _, err := exchangeBuffer(server, hostHello(), request); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("deep payload error = %v", err)
	}
	tooManyVersions := hostHello()
	tooManyVersions.Versions = make([]uint32, 17)
	for index := range tooManyVersions.Versions {
		tooManyVersions.Versions[index] = pluginapi.V1_0
	}
	if _, err := exchangeBuffer(server, tooManyVersions, request); !errors.Is(err, ErrProtocol) {
		t.Fatalf("hello bound error = %v", err)
	}
	failure := &Failure{Category: pluginapi.RemoteInternal, Code: "private", Message: "safe", Cause: errors.New("token=raw-secret")}
	if strings.Contains(failure.Error(), "raw-secret") || !errors.Is(failure, failure.Cause) {
		t.Fatalf("failure exposed cause: %q", failure.Error())
	}
}

func FuzzServerMalformedInput(f *testing.F) {
	f.Add([]byte{})
	f.Add(framedEnvelopesForFuzz(pluginapi.Envelope{Type: "hello", Versions: []uint32{pluginapi.V1_1}}))
	server := extractorServer(extractorFunc(func(_ context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
		return pluginapi.ExtractResponse{ID: request.ID}, nil
	}))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = server.Serve(context.Background(), newReadCloser(data), io.Discard)
	})
}

type transcriptFrame struct {
	Direction string             `json:"direction"`
	Envelope  pluginapi.Envelope `json:"envelope"`
}

func extractorServer(extractor pluginapi.Extractor) Server {
	server := serverFor(pluginapi.CapabilityExtractor)
	server.Extractor = extractor
	return server
}

func serverFor(capability pluginapi.Capability) Server {
	server := Server{Manifest: manifestFor(capability), Codec: pluginapi.Codec{Maximum: 1 << 20}}
	switch capability {
	case pluginapi.CapabilityPostprocessor:
		server.Postprocessor = postprocessorFunc(func(_ context.Context, request pluginapi.PostprocessRequest) (pluginapi.PostprocessResponse, error) {
			return pluginapi.PostprocessResponse{ID: request.ID, Artifacts: []pluginapi.Artifact{{Handle: request.Input.Handle}}}, nil
		})
	case pluginapi.CapabilityProvider:
		server.Provider = providerFunc(func(_ context.Context, request pluginapi.ProviderRequest) (pluginapi.ProviderResponse, error) {
			return pluginapi.ProviderResponse{ID: request.ID, Values: map[string]any{"status": "ok"}}, nil
		})
	}
	return server
}

func manifestFor(capability pluginapi.Capability) pluginapi.Manifest {
	return pluginapi.Manifest{Schema: manifestSchema, ID: "fixture.sdk", Name: "SDK fixture", Release: "1.1.0", Runtime: pluginapi.RuntimeNative, Entrypoint: "fixture-plugin", ABIRange: pluginapi.VersionRange{Minimum: pluginapi.V1_0, Maximum: pluginapi.V1_1}, Capabilities: []pluginapi.Capability{capability}}
}

func hostHello() pluginapi.Envelope {
	rangeValue := pluginapi.VersionRange{Minimum: pluginapi.V1_0, Maximum: pluginapi.V1_1}
	return pluginapi.Envelope{Type: "hello", Versions: []uint32{pluginapi.V1_1, pluginapi.V1_0}, ABI: &rangeValue}
}

func exchangeBuffer(server Server, envelopes ...pluginapi.Envelope) ([]byte, error) {
	input := newReadCloser(framedEnvelopesForFuzz(envelopes...))
	var output bytes.Buffer
	err := server.Serve(context.Background(), input, &output)
	return output.Bytes(), err
}

func framedEnvelopes(t *testing.T, envelopes ...pluginapi.Envelope) []byte {
	t.Helper()
	var buffer bytes.Buffer
	for _, envelope := range envelopes {
		writeEnvelope(t, &buffer, envelope)
	}
	return buffer.Bytes()
}

func framedEnvelopesForFuzz(envelopes ...pluginapi.Envelope) []byte {
	var buffer bytes.Buffer
	for _, envelope := range envelopes {
		_ = (pluginapi.Codec{Maximum: 1 << 20}).Write(&buffer, envelope)
	}
	return buffer.Bytes()
}

func readAll(t *testing.T, encoded []byte) []pluginapi.Envelope {
	t.Helper()
	reader := bytes.NewReader(encoded)
	var result []pluginapi.Envelope
	for reader.Len() != 0 {
		result = append(result, readEnvelope(t, reader))
	}
	return result
}

func writeEnvelope(t *testing.T, writer io.Writer, envelope pluginapi.Envelope) {
	t.Helper()
	if err := (pluginapi.Codec{Maximum: 1 << 20}).Write(writer, envelope); err != nil {
		t.Fatal(err)
	}
}

func readEnvelope(t *testing.T, reader io.Reader) pluginapi.Envelope {
	t.Helper()
	envelope, err := (pluginapi.Codec{Maximum: 1 << 20}).Read(reader)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

type readCloser struct{ *bytes.Reader }

func newReadCloser(data []byte) *readCloser { return &readCloser{Reader: bytes.NewReader(data)} }
func (*readCloser) Close() error            { return nil }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed with token=raw-secret")
}

type failAfterWriter struct {
	mu      sync.Mutex
	calls   int
	allowed int
	buffer  bytes.Buffer
}

func (writer *failAfterWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.calls >= writer.allowed {
		return 0, errors.New("write failed")
	}
	writer.calls++
	return writer.buffer.Write(data)
}

func fixturePath(name string) string {
	return filepath.Join("..", "..", "..", "conformance", "plugin", "sdk-v1.1", name)
}
