# Conservative generic embed discovery

Baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

The generic extractor preserves its existing direct-media `HEAD` flow. When
the response is HTML, it performs one bounded `GET` and scans only explicit
embed-bearing attributes. It never requests an iframe, executes script, or
turns an arbitrary link into a media result.

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
and protocol-relative attributes are resolved against the final page URL, but
the resolved target must still satisfy one of those provider policies.

## Resource and security bounds

- HTML response: 2 MiB;
- tokenizer tokens: 100,000;
- element nesting depth: 256;
- embed-bearing attributes examined: 256;
- unique embeds returned: 64; and
- candidate URL: 8 KiB.

Userinfo, explicit ports, fragments, encoded separators/NULs, unsupported
schemes, provider lookalikes, and URLs outside the fixed embed routes fail
closed. Cancellation is checked during transport and parsing.

## Intentional scope

This increment does not implement generic direct URLs found in scripts,
OpenGraph media, JSON-LD, arbitrary JW Player configuration, redirects,
provider discovery, or iframe crawling. Unsupported HTML remains a categorized
unsupported extraction.

The lane temporarily represents a single embed as a one-entry playlist because
the current shared `Extraction` contract has no root URL-result variant.
Primary integration owns the localized switch to a transparent root URL result.
