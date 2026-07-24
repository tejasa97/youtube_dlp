# SponsorBlock metadata

This document describes the native, Python-free SponsorBlock metadata
foundation available through the public Go product API. The
foundation covers **metadata fetch, normalization, and opt-in chapter
marking**. Media cutting, FFmpeg removal, subtitle synchronization, and CLI
option exposure remain explicitly out of scope.

## What is wired

When a caller passes a `ytdlp.Request` with
`SponsorBlock.Enabled == true` and the operation targets a
YouTube-family extractor, the client performs a single bounded
SponsorBlock API lookup and writes the result to
`result.InfoJSON` under the key `sponsorblock_chapters`. Disabled
requests never touch the network.

The implementation derives its behavior from the pinned yt-dlp
reference at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
(`yt_dlp/postprocessor/sponsorblock.py`). The reference is treated
as a read-only behavioral mirror: it is never executed, imported,
or depended on at build time. The conformance fixtures in
`conformance/sponsorblock/` document this lineage.

## Public request option

```go
type SponsorBlockOptions struct {
    Enabled    bool
    Mark       bool
    Categories []string
    APIBase    string
}
```

`Enabled` is the only field that gates the stage. When false, no
network requests are issued, regardless of the other fields.

`Mark` requires `Enabled`. It overlays normalized SponsorBlock ranges onto the
ordinary `chapters` list without changing media bytes. Existing chapter fields
are preserved on uncovered fragments. When ordinary chapters are unavailable,
a full-duration background chapter is synthesized from the video title.
Overlaps preserve first-seen category order, and only fragments created by
the overlay are eligible for the pinned sub-second merge behavior; originally
tiny chapters remain intact.

`Categories` is the requested non-empty set of SponsorBlock category
identifiers. The list is treated as
caller-owned and is never mutated. Unknown identifiers, empty
entries, whitespace-only entries, and strings longer than 64
bytes are rejected by request validation. Duplicate identifiers
are de-duplicated deterministically by the first-seen index.

`APIBase` is the API origin. When empty, the implementation uses
`https://sponsor.ajay.app`. Custom bases are intended for
deterministic tests and self-hosted deployments that implement
the same API. Only `http` and `https` schemes with a non-empty
host are accepted.

## Supported extractor

Only YouTube-family extractors are supported. The package rejects
a SponsorBlock request against any other extractor with a
categorized `unsupported` error; the operation never silently
claims success. The supported extractor key is the YouTube watch
extractor (the `youtube` key).

## Endpoint contract

The client constructs the canonical endpoint using the first
four lowercase hex characters of `SHA-256(videoID)`:

```
GET <APIBase>/api/skipSegments/<prefix>
    ?service=YouTube
    &categories=<JSON array of strings>
    &actionTypes=<JSON array of strings: skip, poi, chapter>
```

The action types sent to the API are exactly `["skip", "poi",
"chapter"]`; the client never requests any other values. The
response is matched by `videoID` against the requested video ID.
The pinned reference only returns the segments for the matching
group; prefix collisions with other video IDs are ignored.

## Cookies, transport, and cancellation

The client uses the operation's shared `internal/network.Client`
transport. The client requires its credential-isolated request path;
the call fails closed with a categorized security error otherwise.
SponsorBlock requests never receive operation cookies, authorization,
or proxy-authorization headers, including values configured as
operation defaults.

The call is context-aware: a cancelled or expired context
returns a categorized cancellation error that preserves the context cause.
HTTP 401/403 map to authentication, 404 follows pinned
no-segment semantics (success with empty chapters), 429 maps to
network, and any 5xx maps to network. Malformed JSON,
oversized envelopes, and structurally hostile responses map to
internal/invalid metadata.

## Normalization

The pinned normalization rules are implemented in the pure
`internal/sponsorblock.Normalize` function. The function has no
I/O, no global state, and never panics on adversarial input.
The rules are:

