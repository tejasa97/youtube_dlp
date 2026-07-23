# TikTok public-webpage captions evidence

This bounded implementation is based on `TikTokIE._get_subtitles` in
`yt_dlp/extractor/tiktok.py` at reference commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.  That implementation reads the
public-webpage `video.subtitleInfos` list and maps `LanguageCodeName`, `Url`,
and `Format` (`creator_caption`, `srt`, `webvtt`) to subtitle entries.

The Go port deliberately supports only that already-hydrated webpage shape.
It performs no caption download, API signing, fallback endpoint request, or
private/authenticated extraction. It accepts only the exact, fixture-attributed
caption CDN host `v16-webapp.tiktok.com`—not TikTok apex/web/API hosts or
registrable-domain suffixes. Entries are deterministically truncated after
the first 128 attributable list items (and at the total metadata-output cap),
require HTTPS, reject userinfo/ports/fragments and direct or repeated-decoding
encoded path separator/NUL ambiguity, and preserve accepted signed queries
without inspecting or rewriting them. Rejected URLs are never included in
diagnostics. The sanitized checked-in fixture uses inert
`signature=redacted` text.

Primary-owned follow-up: add the TikTok captions fixture/result coverage to
the shared parity manifest and any documentation catalog if that integration
is desired. This increment intentionally does not modify shared manifests or
catalogs.
