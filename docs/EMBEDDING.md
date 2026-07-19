# Embedding ytdlp-go

The supported Go package is github.com/ytdlp-go/ytdlp/pkg/ytdlp. The current
contract version is v1alpha1. It is context-aware, safe for concurrent
independent operations, and returns categorized errors rather than requiring
callers to inspect diagnostic text.

## Minimal metadata extraction

    package main

    import (
        "context"
        "fmt"
        "log"

        "github.com/ytdlp-go/ytdlp/pkg/ytdlp"
    )

    func main() {
        client := ytdlp.NewClient()
        result, err := client.Run(context.Background(), ytdlp.Request{
            URL:          "https://example.invalid/video",
            SkipDownload: true,
        })
        if err != nil {
            log.Fatal(err)
        }
        fmt.Println(string(result.InfoJSON))
    }

The example.invalid URL illustrates the API shape; use a URL handled by a
registered extractor in real code.

## Events and cancellation

    client := ytdlp.NewClient(
        ytdlp.WithEventHandler(func(ctx context.Context, event ytdlp.Event) error {
            fmt.Printf("%s %d/%d\n", event.Kind, event.Bytes, event.Total)
            return nil
        }),
    )

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    result, err := client.Run(ctx, ytdlp.Request{
        URL:       rawURL,
        OutputDir: "downloads",
    })

Cancellation reaches transport, playlist iteration, fragment downloads,
helpers, plugins, external tools, archives, caches, and updater operations.
The Client is stateless between operations. A shared event handler must provide
its own synchronization.

## Error handling

    if ytdlp.IsCategory(err, ytdlp.ErrorAuthentication) {
        // Ask the user for an explicitly scoped cookie source.
    }
    if ytdlp.IsCategory(err, ytdlp.ErrorUnsupported) {
        // The URL or requested behavior is outside the declared corpus.
    }
    if errors.Is(err, context.Canceled) {
        // Cancellation is also categorized as ErrorCancelled.
    }

Public categories are unsupported, authentication, invalid_input, network,
security, cancelled, and internal. Messages are diagnostic and may change;
category values are the compatibility boundary.

## Request and result model

Request exposes output confinement, proxy, impersonation, cookie, and native
netrc sources, timeout,
overwrite and metadata-only operation, format selection/sorting, templates,
filters, metadata transforms, bounded downloader controls, typed
postprocessors, archive/cache locations, and explicit plugin selection.

Result returns normalized InfoJSON, extractor identity, download/archive/skip
state, filename and byte count, ordered playlist entries, and typed artifacts.
Unknown ordered metadata is preserved in the normalized envelope.

Postprocessor is a tagged union. Exactly one operation must be selected per
step. The CLI exposes audio extraction and remuxing; the Go API additionally
exposes subtitle/thumbnail conversion, metadata/chapters/thumbnail/subtitle
embedding, fixups, concat, and safe moves.

## JavaScript and plugins

Use ytdlp.WithJavaScriptHelper to select the isolated pure-Go helper when it is
not beside the main executable. PATH is deliberately not searched.

Telemetry is disabled unless the caller constructs a bounded
ytdlp.TelemetryCollector and passes it with ytdlp.WithTelemetryCollector. The
collector accepts only fixed dimensions and can export deterministic snapshots;
it has no URL, arbitrary-label, error-text, or network-export API.

Signed plugins require InstallPluginPack or RollbackPluginPack to produce an
opaque InstalledPlugin. Pass it with ytdlp.WithInstalledPlugins and provide an
identity-bound permission approver. Request.PluginID must explicitly select the
plugin; plugins do not participate in automatic URL routing.

## Updater

OpenUpdater accepts caller-owned threshold trust and an explicit health
checker. Transport remains outside the API: callers obtain signed metadata and
artifact bytes, then call Apply. Trust-on-first-use, production key generation,
and publisher selection are deliberately absent.

See [API compatibility policy](P2_API_COMPATIBILITY_POLICY.md), [Plugin ABI
v1](P2_PLUGIN_ABI_V1.md), [privacy-safe telemetry](P3_TELEMETRY.md), and [Updater and releases](P2_UPDATER_RELEASES.md) for
the complete stability and security boundaries.
