# YouTube player-response metadata fixture

This corpus is a synthetic, offline fixture for single-video player-response
metadata enrichment (player-response metadata wave). It contains only
structural data needed to exercise the normalized fields derived from the
initial player response: channel and uploader identities, attributable upload
dates and timestamps, age limit, partial availability, categories, tags,
average rating, embeddability, media type, the deterministic thumbnail
collection with deduplication, and the `yt:stretch` stretched ratio. The
fixture never exercises watch-page (`ytInitialData`) metadata, chapters,
heatmap, likes, subscriber counts, badges, or Music auto-generated description
fields, which remain pending.

The player response shapes and field semantics follow the pinned reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- player-response metadata merge (`videoDetails`/`microformat`/playability):
  `yt_dlp/extractor/youtube/_video.py:3941-3954`;
- category, channel ID, and owner profile URL:
  `yt_dlp/extractor/youtube/_video.py:4112-4118`;
- keywords and `yt:stretch` stretched-ratio application:
  `yt_dlp/extractor/youtube/_video.py:4065-4081`;
- thumbnail ladder names, preferences, deduplication, and best-original
  selection: `yt_dlp/extractor/youtube/_video.py:4088-4121`;
- channel URL, average rating, age limit, categories, tags, embeddability,
  media type, and release timestamp:
  `yt_dlp/extractor/youtube/_video.py:4158-4183`;
- uploader identity from the owner profile handle:
  `yt_dlp/extractor/youtube/_video.py:4509-4511` and
  `yt_dlp/extractor/youtube/_base.py:613-616`;
- UTC upload date and attributable timestamp:
  `yt_dlp/extractor/youtube/_video.py:4526-4537`;
- availability precedence (`private` > `needs_auth` > `unlisted`; the public,
  premium, and subscriber-only states need watch-page badge data and are left
  absent here): `yt_dlp/extractor/youtube/_video.py:4555-4571`.

All identifiers, metadata, URLs, dates, and thumbnails are artificial; no
production response, cookie, token, signed URL, or account data is retained.
The video ID `fixture0002` is reserved for this surface and never passes
through the 11-character pilot validator as a live URL.

The expected document is intentionally checked in so field presence, ordering,
and deterministic thumbnail preferences remain reviewable. The watch page
contains a plain-URL player response with no signature or `n` challenges, so
the pinned extraction performs exactly one transport read.
