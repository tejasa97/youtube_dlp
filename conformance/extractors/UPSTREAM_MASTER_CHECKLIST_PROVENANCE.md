# Upstream extractor master-checklist provenance

## Source

- Repository: `yt-dlp/yt-dlp`
- Commit: `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
- Registry: `yt_dlp/extractor/_extractors.py`
- Extractor sources: `yt_dlp/extractor/*.py` and registered extractor packages
- Go comparison base: `tejasa97/youtube_dlp@ca10f693318c4f9084f87a1f65dae409bf63334a`

The reference checkout was read-only. No upstream file was modified, copied as
production code, or executed.

## Derivation

`cmd/extractorinventory` reads the pinned registry import list and emits one
CSV row for every registered `*IE` class. It compares normalized class keys
against registered Go extractor `Name()` values, applies a small documented
alias set, detects same-family Go modules, and scans the relevant upstream class
body for explicit `_WORKING = False`, authentication/impersonation markers, and
handoffs to shared backends already implemented in Go.

Classification precedence is:

1. explicit `_WORKING = False`;
2. curated or exact registered Go mapping;
3. existing Go site-family module;
4. explicit authentication/anti-bot markers;
5. existing shared-backend markers;
6. manual review/new-backend backlog.

The generated result is deliberately conservative. Low- and medium-confidence
rows are unsupported classifications, not compatibility claims. The checked-in consistency
test validates the total, unique `(module, class)` identities, allowed statuses,
and required mapping fields without accessing the reference checkout.

## Python-free boundary

The generator is written in Go and treats upstream Python files as text. Normal
builds, production binaries, and tests neither execute Python nor require the
reference checkout. Regeneration is an explicit maintainer action against a
separately supplied read-only source tree.
