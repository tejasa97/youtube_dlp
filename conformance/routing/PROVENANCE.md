# Routing-controls provenance

Source: read-only inspection of
`/path/to/yt-dlp-reference` at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Attributable reference surfaces:

- `yt_dlp/YoutubeDL.py::extract_info` maps
  `force_generic_extractor` to the explicit `Generic` extractor when no
  explicit extractor key is supplied.
- `yt_dlp/extractor/generic.py::_real_extract` recognizes
  `fixup_error`, `error`, `auto`, and `auto_warning`, repairs an unqualified
  dotted host, and otherwise emits a `ytsearch:` or configured prefix URL.
- `yt_dlp/options.py` documents `--force-generic-extractor` as hidden and
  exposes `--default-search PREFIX`.

The Go port deliberately narrows configured prefixes to the already
registered native pseudo-search extractors and uses fixed safe routing tokens
plus typed query overrides. This is an intentional bounded safety deviation:
it does not add Google Video or any other new backend, and it does not perform
live extractor discovery while classifying input.

Evidence:

- `pkg/ytdlp/routing_controls_test.go`
- `internal/cli/routing_controls_test.go`
- `internal/extractor/youtube_search_test.go`
- `docs/EXTRACTOR_ROUTING_CONTROLS_EVIDENCE.md`
