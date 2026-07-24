# Flickr extractor fixture provenance

Behavior was derived by direct inspection of the read-only yt-dlp checkout at
commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, principally
`yt_dlp/extractor/flickr.py` and its embedded public-video test.

The fixtures under `internal/extractor/testdata/flickr` are synthetic,
repository-authored reductions of the pinned response shapes:

- anonymous `site_key` discovery;
- `flickr.photos.getInfo` video/photo discrimination and metadata; and
- `flickr.video.getStreamInfo` direct and HLS streams.

They contain no captured cookies, accounts, API keys, stream signatures, or
production response payloads. Identifiers used in the pinned public example
are retained only as attributable routing and semantic expectations.

## Deliberate boundaries

- Public videos only; photo-only, private, authenticated, and account/library
  routes are not claimed.
- Discovery and REST calls require the shared transport's cookie-isolated
  request path and fail closed when it is unavailable.
- Stream inventories are bounded to 128 entries. Media URLs must be HTTPS
  `staticflickr.com` hosts without credentials, ports, fragments, encoded path
  separators, or control characters.
- Format quality ordering, core metadata, tags, uploader identity, counts, and
  the pinned Flickr license mapping are native and deterministic.
