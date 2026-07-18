// Package sdk provides the author-side server for pluginapi's length-prefixed
// RPC protocol. Importing it has no side effects: it performs no discovery,
// process execution, environment access, or I/O until Server.Serve is called.
package sdk

import (
	"context"
	"errors"
	"io"

	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

var (
	ErrInvalidConfig       = errors.New("invalid plugin SDK configuration")
	ErrInvalidManifest     = errors.New("invalid plugin SDK manifest")
	ErrIncompatibleVersion = errors.New("incompatible plugin ABI version")
	ErrProtocol            = errors.New("invalid plugin RPC message")
	ErrCapability          = errors.New("undeclared plugin capability")
	ErrPythonRuntime       = errors.New("Python or interpreter plugin runtime prohibited")
	ErrResourceLimit       = errors.New("plugin SDK resource limit exceeded")
	ErrSecretExposure      = errors.New("plugin SDK secret exposure prohibited")
	ErrRead                = errors.New("plugin RPC read failed")
	ErrWrite               = errors.New("plugin RPC write failed")
)

// Server exposes exactly the handlers declared by Manifest. The existing host
// starts one process for one operation, so Serve negotiates one hello and one
// operation. A second operation is rejected instead of queued concurrently.
type Server struct {
	Manifest      pluginapi.Manifest
	Extractor     pluginapi.Extractor
	Postprocessor pluginapi.Postprocessor
	Provider      pluginapi.Provider
	Codec         pluginapi.Codec
}

type operationResult struct {
	envelope pluginapi.Envelope
}

// Serve handles one host exchange. Input ownership transfers to Serve: Close
// must interrupt a concurrent Read. This lets completion and cancellation
// unblock the protocol loop without a detached reader goroutine.
func (server Server) Serve(ctx context.Context, input io.ReadCloser, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if input == nil || output == nil {
		return ErrInvalidConfig
	}
	if err := validateServer(server); err != nil {
		return err
	}
	defer input.Close()

	hello, err := server.Codec.Read(input)
	if err != nil {
		return readError(err)
	}
	version, err := negotiateHello(hello, manifestRange(server.Manifest))
	if err != nil {
		return err
	}
	manifest := cloneManifest(server.Manifest)
	if err := server.write(output, pluginapi.Envelope{Type: "hello", Manifest: &manifest}); err != nil {
		return err
	}

	request, err := server.Codec.Read(input)
	if err != nil {
		return readError(err)
	}
	requestID, err := validateOperation(request, version, server.Manifest)
	if err != nil {
		return err
	}

	operationCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	resultChannel := make(chan operationResult, 1)
	go func() {
		result := operationResult{}
		defer func() {
			if recover() != nil {
				// Panic values and stacks may contain credentials or request data;
				// neither is serialized by the SDK.
				result.envelope = failureEnvelope(request, requestID, internalFailure("handler_panic"))
			}
			resultChannel <- result
			// The result is published before Close, so Serve can always distinguish
			// operation completion from an unrelated transport failure.
			_ = input.Close()
		}()
		result.envelope = server.dispatch(operationCtx, request, requestID)
	}()
	stopContextClose := context.AfterFunc(ctx, func() {
		cancel()
		_ = input.Close()
	})
	defer stopContextClose()

	next, readErr := server.Codec.Read(input)
	if readErr == nil {
		if err := validateCancel(next, requestID); err != nil {
			cancel()
			<-resultChannel
			return err
		}
		cancel()
		<-resultChannel
		return server.write(output, canceledEnvelope(request, requestID))
	}

	if err := ctx.Err(); err != nil {
		cancel()
		<-resultChannel
		return err
	}
	select {
	case result := <-resultChannel:
		return server.write(output, result.envelope)
	default:
	}
	if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrClosedPipe) {
		cancel()
		<-resultChannel
		return readError(readErr)
	}
	// Finite test transports can reach EOF while the handler is completing.
	// Waiting here also guarantees that no operation goroutine escapes Serve.
	result := <-resultChannel
	return server.write(output, result.envelope)
}

func (server Server) dispatch(ctx context.Context, request pluginapi.Envelope, requestID string) pluginapi.Envelope {
	switch request.Type {
	case "extract":
		response, err := server.Extractor.Extract(ctx, *request.ExtractRequest)
		if err != nil {
			response = pluginapi.ExtractResponse{ID: requestID, Error: remoteError(err)}
		} else if validateExtractResponse(response, requestID) != nil {
			response = pluginapi.ExtractResponse{ID: requestID, Error: internalFailure("invalid_response")}
		}
		return pluginapi.Envelope{Type: "result", ExtractResponse: &response}
	case "postprocess":
		response, err := server.Postprocessor.Postprocess(ctx, *request.PostprocessRequest)
		if err != nil {
			response = pluginapi.PostprocessResponse{ID: requestID, Error: remoteError(err)}
		} else if validatePostprocessResponse(response, requestID) != nil {
			response = pluginapi.PostprocessResponse{ID: requestID, Error: internalFailure("invalid_response")}
		}
		return pluginapi.Envelope{Type: "postprocess_result", PostprocessResponse: &response}
	case "provide":
		response, err := server.Provider.Provide(ctx, *request.ProviderRequest)
		if err != nil {
			response = pluginapi.ProviderResponse{ID: requestID, Error: remoteError(err)}
		} else if validateProviderResponse(response, requestID) != nil {
			response = pluginapi.ProviderResponse{ID: requestID, Error: internalFailure("invalid_response")}
		}
		return pluginapi.Envelope{Type: "provider_result", ProviderResponse: &response}
	default:
		panic("validated operation type")
	}
}

func (server Server) write(output io.Writer, envelope pluginapi.Envelope) error {
	if err := server.Codec.Write(output, envelope); err != nil {
		return ErrWrite
	}
	return nil
}

func readError(err error) error {
	if errors.Is(err, pluginapi.ErrMalformedFrame) || errors.Is(err, pluginapi.ErrFrameTooLarge) {
		return ErrProtocol
	}
	return ErrRead
}

func canceledEnvelope(request pluginapi.Envelope, requestID string) pluginapi.Envelope {
	failure := &pluginapi.RemoteError{Category: pluginapi.RemoteUnavailable, Code: "canceled", Message: "plugin operation canceled", Retryable: true}
	return failureEnvelope(request, requestID, failure)
}

func failureEnvelope(request pluginapi.Envelope, requestID string, failure *pluginapi.RemoteError) pluginapi.Envelope {
	switch request.Type {
	case "extract":
		return pluginapi.Envelope{Type: "result", ExtractResponse: &pluginapi.ExtractResponse{ID: requestID, Error: failure}}
	case "postprocess":
		return pluginapi.Envelope{Type: "postprocess_result", PostprocessResponse: &pluginapi.PostprocessResponse{ID: requestID, Error: failure}}
	default:
		return pluginapi.Envelope{Type: "provider_result", ProviderResponse: &pluginapi.ProviderResponse{ID: requestID, Error: failure}}
	}
}
