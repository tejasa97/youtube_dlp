# Vimeo contextual video URL evidence

Status: compatible for the pinned public Vimeo child-video routes that carry a
channel, group, album, or showcase context.

Pinned behavioral reference:
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
`yt_dlp/extractor/vimeo.py` `VimeoIE._VALID_URL` and its channel, group, and
album URL tests.

## Supported routes

- `https://vimeo.com/channels/{safe-slug}/{numeric-id}`
- `https://vimeo.com/groups/{safe-slug}/videos/{numeric-id}`
- `https://vimeo.com/album/{safe-id}/video/{numeric-id}`
- `https://vimeo.com/showcase/{safe-id}/video/{numeric-id}`

The extractor fetches the exact canonical contextual page instead of collapsing
it to `https://vimeo.com/{id}`. That same token-free contextual URL is supplied
as the player-config Referer and retained as `webpage_url`.

Routing fails closed before network access for non-HTTPS schemes, credentials,
ports, query strings, fragments, encoded separators, lookalike hosts, malformed
route shapes, unsafe collection slugs, and nonnumeric or overlong video IDs.
Ordinary numeric and player URLs, as well as channel/user/group playlist roots,
retain their existing dispatch behavior.

## Evidence

| Requirement | Automated evidence |
| --- | --- |
| Four exact contextual families, page identity, config Referer | `TestVimeoContextualVideoRoutesPreservePageAndReferer` |
| Unsafe and ambiguous route rejection before I/O | `TestVimeoContextualVideoRoutingRejectsUnsafeAndAmbiguousInputs` |
| Route classification, canonical round trip, dispatch invariants | `FuzzClassifyVimeoContextVideoURL` |
| Existing video/config behavior | existing `TestVimeo*` extraction tests |
| Existing playlist dispatch behavior | existing Vimeo channel/user/group playlist tests |

Fixture provenance is recorded in
`conformance/extractors/vimeo/PROVENANCE.md`. All responses and identifiers are
synthetic and deterministic.

## Deliberate limits

Numeric public showcase and album enumeration is covered separately. This
increment does not add slug/embed/password forms, unlisted-hash handling,
authenticated/private media, events/live archives, or arbitrary contextual
subpaths.
