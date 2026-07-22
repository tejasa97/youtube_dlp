# Embedding ytdlp-go

The supported Go package contract is `github.com/ytdlp-go/ytdlp/pkg/ytdlp`.
The current contract version is v1alpha1. It is context-aware, safe for
concurrent independent operations, and returns categorized errors rather than
requiring callers to inspect diagnostic text.

## Module availability

The repository is currently hosted at `github.com/tejasa97/youtube_dlp`, but
`go.mod` declares the intended canonical module path
`github.com/ytdlp-go/ytdlp`. Until the repository location and module path are
reconciled, consumers should not publish a dependency that assumes
`go get github.com/ytdlp-go/ytdlp` works.

To compile an application against a local checkout for evaluation, declare the
intended module and replace it locally:

    require github.com/ytdlp-go/ytdlp v0.0.0

    replace github.com/ytdlp-go/ytdlp => /absolute/path/to/youtube_dlp

This workaround is for local development only. A tagged public module requires
canonical hosting, or an intentional module-path migration, before release.

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

## YouTube PO-token providers

Protected YouTube recovery can use an explicit native Go provider chain:

    client := ytdlp.NewClient(ytdlp.WithYouTubePOTProviders(ytdlp.YouTubePOTConfig{
        Policy: ytdlp.YouTubePOTFetchAlways,
        Providers: []ytdlp.YouTubePOTProvider{
            ytdlp.YouTubePOTProviderFunc{
                ProviderName: "application-provider",
                Function: func(ctx context.Context, request ytdlp.YouTubePOTRequest) (ytdlp.YouTubePOTResponse, error) {
                    return resolveApplicationToken(ctx, request)
                },
            },
        },
    }))

Providers are trusted in-process code and must honor context cancellation.
Tokens must be base64url values and should never be logged. The package bounds
provider count, request fields, token size, expiry, and its process-local cache;
it has no built-in token service, executable, or Python fallback. See [native
YouTube PO-token evidence](YOUTUBE_POT_EVIDENCE.md) for the exact boundary.

YouTube manual and automatic captions are included in normalized metadata.
Set `Request.YouTubeTranslatedCaptions` to additionally generate translated
manual-caption entries; automatic-caption translations follow the bounded
player renderer. See [YouTube captions evidence](YOUTUBE_CAPTIONS_EVIDENCE.md).

Set `Request.Subtitles` to select and write manual or automatic caption
sidecars. Language rules accept ordered, bounded RE2 expressions and `all`;
format preferences are slash-separated. Sidecars are reported as subtitle
artifacts and can still be written when `SkipDownload` is true. See [YouTube
subtitle CLI evidence](YOUTUBE_SUBTITLE_CLI_EVIDENCE.md).

## Updater

OpenUpdater accepts caller-owned threshold trust and an explicit health
checker. Transport remains outside the API: callers obtain signed metadata and
artifact bytes, then call Apply. Trust-on-first-use, production key generation,
and publisher selection are deliberately absent.

See [API compatibility policy](P2_API_COMPATIBILITY_POLICY.md), [Plugin ABI
v1](P2_PLUGIN_ABI_V1.md), [privacy-safe telemetry](P3_TELEMETRY.md), and [Updater and releases](P2_UPDATER_RELEASES.md) for
the complete stability and security boundaries.
