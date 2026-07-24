# Format-selector best/worst atom provenance

- Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- Recorded: 2026-07-25
- Source: `yt_dlp/YoutubeDL.py` `build_format_selector`, especially the SINGLE-atom
  branch and the pinned regex
  `(?P<bw>best|worst|b|w)(?P<type>video|audio|v|a)?(?P<mod>\*)?(?:\.(?P<n>[1-9]\d*))?$`
  plus `_select_formats` `incomplete_formats` /
  combined-vs-star predicate behaviour.
- Capture method: grammar, alias tables, incomplete-format fallback rules, and
  one-based indexing expectations were transcribed from the pinned Python
  source and checked against synthetic Go fixtures. No public-site response,
  media bytes, credentials, or Python runtime are included.
- Sanitization: all format IDs, URLs, and codec strings are synthetic
  `example.invalid` values.
- Purpose: evidence for the bounded best/worst atom slice only. Groups,
  comma/multi-output, `mergeall`, extension-name selectors, interactive
  filters, and the rest of the full selector grammar remain out of scope.

## Incomplete-format fallback (pinned)

`incomplete_formats` is computed once per selection from the post-DRM available
format universe, matching `_select_formats`. Atom filters replace only the
candidate list; they do not recompute incompleteness. Plain `best`/`worst`
therefore:

- fall back on original all-video-only or all-audio-only universes (including
  after filters);
- do not fall back on original mixed video-only + audio-only universes, even
  when a filter narrows to one side;
- select a combined progressive format normally when one exists.

## Deliberate bounded deviations

1. Typed exclusive atoms (`bv`/`ba` without `*`) continue to use the port's
   conservative `candidateMediaKinds` helpers for missing codec keys. Combined
   and star predicates use yt-dlp's `!= "none"` / explicit `"none"` checks so
   codec-less progressive formats remain selectable as combined/playable.
2. Atom indexes are capped at `1000` and reject leading zeros, signs, missing
   digits, overflow, and excessive digit length with precise syntax spans.
3. A leading `+` inside a would-be signed index is still the merge operator at
   the selector grammar layer (`best.+2` parses as `best.` merge `2`).
4. Direct IDs remain IDs unless the whole token is a valid atom or a reserved
   malformed `*` / `.N` shape (`.0`, signed, overflow, junk after index/star).
   Ordinary IDs that merely begin with `best`/`worst`/`b`/`w` (`wav`, `bestx`,
   `bestvideox`, `best.mp4`) stay format IDs.
