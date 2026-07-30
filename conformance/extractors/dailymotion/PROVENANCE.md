# Dailymotion discovery fixture provenance

These deterministic, synthetic, license-safe fixtures model the public
`graphql.api.dailymotion.com` collection shapes used by the pinned yt-dlp
reference at `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`DailymotionBaseInfoExtractor`, `DailymotionSearchIE`, and `DailymotionUserIE`
in `yt_dlp/extractor/dailymotion.py` (lines 26–99 and 541 onward).

`token.json` models the anonymous `client_credentials` OAuth response from
`DailymotionBaseInfoExtractor._get_token`. The native Go slice uses the same
OAuth client credential pair embedded by that upstream class; it is request
material rather than a user secret and never appears in diagnostics.

`search_page*.json` and `user_page*.json` model the `SEARCH_QUERY` operation
and `channel(...).videos(...)` pagination envelopes. `search_page1_bad_edge.json`
is a full 20-entry page with one malformed node to prove fail-closed pagination.
All IDs, text, and URLs were independently authored. No production code reads
this directory.

Deliberate hardening beyond the reference: exact-origin credential-isolated
token acquisition, scoped Authorization GraphQL POSTs with no redirects,
identifier and search-term bounds, reserved-route rejection, hostile node URL
rejection with canonical child emission, aggregate page and entry ceilings,
repeated full-page detection, and bounded `allowExplicit: true` user uploads
without a family-filter request option in this public slice.
