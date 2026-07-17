# YouTube extractor pilot fixture

This corpus is a synthetic, offline fixture for the Phase 1 YouTube video
extractor. It contains only structural data needed to exercise player-response
parsing, direct and signature-cipher formats, `n` transformation, signature
deciphering, HLS/DASH manifest exposure, and normalized metadata.

The player program is the pinned synthetic EJS fixture at
`conformance/javascript/ejs-0.8.0/synthetic-player.js`. Its provenance and the
exact upstream `yt-dlp-ejs` version are documented alongside that fixture.

The domains use the reserved `.example` namespace, and the in-memory test
transport rejects every unlisted URL. No live YouTube request is made. The
expected document is intentionally checked in so field presence, ordering, and
challenge-transformed URLs remain reviewable.
