# HLS attributed ad-fragment suppression evidence

Status: compatible for the bounded Anvato/Uplynk marker corpus, the
conservative exact-case `EXT-X-CUE-{OUT,OUT-CONT,IN}` extension, and validated
`EXT-X-DATERANGE` SCTE-35 advertisement boundaries described below.

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

It also recognizes validated `EXT-X-DATERANGE` SCTE-35 boundaries when the raw
line begins at byte zero with exact `#EXT-X-DATERANGE:`:

- `SCTE35-OUT` / `SCTE35-IN` accept structurally valid `splice_insert` or
  `time_signal` commands and derive direction from the attribute name
- `SCTE35-CMD` accepts only structurally valid `splice_insert` commands and
  derives direction from `out_of_network_indicator`
- ordinary dateranges without those exact uppercase attributes remain ignored
- explicitly present but empty `SCTE35-OUT` / `SCTE35-IN` / `SCTE35-CMD` values
  fail closed
- malformed, ambiguous, encrypted, CRC-invalid, oversized, or structurally
  incomplete SCTE-35 section payloads fail closed as invalid playlists rather
  than silently changing ad state

Anvato and Uplynk markers match the pinned reference after each playlist line
is trimmed. Cue tags are matched against the raw line and must begin at byte
zero: a leading space or tab before `#EXT-X-CUE-*` is a rejected pseudo-tag
and does not change advertisement state. Trailing ASCII spaces or tabs on an
otherwise exact cue line are ignored consistently. DATERANGE SCTE-35 tags use
the same byte-zero rule: leading whitespace is a rejected pseudo-tag and is
ignored, while trailing ASCII spaces or tabs are ignored. Start wins when one Anvato line
contains both tokens. Cue lookalikes that only share a prefix without an
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
That pin does not implement cue-tag or DATERANGE SCTE-35 advertisement
suppression.

Cue handling is a deliberate Go-native extension based on exact uppercase tag
names observed as packager de-facto practice, with RFC 8216 consulted only for
ordinary HLS media-sequence / part / map / key / discontinuity context. Cue
payloads are not interpreted.

DATERANGE SCTE-35 handling is a further Go-native extension. Fixture hex
payloads are generated deterministically from explicit SCTE-35 bit layouts with
MPEG-2 CRC-32. SCTE 35 2023 and RFC 8216 provide structural context only;
segmentation-descriptor inference, generic `CLASS` matching, asset lists, URI
heuristics, and markerless ad detection are not implemented.

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
- `internal/protocol/hls.TestValidateSCTE35DirectionSpliceInsertAndTimeSignal`
- `internal/protocol/hls.TestValidateSCTE35RejectsMalformedPayloads`
- `internal/protocol/hls.TestApplyDaterangeSCTE35GrammarAndAmbiguity`
- `internal/protocol/hls.TestParseDaterangeSCTE35AdvertisementState`
- `internal/protocol/hls.TestParseDaterangeSCTE35RejectsInvalidLines`
- `internal/protocol/hls.TestParseDaterangeSCTE35DeltaPartsAndOrdinaryIgnored`
- `internal/protocol/hls.TestDownloadSuppressesAttributedVODAdvertisements`
- `internal/protocol/hls.TestDownloadSuppressesCueVODAdvertisements`
- `internal/protocol/hls.TestDownloadSuppressesDaterangeVODAdvertisements`
- `internal/protocol/hls.TestDownloadLiveAdvertisementReclassificationAndCompleteReplacement`
- `internal/protocol/hls.TestDownloadCueLiveOUTCONTReclassificationAndCompleteReplacement`
- `internal/protocol/hls.TestDownloadDaterangeLiveReclassificationAndDelta`
- `internal/protocol/hls.TestDownloadAdvertisementKeysMapsAndPhysicalAESIV`
- `internal/protocol/hls.TestDownloadCueAdvertisementKeysMapsAndPhysicalAESIV`
- `internal/protocol/hls.TestDownloadDaterangeAdvertisementKeysMapsAndPhysicalAESIV`
- `internal/protocol/hls.TestDownloadAllAdvertisementsReturnsNoSegmentsWithoutScratch`
- `internal/protocol/hls.TestDownloadCueAllAdvertisementsAndCancellation`
- `internal/protocol/hls.TestDownloadDaterangeAllAdvertisementsAndCancellation`
- `internal/protocol/hls.FuzzParse`
- `internal/protocol/hls.FuzzAdvertisementMarkers`
- `internal/protocol/hls.FuzzSCTE35Daterange`
- `internal/protocol/hls.FuzzSCTE35Payload`
- `engine.TestClientHLSSuppressesAttributedAdFragments`

## Known deviations and improvements

- Interstitial asset lists, URI heuristics, generic `CLASS` strings, base64
  SCTE-35 transport encodings, and markerless server-side ad insertion are not
  inferred.
- Go applies the same suppression to its native live and low-latency polling
  paths. The pinned native HLS downloader delegates correctly classified live
  streams to ffmpeg before reaching this marker parser, and still lacks cue-tag
  and DATERANGE SCTE-35 recognition at the pinned commit.
- Marker state starts fresh for each fetched playlist. A sliding snapshot that
  begins inside an ad without a start marker or validated DATERANGE/CUE
  continuation cannot be inferred safely.
- Physical HLS sequence identities are preserved across ads for live/delta
  de-duplication and implicit AES IVs. The pinned native construction compacts
  accepted sequence numbers after ads.
- The Go event model does not reproduce the pinned console suffix reporting
  the number of excluded ad URIs.
