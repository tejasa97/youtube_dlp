# Bluesky extractor fixture provenance

These deterministic, synthetic JSON responses model the public post
extraction contract in the pinned read-only yt-dlp reference checkout:

- repository: `yt-dlp/yt-dlp`
- commit: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- source: `yt_dlp/extractor/bluesky.py`, `BlueskyIE._real_extract`,
  `_extract_videos`, `_get_service_endpoint`, and `_extract_post`.

The fixtures exercise the schema-derived public API surface only:

- the public `app.bsky.feed.getPostThread` XRPC response shape with
  `thread.post` (record, embed, author, indexedAt, likeCount,
  repostCount, replyCount, labels);
- the supported embed shapes `app.bsky.embed.video.view`,
  `app.bsky.embed.record`, `app.bsky.embed.recordWithMedia`, and
  `app.bsky.embed.external` plus a one-level quoted post via
  `embed.record.embeds[0]`;
- caption entries with `lang`, `file.mimeType`, and `file.ref.$link`;
- the DID document shapes used by the resolver for both `did:plc` and
  `did:web` including the explicit `AtprotoPersonalDataServer` service
  with the canonical `serviceEndpoint` field.

Method:

- hosts are the reserved `*.example.test` and `bsky.app`/`video.bsky.app`
  domains used elsewhere in the conformance catalog;
- post IDs are taken from the pinned reference's test URLs to preserve
  shape parity (lowercase base32-ish, 13-22 characters);
- the malicious DID documents exercise the loopback, userinfo, and
  HTTP service endpoint rejection paths without touching real
  infrastructure;
- values are invented; no real cookie, bearer token, signed media URL,
  account identifier, or production media bytes are present.

Sanitization:

- response bodies never include bearer tokens, signed URLs, real
  account identifiers, real media URLs, or private DIDs;
- the `did_malicious.json` and `did_malicious_loopback.json` fixtures
  carry inert loopback / userinfo endpoints that are explicitly
  rejected by `blueskyTrustedPDSEndpoint` and never reach the
  outgoing blob URL builder;
- the `thread_no_video.json` and `thread_not_found.json` fixtures
  model the no-media and missing-thread failure paths so the
  `ErrUnavailable` category can be exercised in isolation.

Deviations from the pinned reference (declared, audit-friendly):

- public posts only; no login, authenticated sessions, private
  repositories, or arbitrary record types;
- one bounded level of quoted/nested embeds; cycles and duplicate
  entries are dropped;
- did:web is restricted to publicly resolvable HTTPS hosts with the
  first exact-type `AtprotoPersonalDataServer` endpoint;
- did:plc and did:web are the only DID methods accepted;
- HLS is delegated to the existing m3u8 downloader; the extractor
  produces no segment-level fetching or signing;
- fixtures do not establish interoperability, G3/G4 exit, or live
  parity claims.
