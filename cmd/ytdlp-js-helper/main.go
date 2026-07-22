// Command ytdlp-js-helper runs the isolated JavaScript engine protocol.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"

	"github.com/ytdlp-go/ytdlp/internal/javascript/engine"
	"github.com/ytdlp-go/ytdlp/internal/javascript/protocol"
)

const memoryLimitEnvironment = "YTDLP_JS_MEMORY_BYTES"

type executor interface {
	Execute(context.Context, protocol.Request) protocol.Response
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("ytdlp-js-helper protocol=%d engine=%s@%s\n", protocol.Version, engine.Name, engine.PinnedRevision[:12])
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: ytdlp-js-helper [--version]")
		os.Exit(2)
	}
	debug.SetMemoryLimit(configuredMemoryLimit())
	if err := serve(os.Stdin, os.Stdout, engine.New(engine.DefaultCacheSize)); err != nil {
		fmt.Fprintln(os.Stderr, "ytdlp-js-helper: protocol terminated")
		os.Exit(1)
	}
}

func serve(reader io.Reader, writer io.Writer, runtime executor) error {
	for {
		payload, err := protocol.ReadFrame(reader, protocol.MaxFrameBytes)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		request, err := decodeRequest(payload)
		if err != nil {
			if err := writeResponse(writer, protocol.FailureResponse("", protocol.CodeProtocol, errors.New("malformed helper request"))); err != nil {
				return err
			}
			continue
		}
		response := executeSafely(runtime, request)
		if err := writeResponse(writer, response); err != nil {
			return err
		}
	}
}

func decodeRequest(payload []byte) (protocol.Request, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var request protocol.Request
	if err := decoder.Decode(&request); err != nil {
		return protocol.Request{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return protocol.Request{}, errors.New("multiple JSON values")
		}
		return protocol.Request{}, err
	}
	return request, nil
}

func executeSafely(runtime executor, request protocol.Request) (response protocol.Response) {
	defer func() {
		if recover() != nil {
			response = protocol.FailureResponse(request.ID, protocol.CodeHelperCrash, errors.New("JavaScript engine panicked"))
		}
	}()
	// The helper validates wall time against TrustedWallTimeMS when the
	// supervisor has explicitly granted an extended ceiling (EJS preprocessing).
	// Requests without this field are bounded at HardMaxWallTime (30 s).
	// The helper never originates TrustedWallTimeMS itself.
	return runtime.Execute(context.Background(), request)
}

func writeResponse(writer io.Writer, response protocol.Response) error {
	payload, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	return protocol.WriteFrame(writer, payload)
}

func configuredMemoryLimit() int64 {
	value := os.Getenv(memoryLimitEnvironment)
	if value == "" {
		return protocol.DefaultMemoryBytes
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || parsed > protocol.HardMaxMemoryBytes {
		return protocol.DefaultMemoryBytes
	}
	return parsed
}
