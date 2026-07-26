# Rai ecosystem evidence

The product registers `raiplay`, `raiplay_live`, `raiplay_playlist`,
`raiplaysound`, `raiplaysound_live`, `raiplaysound_playlist`, `rai`,
`rainews`, `raicultura`, and `raisudtirol` before generic extraction.

`internal/extractor.TestRaiRoutingMatrixAndHostHardening` covers exact-host
routing and hostile forms. `TestRaiPlayRelinkerMetadataAndAudioCodec` covers
Rai JSON, bounded relinker XML, the required `User-Agent: Rai`, subtitles, and
audio-only HLS normalization. The two Rai fuzz targets preserve route and
media-URL safety invariants.

Relinker requests are redirect-disabled and credential-isolated when the
operation transport supplies that capability. URLs, JSON/XML bodies, nested
XML, formats, subtitles, and playlist entries are bounded. Geo placeholder
media is categorized as region restricted; non-empty DRM licenses are not
treated as playable. F4M/HDS remains unsupported and is intentionally not
advertised as a download format.
