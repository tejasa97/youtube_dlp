# PeerTube fixture provenance

These deterministic fixtures were authored on 2026-07-19 for the Go port from
the response fields and behavior inspected in the read-only yt-dlp reference
checkout at commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Relevant reference locations in `yt_dlp/extractor/peertube.py` at that commit:

- lines 1317–1326: 22-character and UUID identifiers, explicit
  `peertube:host:id` URLs, supported video/watch/embed/API paths;
- lines 1539–1543: `/api/v1/videos/{id}` endpoint construction;
- lines 1544–1562: captions response and relative caption URL handling;
- lines 1564–1644: video metadata, direct files, streaming playlists, full
  description fallback, uploader/channel fields, thumbnail, counts, tags,
  category, language, licence, and live state.

No upstream response body or user media metadata was copied. Hosts use reserved
`.test` names, identifiers and counts are synthetic, and media assets are never
requested by tests. The expected fixture records a native Go normalization
contract rather than byte-for-byte Python output.

Known deliberate differences are documented in `docs/P3_PEERTUBE_EVIDENCE.md`.
The fixtures require no network access, Python interpreter, or reference
checkout at test/build/runtime.
