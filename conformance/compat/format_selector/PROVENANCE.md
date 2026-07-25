# Format-selector advanced AST provenance

- Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- Recorded: 2026-07-25
- Source: `yt_dlp/YoutubeDL.py` `build_format_selector` (parser, grouping, comma,
  slash, plus, filters, SINGLE atoms, `all`, `mergeall`, extension selectors)
  and `test/test_YoutubeDL.py` format-selection traps.
- Capture method: operator grammar, atom alias table, extension sets from
  `utils._utils.MEDIA_EXTENSIONS`, incomplete-format fallback rules, and
  expected IDs were transcribed into synthetic Go table tests. No Python
  runtime, public-site payloads, or credentials are included.
- Sanitization: all format IDs, URLs, and codec strings use `example.invalid`.
- Purpose: evidence for the bounded advanced selector slice (AST, evaluator,
  OutputPlan, product multi-output naming). Interactive filters, unbounded
  expressions, and full upstream parity beyond this corpus remain out of scope.

## Deliberate bounded deviations

1. Plain `best`/`worst` without a media type use the port's historical
   quality-first playable-universe selection so the existing pinned
   compatibility pilot (`best[ext~=webm|mp4]`) remains stable. Typed
   `bestvideo`/`bestaudio`/star atoms follow yt-dlp predicates.
2. `all` preserves extractor list order (forward) rather than Python's reversed
   iteration; order is documented and deterministic in fixtures.
3. Product `mergeall` and >2-track merges remain explicit unsupported at
   download time when ffmpeg cannot represent the track set as a single
   video+audio pair.
4. Atom indexes are capped at `1000` with bounded digit width and precise syntax
   spans for malformed `.N` / `*` tails.
5. Evaluator limits (`all`, `mergeall`, final plan count, merge track count) are
   enforced during evaluation with `ErrSelectorLimit`.
6. Multi-output destinations use stable one-based ordinals plus sanitized IDs
   and per-plan container extensions derived from each `OutputPlan`. Merge
   evaluation retains distinct same-kind operands; only exact duplicates are
   removed. Non-empty `Request.Postprocessors`, SponsorBlock remove, and
   subtitle embedding fail closed before media download when multiple
   independent outputs are selected.
7. `ErrSelectorLimit` is a distinct evaluation sentinel categorized as
   `invalid_input` by the product API.
