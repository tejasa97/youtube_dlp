# Phase 3 Shadow Corpus Provenance

- Reference context: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.
- Capture date: 2026-07-19.
- Source: wholly synthetic observations using reserved `.example` hostnames.
- Attribution: repository-authored factual test data; no upstream source or
  response bytes are copied.
- Sanitization: all identifiers, credentials, URLs, formats, warnings, and
  playlist records are invented. Deliberately different synthetic secrets
  prove that persistence redaction occurs before semantic comparison.
- Purpose: prove typed routing/request/metadata/format/playlist/warning and
  protocol-usability observations, identity/set order policies, canonical
  serialization, and credential-safe shadow comparison without running an
  oracle or Python.

## Explicit deviations

- This corpus proves the comparison mechanism, not live extractor parity.
- Warning message redaction recognizes typed URLs and conventional credential
  assignments; producers must not place arbitrary secrets in free-form text.
- Object key order is canonicalized and is not a semantic signal in the Phase
  3 envelope. Format, playlist, warning, and protocol order are explicit policy.
- Windows and regional behavior require separately attributable observations;
  no network or location claim is made here.
