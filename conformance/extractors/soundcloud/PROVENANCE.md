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
- `SoundcloudBaseIE._extract_thumbnails` for artwork-over-avatar selection, the
  ordered `mini` through `original` size matrix, avatar-specific tiny size,
  non-original JPEG URLs, original-image preference, and original-extension
  probing;
- `SoundcloudPlaylistBaseIE._extract_set` for ordered transparent set entries
  and tokenized private-set hydration through the v2 `/tracks` batch endpoint;
- `SoundcloudPagedPlaylistBaseIE._browser_impersonate_target` for Chrome 116+
  minimum impersonation selection in the pinned reference;
- `SoundcloudPagedPlaylistBaseIE._entries` for linked partitioning,
  the initial `offset=0` request, removal of the local offset after
  `next_href`, Chrome-impersonated page downloads, bounded HTTP 502 retries,
  nested track candidates, and lazy ordering;
- `SoundcloudUserIE` for bare-profile and profile-tab routing (`tracks`,
  `albums`, `sets`, `reposts`, `likes`, `spotlight`, and `comments`), resolving
  the user profile URL and using the pinned `_BASE_URL_MAP` API path with
  `{username} (<Resource>)` playlist titles;
- `SoundcloudUserPermalinkIE` for exact legacy
  `https://api.soundcloud.com/users/{positive-numeric-id}` routing, resolving
  that API URL and lazily enumerating the resolved user's v2 tracks collection
  with the username alone as playlist title;
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
- Legacy API user routes require exact HTTPS `api.soundcloud.com` identity,
  reject caller query/fragment data and encoded IDs before I/O, and require the
  resolved user ID to equal the requested numeric ID;
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
  reference `track.get('title') or slug` behavior;
- Private-set hydration validates canonical positive track IDs before I/O,
  de-duplicates network IDs, splits requests at 200 IDs and the existing URL
  bound, validates returned IDs against the current request, reconstructs
  source order (including repeated source IDs), and retains the original
  tokenized direct-track fallback when an API row is absent. The pinned
  reference sends all IDs in one request and consumes returned order without
  these validation and recovery guards;
- Thumbnail expansion is limited to exact HTTPS `i1.sndcdn.com` and
  `a1.sndcdn.com` sources with no userinfo, port, fragment, encoded separator,
  NUL, or overlong URL. The optional original-extension HEAD uses the existing
  credential-isolated, no-redirect transport capability. When that capability
  is unavailable, the metadata-supplied original extension is retained rather
  than issuing a weaker probe; ordinary isolated probe failures remain
  nonfatal and select the alternate extension, matching the pinned fallback.
- Paged collection requests use the Go port's fixed `chrome-133` profile when
  the transport implements `ProfileTransport`, fall back once to the native
  transport when impersonation is unavailable, and otherwise keep the ordinary
  API request identity. The pinned reference selects the newest available
  Chrome target with a Chrome 116 minimum. Each collection page allows one
  initial request plus three HTTP 502 retries (four total attempts). These
  behaviors are evidenced only by the synthetic fixture corpus, not live
  SoundCloud service compatibility.

The fixture client ID, IDs, timestamps, titles, cursors, URLs, counts, and
response bodies were independently authored for this Go conformance corpus.
`profile_tab_page.json` is a synthetic mixed-wrapper page reused across the
pinned profile resources. `private_set.json` and
`private_tracks_batch.json` independently model incomplete tokenized playlist
rows, repeated IDs, reordered hydration rows, and an absent permalink. They
contain no captured user or service data.
The production implementation neither reads this directory nor depends on the
reference checkout.
