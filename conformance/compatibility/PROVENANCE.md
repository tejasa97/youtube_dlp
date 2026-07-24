# Compatibility Pilot Provenance

- Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.
- Recorded: 2026-07-17; regex-filter case corrected 2026-07-25.
- Sources: the output-template and format-selection syntax documented at the
  pinned reference commit, especially `YoutubeDL.build_format_selector` and
  `_select_formats` incomplete-format semantics.
- Capture method: selected expressions and expected scalar outcomes were
  transcribed into the corpus and independently reviewed against the pinned
  documented semantics. No public-site response, media, credential, or Python
  runtime is included.
- Sanitization: all metadata and URLs are synthetic and use reserved example
  values.
- Purpose: define the exact Phase 1 compatibility-parser pilot boundary. It is
  not evidence for syntax outside the checked-in expressions.

## Attributable corpus correction (2026-07-25)

The Phase 1 regex-filter case originally used `best[ext~=webm|mp4]` against a
mixed video-only + audio-only fixture and expected `720`. Under the pinned
`_select_formats` contract, `incomplete_formats` is computed once from the
original available set (false for mixed adaptive) and atom filters do not
recompute it, so plain `best` does not fall back after the filter removes
audio. The expression was corrected to `bestvideo[ext~=webm|mp4]`, which keeps
the selected ID `720` as a valid pinned expectation for the regex-filter
syntax check without claiming incorrect plain-`best` fallback.

The corpus is evaluated by both native Go parsers in one golden test. Future
reference-runtime captures must add their exact command line and environment to
this file or a fixture-specific provenance record.
