# SponsorBlock metadata

This document describes the native, Python-free SponsorBlock product
surface available through the public Go product API and the CLI. The
foundation covers **metadata fetch, normalization, opt-in chapter
marking, and FFmpeg-driven media cutting with subtitle synchronization**.

## What is wired

When a caller passes a `ytdlp.Request` with
`SponsorBlock.Enabled == true` and the operation targets a
YouTube-family extractor, the client performs a single bounded
SponsorBlock API lookup and writes the result to
`result.InfoJSON` under the key `sponsorblock_chapters`. Disabled
requests never touch the network.

When `Remove` is also set, matching skip ranges are cut from the
downloaded media after postprocessors run and before subtitle
embedding. `Simulate` and `SkipDownload` never invent media cuts.

The implementation derives its behavior from the pinned yt-dlp
reference at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
(`yt_dlp/postprocessor/sponsorblock.py` and
`yt_dlp/postprocessor/modify_chapters.py`, plus
`force_keyframes` / concat helpers in
`yt_dlp/postprocessor/ffmpeg.py`). The reference is treated
as a read-only behavioral mirror: it is never executed, imported,
or depended on at build time. The conformance fixtures in
`conformance/sponsorblock/` document this lineage.

## Public request option

```go
type SponsorBlockOptions struct {
    Enabled          bool
    Mark             bool
    Remove           bool
    Categories       []string
    RemoveCategories []string
    ForceKeyframes   bool
    APIBase          string
}
```

`Enabled` is the only field that gates the metadata stage. When false, no
network requests are issued, regardless of the other fields.

`Mark` requires `Enabled`. It overlays normalized SponsorBlock ranges onto the
ordinary `chapters` list without changing media bytes. Existing chapter fields
are preserved on uncovered fragments. When ordinary chapters are unavailable,
a full-duration background chapter is synthesized from the video title.
Overlaps preserve first-seen category order, and only fragments created by
the overlay are eligible for the pinned sub-second merge behavior; originally
tiny chapters remain intact.

`Remove` requires `Enabled`. After a real download it plans cut ranges from
`sponsorblock_chapters`, optionally force-keyframes around boundaries, and
concatenates keep segments with the ffmpeg concat demuxer
(`inpoint`/`outpoint`), matching
`ModifyChaptersPP._make_concat_opts` / `remove_chapters`. Mark and Remove may
both be set: marking rewrites chapter metadata; removing mutates media and then
remaps marked chapter timestamps onto the post-cut timeline.

`Categories` is the requested non-empty set of SponsorBlock category
identifiers used for the API fetch (and for Mark). The list is treated as
caller-owned and is never mutated. Unknown identifiers, empty
entries, whitespace-only entries, and strings longer than 64
bytes are rejected by request validation. Duplicate identifiers
are de-duplicated deterministically by the first-seen index.

`RemoveCategories` optionally selects which fetched categories to cut.
When empty and `Remove` is true, `Categories` is used after dropping
non-removable `poi_highlight` and `chapter` entries (pinned yt-dlp
behavior). Explicit non-removable remove categories are rejected at
validation. The fetch set is the first-seen union of `Categories` and
`RemoveCategories`.

`ForceKeyframes` requires `Remove`. When true, media is re-encoded with
`-force_key_frames` at cut boundaries before concat (yt-dlp
`--force-keyframes-at-cuts`). Subtitle sidecars are never force-keyframed.

`APIBase` is the API origin. When empty, the implementation uses
`https://sponsor.ajay.app`. Custom bases are intended for
deterministic tests and self-hosted deployments that implement
the same API. Only `http` and `https` schemes with a non-empty
host are accepted.

## CLI flags

The CLI exposes the mark/metadata path with yt-dlp-familiar names:

| Flag | Effect |
| --- | --- |
| `--sponsorblock-mark CATEGORIES` | Sets `Enabled=true`, `Mark=true`, and accumulates `Categories` |
| `--sponsorblock-api URL` | Sets `APIBase` (kept even when marking is later disabled) |
| `--no-sponsorblock` | Clears mark enablement only; does not clear `--sponsorblock-api` |

