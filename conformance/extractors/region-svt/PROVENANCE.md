# SVT Play fixture provenance

The deterministic responses model the SVT Play single-video behavior in the
pinned read-only yt-dlp checkout:

- repository: `yt-dlp/yt-dlp`
- commit: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- source: `yt_dlp/extractor/svt.py`
- reference classes: `SVTBaseIE` and `SVTPlayIE`
- public API shape: `https://api.svt.se/videoplayer-api/video/{video_id}`
- reference region: Sweden (`_GEO_COUNTRIES = ['SE']`)

`video.json` preserves the reference field mapping for `videoReferences`,
`rights.validFrom`, `rights.geoBlockedSweden`, localized program/episode
metadata, child-suitability, material length, and subtitle references. The
forced-subtitle URL models the reference's `text-open` language fixup.
`geo-blocked.json` records the reference condition that an empty format list
combined with `geoBlockedSweden` is a geo-restriction rather than malformed
metadata.

The page fixture is synthetic and minimal. Its `videoSvtId` key represents the
same ID ultimately selected from the reference's `URQL_DATA` traversal; no live
SVT page or media response was copied. All media hosts and IDs are invented,
and tests use an injected offline transport.
