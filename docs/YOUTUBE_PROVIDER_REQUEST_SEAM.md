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
| Provider-neutral engine (`internal/extraction.Request`) | `URL`, `SearchQueryOverride`, `Referer`, `Transport`, `Credentials`, `VideoPassword`, `NoPlaylist` | Routing and operation-scoped facilities used across provider families. |
| Complete YouTube family (`internal/providers/youtube.Options`) | `ChallengeSolver`, `YouTubePOT`, `YouTubeTranslatedCaptions`, `YouTubeLiveFromStart`, `YouTubeComments` | YouTube player challenges, PO tokens, captions, live handling, and comment continuation behavior. |
| Other concrete providers | `SoundCloudComments`, `NHK` | Options remain with their existing provider implementations and are not copied into the YouTube request. |

The broad product maps its public request into the legacy request exactly once
through `operation.providerRequest`. `internal/extractor.Request.NeutralRequest`
copies only engine-owned fields, and `YouTubeRequest` combines that neutral
request with typed YouTube options. Transports and credential providers retain
their operation-local identities; neither is stored globally or copied into a
provider-specific option bag. All three request forms use fixed redacted
diagnostic formatting.

The new provider request package is internal because the current neutral
contracts are internal. Publishing `providers/youtube` now would expose
signatures containing types external callers cannot legally import. A public
provider package therefore requires a separately reviewed public neutral
engine contract; this change does not widen the public API.

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
entry limiting live in `internal/extraction`, with compatibility wrappers for
non-YouTube providers. The broad provider names and positions remain fixed.

## Remaining PR 7 seam

PR 7 may expose the reviewed neutral engine/provider composition API and a
publicly consumable complete YouTube provider. Before doing so it must give
external callers nameable public contracts for the request, transport,
credentials, extraction result, entries, registry/provider interface, and
JavaScript challenge boundary. It must not publish signatures that contain
types under `internal/`.

That public composition should adapt directly to `youtube.Request`, preserve
the full-catalog `pkg/ytdlp.NewClient` facade, and keep plugins exclusive to
the broad composition. Desktop remains unchanged until a later dedicated
switch and dependency-proof change.

The generic registry type remains parameterized by its composition-owned
request. The compatibility catalog still uses `extractor.Request`; a future
focused composition can bind the moved provider to `youtube.Request` without
making SoundCloud or NHK options part of its dependency.
