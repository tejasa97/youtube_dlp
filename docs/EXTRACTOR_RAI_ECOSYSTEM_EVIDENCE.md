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
`TestRaiFilteredPlaylists` covers the two upstream selector forms, while
`TestRaiNewsAndCulturaEscapedPlayerData` covers HTML-escaped current-player
data. The two Rai fuzz targets preserve route and media-URL safety invariants.

Relinker requests require a redirect-disabled, credential-isolated operation
transport. URLs, JSON/XML bodies, nested
XML, formats, subtitles, and playlist entries are bounded. Geo placeholder
media is categorized as region restricted; non-empty DRM licenses are not
treated as playable. F4M/HDS remains unsupported and is intentionally not
advertised as a download format.
