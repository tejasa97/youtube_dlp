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

The pinned implementation also maps Schema.org `InteractionCounter` actions to
view, like, dislike, and comment counts, gives the legacy `interactionCount`
field precedence for views, and reads `aggregateRating.ratingValue`. The Go
implementation preserves those rules over at most 128 statistics per media
object. Relaxed string counts remove the same comma, period, and plus
separators as the reference before bounded int64 conversion.

Schema.org `hasPart` Clip objects follow the pinned chapter model: the first
missing start defaults to zero, a missing end may use the next explicit start,
a later missing start may use the prior end, and the final missing end may use
the media duration. The Go implementation additionally rejects missing titles,
unresolvable boundaries, negative/reversed intervals, overlaps, and more than
256 clips rather than emitting an ambiguous timeline.

JSON-LD `uploadDate` accepts the pinned machine-date corpus relevant to
Schema.org: RFC3339, compact or colonized numeric offsets, timezone-less
fractional timestamps interpreted as UTC, spaced ISO timestamps, slash dates,
and compact YYYYMMDD forms. Inputs are bounded to 128 bytes and pre-epoch dates
are omitted. Locale-dependent human date strings remain outside this corpus.

The Go implementation additionally recognizes Schema.org `embedUrl` when a
VideoObject/AudioObject has no `contentUrl`. This is an intentional native
improvement rather than a pinned-reference parity claim. It never follows the
URL generically: the candidate must pass the same exact provider host, route,
userinfo, port, fragment, and encoded-separator policy as an explicit HTML
embed. A declared `contentUrl` remains authoritative.
If any JSON-LD `contentUrl` produces a valid media result, that result takes
precedence over all embed-only candidates from the same document.

`open_graph.html`, `json_ld.html`, `json_ld_embed.html`, and
`metadata_unsafe.html` were independently authored for this repository. They
contain only reserved hosts, relative paths, and inert metadata. No upstream
webpage or service response was copied.

The current shared `Extraction` model supports root URL results, playlists,
and media. One native-provider embed is a root URL result; multiple embeds are
an ordered playlist; structured metadata is one page media result whose
deduplicated URLs are formats.
