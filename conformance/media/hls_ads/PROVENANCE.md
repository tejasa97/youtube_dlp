# HLS advertisement marker fixture provenance

The fixtures in this directory are deterministic and synthetic. Segment names,
payloads, media sequences, and marker payloads are invented; no network
capture, account data, credential, or Python runtime is used.

## Anvato / Uplynk (pinned reference)

The Anvato and Uplynk marker behavior is attributed to the read-only yt-dlp
reference checkout at commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/downloader/hls.py`, specifically the local functions
`is_ad_fragment_start` and `is_ad_fragment_end` and the ordered
`if`/`elif` state transition in `HlsFD.real_download`.

The Go implementation intentionally reproduces only that exact,
case-sensitive grammar after trimming each playlist line:

- Anvato start: prefix `#ANVATO-SEGMENT-INFO` and substring `type=ad`;
- Uplynk start: prefix `#UPLYNK-SEGMENT` and suffix `,ad`;
- Anvato end: prefix `#ANVATO-SEGMENT-INFO` and substring `type=master`;
- Uplynk end: prefix `#UPLYNK-SEGMENT` and suffix `,segment`.

Start wins when one Anvato line contains both tokens.

The pinned reference at that commit does **not** recognize
`EXT-X-CUE-OUT`, `EXT-X-CUE-OUT-CONT`, or `EXT-X-CUE-IN`.

## Conservative SCTE-style cue tags (Go extension)

`mixed-cue-vod.m3u8` and `delta-cue-midbreak.m3u8` exercise an additional,
exact-case cue-marker state machine that is intentionally broader than the
pinned downloader:

- `#EXT-X-CUE-OUT` and `#EXT-X-CUE-OUT:`… begin advertisement state;
- `#EXT-X-CUE-OUT-CONT` and `#EXT-X-CUE-OUT-CONT:`… confirm or re-establish
  advertisement state (including a sliding live/delta snapshot that starts
  mid-break);
- `#EXT-X-CUE-IN` and `#EXT-X-CUE-IN:`… end advertisement state before the
  following segment or part.

Cue payloads after `:` are accepted and ignored. Cue tags match the raw
playlist line and must begin at byte zero (leading spaces or tabs are
rejected pseudo-tags and do not change state); trailing ASCII spaces or tabs
are ignored. These tags are de-facto HLS extensions used by some packagers;
they are not RFC 8216 `EXT-X-DATERANGE` SCTE-35 binary payload decoding.
Lowercase names, prefix lookalikes, space-after-`#` pseudo-tags,
`EXT-X-DATERANGE`, SCTE-35 command/payload fields, asset lists, URI
heuristics, and markerless insertion are not inferred.

Unlike the pinned implementation's compact counter for retained fragments,
the Go parser retains the physical HLS media sequence and part identity for
advertisements and media alike. Filtering occurs only when building the
download plan. This keeps live/delta deduplication and implicit AES-128 IVs
standards-correct while producing the same ad-suppressed media output.
