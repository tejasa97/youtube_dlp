# TED public family risk fixtures

These deterministic HTML, HLS, WebVTT, and media fixtures were authored on
2026-07-30 from the pinned `yt_dlp/extractor/ted.py` implementation at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. They model the anonymous public
`TedTalkIE`, `TedSeriesIE`, `TedPlaylistIE`, and `TedEmbedIE` shapes without
using live TED traffic or user data.

The URLs intentionally retain attributable production hostnames (`ted.com`,
`hls.ted.com`, `download.ted.com`, and `pi.tedcdn.com`). Tests intercept every
request in memory; no fixture-only host is admitted by production URL policy.
Signed query strings are synthetic and are asserted byte-for-byte through
extraction and product dispatch.

The corpus covers public Next.js metadata, direct H.264, HLS variants and
subtitle renditions, audio-only download, thumbnails, chapters, season
filtering, distinct series/playlist child identities and media bytes, strict
embed canonicalization, ambient credential stripping, no-redirect behavior,
role-specific manifest/media/segment/subtitle/thumbnail host policy,
cancellation, no-overwrite output paths, deterministic repeated execution, and
artifact cleanup. Login/private, unavailable, DRM, geo, and arbitrary external
media handoffs remain outside the claim.