`CATEGORIES` is a comma-separated list. Repeated `--sponsorblock-mark`
flags accumulate in parse order (config arguments first, then command
line; later `--sponsorblock-api` values overwrite earlier ones). The
tokens `all` and `default` expand to the pinned full category set from
`internal/sponsorblock.AllCategories()`. Prefix a category or alias
with `-` to exclude it after expansion, matching the reference grammar
(for example `all,-preview`, `default,-intro`, or `sponsor,-sponsor`).
Exclusions may leave an empty category set, which disables marking.
`--no-sponsorblock` is applied after option parsing (matching pinned
yt-dlp `opts.no_sponsorblock`), so it clears marking regardless of
whether mark flags appear before or after it in config or on the
command line, while preserving `--sponsorblock-api`. Unknown
identifiers and explicit empty values such as `--sponsorblock-mark=`
are rejected at CLI parse time (exit status `2`) before any network
work. Marking writes both `sponsorblock_chapters` and the overlaid
ordinary `chapters` list. Media cutting (`--sponsorblock-remove` /
FFmpeg) is not exposed.

Example:

```sh
./bin/ytdlp-go --sponsorblock-mark all,-preview --skip-download --print-json \
  'https://www.youtube.com/watch?v=VIDEO_ID'
```

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

## Cutting and keyframes

Pure cut planning lives in `internal/sponsorblock.PlanCuts`:

1. Only removable categories are cut (`poi_highlight` and `chapter`
   never are).
2. Adjacent and overlapping remove ranges merge.
3. Zero-length / non-finite ranges are ignored.
4. Keep segments are emitted as concat `inpoint`/`outpoint` directives,
   omitting empty leading/trailing chunks.
5. Removing the entire media fails closed.

Typed ffmpeg operations in `internal/media/ffmpeg` perform optional
force-keyframes re-encode and concat-range finalize. Product Remove
orchestrates a transactional multi-artifact cut: every media and
supported subtitle path is prevalidated, every output is staged into a
private temporary directory, and originals are replaced only after all
staging succeeds (with rename-backup rollback if a later commit step
fails). Missing tools fail closed. Planning rejects layouts that would
exceed the ffmpeg concat-range (128) or force-keyframe (512) limits.

## Subtitle synchronization

When subtitle sidecars exist beside the downloaded media, Remove rewrites
them with deterministic cue removal and timestamp remapping for the
supported extensions (`srt`, `vtt`, `ass`/`ssa`, `lrc`). Cues entirely
inside cut ranges are dropped; cues before, overlapping, or after cuts are
kept with remapped timestamps. WebVTT `STYLE`, `REGION`, and `NOTE`
blocks are preserved verbatim. LRC lines with multiple leading timestamps
remap each contiguous leading tag independently and keep surviving lyric text;
timestamp-shaped text later in lyrics is preserved literally. Malformed
or unrecognized SRT cue blocks fail closed with `invalid_input` instead
of silently dropping content. FFmpeg concat is not used for subtitles.
Unsupported sidecar formats fail closed with a categorized `unsupported`
error so the product never silently leaves subtitles desynced. This is
stricter than yt-dlp's warn-and-continue policy for unsupported external
subtitle types and is recorded as a known deviation.

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
`encoding/json` rules for `float64`. After a successful Remove, Info
`duration` is updated to the post-cut length and ordinary `chapters`
timestamps are remapped onto the post-cut timeline even when Mark is
false. `sponsorblock_chapters` retains pre-cut fetch times.

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
- Maximum remove cut ranges after merge: 256 (512 ÷ 2 force-keyframe slots).
- Maximum keep segments after planning: 128 (ffmpeg concat-range limit).
- Maximum unique force-keyframe timestamps: 512.

Exceeding any bound produces a categorized invalid metadata
error and the operation stops.

## Conformance fixtures

`conformance/sponsorblock/` contains three deterministic JSON
fixtures (`sample_response.json`, `sample_collision.json`,
`sample_malformed.json`) and a `PROVENANCE.md` file that names
the local pinned reference checkout and commit plus the source
files (`sponsorblock.py`, `modify_chapters.py`). The fixtures contain no
real cookies, tokens, video IDs, or captured production
response. They are mirrored by deterministic package fixtures and
exercised without network access, Python, or a clock.

## Out of scope / remaining deviations

The following SponsorBlock features from the pinned reference
remain unimplemented or intentionally different in this release:

- CLI remove/cut flags (`--sponsorblock-remove` and related).
- SponsorBlock metadata for services other than YouTube
  (PeerTube, Vimeo, etc.).
- The reference's user-facing `report_warning` call when some
  segments are filtered by duration mismatch.
- Regex-based ordinary chapter removal (`--remove-chapters`) and
  manual `--remove-ranges` outside SponsorBlock categories.
- yt-dlp's warn-and-continue policy for unsupported external
  subtitle formats during remove (this port fails closed instead).
- The full ModifyChapters sponsor/normal heap arrangement when
  mixing remove markers with simultaneous mark overlays beyond the
  post-cut timestamp remap implemented here.

These are documented in the capability manifest's
`known_deviation` and are not a regression of any prior claim.
