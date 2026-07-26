# Rai extractor-family provenance

Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/extractor/rai.py`.

The synthetic tests in `internal/extractor/rai_test.go` derive URL families,
Rai JSON fields, `Rai` relinker User-Agent, XML `output=64` handling, HLS
audio/video rendition correction, geo placeholder detection, and explicit
RaiPlay/RaiPlaySound playlist re-entry from `RaiBaseIE`, `RaiPlayIE`,
`RaiPlaySoundIE`, their live/playlist forms, and the legacy Rai classes.
Fixtures contain no copied webpages, cookies, signed URLs, or production
tokens.

Deliberate deviations: F4M/HDS is not emitted because this Go product has no
HDS downloader; direct HLS manifests are delegated to the existing native HLS
pipeline rather than expanded by the extractor.  The legacy HTML fallback is
limited to bounded player-data discovery.  These constraints keep every Rai
row partial rather than claiming full upstream parity.
