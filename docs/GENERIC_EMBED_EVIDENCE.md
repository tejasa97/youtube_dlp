# Conservative generic embed discovery

Baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

The generic extractor preserves its direct-media `HEAD` fast path. A rejected
`HEAD` (405/501), absent media type, HTML response, or inconclusive/misleading
media type falls back to one bounded `GET`. HTML scanning examines only
explicit embed-bearing attributes. It never requests an iframe, executes
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

## Resource and security bounds

- HTML response: 2 MiB;
- tokenizer tokens: 100,000;
- element nesting depth: 256;
- embed-bearing attributes examined: 256;
- unique embeds returned: 64; and
- candidate URL: 8 KiB.

Userinfo, explicit ports (including an empty explicit port), fragments,
encoded separators/NULs, unsupported schemes, provider lookalikes, and URLs
outside the fixed embed routes fail closed. Element closing tags are tracked
by name so malformed nesting cannot underflow the depth bound. Cancellation is
checked during transport and parsing. Response-body transport failures retain
their underlying error instead of being relabeled as metadata failures.

## Intentional scope

This increment does not implement generic direct URLs found in scripts,
OpenGraph media, JSON-LD, arbitrary JW Player configuration, provider
discovery, or iframe crawling. Unsupported HTML remains a categorized
unsupported extraction.
