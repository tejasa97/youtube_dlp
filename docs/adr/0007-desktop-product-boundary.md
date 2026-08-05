# ADR 0007: Desktop boundary through explicit provider composition

Status: Proposed (2026-08-05)

## Context

`ytdlp-go` currently contains three interfaces over one native media engine:
the broad CLI, the embeddable Go API, and a Wails Desktop preview. The engine
supports many provider families, while Desktop V0 intentionally exposes only
public, on-demand, single-video YouTube downloads.

Desktop formerly imported `pkg/ytdlp`, whose broad `NewClient` entry point
constructed the complete provider catalog. The UI was narrow, but its
dependency graph reached the broad catalog and all provider implementations.

A runtime profile or extractor allowlist would restrict routing after broad
code had already been linked. Such gates would duplicate product policy in the
engine, create another configuration mode to secure, and would not prove that
unrelated providers were absent from the Desktop binary. Conversely, copying
the repository and deleting providers would duplicate networking, media,
FFmpeg, output, and YouTube challenge code.

The boundary must therefore be established by ordinary, explicit Go
dependencies. Product capability is determined by which providers a
composition root supplies to a provider-neutral engine, not by a runtime mode
that filters a global catalog.

## Decision

Treat the broad engine and focused Desktop application as separate products
that will eventually live in separate repositories. Establish their boundary
through explicit provider composition before extracting the Desktop repository.

### Composition model

The media engine defines provider-neutral contracts and receives its providers
explicitly from its composition root. The engine does not discover providers
through global initialization, blank imports, package-level registration, or a
runtime extractor allowlist.

There are two product compositions:

- the broad CLI and compatibility facade compose the engine with the complete
  provider catalog;
- Desktop composes the engine with the complete YouTube provider and no other
  provider.

`pkg/ytdlp.NewClient` remains the broad compatibility facade. Calls that use
its current API, including `NewClient()` with no options, retain the complete
catalog and existing behavior. The facade may delegate to the new neutral
composition point, but callers are not required to select a profile or assemble
providers.

Desktop does not use `ProfileYouTubePublicVideo`, another runtime profile, an
extractor allowlist, or feature gates to narrow the engine. It imports the
Desktop composition package, which explicitly constructs the neutral engine
with the YouTube provider. Provider registration must remain visible in normal
code review and dependency analysis; there is no global `init` registration or
blank-import registration.

The YouTube provider is a provider boundary, not the Desktop product-policy
boundary. It may contain the complete related implementation set needed for
YouTube video, playlist, channel, search, Music, and live behavior even while
Desktop V0 exposes only public single-video downloads. Keeping that dependency
closure together avoids splitting tightly coupled YouTube code into artificial
product variants.

The Desktop UI and its narrow request types define which workflows the product
supports. Unsupported workflows are unrepresentable or rejected at that
boundary before the engine is invoked. Engine or provider support does not
implicitly widen the Desktop product.

Adding a future provider to Desktop requires an explicit change in the Desktop
composition package together with the corresponding UI, request mapping,
product tests, and documentation. Importing or adding the provider elsewhere in
the engine does not make it a Desktop capability.

### Product responsibilities

The current repository remains the source of truth for:

- the provider-neutral media engine and public compatibility facade;
- the broad CLI and complete provider catalog;
- provider implementations, including the complete YouTube provider;
- shared networking, media, output, compatibility, and conformance code; and
- engine and provider release tags.

The future focused repository will own:

- the Wails application and UI-specific state;
- the Desktop composition package and explicit provider selection;
- the narrow Desktop request types, mapping, and product policy;
- installers, signing, packaging, and end-user release automation;
- product branding and user-facing documentation; and
- only the optional CLI surface, if any, that the focused product exposes.

The released application will use an independent product name. Service names
may describe compatibility targets, but they are not the product identity. The
documentation and release metadata will continue to state that the application
is not affiliated with or endorsed by YouTube.

### Desktop V0 scope

Desktop V0 remains limited to:

