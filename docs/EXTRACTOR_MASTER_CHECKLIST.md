# Extractor master checklist

Reference baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`

The authoritative row-level checklist is
[`conformance/extractors/upstream_master_checklist.csv`](../conformance/extractors/upstream_master_checklist.csv).
It contains every concrete extractor class registered by the pinned
`yt_dlp/extractor/_extractors.py`; internal base classes that are not registered
are excluded.

## Baseline result

| Classification | Classes | Meaning |
| --- | ---: | --- |
| `already_supported` | 99 | An exact registered Go extractor mapping is known. Compatibility remains bounded by that extractor's manifest claim. |
| `partially_supported` | 127 | The site family exists in Go, but this upstream class does not have a proven exact mapping. |
| `uses_existing_shared_backend` | 59 | The upstream class visibly hands off to a backend already implemented in Go. |
| `requires_authentication_or_antibot` | 141 | The class contains explicit login, password, OAuth, authorization, or impersonation behavior. |
| `obsolete_or_intentional_deviation` | 136 | The pinned upstream class explicitly declares `_WORKING = False`. |
| `requires_new_backend` | 1,189 | No exact Go mapping or existing-backend handoff was detected; manual family review is required. |
| **Total** | **1,751** | All registered concrete classes in the pinned reference. |

Exact extractor-class coverage is therefore 99/1,751 (5.7%). Including partial
site-family coverage gives 226/1,751 (12.9%), but partial rows must not be
treated as complete. These figures measure extractor-class breadth only, not
the completion of downloaders, post-processing, the CLI, or the overall Go
port.

## How to use the checklist

1. Filter `uses_existing_shared_backend` first. These are the best candidates
   for Composer adapter batches.
2. Review `partially_supported` by existing Go family and split missing URL
   classes into bounded depth PRs.
3. Group `requires_new_backend` by upstream module/base family. Implement a
   shared backend before leaf adapters whenever several classes share one.
4. Handle `requires_authentication_or_antibot` one family at a time after the
   credential, origin, redirect, and fallback policy is designed.
5. Re-check `obsolete_or_intentional_deviation` during every reference refresh;
   `_WORKING` can change upstream.
6. Replace low-confidence generated classifications with reviewed overrides as
   implementation work proceeds.

No row should move to `already_supported` merely because its hostname is
recognized. Promotion requires successful fixture-backed extraction or
adapter-to-backend re-entry, negative routing evidence, categorized failures,
cancellation, bounds, secret safety, provenance, registry integration, and a
passing manifest claim.

## Classification limits

This is a conservative generated baseline, not 1,751 hand-reviewed design
decisions.

- Exact normalized key matches and curated aliases are high confidence.
- Same-module Go coverage is marked partial rather than assumed complete.
- Existing-backend and auth/anti-bot detection is source-token based and must
  be confirmed before implementation.
- The `requires_new_backend` bucket includes standalone public extractors as
  well as true shared families. It is intentionally low confidence.
- `_WORKING = False` is recorded as the pinned upstream state, not a permanent
  decision to exclude the extractor.

## Refresh

The generator reads the reference checkout as source text; it does not execute
Python and is not part of normal build or test execution:

```sh
go run ./cmd/extractorinventory \
  -reference /absolute/path/to/yt-dlp-reference \
  -repository . \
  -output conformance/extractors/upstream_master_checklist.csv
```

Normal builds and tests depend only on the checked-in CSV. They have no runtime
or build-time dependency on Python or the reference checkout.
