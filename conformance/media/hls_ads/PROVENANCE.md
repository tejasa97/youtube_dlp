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

## EXT-X-DATERANGE SCTE-35 (Go extension beyond pinned reference)

`mixed-daterange-vod.m3u8` and `delta-daterange-midbreak.m3u8` exercise
validated `EXT-X-DATERANGE` SCTE-35 advertisement boundaries. The pinned
reference at commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` does **not**
decode DATERANGE SCTE-35 payloads.

Recognition rules:

- the raw playlist line must begin at byte zero with exact `#EXT-X-DATERANGE:`;
- leading whitespace before `#` is a rejected pseudo-tag and is ignored (the line
  does not participate in SCTE-35 processing);
- trailing ASCII spaces or tabs on an otherwise exact line are ignored;
- attribute lists use the strict HLS parser with duplicate-attribute rejection;
- a non-empty bounded `ID` is required when `SCTE35-OUT`, `SCTE35-IN`, or
  `SCTE35-CMD` is present; an explicitly present but empty directional value
  fails closed;
- only exact uppercase `SCTE35-OUT`, `SCTE35-IN`, and `SCTE35-CMD` names are
  inspected; ordinary dateranges without those attributes remain ignored.

Accepted SCTE-35 values must be even-length `0x`-prefixed hexadecimal strings
within an explicit decoded-size bound. Each value decodes to one complete
`splice_info_section` with `table_id` `0xFC`, a consistent 12-bit
`section_length`, valid CRC-32/MPEG-2, `encrypted_packet` cleared, and bounded
`splice_insert` or `time_signal` command fields:

- `SCTE35-OUT` / `SCTE35-IN` accept structurally valid `splice_insert` or
  `time_signal` commands and derive direction from the attribute name;
- `SCTE35-CMD` accepts only structurally valid `splice_insert` commands and
  derives direction from `out_of_network_indicator`;
- ambiguous lines with more than one directional attribute, malformed or
  duplicate attributes, conflicting direction, unsupported commands, encrypted
  packets, invalid CRC, or oversized payloads fail closed as invalid playlists.

Fixture hex payloads are generated deterministically from explicit SCTE-35 bit
layouts (program-splice and component-splice `splice_insert` with global
`splice_immediate_flag`, specified and unspecified `splice_time` branches,
timed `splice_insert`, component `break_duration`, `time_signal`, and negative
cases including legacy 14-bit false `splice_time` encodings) with MPEG-2
CRC-32. No network capture or Python runtime is used.
