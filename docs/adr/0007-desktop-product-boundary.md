# ADR 0007: Desktop product boundary and staged repository split

Status: Proposed (2026-08-05)

## Context

`ytdlp-go` currently contains three interfaces over one native media engine:
the broad CLI, the embeddable Go API, and a Wails Desktop preview. The engine
supports many extractor families, while Desktop V0 intentionally accepts only
one public, on-demand YouTube video at a time.

The Desktop module imports `pkg/ytdlp`, and `Client.Run` constructs the complete
product registry. Concrete YouTube and non-YouTube extractors also share the
single `internal/extractor` package. The UI therefore enforces a narrow product
policy, but the engine does not yet provide a matching runtime or compilation
boundary.

Creating a second repository by copying the current tree and deleting unrelated
files would duplicate networking, selection, media protocols, FFmpeg
orchestration, output handling, and YouTube challenge code. Fixes would then
need to be ported between repositories. Moving the Desktop application before
the engine has a stable restricted composition point would improve presentation
without establishing a real product boundary.

## Decision

Treat the broad engine and the focused Desktop application as separate products
that will eventually live in separate repositories, while implementing the
split in stages.

### Product responsibilities

The current repository remains the source of truth for:

- the general extraction engine and public Go API;
- the broad CLI;
- provider implementations, including YouTube;
- shared networking, media, output, compatibility, and conformance code; and
- engine and provider release tags.

The future focused repository will own:

- the Wails application and UI-specific state;
- the Desktop URL and feature policy;
- installers, signing, packaging, and end-user release automation;
- product branding and user-facing documentation; and
- only the optional CLI surface, if any, that the focused product exposes.

The released application will use an independent product name. Service names
may describe compatibility targets, but they are not the product identity. The
documentation and release metadata will continue to state that the application
is not affiliated with or endorsed by YouTube.

### V0 scope

Desktop V0 remains limited to:

- public, on-demand, single-video `youtube.com/watch` and `youtu.be` URLs;
- metadata preview;
- Best, 4K, 1440p, 1080p, 720p, and Audio only presets;
- one active FIFO download with queueing, progress, cancellation, and retry;
- persisted download history and output-folder selection; and
- understandable unsupported, dependency, and download errors.

Playlists, channels, handles, search, Shorts URL workflows, live and post-live
streams, YouTube Music, comments, authentication, restricted content, and
other sites remain outside V0. Supporting one of those workflows requires an
explicit product decision and corresponding tests; engine support does not
implicitly widen Desktop support.

### Stage 1: restricted runtime profile

Before moving repositories, `pkg/ytdlp` will provide a high-level restricted
client profile, conceptually:

```go
client := ytdlp.NewClient(
    ytdlp.WithProfile(ytdlp.ProfileYouTubeSingleVideo),
)
```

The exact exported names may change during API review, but the semantics are
fixed:

- `NewClient()` retains the existing broad registry and behavior;
- the restricted profile registers only the extractors required by Desktop V0;
- the generic extractor is absent;
- installed plugins and extractor-selection rules cannot widen the profile;
- unsupported inputs fail before transport construction; and
- URL-result or playlist re-entry cannot escape the restricted registry.

The Desktop application will use this profile in the current repository before
it is moved. Public profile selection is preferred over exposing the existing
internal extractor interface as a raw provider API. A reusable public provider
contract may be designed later if another embedding use case requires it.

### Stage 2: provider compilation boundary

Runtime restriction alone does not prove that unrelated implementations are
absent from a binary. YouTube code will therefore be separated from the current
mixed extractor package behind shared extraction contracts and an explicit
composition root. The broad product composition may depend on every provider;
the focused composition must not depend on the broad registry or non-YouTube
provider packages.

Compilation isolation is complete only when dependency and binary inspection
show that focused builds do not include non-YouTube providers. Runtime routing
tests remain authoritative for reachability; symbol inspection is a secondary
guard rather than the sole proof.

### Stage 3: repository extraction

Create the focused repository only after:

1. the restricted profile is used by Desktop and covered by public-API tests;
2. non-YouTube and generic routing cannot be reached through the profile;
3. the provider compilation boundary is established;
4. the engine dependency can use a reviewed tag instead of a local `replace`;
   and
5. release ownership, attribution, and independent product naming are decided.

Move Desktop-specific files with history and attribution. Do not copy engine or
provider source into the focused repository.

## Validation requirements

The restricted profile must have public-API regression tests proving:

- accepted watch and short-link inputs work for preview and download planning;
- each Desktop quality preset remains valid;
- unsupported YouTube workflows and representative non-YouTube URLs fail with
  the expected category before any network request;
- generic extraction, plugins, and extractor-selection controls cannot widen
  the profile;
- cancellation and error categorization remain unchanged; and
- the default broad client retains its existing registry and routing behavior.

The later focused build must add dependency checks demonstrating that its
composition does not import the broad registry or non-YouTube provider
packages.

## Consequences

The Desktop application remains in this repository temporarily, and the first
restricted profile may still compile more provider code than the final focused
build. That intermediate state is acceptable only while it is documented as a
runtime boundary rather than a compilation-isolation claim.

The staged approach adds refactoring work before repository creation, but it
keeps one implementation of shared downloader behavior and preserves the
existing CLI and Go API by default. The future Desktop repository can remain
small, release-focused, and unable to claim the engine's broad site support
accidentally.

Product naming, signing identities, FFmpeg redistribution, and supported
installer targets remain separate release decisions.
