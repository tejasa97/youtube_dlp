# SoundCloud search compatibility evidence

## Scope

`internal/extractor/soundcloud_search.go` is a native-Go compatibility slice for pinned yt-dlp `SoundcloudSearchIE` at `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. It supports public `scsearch:QUERY`, `scsearchN:QUERY` (`1..200`), and `scsearchall:QUERY` (hard-capped at 200). A narrow exact public HTTPS form, `https://soundcloud.com/search?q=QUERY`, is also accepted.

The API shape, pagination arguments and client-ID bootstrap are attributable to `SoundcloudSearchIE._get_collection`, `_get_n_results`, and `SoundcloudBaseIE._update_client_id`. The checked-in synthetic corpus and its provenance are in `conformance/extractors/soundcloud_search/`.

Deliberate hardening beyond the reference: a 500-byte query maximum, 4 KiB target/continuation maximum, a 200-entry response page maximum, exact HTTPS `api-v2.soundcloud.com/search/tracks` continuations, bounded continuation query cardinality/value sizes inherited from the shared SoundCloud policy, and a 200-result `all` cap. Cross-origin, downgrade, userinfo, ports, fragments, encoded separators, malformed query encodings, and replayed continuation URLs fail closed.

Only entries with a positive stable ID, non-empty title, and a canonical public `https://soundcloud.com/<artist>/<track>` track URL are emitted. Users, playlists, malformed candidates, private-token URLs, API URLs, and off-origin URLs are skipped.

## Mutation / failure-order audit

1. Target parsing and playlist construction only use local values; all failure paths return before an `Extraction` is exposed.
2. Each iterator gets independent `ContinuationEntries` cursor, page, seen-set, and page-counter state. The shared sequence retains only cloned first-page data and an immutable fetch closure.
3. Before every request, continuation validation returns a local canonical URL; rejected URLs do not alter iterator cursor/page state. `client_id` is stripped then re-added by the existing request routine.
4. `SoundCloud.clientID` is the only shared mutable state. Its existing mutex protects discovery/refresh. A failed bootstrap leaves it empty; a failed API request does not overwrite it. Iterators never mutate it directly.
5. A response is decoded and fully validated into local page/entry slices before `ContinuationEntries` commits the next iterator page. Fetch failure marks only that iterator done; other iterators can restart independently.
6. Cancellation is checked before iterator work and carried in request context; blocked transport calls return the context error without committing page or cursor state.
7. Repeated cursors are detected by `ContinuationEntries` after a successful fetch and terminate safely; no shared state is replayed or corrupted.

## Tests and fuzz invariants

`soundcloud_search_test.go` loads every fixture and checks route policy, count grammar/cap, lazy first/continuation requests, method/path/query/client-ID/bootstrap/header policy, mixed filtering, malformed JSON/shape/service error, auth/unavailable/network categories, initial and continuation failures, pre- and in-flight cancellation, continuation origin/replay rejection, bounds, and two full independent iterations. Fuzz targets assert successful targets remain bounded/canonical and successful entries retain canonical SoundCloud URLs, stable IDs, extractor key, and non-zero required fields.

## Primary integration requirements

The primary owner must, separately:

1. Register `NewSoundCloudSearch()` in the product extractor registry with the intended routing priority (before generic handling and without displacing normal SoundCloud track/set routes).
2. Add its focused tests/fuzz targets and this evidence corpus to the parity manifest/catalog only after registration lands.
3. Surface the documented `scsearchall` cap of 200 in user-facing capability documentation, rather than claiming upstream's unbounded `all` behavior.

No registry, client, manifest, or existing SoundCloud files were changed by this increment.
