# YouTube legacy alias tab fixture provenance

These fixtures are deterministic, synthetic JSON/HTML and contain no captured
account data, cookies, credentials, or executable Python.

Behavior was derived from the read-only yt-dlp checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, principally:

- `yt_dlp/extractor/youtube/_tab.py`: `YoutubeTabIE`, `_VALID_URL`,
  `_extract_tab_renderers`, `_extract_selected_tab`, `_extract_from_tabs`,
  `_entries`, and `_extract_metadata_from_tabs`;
- the `YoutubeTabIE._TESTS` cases for legacy `/user/.../playlists`,
  `/user/.../videos`, and `/c/...` channel URLs, including the NASA legacy
  user route;
- `YoutubeTabBaseInfoExtractor._extract_tab_renderers` and continuation
  extraction behavior for `continuationItemRenderer`.

The fixture shapes deliberately cover legacy `playlistRenderer` /
`gridVideoRenderer` and modern `lockupViewModel` / `richGridRenderer`
renderers. IDs, titles, aliases, tokens, visitor data, and API configuration
are invented. Expected behavior is recorded as Go tests; neither tests nor
production code execute or import Python or access the reference checkout.

The Go extractor intentionally supports only exact `/user/<alias>/<tab>` and
`/c/<alias>/<tab>` URLs for the four public tabs. Alias values are strict
UTF-8, preserve case, and are capped at 100 UTF-8 bytes as a conservative
resource bound. Canonical URLs are built with Go's structured `url.URL`
escaping so a literal percent sequence in an alias stays data and cannot
become a decoded separator or NUL. This is a documented bounded subset of
upstream URL parity.
