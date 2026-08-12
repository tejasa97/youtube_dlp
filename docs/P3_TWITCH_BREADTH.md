# Twitch VOD, clip, and channel-video breadth

This increment extends the existing native live-channel extractor with:

- VOD routes on `/videos/{id}`, historical channel routes, player embeds, and
  schedule links;
- three-operation VOD metadata, signed Usher replay HLS, archived-live state,
  start offsets, full-size thumbnails, and bounded chapters;
- clip routes on `clips.twitch.tv` and channel clip paths;
- signed landscape and portrait direct clip qualities, thumbnails, channel,
  curator, follower, verification, category, and timestamp metadata;
- historical clip download-archive identities: the final accepted format URL is
  matched, without percent-decoding and with the signed query excluded, against
  the pinned `%7C<digits>(-<digits>)?.mp4` rule; a match emits exactly
  `_old_archive_ids: ["twitchclips <first-digits>"]` and no match omits the
  field entirely;
- pinned clip preference scores: portrait formats carry `quality: -2`
  (landscape formats carry no quality penalty), and clip thumbnails carry
  `preference` 0 (`default`), -1 (`portrait`), and -2 (`small`), preserving the
  default/portrait/small ordering and duplicate `small` suppression;
- public channel videos/profile playlist routes with pinned filter/sort query
  mapping and bounded lazy `FilterableVideoTower_Videos` GraphQL pagination;
- direct `/collections/{id}` routes and channel `/videos?filter=collections`
  enumeration through bounded `CollectionSideBar` and
  `ChannelCollectionsContent` GraphQL pagination;
- channel `/clips` and `/videos?filter=clips` enumeration through bounded
  `ClipsCards__User` GraphQL pagination (page size 20) with pinned range
  mapping to Top 24H / Top 7D / Top 30D / Top All labels.

Routing rejects credentials, explicit ports, encoded IDs, malformed numeric VOD
IDs, and malformed or excessive clip slugs. Channel videos routes additionally
reject fragments, reserved channel names, and extra path components beyond
`/videos`, `/videos/all`, and `/profile`. Direct collection routes reject
fragments, encoded separators, malformed or oversized collection IDs, and
extra path components. Channel collections routes reject fragments, duplicate
filter or sort keys, and malformed query escaping while permitting benign
unrelated query keys. Channel clips routes accept `/clips` and
`/videos?filter=clips` only; they reject fragments, duplicate range or filter
keys, reserved channel names, extra path components, `/profile` and
`/videos/all` clips filters, and malformed query escaping while permitting
benign unrelated query keys. Both collection GraphQL responses are fail-closed
at 100 edges per page; channel clips pages are fail-closed at 20 edges per
page. Clip media URLs must be bounded HTTPS assets on the explicit Twitch
clip-media origins (`clips-media.twitch.tv`, `clips-media-assets.twitch.tv`,
and `clips-media-assets2.twitch.tv`), without
credentials, ports, IP hosts, fragments, or local/internal suffixes. Format,
asset, chapter, and playlist page collections have hard bounds. API/transport
failures are reduced to categorized, secret-safe errors.

The product registry exposes exact normalized keys for all seven retained
upstream classes: `twitch_vod`, `twitch_collection`, `twitch_videos`,
`twitch_videos_clips`, `twitch_videos_collections`, `twitch_stream`, and
`twitch_clips`. The adapters are thin route-kind selectors over the shared
backend; transparent children use the corresponding exact key.

GraphQL, Usher, and storyboard requests require credential-isolated,
no-redirect transport. Twitch HLS formats carry an allowed-host policy that is
enforced across master, variant, segment, key, and initialization-map URLs;
signed query parameters are retained. Direct media and thumbnails are also
marked for isolated no-redirect download. HLS uses the Twitch CDN zones
`ttvnw.net`, `twitchcdn.net`, and `jtvnw.net`; thumbnails use the preview CDN
zones plus the explicit clip-media origins; storyboards use preview CDN zones
only. Role crossing is rejected. Allowed-host metadata is accepted only as
strict DNS hostnames (ASCII labels, no whitespace, ports, paths, IP literals,
empty labels, or duplicates); matching is exact or dot-delimited subdomain
matching, and malformed policy fails closed during format selection.

The registered-product tests invoke the public `Client.Run` lifecycle through
the same product registry and a narrow transport-construction seam. They seed
ambient Authorization, Cookie, Proxy-Authorization, and Referer values and
verify that every GraphQL, Usher, HLS child, direct-media, thumbnail, and
storyboard request strips them. Each retained playlist family is consumed
twice with fresh bounded cursor requests and identical child metadata and
download bytes.

The retained boundary is anonymous public behavior only. Login/private,
subscriber-only or entitlement-gated media, restricted mature/geo responses,
chat/comments, live-from-start VOD association, and interactive credential
flows remain unsupported. Oversize, malformed, redirect, client, rate-limit,
legal, server, and cancellation failures remain typed and secret-safe.

Known deviations from the pinned reference:

- subscriber-only playback is categorized as authentication-required, but the
  shared request contract does not yet carry an authenticated Twitch cookie;
- chat and arbitrary Twitch routes remain outside this lane;
- VOD HLS is represented as a signed replay manifest for the existing native
  HLS pipeline; manifest expansion occurs during product download as elsewhere
  in this repository.

Channel clips playlist entries preserve available card metadata (`thumbnail`,
`duration`, `timestamp`, `view_count`, `language`) on transparent URL results
when GraphQL values are present and within bounded, validated form. Malformed
optional card fields are omitted without dropping an otherwise valid clip URL.
