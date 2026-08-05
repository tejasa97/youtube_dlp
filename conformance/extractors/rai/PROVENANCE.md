# Rai extractor-family provenance

Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/extractor/rai.py`.

Synthetic evidence spans `internal/extractor/rai_test.go` and the new
`internal/extractor/rai_mp4_test.go`; together they derive URL families,
Rai JSON fields, `Rai` relinker User-Agent, XML `output=64` handling,
audio-only HLS normalization, geo placeholder detection, and explicit
RaiPlay/RaiPlaySound playlist re-entry from `RaiBaseIE`, `RaiPlayIE`,
`RaiPlaySoundIE`, their live/playlist forms, and the legacy Rai classes.
The MP4 file attributes the bounded `_create_http_urls` synthesis path -
credential-isolated no-redirect HEAD availability probe, signed RawQuery
byte-order preservation, manifest quality-list parsing, percentage-and-roof
bitrate matching, table-default dimensions, last-candidate selection,
float tbr coercion, and cap/audio-only/live suppression - entirely to the
pinned `_MANIFEST_REG`, `_QUALITY`, `percentage`, and `get_format_info`
definitions in `yt_dlp/extractor/rai.py`.  Fixtures contain no copied
webpages, cookies, signed Rai URLs, or production tokens.

The Rai F4M branch is now wired to the generic bounded HDS/F4M VOD downloader
from #163 through metadata-only `f4m_native` format emission. The extractor
does not parse or download HDS. `raiF4MManifestURL` preserves existing signed
raw-query bytes and order, normalizes only the pinned
`manifest#live_hds.f4m` spelling, appends the pinned Adobe compatibility
parameters without duplicating exact pinned controls, and rejects conflicting
controls, arbitrary fragments, userinfo, and private media URLs. Product-level
bridge fixtures prove selection, HDS dispatch, ordered FLV assembly, transport
credential isolation, atomic cleanup, and stable live/DRM/error categories.
The stale no-HDS claim is therefore closed only for bounded unencrypted Rai
F4M VOD; the direct MP4 path remains attributed to `_create_http_urls` and is
covered by the synthetic evidence below.
Direct HLS manifests remain delegated to the existing native HLS pipeline
rather than expanded by the extractor.  The legacy HTML fallback is
limited to bounded player-data discovery.  These constraints keep every
Rai row partial rather than claiming full upstream parity.

Attributable synthetic evidence (no production data):

- `internal/extractor.TestRaiF4MFormatEmissionPreservesSignedQuery`,
  `TestRaiF4MLegacyManifestShapeNormalizesWithoutDroppingQuery`,
  `TestRaiF4MExistingPinnedControlsRemainByteExact`, and
  `TestRaiF4MConflictingControlsAreRejected` attribute the pinned F4M URL
  normalization and exact-query/control-key policy. The extractor emits one
  `hds`/`f4m_native`/`flv` format and does not own HDS parsing or download.
- `engine.TestProductRaiF4MExtractionBridgesIntoHDSAndAssemblesFLV`
  proves the extracted selection enters the merged generic HDS path and
  assembles the deterministic FLV fixture in fragment order while preserving
  duplicate signed query parameters on every fragment and keeping credentials
  out of HDS manifest, bootstrap, and fragment requests after sensitive
  selection headers are seeded. It does not claim credential coverage for the
  earlier Rai page or relinker requests. `TestProductRaiF4MBridgeFailureCleansDestinationAndPreservesCategory`
  proves malformed-manifest rollback and invalid-input categorization, while
  `TestProductRaiF4MBridgeCancellationCleansDestination` proves cancellation
  stops before the HDS manifest fetch. `TestProductRaiF4MBridgeSizeBoundCleansDestinationAndPreservesCategory`
  proves the product byte caps surface the HDS size sentinel and roll back the
  destination. `TestProductRaiF4MBridgePreservesLiveAndDRMRestrictions`
  proves live and DRM remain unsupported and leave no destination.

- `internal/extractor.TestRaiMP4ManifestQualitiesStates` and
  `TestRaiMP4ManifestQualitiesBound` lock the three-state manifest return
  (unmatched, matched-no-quality, matched-with-list) and the bounded
  `raiMaxMP4Qualities` cap.
- `internal/extractor.TestRaiMP4URLPreservesSignedRawQueryOrder` and
  `TestRaiMP4URLRejectsUnsafeInputs` pin the `overrideUserAgentRule=mp4-<q>`
  template to a byte-exact relinker URL while rejecting userinfo,
  fragments, empty RawQuery, pre-existing overrides, and unsafe quality
  tokens.
- `internal/extractor.TestRaiMP4ProbeSuccessOnEmptyTwoHundred`,
  `TestRaiMP4ProbeRejectsNonTwoXXWithoutError`,
  `TestRaiMP4ProbeRejectsOversizedBody`,
  `TestRaiMP4ProbePropagatesContextCancellation`,
  `TestRaiMP4ProbeRejectsNonIsolatedTransport`, and
  `TestRaiMP4ProbeRejectsUnsafeProbeURL` lock the HEAD availability gate
  to the credential-isolated no-redirect transport, the 14 KiB body
  ceiling, and the cancellation/isolation contract.
- `internal/extractor.TestRaiMP4SynthesizeProbeFailurePreservesBaseHLS`,
  `TestRaiMP4SynthesizeProbeSuccessEmitsAllManifestQualities`,
  `TestRaiMP4SynthesizeCancellationPropagates`,
  `TestRaiMP4SynthesizeLiveSkipsSynthesis`,
  `TestRaiMP4SynthesizeUnmatchedManifestSkipsSynthesis`,
  `TestRaiMP4SynthesizeAudioOnlySkipsSynthesis`,
  `TestRaiMP4SynthesizeCapRespectsRaiMaxFormats`, and
  `TestRaiMP4SynthesizeProbeURLPrepFailurePreservesBaseHLS` confirm that
  every non-cancellation MP4 prep/probe failure preserves the base HLS
  extraction and only a successful probe advances to manifest-regex
  parsing, mirroring the pinned order in `_create_http_urls`.
- `internal/extractor.TestRaiMP4FormatWildcardSingleBaseUnder300UsesDefault`,
  `TestRaiMP4FormatWildcardSingleBaseAbove300UsesDerived`,
  `TestRaiMP4FormatWildcardSingleBaseExactly300FallsThrough`,
  `TestRaiMP4FormatWildcardSingleBase301Derives300`, and
  `TestRaiMP4FormatWildcardSingleBase399Derives300` cover the wildcard
  `*` derivation path including the strict `br > 300` rule.
- `internal/extractor.TestRaiMP4FormatExplicitQualityPicksLastBitrateMatch`,
  `TestRaiMP4FormatBitrateMatchPreferredOverResolutionMatch`, and
  `TestRaiMP4FormatFloatBaseTBRIsHonored` lock the bitrate-priority,
  last-candidate, and float-to-int coercion semantics; the
  `TestRaiMP4FormatMissingDimensionsOmitted` test locks the no-copy-no-table
  dimension omission.
- `internal/extractor.TestRaiMP4FormatCommaListUnknownSkipsUnmatchedToken`
  and `TestRaiMP4FormatCommaListUnknownResolvesViaBitrate` cover
  `_777,9999`-style comma lists where one token has no table entry and no
  base match (skip) while another resolves via a near-bitrate base.

Audited non-HDS deviations (unchanged from the previous cycle):

- Authentication-gated Rai pages, dynamic-page schema variants, and
  remote-schema HLS variants remain outside the public fixture-backed
  scope.  These constraints keep every Rai row partial rather than
  claiming full upstream parity.
