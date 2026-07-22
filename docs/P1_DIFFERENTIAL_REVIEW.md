# Phase 1 differential conformance review

## Review basis

The behavioral reference is the read-only `yt-dlp/yt-dlp` checkout at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. Production Go code does not read
that checkout and no build or runtime path invokes Python. Reference-derived
fixtures are checked in as sanitized, deterministic data with provenance.

The generic differential runner proves exact, ordered, set-like, ignored,
tolerance, missing-versus-null, URL-redaction, format-selection, filename, and
checksum comparison rules. Site corpora then assert normalized behavior
directly in their extractor tests. This is intentional: a live Python oracle
would make the Python-free gate non-hermetic and would make future upstream
changes silently alter the expected result.

Critical semantics for this review are media identity, availability and
authentication classification, normalized format URL/protocol, live state,
playlist entry identity/order/laziness, selected filename, and output bytes.
Optional metadata outside a pilot corpus is a known deviation, not an ignored
critical difference.

## Reviewed corpus

| Risk and corpus | Reference basis | Automated review | Outcome |
| --- | --- | --- | --- |
| comparison policy | pinned synthetic reference/Go documents | `internal/differential` tests and `conformance/differential/pilot` | exact and policy-based differences are detected; no unexplained ignore rule |
| generic/direct media | normalized common result and local media server | `internal/extractor` and `pkg/ytdlp` direct-download tests | identity, format selection, filename, and bytes agree with the declared corpus |
| YouTube video, challenge, playlist, live | pinned player and renderer behavior | YouTube extractor, EJS, playlist, and HLS tests | selected metadata, transformed URLs, continuation order, and live HLS state agree |
| Vimeo protected manifest flow | pinned `_parse_config` and webpage behavior | Vimeo extractor tests | progressive/HLS/DASH normalization and protected request profile agree |
| Twitch live | pinned GraphQL and Usher behavior | Twitch extractor plus HLS end-to-end tests | metadata, signed manifest query semantics, live polling, and output order agree |
| SoundCloud track, set, user tracks | pinned client discovery, resolve, transcoding, and pagination behavior | SoundCloud extractor tests | format IDs/protocols, metadata, static order, and lazy continuation order agree |
| TikTok protected public video | pinned hydration and web-format behavior | TikTok extractor and impersonation tests | public/private/blocked statuses, normalized formats, and explicit profile agree |
| authenticated boundary | project-owned reserved-origin contract | synthetic-auth extractor and real operation-cookie-jar tests | auth scoping, categorized failure, and secret exclusion meet the declared contract |
| SVT Play regional video | pinned SVT single-video API behavior | SVT extractor tests | localized metadata, HLS/DASH/direct formats, subtitles, live state, and geo failure agree |

Each reference-derived corpus has a colocated `PROVENANCE.md`. The synthetic
authenticated corpus identifies itself as project-owned rather than claiming
upstream derivation.

## Reviewed differences

The capability manifest records all accepted pilot boundaries, including
unsupported dynamic SegmentBase/SIDX, hierarchical SIDX references and DASH
dynamic/unfragmented/format-incompatible multi-period composition, broader
postprocessing, wider YouTube renderers and authentication, Twitch
VOD/chat/entitlements, Vimeo password/showcase flows, expanded SoundCloud
surfaces, TikTok signing and slideshows, SVT series/geo bypass, additional
browser profiles and cookie stores, and automatic plugin discovery/signing.
Bounded static SegmentBase `indexRange` expansion and compatible fragmented
multi-period composition were added after the original Phase 1 review and are
covered by `docs/DASH_SIDX_EVIDENCE.md` and
`docs/DASH_MULTI_PERIOD_EVIDENCE.md`. The remaining items are visible feature
limits; none is hidden by a differential ignore rule.

No critical semantic difference remains unreviewed in the Phase 1 pilot
corpus. Expanding a pilot requires new attributable fixtures and passing
evidence before its compatibility target can be widened.
