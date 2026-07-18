# Low-latency HLS fixture provenance

The low-latency playlists embedded in `internal/protocol/hls` tests are wholly
synthetic. They exercise partial segments, part byte ranges, delta skips, part
targets, completion replacement, live polling, and cancellation without using
a network service or copied media.

The pinned yt-dlp checkout at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` contains no explicit
`EXT-X-PART`/`EXT-X-SKIP` implementation or fixture from which to derive this
behavior. This is a native Go improvement: completed segments replace their
previous partials before the fragment plan is executed, while parts belonging
to an incomplete final segment remain ordered and downloadable. Delta skips
advance media sequence identity so sliding updates cannot collide with older
segments.

Preload hints and rendition reports are intentionally ignored rather than
speculatively downloaded. SAMPLE-AES and other non-AES-128 encryption remain
unsupported. No Python runtime, upstream response bytes, credentials, or
copyrighted media are used.
