# YouTube post-live DVR protocol provenance

The finite sequence behavior is derived from yt-dlp's
`YoutubeIE._live_adaptive_fragments` in
`yt_dlp/extractor/youtube/_video.py`, pinned at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

For a post-live adaptive format (no live URL feed), the reference:

1. requests the bare adaptive base URL and parses `X-Head-Seqnum`;
2. advances the exclusive range end by one and then subtracts two;
3. yields `sq=N` URLs from sequence zero to that exclusive end; and
4. stops after the first finite iteration.

Consequently a head sequence of `N` yields sequences `0..N-2`, excluding the
newest two sequences. Fixtures in `postlive_test.go` are synthetic and assert
that derived range rule without copying upstream network responses.

The same reference limits old live recordings to YouTube's last 432,000
seconds (120 hours). When the live start is older than that, the first sequence
is clamped to `end-floor(432000/target_duration)`. The implementation exposes
an injectable clock so this behavior has deterministic tests.

The Go implementation adds explicit URL, count, response-size, concurrency,
retry, cancellation, and filesystem bounds. It has no runtime or build-time
dependency on Python or the reference checkout.
