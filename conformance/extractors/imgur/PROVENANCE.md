# Imgur extractor fixture provenance

Behavior was derived by direct inspection of the read-only yt-dlp checkout at
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, principally:

- `yt_dlp/extractor/imgur.py` (`ImgurIE`, `ImgurGalleryIE`, and
  `ImgurAlbumIE`);
- the embedded routing and result expectations in that module; and
- the common extractor result conventions exercised by the repository's
  existing API and playlist extractors.

The JSON files under `internal/extractor/testdata/imgur` are synthetic,
repository-authored fixtures. They preserve only attributable response shapes:
the `post/v1/media` and `post/v1/albums` envelopes, video versus animated/static
media classification, account/count metadata, timestamps, and ordered album
media. They contain no captured accounts, tokens, cookies, or production media
URLs.

The client identifier is the public identifier embedded in the pinned
reference implementation. It is used only as a fixed API query parameter and
is never accepted from callers or reported through errors.

## Deliberate boundaries

- Public, anonymous media/gallery/album extraction only.
- API media URLs are accepted only from `i.imgur.com`, normalized to HTTPS,
  and rejected when they contain credentials, ports, fragments, encoded path
  separators, or control characters.
- Static-only items are unavailable; animated images and videos are retained.
- Gallery/album inventories are capped at 512 items and remain lazy through
  transparent Imgur entries.
- The pinned webpage `.gifv` source/OG/Twitter fallback is not implemented in
  this increment. API formats are the compatibility target, and this
  difference remains explicit in the parity manifest.
