# SoundCloud pilot fixture provenance

All responses in this directory are synthetic, deterministic, and
license-safe. Hostnames under `sndcdn.com` mirror response shapes but do not
identify or contain media copied from a SoundCloud user.

Behavioral expectations were derived from the pinned yt-dlp checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`yt_dlp/extractor/soundcloud.py`:

- `SoundcloudBaseIE._update_client_id` for bounded first-party script discovery;
- `SoundcloudIE._real_extract` and `_extract_info_dict` for resolve/direct-track
  requests, transcoding resolution, format identifiers, codecs, protocols, and
  normalized metadata;
- `SoundcloudPlaylistBaseIE._extract_set` for ordered transparent set entries;
- `SoundcloudPagedPlaylistBaseIE._entries` for linked partitioning,
  `next_href`, nested track candidates, and lazy ordering;
- `SoundcloudTrackStationIE._real_extract` for station URL resolution, opaque
  `soundcloud:track-stations:<id>` identifier validation, station tracks API
  routing, and `Track station: <title>` playlist metadata;
- `SoundcloudRelatedIE._real_extract` for base-track resolution, `errors` field
  handling, relation-specific API routing (`related`, `albums`,
  `playlists_without_albums`), and `<title> (<Relation>)` playlist metadata.

Deliberate Go hardening beyond the pinned reference:

- Route-aware continuation policy with exact path matching (the reference uses
  a prefix-free `next_href` passthrough);
- Cross-station, cross-track, cross-user, and cross-relation continuation
  rejection via exact allowedPath comparison;
- Encoded path-separator (`%2f`, `%5c`) and NUL rejection in both URL
  classification and continuation validation;
- Bounded query parameter count and per-value length on continuations;
- `stations` and `recommended` added to the reserved-segment set to prevent
  ambiguous profile misclassification;
- API playlist URL fallback for playlist collection entries whose permalink
  does not classify as a SoundCloud set.

The fixture client ID, IDs, timestamps, titles, cursors, URLs, counts, and
response bodies were independently authored for this Go conformance corpus.
The production implementation neither reads this directory nor depends on the
reference checkout.
