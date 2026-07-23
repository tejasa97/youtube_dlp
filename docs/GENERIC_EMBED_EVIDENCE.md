# Conservative generic page discovery

Baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

The generic extractor preserves its direct-media `HEAD` fast path. A rejected
`HEAD` (405/501), absent media type, HTML response, or inconclusive/misleading
media type falls back to one bounded `GET`. HTML scanning examines only
explicit embed-bearing attributes, then bounded structured metadata when no
native-provider embed was accepted. It never requests an iframe, executes
script, or turns an arbitrary link into a media result.

HTTP redirects are emitted as root URL results before the final document is
scanned, matching the pinned reference's extractor handoff boundary. A single
discovered embed is likewise a root URL result, while multiple embeds remain a
playlist. Playlist selection and flat-playlist controls therefore cannot
discard or suppress a single embedded item.

## Accepted provider boundaries

Discovery can emit only strict URL shapes already owned by these native
extractors:

- YouTube `/embed/<id>` and privacy-enhanced embeds;
- Vimeo `player.vimeo.com/video/<id>`;
- Brightcove Player embeds;
- Kaltura HTTP embeds;
- JW Platform player/feed URLs;
- Wistia media, playlist, and channel embeds;
- SproutVideo embeds;
- Dailymotion player/video embeds;
- Rumble embeds;
- Streamable `/e/<id>` embeds; and
- recognized PeerTube `/videos/embed/<id>` URLs.

Each accepted URL is reduced to the provider's canonical target and exact
canonical duplicates are discarded while document order is retained. Relative
and protocol-relative attributes are resolved against the requested page URL
when no redirect handoff occurred, and the resolved target must still satisfy
one of those provider policies.

## Structured metadata media

When no supported provider embed is found, discovery follows the pinned
generic extractor's precedence:

1. Schema.org-context `VideoObject` or `AudioObject` `contentUrl` values from
   `application/ld+json`;
2. `twitter:player:stream`; then
3. exact `og:video` or `og:audio` properties.

JSON-LD preserves bounded `name`, `description`, `thumbnailUrl`, ISO-8601
duration, and `encodingFormat` fields. OpenGraph/Twitter metadata supplies
title, description, and thumbnail fallbacks. Relative URLs resolve against the
final page URL. Direct audio/video and native HLS, DASH, and ISM formats carry
the page Referer through actual product downloads. Exact duplicate media URLs
are suppressed.

Provider embeds remain authoritative over metadata fallbacks. JSON-LD has
priority over Twitter and OpenGraph even when those tags appear earlier in the
document. Malformed JSON-LD is ignored so a valid lower-priority metadata
source can still be used.

JSON-LD VideoObject/AudioObject metadata also carries the pinned core fields
for uploader, artist, upload timestamp, content size, bitrate, width, height,
interaction count, and deduplicated keywords. Numeric strings are accepted
where the reference accepts them; invalid or out-of-range optional values are
omitted without discarding a valid media URL.

An `embedUrl`-only JSON-LD object may route through an existing native provider
extractor. This is deliberately narrower than generic URL discovery: the URL
must pass the same canonical provider allowlist used for explicit iframe/embed
attributes. A node with `contentUrl` never falls back to its `embedUrl`;
any valid JSON-LD `contentUrl` media result precedes all embed-only candidates;
unsupported or hostile embed URLs are ignored so lower-priority metadata can
still be considered. Multiple accepted JSON-LD embeds preserve their JSON-array
order, deduplicate canonically, and use the existing 64-entry embed limit.

## Resource and security bounds

- HTML response: 2 MiB;
- tokenizer tokens: 100,000;
- element nesting depth: 256;
- embed-bearing attributes examined: 256;
- unique embeds returned: 64; and
- candidate URL: 8 KiB.
- metadata candidates: 256;
- JSON-LD scripts: 32;
- one JSON-LD script: 512 KiB;
- traversed JSON-LD nodes: 2,048 with depth 64; and
- normalized title/description: 1 KiB/8 KiB; and
- JSON-LD keywords: 128 unique values of at most 256 bytes each.

Userinfo, explicit ports (including an empty explicit port), fragments,
encoded separators/NULs, unsupported schemes, provider lookalikes, and URLs
outside the fixed embed routes fail closed. Element closing tags are tracked
by name so malformed nesting cannot underflow the depth bound. Cancellation is
checked during transport and parsing. Response-body transport failures retain
their underlying error instead of being relabeled as metadata failures.

## Intentional scope

Metadata-media URLs permit valid explicit ports because media CDNs commonly
use them, but still reject userinfo, fragments, encoded separators/NULs,
unsupported schemes, non-media extensions without a trusted media type, and
unsafe thumbnail URLs. Multiple metadata URLs from one source are represented
as formats of one page media item rather than separate playlist entries.

This increment does not implement generic direct URLs found in arbitrary
scripts, arbitrary JW Player configuration, OpenGraph structured properties
beyond the documented core, JSON-LD interaction
statistics/chapters/ratings and broader date formats, provider discovery, or
iframe crawling. Unsupported HTML remains a categorized unsupported extraction.
