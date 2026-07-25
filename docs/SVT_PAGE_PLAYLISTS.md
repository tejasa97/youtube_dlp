# SVT article page playlists

The native `region_svt` extractor supports exact HTTPS article routes on
`svt.se` and `www.svt.se`. It performs one credential-isolated,
redirect-disabled page request, reads the OpenGraph title, and decodes the
bounded `urqlState` JSON envelope used by the pinned `SVTPageIE`.

Entries are collected from `page.topMedia.svtId` and nested
`page.body...video.svtId` values. Valid IDs retain deterministic occurrence
order, duplicates are suppressed, and each result is a transparent
`svt:<video-id>` handoff to the existing single-video flow with the article
title as an overlay.

Routing rejects HTTP, userinfo, explicit ports, query strings, fragments,
ambiguous slashes, dot segments, encoded separators/NULs, control characters,
and overlong URLs or path components. HTML, embedded JSON, traversal depth,
node count, and playlist size are bounded. Public failures are categorized
without including response bodies or transport error text.

The deterministic fixture and expectations are derived from
`yt_dlp/extractor/svt.py` at pinned commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`; provenance is recorded beside
the corpus in `conformance/extractors/region-svt/PROVENANCE.md`.

Deliberate deviations: arbitrary JavaScript execution, non-JSON `urqlState`
expressions, HTTP article pages, redirects, and malformed video rows are not
accepted. The existing media extractor remains responsible for hydrating each
validated `svt:` result.