- public, on-demand, single-video URLs accepted by the Desktop validator:
  YouTube watch, `/embed/`, and `/v/` forms on `youtube.com` or
  `youtube-nocookie.com`, plus `youtu.be` short links, canonicalized to a
  `https://www.youtube.com/watch?v=...` URL;
- metadata preview;
- Best, 4K, 1440p, 1080p, 720p, and Audio only presets;
- one active FIFO download with queueing, progress, cancellation, and retry;
- persisted download history and output-folder selection; and
- understandable unsupported, dependency, and download errors.

Playlists, channels, handles, search, Shorts URL workflows, live and post-live
streams, YouTube Music, comments, authentication, restricted content, and
other sites remain outside V0. Supporting one of those workflows requires an
explicit product decision and changes to the UI, narrow request mapping, tests,
and documentation.

### Staged implementation

The boundary will be implemented in this order:

1. **Neutral contracts.** Separate provider-neutral extraction contracts and
   shared engine behavior from concrete provider implementations.
2. **Provider-neutral composition point.** Add an explicit engine constructor
   that receives provider implementations from its caller. Do not add profiles,
   allowlists, feature gates, or implicit registration.
3. **Move the YouTube dependency closure.** Place the complete YouTube provider
   and its tightly coupled video, playlist, channel, search, Music, live, and
   challenge dependencies behind that provider boundary. Keep other providers
   outside the YouTube dependency graph.
4. **Switch Desktop.** Add the Desktop composition package, supply only the
   complete YouTube provider, and route the Wails application through narrow
   Desktop request types and explicit mapping. Preserve the full-catalog
   `pkg/ytdlp.NewClient` behavior through the compatibility facade.
5. **Dependency proof.** Add package-dependency and binary checks showing that
   Desktop does not import or link the broad catalog or non-YouTube providers.
   Product tests prove the V0 UI/request boundary; broad compatibility tests
   prove the CLI and `pkg/ytdlp.NewClient` behavior remains unchanged.
6. **Repository extraction.** After the composition and dependency proofs pass,
   move Desktop-owned files with history and attribution to the focused
   repository and consume a reviewed engine/provider release. Do not copy
   engine or provider source.

As of 2026-08-05, stages 1 through 5 are complete. Root `engine` has no
production dependency on the broad catalog or concrete providers;
`pkg/ytdlp.NewClient` explicitly owns the compatibility catalog; public
`providers/youtube` composes the complete family; and Desktop uses that focused
composition for analysis and downloads with dependency proof. Repository
extraction remains staged work.

## Validation requirements

The implementation must prove both composition and product behavior:

- neutral-engine tests use explicitly supplied fake or concrete providers;
- full-catalog regression tests preserve broad CLI and `pkg/ytdlp.NewClient`
  routing and compatibility;
- Desktop composition tests show that only the YouTube provider is supplied;
- Desktop request-mapping tests accept every V0 workflow and reject unsupported
  YouTube workflows and representative non-YouTube inputs before engine use;
- dependency checks show that Desktop does not depend on the broad catalog or
  non-YouTube provider packages; and
- adding a provider to the engine catalog alone does not change Desktop
  capability.

Runtime routing tests remain useful product checks, but they are not a
substitute for dependency proof. Symbol inspection may be used as a secondary
binary guard, not as the sole proof of package isolation.

## Consequences

The complete YouTube implementation can remain larger than Desktop V0 without
making those workflows product features. This deliberately separates provider
capability from UI capability and avoids maintaining a second, partial YouTube
implementation.

Explicit composition creates reviewable dependency edges and lets Go's package
graph enforce the product boundary. It also means provider additions require
deliberate composition work rather than registration side effects.

The staged refactor adds work before repository extraction, but preserves one
implementation of shared downloader behavior and the existing broad public
facade. The future Desktop repository can remain release-focused and cannot
accidentally claim broad site support merely because the engine catalog grows.

Product naming, signing identities, FFmpeg redistribution, and supported
installer targets remain separate release decisions.
