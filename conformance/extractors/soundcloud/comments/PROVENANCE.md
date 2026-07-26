# SoundCloud track-comment provenance

Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

| Field | Value |
|-------|-------|
| Reference file | `yt_dlp/extractor/soundcloud.py` |
| Reference class | `SoundcloudIE` |
| Reference function | `_get_comments` |

## Derived behavior

- `tracks/{id}/comments` uses `sort`, `limit=20`, `offset=0`, and `threaded=1`.
- Supported sorting is `newest`, `oldest`, or `track-timestamp`.
- Numeric comment and author identifiers become strings. Millisecond track
  timestamps become equal `start_time` and `end_time` values in seconds.
- Pages and rows retain service order.

Fixtures are synthetic, license-safe, secret-free JSON. They contain no copied
comment text or media:

- `conformance/extractors/soundcloud/comments_page1.json`
- `conformance/extractors/soundcloud/comments_page2.json`

## Go hardening and bounded deviations

- Retrieval is opt-in, deferred until after filtering, capped at 100 comments
  by default and 10,000 explicitly. Reaching the selected limit stops before
  another page request. Invalid sort or limit options fail before comment
  network traffic.
- Every page requires credential-isolated, no-redirect transport and an exact
  same-track continuation.
- Dedicated API/CLI options replace arbitrary extractor-argument parsing.
