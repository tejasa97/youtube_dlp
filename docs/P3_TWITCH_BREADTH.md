# Phase 3 Twitch VOD and clip breadth

This increment extends the existing native live-channel extractor with:

- VOD routes on `/videos/{id}`, historical channel routes, player embeds, and
  schedule links;
- three-operation VOD metadata, signed Usher replay HLS, archived-live state,
  start offsets, full-size thumbnails, and bounded chapters;
- clip routes on `clips.twitch.tv` and channel clip paths;
- signed landscape and portrait direct clip qualities, thumbnails, channel,
  curator, follower, verification, category, and timestamp metadata.

Routing rejects credentials, explicit ports, encoded IDs, malformed numeric VOD
IDs, and malformed or excessive clip slugs. Clip media URLs must be bounded
HTTPS assets on Twitch CDN domains (reserved `.example.test` is accepted only
for deterministic fixtures), without credentials, ports, IP hosts, fragments,
or local/internal suffixes. Format, asset, and chapter collections have hard
bounds. API/transport failures are reduced to categorized, secret-safe errors.

Known deviations from the pinned reference:

- storyboard (`mhtml`) formats are not emitted yet, although the metadata URL
  is parsed under the bounded response contract;
- subscriber-only playback is categorized as authentication-required, but the
  shared request contract does not yet carry an authenticated Twitch cookie;
- clip historical archive IDs and format preference scores are not emitted;
- channel/collection/playlist enumeration remains outside this lane;
- VOD HLS is represented as a signed replay manifest for the existing native
  HLS pipeline; manifest expansion occurs during product download as elsewhere
  in this repository.
