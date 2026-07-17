# Compatibility Pilot Provenance

- Reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.
- Recorded: 2026-07-17.
- Sources: the output-template and format-selection syntax documented at the
  pinned reference commit.
- Capture method: selected expressions and expected scalar outcomes were
  transcribed into the corpus and independently reviewed against the pinned
  documented semantics. No public-site response, media, credential, or Python
  runtime is included.
- Sanitization: all metadata and URLs are synthetic and use reserved example
  values.
- Purpose: define the exact Phase 1 compatibility-parser pilot boundary. It is
  not evidence for syntax outside the checked-in expressions.

The corpus is evaluated by both native Go parsers in one golden test. Future
reference-runtime captures must add their exact command line and environment to
this file or a fixture-specific provenance record.
