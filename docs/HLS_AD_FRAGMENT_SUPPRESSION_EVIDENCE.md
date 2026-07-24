# HLS attributed ad-fragment suppression evidence

Status: compatible for the bounded Anvato and Uplynk marker corpus described
below.

## Behavior

The native HLS parser recognizes the same case-sensitive textual state markers
used by the pinned reference:

- Anvato start: a line beginning `#ANVATO-SEGMENT-INFO` containing `type=ad`
- Anvato end: a line beginning `#ANVATO-SEGMENT-INFO` containing `type=master`
- Uplynk start: a line beginning `#UPLYNK-SEGMENT` ending `,ad`
- Uplynk end: a line beginning `#UPLYNK-SEGMENT` ending `,segment`

Lines are trimmed before matching. Start wins when one Anvato line contains
both tokens. The state is Boolean rather than nested: repeated starts remain
active and one end clears the state. An unmatched start marks every following
media URI in that playlist snapshot as an advertisement.

Attributed ordinary segments and low-latency parts remain represented during
parsing with their physical sequence, range, key, map, and discontinuity
state. The downloader keeps those identities while reconciling live polls but
excludes advertisements before planning network work. Ad media, ad-only keys,
and ad-only initialization maps are not requested; ads do not consume the
download segment limit or appear in the published artifact. An all-ad
playlist fails through the existing no-segments contract without publishing
output.

## Provenance

The marker grammar and Boolean state machine derive from the local read-only
reference `yt-dlp/yt-dlp` pinned at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`yt_dlp/downloader/hls.py` (`is_ad_fragment_start`,
`is_ad_fragment_end`, the fragment-counting pass, and fragment construction).

All manifests and media bytes used by tests are synthetic. Production and
tests do not import or execute Python and do not depend on the reference
checkout.

## Automated evidence

- `internal/protocol/hls.TestAdvertisementMarkerExactGrammar`
- `internal/protocol/hls.TestParseAdvertisementStateOrderSequencesAndReset`
- `internal/protocol/hls.TestParseAdvertisementDeltaPartsPreserveMetadataAndRanges`
- `internal/protocol/hls.TestDownloadSuppressesAttributedVODAdvertisements`
- `internal/protocol/hls.TestDownloadLiveAdvertisementReclassificationAndCompleteReplacement`
- `internal/protocol/hls.TestDownloadAdvertisementKeysMapsAndPhysicalAESIV`
- `internal/protocol/hls.TestDownloadAllAdvertisementsReturnsNoSegmentsWithoutScratch`
- `internal/protocol/hls.FuzzParse`
- `internal/protocol/hls.FuzzAdvertisementMarkers`
- `pkg/ytdlp.TestClientHLSSuppressesAttributedAdFragments`

## Known deviations and improvements

- Only the attributed Anvato and Uplynk marker families above are recognized.
  SCTE-35, `EXT-X-CUE-*`, `EXT-X-DATERANGE`, asset lists, URI heuristics, and
  markerless server-side ad insertion are not inferred.
- Go applies the same suppression to its native live and low-latency polling
  paths. The pinned native HLS downloader delegates correctly classified live
  streams to ffmpeg before reaching this marker parser.
- Marker state starts fresh for each fetched playlist. A sliding snapshot that
  begins inside an ad without its start marker cannot be inferred safely.
- Physical HLS sequence identities are preserved across ads for live/delta
  de-duplication and implicit AES IVs. The pinned native construction compacts
  accepted sequence numbers after ads.
- The Go event model does not reproduce the pinned console suffix reporting
  the number of excluded ad URIs.
