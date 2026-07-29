# HDS protocol conformance provenance

The deterministic bounded Adobe HDS/F4M protocol layer in this lane was
derived by inspection only from the read-only `yt-dlp` checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- `yt_dlp/downloader/f4m.py` — `FlvReader` (box framing), `read_abst`
  (bootstrap parsing), `build_fragments_list` (segment × fragment pair
  derivation), `real_download` (URL construction with `SegN-FragN` suffix,
  FLV header + optional metadata tag + per-fragment `mdat` extraction).

The supported subset is intentionally narrow:

- Unencrypted VOD F4M/HDS only.
- Live ABST is rejected (`flags & 0x20`).
- DRM-attached media (`drmAdditionalHeaderId`, `drmAdditionalHeaderSetId`)
  is filtered or rejected with `ErrUnsupportedDRM`.
- Cross-host foreign schemes are rejected; userinfo in any URL is rejected.
- Inline and external bootstrapInfo sources are both supported; the
  external URL wins when both are present, matching `_parse_bootstrap_node`.

Fixtures are synthetic and deterministic. The Go implementation imports no
code, runtime, or build dependency from that checkout or from Python.

Validation:

- `go build ./...` succeeds on darwin/amd64, darwin/arm64, linux/amd64,
  and windows/amd64.
- `go test ./internal/protocol/hds -count=1 -race` passes.
- Three fuzz targets (`FuzzParseManifest`, `FuzzParseBootstrap`,
  `FuzzFixBareAmpersands`) each run for at least 10 seconds without
  finding new corpora.
- `go run ./cmd/paritycheck` validates 75 capabilities without flagging
  this lane as a deviation.

Remaining deviations:

- AES-128 encrypted HDS is not implemented.
- Live HDS is rejected (see above).
- The F4M 2.0 schema extensions beyond the documented VOD corpus are not
  consumed.
