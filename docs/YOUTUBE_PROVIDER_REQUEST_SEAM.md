# YouTube provider request seam

Status: implemented request boundary; concrete provider move pending.

ADR 0007 requires the complete YouTube family to become an explicit provider
dependency without making provider capability a Desktop workflow. This seam
separates request ownership before any concrete implementation files move.

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

## Exact PR 6 move

PR 6 is a dependency-closure relocation, not another request redesign:

1. Move the complete `internal/extractor/youtube*.go` implementation and tests
   together into `internal/providers/youtube`, including video, clips,
   playlists, channels and tabs, search, Music, comments, captions,
   authentication, live, SABR, and renderer helpers.
2. Change those concrete providers to accept `youtube.Request` directly and
   replace neutral aliases from `internal/extractor` with imports from
   `internal/extraction`.
3. Leave thin constructors/adapters in `internal/extractor` for the broad
   compatibility catalog. Each adapter converts the legacy request with
   `YouTubeRequest`; the provider package must not import `internal/extractor`.
4. Preserve broad catalog order, names, selection behavior, and
   `pkg/ytdlp.NewClient` behavior. Do not switch Desktop in that move.
5. Move or extract every shared helper reached by the YouTube files as part of
   the same dependency closure. PR 6 must not solve compile failures by making
   the provider import the mixed implementation package.

The generic registry type remains parameterized by its composition-owned
request. The compatibility catalog still uses `extractor.Request`; a future
focused composition can bind the moved provider to `youtube.Request` without
making SoundCloud or NHK options part of its dependency.
