# Phase 1 authenticated and regional extractor pilots

## Synthetic authenticated gate

`synthetic_auth` is a deterministic product conformance target on the reserved
`auth-fixture.invalid` origin. It proves that imported cookies remain owned by
the per-operation transport jar: the extractor requests protected JSON but
never reads, copies, logs, or emits a cookie. The automated gate uses the real
network client and checks host/path scoping, missing and invalid sessions,
unavailable and malformed responses, cancellation, and secret-free normalized
metadata/errors.

Registry integration should place this explicit host extractor before the
generic extractor. It is a conformance surface, not a real internet service.

## Regional selection: SVT Play

SVT Play was selected from the pinned yt-dlp reference because its public
single-video path is bounded and credential-free while still exercising
region-specific semantics. A page supplies `videoSvtId`, one public JSON API
supplies localized Swedish metadata and media references, and
`rights.geoBlockedSweden` distinguishes Sweden-only content when no formats are
available. This is materially stronger regional evidence than merely storing a
country label.

The pilot supports SVT Play and Öppet arkiv single-video/clip/channel URLs,
explicit `modalId`/`id` selection, basic page ID discovery, HLS/DASH/direct
references, Swedish and forced subtitles, localized episode fields, child
suitability, live state, and geo/unavailable/error categorization. Registry
integration should place it before the generic extractor.

Known deviations from the pinned reference are explicit:

- series/page playlists are not implemented;
- URQL discovery is a bounded `videoSvtId` search rather than full JavaScript
  object transformation and traversal;
- manifests are returned for the existing media pipeline to expand rather than
  eagerly expanded inside the extractor;
- legacy Adobe HDS (`.f4m`) references are treated as direct formats because
  the Go media pipeline does not implement HDS;
- geo-bypass header synthesis is not attempted;
- the public error taxonomy requires the integrator to map
  `extractor.ErrRegionRestricted` to the product's unsupported/unavailable
  category until a first-class geo category is introduced.
