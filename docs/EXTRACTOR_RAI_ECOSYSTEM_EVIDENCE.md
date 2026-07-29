# Rai ecosystem evidence

The product registers `raiplay`, `raiplay_live`, `raiplay_playlist`,
`raiplaysound`, `raiplaysound_live`, `raiplaysound_playlist`, `rai`,
`rainews`, `raicultura`, and `raisudtirol` before generic extraction.

`internal/extractor.TestRaiRoutingMatrixAndHostHardening` covers exact-host
routing and hostile forms. `TestRaiPlayRelinkerMetadataAndAudioCodec` covers
Rai JSON, bounded relinker XML, the required `User-Agent: Rai`, credential
isolation, subtitles, and audio-only HLS normalization.
`TestRaiRelinkerGeoDRMAndMalformed` exercises `output=64`, geo placeholder,
DRM-license, malformed XML, private-media, and HTTP-status paths.
`TestRaiFilteredPlaylists` and `TestRaiSoundUnfilteredPlaylistDescription`
cover the RaiPlay and selected/unselected RaiPlay Sound playlist-description
sources, while
`TestRaiNewsAndCulturaEscapedPlayerData` covers HTML-escaped current-player
data. `TestRaiLiveAndSoundIdentityFlows`, `TestRaiSudtirolSMILIdentityAndHLS`,
`TestRaiIdentityCancellationAndSecretSafety`, and
`TestRaiIdentityFallbackAndTimestampPolicies` cover route-specific identity,
cancellation, secret-safety, URL-ID fallback, and timestamp contracts.
`TestRaiSoundPodcastMetadata` covers top-level RaiPlay Sound podcast metadata
and its live-card fallback for series and images.
`TestRaiPlaylistSkipsBrokenOrUnavailableSets` and
`TestRaiPlaylistPreservesCancellationAndMeaningfulSetFailures` cover the
non-fatal set boundary. `TestRaiPublicURLRejectsLocalAndIPLiteralVariants` and
`TestRaiThumbnailsAreStableAndBounded` cover media-host rejection and output
ordering.

The direct-MP4 synthesis path (pinned `_create_http_urls`) is attributed to
`internal/extractor/rai_mp4_test.go`. `TestRaiMP4ManifestQualitiesStates`
and `TestRaiMP4ManifestQualitiesBound` lock the three-state manifest match
return and the bounded quality-list cap. `TestRaiMP4URLPreservesSignedRawQueryOrder`
and `TestRaiMP4URLRejectsUnsafeInputs` pin the
`overrideUserAgentRule=mp4-<quality>` template to a byte-exact relinker URL
while rejecting userinfo, fragments, empty RawQuery, pre-existing
overrides, and unsafe quality tokens. The `TestRaiMP4Probe*` family locks
the HEAD availability gate to the credential-isolated no-redirect
transport, the 14 KiB body ceiling, and the cancellation/isolation contract.
`TestRaiMP4SynthesizeProbeFailurePreservesBaseHLS` and
`TestRaiMP4SynthesizeProbeURLPrepFailurePreservesBaseHLS` confirm that
every non-cancellation MP4 prep/probe failure preserves the base HLS
extraction without surfacing an error. `TestRaiMP4SynthesizeProbeSuccessEmitsAllManifestQualities`,
`TestRaiMP4SynthesizeLiveSkipsSynthesis`,
`TestRaiMP4SynthesizeUnmatchedManifestSkipsSynthesis`,
`TestRaiMP4SynthesizeAudioOnlySkipsSynthesis`,
`TestRaiMP4SynthesizeCapRespectsRaiMaxFormats`, and
`TestRaiMP4SynthesizeCancellationPropagates` lock the live/audio-only/cap
suppression paths and the cancellation contract.
`TestRaiMP4FormatWildcardSingleBaseUnder300UsesDefault`,
`TestRaiMP4FormatWildcardSingleBaseAbove300UsesDerived`,
`TestRaiMP4FormatWildcardSingleBaseExactly300FallsThrough`,
`TestRaiMP4FormatWildcardSingleBase301Derives300`, and
`TestRaiMP4FormatWildcardSingleBase399Derives300` cover the wildcard `*`
derivation including the strict `br > 300` rule (singleTBR 301/399 both
emit `format_id https-300`).
`TestRaiMP4FormatExplicitQualityPicksLastBitrateMatch`,
`TestRaiMP4FormatBitrateMatchPreferredOverResolutionMatch`,
`TestRaiMP4FormatFloatBaseTBRIsHonored`,
`TestRaiMP4FormatMissingDimensionsOmitted`, and
`TestRaiMP4FormatCommaListUnknown*` lock the bitrate-priority,
last-candidate, float-to-int coercion, missing-dimensions omission, and
comma-list synthesis semantics; `TestRaiMP4FormatExplicitQualityWithoutMatchIsSkipped`
verifies that genuinely distant base formats do not satisfy
`raiMP4FormatResolves`.

`pkg/ytdlp.TestProductRegistryRoutesRaiAndPlaylistReentry` exercises product
registry selection and typed playlist re-entry. The two Rai fuzz targets
preserve route and media-URL safety invariants.

Relinker requests require a redirect-disabled, credential-isolated operation
transport. URLs, JSON/XML bodies, nested XML, formats, subtitles, thumbnails,
and playlist entries are bounded. Geo placeholder media is categorized as
region restricted; non-empty DRM licenses are not treated as playable.
A generic bounded HDS/F4M VOD downloader exists in this product
(`internal/protocol/hds`, merged via #163).  This Rai foundation
intentionally does NOT emit or wire F4M formats into Rai extractions;
linking the Rai base extractor to the HDS/F4M downloader and
advertising the corresponding parity row are deferred to an
integration PR owned outside this cycle.  No HDS-specific Rai product
files are introduced here.  Direct MP4 synthesis is attributed to the
pinned `yt_dlp/extractor/rai.py@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.
The remaining non-HDS residual audit concluded that untested
authenticated pages, dynamic-page schema variants, and remote-schema
HLS variants are kept outside the public fixture-backed scope; these
constraints keep every Rai row partial rather than claiming full
upstream parity.