1. Whole-video markers `(0, 0)` are discarded.
2. Start times `<= 1s` snap to zero.
3. POI categories (`poi_highlight`) are extended by exactly one
   second at the end.
4. End times within one second of the known video duration snap
   to the duration. End times are never allowed to exceed the
   duration.
5. Non-finite, negative, inverted, or oversized timestamps are
   rejected. The defensive maximum accepted timestamp is ten years; the
   duration mismatch filter rejects segments whose reported
   `videoDuration` differs from the known duration by more than
   one second, or by more than five seconds when the relative
   difference is at least five percent. The filter guards
   against divide-by-zero hazards.
6. Output chapters are sorted deterministically by
   `(start, end, source order)`.

The function is also exercised by a fuzz target so the
normalization is verified against random segment shapes and
response bodies.

## Output schema

The public `sponsorblock_chapters` value is a list of objects
with exactly the pinned fields:

```json
[
  {
    "start_time": 10.0,
    "end_time": 25.0,
    "category": "sponsor",
    "title": "Sponsor",
    "type": "skip"
  }
]
```

`title` is the canonical display title for the category. The
`chapter` category uses the bounded segment description as
title. The pinned title mapping is:

| Category | Title |
| --- | --- |
| sponsor | Sponsor |
| intro | Intermission/Intro Animation |
| outro | Endcards/Credits |
| selfpromo | Unpaid/Self Promotion |
| preview | Preview/Recap |
| filler | Filler Tangent |
| interaction | Interaction Reminder |
| music_offtopic | Non-Music Section |
| hook | Hook/Greetings |
| poi_highlight | Highlight |
| chapter | (segment description) |

Extra API-provided fields are dropped. Floating-point values are
encoded as JSON numbers; the precision follows the standard
`encoding/json` rules for `float64`.

## Error categories

Internal errors are mapped to the public `pkg/ytdlp` taxonomy:

| Internal sentinel | Public category |
| --- | --- |
| `ErrInvalidInput` | `invalid_input` |
| `ErrUnsupported` | `unsupported` |
| `ErrNetwork` | `network` |
| `ErrAuthentication` | `authentication` |
| `ErrIsolation` | `security` |
| `ErrInvalidMetadata` | `internal` |
| `ErrUnavailable` | `internal` |

The rendered error messages never include raw response bodies,
the requested video ID, the API base, or any arbitrary string
returned by the API. The public error surface is reduced to a
short static label per category.

## Bounds

The implementation enforces the following limits on every
SponsorBlock request:

- Maximum categories: 64 (pinned set has 11).
- Maximum segments decoded per group: 4096.
- Maximum response bytes: 4 MiB.
- Maximum string length per decoded field: 1024 bytes.
- Maximum JSON depth: 16.
- Maximum number of groups in a response: 64.

Exceeding any bound produces a categorized invalid metadata
error and the operation stops.

## Conformance fixtures

`conformance/sponsorblock/` contains three deterministic JSON
fixtures (`sample_response.json`, `sample_collision.json`,
`sample_malformed.json`) and a `PROVENANCE.md` file that names
the pinned reference commit and the source file
(`yt_dlp/postprocessor/sponsorblock.py`). The fixtures contain no
real cookies, tokens, video IDs, or captured production
response. They are mirrored by deterministic package fixtures and
exercised without network access, Python, or a clock.

## Out of scope

The following SponsorBlock features from the pinned reference
remain unimplemented in this release and are explicitly
deferred:

- FFmpeg-driven media cutting using `cut_out_range`.
- Force-keyframes injection around cut boundaries.
- Subtitle synchronization across cut boundaries.
- CLI flags for the option, the API base, or the categories.
- SponsorBlock metadata for services other than YouTube
  (PeerTube, Vimeo, etc.).
- The reference's user-facing `report_warning` call when some
  segments are filtered.

These are documented in the capability manifest's
`known_deviation` and are not a regression of any prior claim.
