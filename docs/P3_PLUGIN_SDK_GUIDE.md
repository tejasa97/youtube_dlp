# Go plugin SDK author guide

The author SDK in `pkg/pluginapi/sdk` turns the existing public
`pluginapi.Extractor`, `Postprocessor`, and `Provider` interfaces into a bounded
stdio RPC server. It supports ABI v1.0 and v1.1, uses the public
length-prefixed `pluginapi.Codec`, and has no Python, CGO, process-launch,
discovery, environment, filesystem, network, or secret-store dependency.
Importing the package performs no work.

## Minimal extractor

```go
package main

import (
    "context"
    "os"

    "github.com/ytdlp-go/ytdlp/pkg/pluginapi"
    "github.com/ytdlp-go/ytdlp/pkg/pluginapi/sdk"
)

type extractor struct{}

func (extractor) Extract(_ context.Context, request pluginapi.ExtractRequest) (pluginapi.ExtractResponse, error) {
    return pluginapi.ExtractResponse{
        ID: request.ID,
        Metadata: map[string]any{
            "id": "example",
            "title": "Example",
            "webpage_url": request.URL,
        },
    }, nil
}

func main() {
    server := sdk.Server{
        Manifest: pluginapi.Manifest{
            Schema: "ytdlp-go.plugin/v1",
            ID: "example.extractor",
            Name: "Example extractor",
            Release: "1.0.0",
            Runtime: pluginapi.RuntimeNative,
            Entrypoint: "example-extractor",
            ABIRange: pluginapi.VersionRange{Minimum: pluginapi.V1_0, Maximum: pluginapi.V1_1},
            Capabilities: []pluginapi.Capability{pluginapi.CapabilityExtractor},
        },
        Extractor: extractor{},
    }
    if err := server.Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
        os.Exit(2)
    }
}
```

Declare exactly the capabilities you implement. A declared capability without
a handler, or a handler without its declaration, is rejected before protocol
I/O. The server returns the exact validated manifest during hello and dispatches
only the negotiated version and declared operation.

## Failures and cancellation

Handlers should check their context during blocking work. The host sends a
`cancel` envelope for the active request; the SDK cancels the handler, waits for
it to return, emits a categorized cancellation result when possible, and does
not detach operation or reader goroutines.

Return `*sdk.Failure` when the host should receive a public category:

```go
return pluginapi.ExtractResponse{}, &sdk.Failure{
    Category: pluginapi.RemoteNetwork,
    Code: "rate_limited",
    Message: "service rate limited",
    Retryable: true,
    Cause: err, // retained locally and never serialized
}
```

Plain Go error text is never sent to the host. Invalid categories, codes,
control characters, secret-shaped diagnostics, mismatched response IDs, and
secret-bearing result maps become a generic `internal` failure. Secrets must
remain host-managed opaque `SecretHandle` values; do not place credentials in
ordinary options, metadata, arguments, values, failure messages, logs, or the
environment.

## Contract limits and deviations

- One `Serve` call handles one hello and one operation because the current host
  launches and reaps one plugin process per operation. Concurrent or queued
  operations are intentionally rejected.
- `Serve` takes ownership of its `io.ReadCloser`. `Close` must interrupt a
  concurrent `Read`; `os.Stdin` and `io.PipeReader` satisfy the intended use.
- A handler that ignores context can delay cooperative shutdown. The host's
  existing process timeout and termination remain the external hard stop.
- Manifest permissions are declarations for host review, not grants. This SDK
  never opens files, resolves secret handles, reads environment variables, or
  performs network access for a handler.
- This stdio server accepts only the native RPC runtime. WASM plugins use the
  separate constrained export ABI and host; treating a WASM declaration as
  native RPC would be an invalid configuration. Python runtimes, Python files,
  shell scripts, and interpreter entrypoints are rejected.

## Evidence

Automated tests cover v1.0/v1.1 negotiation, version and request-ID checks,
exact dispatch for all three interfaces, cancellation, one-operation policy,
malformed frames, EOF and write failures, payload/resource bounds, categorized
secret-safe failures, Python/interpreter rejection, transcript determinism,
fuzzing, and race detection. Fixture hashes and derivation are recorded in
`conformance/plugin/sdk-v1.1/PROVENANCE.md`.
