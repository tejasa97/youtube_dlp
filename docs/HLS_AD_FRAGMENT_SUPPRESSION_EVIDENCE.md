# HLS attributed ad-fragment suppression evidence

Status: compatible for the bounded Anvato/Uplynk marker corpus and the
conservative exact-case `EXT-X-CUE-{OUT,OUT-CONT,IN}` extension described
below.

## Behavior

The native HLS parser recognizes the same case-sensitive textual state markers
used by the pinned reference:

- Anvato start: a line beginning `#ANVATO-SEGMENT-INFO` containing `type=ad`
- Anvato end: a line beginning `#ANVATO-SEGMENT-INFO` containing `type=master`
- Uplynk start: a line beginning `#UPLYNK-SEGMENT` ending `,ad`
- Uplynk end: a line beginning `#UPLYNK-SEGMENT` ending `,segment`

It also recognizes these de-facto cue tags (not RFC 8216 `EXT-X-DATERANGE`
SCTE-35 binary decoding):

- Cue start / continue: exact `#EXT-X-CUE-OUT` or `#EXT-X-CUE-OUT-CONT`, or the
  same names with a conventional `:` payload that is accepted and ignored
- Cue end: exact `#EXT-X-CUE-IN` or `#EXT-X-CUE-IN:`… with ignored payload

Anvato and Uplynk markers match the pinned reference after each playlist line
is trimmed. Cue tags are matched against the raw line and must begin at byte
zero: a leading space or tab before `#EXT-X-CUE-*` is ignored as a pseudo-tag
and does not change advertisement state. Trailing ASCII spaces or tabs on an
otherwise exact cue line are ignored consistently. Start wins when one Anvato
line contains both tokens. Cue lookalikes that only share a prefix without an
immediate `:` (for example `#EXT-X-CUE-OUTING`) are rejected;
`#EXT-X-CUE-OUT-CONT` is not treated as `#EXT-X-CUE-OUT`. The state is Boolean
rather than nested: repeated starts remain active and one end clears the
state. An unmatched start marks every following media URI in that playlist
snapshot as an advertisement. `EXT-X-CUE-OUT-CONT` present in a new snapshot
can re-establish advertisement state for a sliding live/delta window that
begins mid-break.

Attributed ordinary segments and low-latency parts remain represented during
parsing with their physical sequence, range, key, map, and discontinuity
state. The downloader keeps those identities while reconciling live polls but
excludes advertisements before planning network work. Ad media, ad-only keys,
and ad-only initialization maps are not requested; ads do not consume the
download segment limit or appear in the published artifact. An all-ad
playlist fails through the existing no-segments contract without publishing
output.

## Provenance

The Anvato/Uplynk marker grammar and Boolean state machine derive from the
local read-only reference `yt-dlp/yt-dlp` pinned at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`yt_dlp/downloader/hls.py` (`is_ad_fragment_start`,
`is_ad_fragment_end`, the fragment-counting pass, and fragment construction).
That pin does not implement cue-tag advertisement suppression.

Cue handling is a deliberate Go-native extension based on exact uppercase tag
names observed as packager de-facto practice, with RFC 8216 consulted only for
ordinary HLS media-sequence / part / map / key / discontinuity context. Cue
payloads are not interpreted. `EXT-X-DATERANGE` SCTE-35 fields are not decoded.

All manifests and media bytes used by tests are synthetic. Production and
tests do not import or execute Python and do not depend on the reference
checkout.

## Automated evidence

- `internal/protocol/hls.TestAdvertisementMarkerExactGrammar`
- `internal/protocol/hls.TestParseAdvertisementStateOrderSequencesAndReset`
- `internal/protocol/hls.TestParseCueAdvertisementStateBoundariesAndOrdering`
- `internal/protocol/hls.TestParseCueLeadingWhitespaceRejectedTrailingAccepted`
- `internal/protocol/hls.TestParseAdvertisementDeltaPartsPreserveMetadataAndRanges`
- `internal/protocol/hls.TestParseCueAdvertisementMapKeyDiscontinuityAndAdOnly`
- `internal/protocol/hls.TestDownloadSuppressesAttributedVODAdvertisements`
- `internal/protocol/hls.TestDownloadSuppressesCueVODAdvertisements`
- `internal/protocol/hls.TestDownloadLiveAdvertisementReclassificationAndCompleteReplacement`
- `internal/protocol/hls.TestDownloadCueLiveOUTCONTReclassificationAndCompleteReplacement`
- `internal/protocol/hls.TestDownloadAdvertisementKeysMapsAndPhysicalAESIV`
- `internal/protocol/hls.TestDownloadCueAdvertisementKeysMapsAndPhysicalAESIV`
- `internal/protocol/hls.TestDownloadAllAdvertisementsReturnsNoSegmentsWithoutScratch`
- `internal/protocol/hls.TestDownloadCueAllAdvertisementsAndCancellation`
- `internal/protocol/hls.FuzzParse`
- `internal/protocol/hls.FuzzAdvertisementMarkers`
- `pkg/ytdlp.TestClientHLSSuppressesAttributedAdFragments`

## Known deviations and improvements

- `EXT-X-DATERANGE` SCTE-35 OUT/IN attributes, SCTE-35 binary / base64 payload
  parsing, interstitial asset lists, URI heuristics, generic `CLASS` strings,
  SCTE35-CMD-only signals, and markerless server-side ad insertion are not
  inferred.
- Go applies the same suppression to its native live and low-latency polling
  paths. The pinned native HLS downloader delegates correctly classified live
  streams to ffmpeg before reaching this marker parser, and still lacks cue-tag
  recognition at the pinned commit.
- Marker state starts fresh for each fetched playlist. A sliding snapshot that
  begins inside an ad without a start or `OUT-CONT` marker cannot be inferred
  safely.
- Physical HLS sequence identities are preserved across ads for live/delta
  de-duplication and implicit AES IVs. The pinned native construction compacts
  accepted sequence numbers after ads.
- The Go event model does not reproduce the pinned console suffix reporting
  the number of excluded ad URIs.
