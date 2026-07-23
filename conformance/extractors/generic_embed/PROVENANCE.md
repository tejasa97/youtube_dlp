# Generic embed discovery fixtures

Reference repository: `yt-dlp/yt-dlp`

Reference commit: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

Reference implementation: `yt_dlp/extractor/generic.py`,
`GenericIE._real_extract` and `GenericIE._extract_embeds`

The pinned reference asks registered extractors to identify supported embeds,
returns a single URL result when one embed is found, and otherwise returns an
ordered playlist. If no registered embed is accepted,
`GenericIE._extract_embeds` next examines Schema.org VideoObject metadata,
Twitter player streams, and OpenGraph video/audio metadata in that order.
The fixtures preserve this observable precedence, metadata normalization,
relative URL resolution, direct/manifest dispatch, Referer propagation, and
canonical de-duplication.

All hosts, identifiers, titles, and markup were independently authored for this
repository. The media identifiers are inert synthetic values, the unsupported
hosts use reserved domains, and no page, player response, media, credential,
token, or user data was copied from a service. Tests use an in-memory transport
and make no network request.

The Go implementation is deliberately narrower than the reference. It examines
only `iframe[src]`, `embed[src]`, `object[data]`, and
`meta[name|property=twitter:player][content]` for provider embeds. Its metadata
fallback accepts only context-bearing JSON-LD VideoObject/AudioObject `contentUrl`,
`twitter:player:stream`, and exact `og:video`/`og:audio` properties. It does not
execute JavaScript, parse arbitrary player configuration, follow an iframe, or
treat ordinary links as media. Provider candidates must pass an existing
built-in embed policy; metadata candidates must pass the documented direct
audio/video or native-manifest allowlist.

For JSON-LD media, the pinned `InfoExtractor._json_ld` implementation also
derives uploader, artist, upload timestamp, content size, bitrate, dimensions,
interaction count, and keywords. The synthetic fixture exercises those fields
with independently authored values. The Go parser accepts bounded JSON numbers
or decimal strings, ISO-8601 timestamps, string/object people, and string/list
keywords; malformed optional values are omitted rather than poisoning valid
media discovery.

`open_graph.html`, `json_ld.html`, and `metadata_unsafe.html` were independently
authored for this repository. They contain only reserved hosts, relative paths,
and inert metadata. No upstream webpage or service response was copied.

The current shared `Extraction` model supports root URL results, playlists,
and media. One native-provider embed is a root URL result; multiple embeds are
an ordered playlist; structured metadata is one page media result whose
deduplicated URLs are formats.
