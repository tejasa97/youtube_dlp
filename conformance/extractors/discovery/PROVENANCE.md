# Extractor discovery provenance

## Source

- Repository: `yt-dlp/yt-dlp`
- Commit: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- CLI source: `yt_dlp/options.py` and `yt_dlp/__init__.py`
- Registry/display source: `yt_dlp/extractor/__init__.py` and
  `yt_dlp/extractor/common.py`

## Derivation

The pinned reference was inspected read-only to establish the option names,
early-exit behavior, stdout channel, input URL deduplication and association,
sorted display order, overlapping URL association, generic placement, and
description shape. The Go
implementation derives its catalog from the existing explicit native product
registry. No upstream extractor source or runtime metadata is copied into
production code.

This is a metadata-only conformance lane. It has no network fixture because
discovery must be deterministic and offline. The tests prove that list input
matching uses only native `Suitable` calls and that descriptions do not inspect
URLs or invoke the normal extraction runner.

## Scope boundary

Native descriptions are bounded and deterministic. Reference Python working
state, search prefixes, netrc hints, plugin discovery, and extractor selection
remain outside this lane and are recorded as deviations in
`docs/EXTRACTOR_DISCOVERY_EVIDENCE.md`.
