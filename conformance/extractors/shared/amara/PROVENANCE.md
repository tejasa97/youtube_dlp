# Amara fixture provenance

These deterministic fixtures were authored on 2026-07-27 for the Go port from
the response fields and behavior inspected in the read-only yt-dlp reference
checkout at commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

Relevant reference locations in `yt_dlp/extractor/amara.py` at that commit:

- lines 12–59: supported public video URL shapes and representative tests for
  YouTube, Vimeo, and direct media;
- lines 61–100: `https://amara.org/api/videos/<id>/?format=json` request,
  metadata normalization, published subtitle expansion, and transparent
  YouTube/Vimeo handoff.

The checked-in copies live under `internal/extractor/testdata/amara/`. No
upstream response body or user media metadata was copied. Fixtures use the
public Amara host with synthetic identifiers, and media assets are never
requested by tests. The expected normalization contract is native Go behavior
rather than byte-for-byte Python output.

Known deliberate differences are documented in `docs/EXTRACTOR_AMARA_EVIDENCE.md`.
The fixtures require no network access, Python interpreter, or reference
checkout at test/build/runtime.
