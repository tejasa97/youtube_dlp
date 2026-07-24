# Phase 3 Twitch VOD, clip, and channel videos breadth

This increment extends the existing native live-channel extractor with:

- VOD routes on `/videos/{id}`, historical channel routes, player embeds, and
  schedule links;
- three-operation VOD metadata, signed Usher replay HLS, archived-live state,
  start offsets, full-size thumbnails, and bounded chapters;
- clip routes on `clips.twitch.tv` and channel clip paths;
- signed landscape and portrait direct clip qualities, thumbnails, channel,
  curator, follower, verification, category, and timestamp metadata;
- public channel videos/profile playlist routes with pinned filter/sort query
  mapping and bounded lazy `FilterableVideoTower_Videos` GraphQL pagination;
- direct `/collections/{id}` routes and channel `/videos?filter=collections`
  enumeration through bounded `CollectionSideBar` and
  `ChannelCollectionsContent` GraphQL pagination.

Routing rejects credentials, explicit ports, encoded IDs, malformed numeric VOD
IDs, and malformed or excessive clip slugs. Channel videos routes additionally
reject fragments, clips filter enumeration, reserved channel names,
and extra path components beyond `/videos`, `/videos/all`, and `/profile`.
Direct collection routes reject fragments, encoded separators, malformed or
oversized collection IDs, and extra path components. Channel collections routes
reject fragments, duplicate filter or sort keys, and malformed query
escaping while permitting benign unrelated query keys. Clip
media URLs must be bounded HTTPS assets on Twitch CDN domains (reserved
`.example.test` is accepted only for deterministic fixtures), without
credentials, ports, IP hosts, fragments, or local/internal suffixes. Format,
asset, chapter, and playlist page collections have hard bounds. API/transport
failures are reduced to categorized, secret-safe errors.

Known deviations from the pinned reference:

- storyboard (`mhtml`) formats are not emitted yet, although the metadata URL
  is parsed under the bounded response contract;
- subscriber-only playback is categorized as authentication-required, but the
  shared request contract does not yet carry an authenticated Twitch cookie;
- clip historical archive IDs and format preference scores are not emitted;
- clips channel enumeration remains outside this lane;
- chat and arbitrary Twitch routes remain outside this lane;
- VOD HLS is represented as a signed replay manifest for the existing native
  HLS pipeline; manifest expansion occurs during product download as elsewhere
  in this repository.
