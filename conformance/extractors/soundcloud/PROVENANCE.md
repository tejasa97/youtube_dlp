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

- Route-aware continuation policy with genuinely exact path matching (the
  reference uses a prefix-free `next_href` passthrough). The decoded path must
  equal `allowedPath` exactly; `path.Clean` is not used. Dot segments (`.` and
  `..`), trailing slashes, fragments, explicit ports, userinfo, and encoded
  separators (`%2f`, `%5c`, `%00`) are all rejected fail-closed;
- Cross-station, cross-track, cross-user, and cross-relation continuation
  rejection via exact allowedPath comparison;
- Bounded query parameter count and per-value length on continuations;
- `stations` and `recommended` added to the reserved-segment set to prevent
  ambiguous profile misclassification;
- API playlist URL fallback for playlist collection entries whose permalink
  does not classify as a SoundCloud set;
- Direct collection item dispatch matching the reference `resolve_entry(e,
  e.get('track'), e.get('playlist'))` ordering: the direct item is classified
  by its explicit `kind` field and/or permalink URL kind before track fallback.
  Direct `playlist` objects produce set entries (or `/playlists/<id>` fallback);
  direct `track` objects produce track entries (or `/tracks/<id>` fallback).
  Unknown or contradictory kind/permalink combinations fail closed (skip) unless
  the permalink independently provides an unambiguous supported type;
- Malformed continuation query rejection: `url.ParseQuery` is used explicitly
  instead of `parsed.Query()` to reject malformed percent-escaping and invalid
  semicolon syntax that would otherwise be silently discarded;
- Secret-safe related-resource failures: `errors[].error_message` from the
  remote response is never exposed in public Go errors. A generic
  `ErrUnavailable: SoundCloud related resource unavailable` diagnostic is
  returned instead, preventing leakage of client IDs, signed URLs, tokens, or
  arbitrary server messages;
- Slug fallback for missing related-track title: when `track.title` is blank,
  the playlist title falls back to the URL slug (`artist/track`), matching the
  reference `track.get('title') or slug` behavior.

The fixture client ID, IDs, timestamps, titles, cursors, URLs, counts, and
response bodies were independently authored for this Go conformance corpus.
The production implementation neither reads this directory nor depends on the
reference checkout.
