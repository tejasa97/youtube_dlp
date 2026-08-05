# Niconico public ecosystem fixtures

These synthetic fixtures are pinned to the upstream extractor source at
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` and model only the anonymous public
watch/shorts, mylist, series, user, search, and tag contracts retained by the
native extractor.

No live Niconico request, account, cookie, access key, action track ID, signed
URL, or response body was copied into this corpus. The access-right key,
returned watch-track ID, and signed HLS query are deterministic placeholders
used to verify pinned request construction, dynamic guest action-track
formatting, byte-preserving URL handling, and credential isolation. The guest
action-track value is generated at extraction time and is never taken from this
fixture.

The collection fixtures contain two valid child rows on page 1, ninety-eight
malformed/hostile-ID source rows to keep the pinned page-size boundary honest,
and one valid child row on page 2. `mylist_page_*.json`,
`series_page_*.json`, and `user_page_*.json` are consumed by the registered
playlist product tests; `search_page_*.html` supplies the equivalent two-page
search, tag, and pseudo-search child corpus. The product assertions verify
registered child keys, access-rights POST output pairing, exact best-video and
best-audio segment bytes, and isolated requests. The only named product evidence
is provided by these public `Client.Run` tests:

- `engine.TestProductNiconicoRegisteredWatchHLSDownloadIsolatedAndSigned`
- `engine.TestProductNiconicoRegisteredBestAudioDownloadIsExactAndIsolated`
- `engine.TestProductNiconicoRegisteredSearchChildReentryIsolation`
- `engine.TestProductNiconicoPlaylistChildReentryUsesRegisteredKeys`
- `engine.TestProductNiconicoHLSFailureAndCancellationLeaveNoArtifacts`

History is deferred because the upstream path requires cookies and has no
registered native mapping or product evidence. Live is deferred because the
upstream websocket seat/heartbeat lifecycle is outside the existing finite
native HLS abstractions and has no registered native mapping or product
evidence. Date-recursive search remains deferred because it has no registered
exact class mapping, fixed bounded native contract, or product evidence;
comments, premium/member/PPV playback, sensitive-account playback, and
geo-restricted playback are not claimed.
