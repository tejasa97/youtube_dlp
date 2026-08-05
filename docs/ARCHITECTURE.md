# Architecture Overview

`ytdlp-go` has one native media engine with three public interfaces:

```text
YTDLP Go Desktop       ytdlp-go CLI       Go application
        │                    │                  │
        └────────────────────┴──────────────────┘
                             │
                         pkg/ytdlp
                             │
        ┌────────────────────┼────────────────────┐
        │                    │                    │
  extractor registry   operation policy    structured events
        │                    │                    │
        └──────────────► selection ◄──────────────┘
                             │
               direct / HLS / DASH / ISM
                             │
                  typed FFmpeg operations
                             │
                 confined output artifacts
```

The interfaces share operation behavior rather than reimplementing extraction
or download logic independently.

## Public interfaces

### Desktop

The Wails Desktop application owns UI-specific concerns:

- URL validation for the narrower Desktop product scope;
- settings and download-history persistence;
- FIFO queue lifecycle;
- folder selection and native file actions;
- FFmpeg detection and diagnostics; and
- rendering events and errors for non-technical users.

It calls the public `pkg/ytdlp` API and does not import extractor internals.

### CLI

The CLI parses command-line and configuration inputs, translates them into a
public request, and renders structured engine events for terminal or JSON
consumers. Core packages do not write terminal presentation directly.

### Go API

`pkg/ytdlp` is the supported embedding boundary. It exposes context-aware
requests, client options, metadata, lazy playlists, categorized errors,
events, results, and produced artifacts. Internal extractor and downloader
packages are not public compatibility contracts.

## Operation lifecycle

A typical operation:

1. validates the public request and output policy;
2. creates isolated network, credential, cache, helper, and filesystem state;
3. routes the input through the registered extractor set;
4. produces normalized metadata, formats, subtitles, or lazy playlist entries;
5. applies compatibility, filter, selection, and output-planning rules;
6. transfers direct or segmented media;
7. performs explicitly requested typed post-processing; and
8. publishes confined artifacts and a final categorized result.

Cancellation flows through `context.Context`. Blocking operations are expected
to observe it and avoid publishing a successful final artifact after
cancellation.

## Extractors

Extractors identify supported URL families, obtain publication-safe metadata,
and describe available media. They do not own terminal rendering or arbitrary
output publication.

Reusable backend extractors are preferred when multiple sites expose the same
player or API. Specific extractors are ordered before generic discovery.
Transparent and playlist child URLs re-enter the declared registry path rather
than bypassing operation policy.

Provider-neutral contracts and shared bounded helpers live in
`internal/extraction`. The complete first-party YouTube dependency closure
lives in `internal/providers/youtube` and accepts its typed provider request;
it has no dependency on the mixed `internal/extractor` compatibility package.
That compatibility package retains thin adapters so the broad catalog keeps
its names, routing order, request surface, and URL-result re-entry behavior.

The concrete provider-composition boundary is still internal. The public
client orchestration API, public YouTube bundle, and Desktop's focused
composition are separate later stages; the current `pkg/ytdlp.NewClient`
remains the broad full-catalog facade.

The public extraction-contract owner is now
`github.com/tejasa97/youtube_dlp/engine`. Its generic registry, operation
request, result/entry model, transport and credential capabilities, metadata
values, and typed challenge/token extension seams are nameable by external
provider packages. Historical internal contract packages are compatibility
aliases to this implementation.

Provider-neutral client orchestration and the public YouTube bundle remain
staged changes described in [Public engine extraction contracts](PUBLIC_ENGINE_CONTRACTS.md).

See [Supported sites](SUPPORTED_SITES.md), [Extractor selection](EXTRACTOR_SELECTION_EVIDENCE.md),
and [the extractor master checklist](EXTRACTOR_MASTER_CHECKLIST.md).

## Format selection and output planning

Extracted formats enter a bounded selector and sorting pipeline. The planner
can choose direct combined media, separate audio/video tracks, fallbacks, and
documented multi-track or multi-output plans.

Selection does not transfer bytes. Download and post-processing stages consume
the selected plan while preserving per-format headers and operation policy.

See [Format selector behavior](FORMAT_SELECTOR_PARITY.md).

## Download protocols

The engine contains native protocol paths for:

- direct HTTP(S);
- HLS playlists and fragments;
- DASH manifests and addressing;
- ISM/Smooth Streaming; and
- explicitly configured external downloader boundaries.

Protocol implementations enforce documented retry, redirect, size, fragment,
path, cancellation, and signed-URL handling. Sensitive URLs and headers are
redacted from public diagnostics.

See [Downloader protocols](P2_DOWNLOADER_PROTOCOLS.md) and the protocol-specific
evidence linked from the documentation index.

## FFmpeg boundary

FFmpeg and FFprobe are external media tools, not shell snippets assembled from
untrusted metadata. Typed operations define validated inputs and outputs for
merging, extraction, remuxing, conversion, subtitles, metadata, chapters,
fixups, concat, and cuts.

Operations use confined paths and transactional publication rules so a failed
post-processing step does not silently replace an existing destination with a
partial result.

See [Post-processing](P2_POSTPROCESSING.md).

## JavaScript helper boundary

Some supported extractors require JavaScript challenge solving. The main
process delegates that work to a separate pure-Go helper using a versioned,
bounded protocol.

The helper:

- is explicitly configured or discovered beside the main executable;
- is not discovered from `PATH`;
- receives bounded source, argument, memory, output, and execution budgets;
- runs with a scrubbed credential environment; and
- is started only when a selected flow needs it.

See [JavaScript helper protocol](JAVASCRIPT_HELPER_PROTOCOL.md).

## Credentials and network state

Credential access is explicit. Cookie files, selected browser profiles, netrc,
authorization values, signed query parameters, and impersonation profiles stay
within the current operation boundary and are excluded from public events and
diagnostics.

Redirect and nested extraction paths preserve the operation's trust and
credential policy. See [Configuration](CONFIGURATION.md),
[Browser cookie import](CHROMIUM_COOKIE_IMPORT.md), and the security documents.

## Extensions and updates

Native RPC and constrained WASM plugins are explicit extension boundaries.
Plugins do not automatically claim arbitrary URLs. Signed packs, catalogs, and
updater transactions have separate trust, permission, version, rollback, and
revocation contracts.

The repository contains implementation evidence for these mechanisms but does
not yet define production signing identities or an endorsed update service.

## Persistence

Core operation state is scoped to the client or request. The Desktop app owns
its separate settings/history store; the library does not persist GUI state.
Cache and download-archive behavior is explicit and follows its own confined
filesystem policy.

## Evidence and design decisions

Architecture Decision Records live under [`docs/adr`](adr/README.md). Historical
phase reports and evidence explain the baseline of individual decisions. The
[project-status guide](PROJECT_STATUS.md) describes how those records relate to
current compatibility claims.
