# YouTube provider request seam

Status: request boundary and internal provider move implemented; public
composition pending.

ADR 0007 requires the complete YouTube family to become an explicit provider
dependency without making provider capability a Desktop workflow. This seam
separates request ownership from the broad compatibility facade.

## Request ownership inventory

The legacy `internal/extractor.Request` remains source-compatible, but its
fields now have these owners:

| Owner | Fields | Reason |
| --- | --- | --- |
| Provider-neutral engine (`engine/provider.Request`) | `URL`, `SearchQueryOverride`, `Referer`, `Transport`, `Credentials`, `VideoPassword`, `NoPlaylist` | Routing and operation-scoped facilities used across provider families. |
| Complete YouTube family (`internal/providers/youtube.Options`) | `ChallengeSolver`, `YouTubePOT`, `YouTubeTranslatedCaptions`, `YouTubeLiveFromStart`, `YouTubeComments` | YouTube player challenges, PO tokens, captions, live handling, and comment continuation behavior. |
| Other concrete providers | `SoundCloudComments`, `NHK` | Options remain with their existing provider implementations and are not copied into the YouTube request. |

The broad product maps its public request into the legacy request exactly once
through `operation.providerRequest`. `internal/extractor.Request.NeutralRequest`
copies only engine-owned fields, and `YouTubeRequest` combines that neutral
request with typed YouTube options. Transports and credential providers retain
their operation-local identities; neither is stored globally or copied into a
provider-specific option bag. All three request forms use fixed redacted
diagnostic formatting.

The cycle-free public contract is now `engine/provider`. The internal YouTube
implementation depends directly on that package, so a later public
`providers/youtube` facade can expose only nameable public signatures.

## Completed internal move

The complete video, clips, playlists, channels, handles, aliases, tabs,
search, hashtag, Music, comments, captions, authentication, metadata,
renderer, live/post-live, format, and SABR-related provider implementation and
tests now live in `internal/providers/youtube`. Every concrete implementation
accepts `youtube.Request`; the package dependency graph contains no
`internal/extractor` edge.

`internal/extractor` retains thin named constructors and adapters for the
broad catalog, URL-result re-entry, and legacy tests. Each adapter converts an
`extractor.Request` through `YouTubeRequest` once. Shared bounded JSON
transport, balanced JSON-object extraction, manifest-format construction, and
entry limiting are owned by `engine/provider`, with compatibility wrappers for
non-YouTube providers. The broad provider names and positions remain fixed.

## Remaining orchestration and public-provider seams

The next orchestration PR moves the operational client/API into root `engine`
and fixes `engine/provider.Bundle[C]` to its public run configuration type.
Neutral composition tests stay beside `engine/provider` and use public fake
providers. Broad catalog, concrete-provider, plugin, and compatibility tests
remain under `pkg/ytdlp`, avoiding a test-only dependency from `engine` back to
`internal/extractor`.

That public composition should adapt directly to `youtube.Request`, preserve
the full-catalog `pkg/ytdlp.NewClient` facade, and keep plugins exclusive to
the broad composition. Desktop remains unchanged until a later dedicated
switch and dependency-proof change.

The generic bundle and registry remain parameterized by composition-owned
configuration and request types. The compatibility catalog can bind
`extractor.Request`; a future focused composition can bind `youtube.Request`
without making SoundCloud or NHK options part of its dependency.
