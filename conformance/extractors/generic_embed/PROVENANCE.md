# Generic embed discovery fixtures

Reference repository: `yt-dlp/yt-dlp`

Reference commit: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

Reference implementation: `yt_dlp/extractor/generic.py`,
`GenericIE._real_extract` and `GenericIE._extract_embeds`

The pinned reference asks registered extractors to identify supported embeds,
returns a single URL result when one embed is found, and otherwise returns an
ordered playlist. These fixtures preserve only that observable control flow:
HTML embed-bearing attributes, document order, canonical target de-duplication,
and the distinction between one and multiple unique targets.

All hosts, identifiers, titles, and markup were independently authored for this
repository. The media identifiers are inert synthetic values, the unsupported
hosts use reserved domains, and no page, player response, media, credential,
token, or user data was copied from a service. Tests use an in-memory transport
and make no network request.

The Go implementation is deliberately narrower than the reference. It examines
only `iframe[src]`, `embed[src]`, `object[data]`, and
`meta[name|property=twitter:player][content]`; it does not execute JavaScript,
parse arbitrary player configuration, follow an iframe, or treat ordinary
links as media. A candidate is retained only when one of the product's existing
built-in embed URL policies accepts it.

At this lane boundary, the common `Extraction` type can represent media or a
playlist but not a root URL result. The single fixture is temporarily surfaced
as a one-entry playlist. Primary integration will replace that localized
construction with the shared root URL-result variant; the discovery corpus and
ordering expectations do not depend on the temporary representation.
