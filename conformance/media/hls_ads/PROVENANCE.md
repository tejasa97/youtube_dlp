# HLS advertisement marker fixture provenance

The fixture in this directory is deterministic and synthetic. Segment names,
payloads, media sequences, and marker payloads are invented; no network
capture, account data, credential, or Python runtime is used.

The marker behavior is attributed to the read-only yt-dlp reference checkout
at commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/downloader/hls.py`, specifically the local functions
`is_ad_fragment_start` and `is_ad_fragment_end` and the ordered
`if`/`elif` state transition in `HlsFD.real_download`.

The Go implementation intentionally reproduces only that exact,
case-sensitive grammar after trimming each playlist line:

- Anvato start: prefix `#ANVATO-SEGMENT-INFO` and substring `type=ad`;
- Uplynk start: prefix `#UPLYNK-SEGMENT` and suffix `,ad`;
- Anvato end: prefix `#ANVATO-SEGMENT-INFO` and substring `type=master`;
- Uplynk end: prefix `#UPLYNK-SEGMENT` and suffix `,segment`.

Start wins when one Anvato line contains both tokens. No CUE, DATERANGE,
SCTE, or heuristic ad detection is inferred.

Unlike the pinned implementation's compact counter for retained fragments,
the Go parser retains the physical HLS media sequence and part identity for
advertisements and media alike. Filtering occurs only when building the
download plan. This keeps live/delta deduplication and implicit AES-128 IVs
standards-correct while producing the same ad-suppressed media output.
