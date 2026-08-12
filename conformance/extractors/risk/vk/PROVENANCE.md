# VK public ecosystem risk fixtures

Synthetic fixtures authored on 2026-07-30 from `yt_dlp/extractor/vk.py` at
pinned upstream commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. Fixture
identities, signed query values, CDN paths, UUIDs, and media bytes are synthetic
and are not production captures.

## Promoted public behavior

- `vk` accepts only the documented anonymous public `video`, `clip`,
  `video_ext.php`, bounded `z=video|clip` feed forms, and the retained
  `vksport.vkvideo.ru` aliases on `vk.com`, `m.vk.com`, `new.vk.com`, and
  `vkvideo.ru`. Signed query bytes are preserved for validated requests and
  returned asset URLs. Daxab forms and YouTube, Vimeo, and Dailymotion
  handoffs are outside the current claim; arbitrary embeds are rejected.
- `vk_uservideos` accepts public `vk.com/{m,new}.vk.com/video/@slug` and
  `vkvideo.ru/@slug` user/group video pages plus public numeric playlist routes.
  The first page is lazy after page identity discovery, each iterator is
  reusable, offsets advance by server row counts, exact repeated pages fail
  closed, duplicate row occurrences are not globally deduplicated, and partial
  consumption/cancellation stop further requests.
- `vk_wallpost` accepts direct numeric wall posts and one-key public-page
  `?w=wall...` forms. Public audio is represented as a validated `vkaudio:` URL
  result; embedded VK video/clip links are bounded, safe, stable-order,
  duplicate-exact-link suppressed, and transparent with the wall URL as the
  child Referer.
- `vkplay` accepts exact HTTPS `vkplay.live`, `live.vkplay.ru`, and
  `live.vkvideo.ru` `/{username}/record/{uuid}` routes with the optional
  `/records` suffix and uses the canonical anonymous public recording endpoint.
  `vkplay_live` is partial: only signed anonymous live HLS is promoted when
  the stream is attributable and online.

## Isolation and attribution

Every VK page, AJAX/API, manifest, variant, initialization, key, segment,
direct media, subtitle, and thumbnail request uses the credential-isolated
no-redirect transport. Native HLS/DASH downloaders revalidate the extractor-
owned host policy at every child hop before network access. VK AJAX requests
retain only the exact endpoint Referer required by the public protocol;
ambient Authorization, Cookie, Proxy-Authorization, and Referer are never
forwarded. Returned media roles accept only HTTPS VK/VK CDN hosts or HTTPS
attributable VK Play alias/CDN hosts, with fragments, userinfo, ports, unsafe
encoded paths, and unvalidated redirects rejected. Signed query strings are
retained without normalization or stripping.

## Product evidence

`internal/extractor/vk_test.go` covers routing, route overlap, metadata,
chapters, subtitles, thumbnails, lazy reusable pagination, repeated pages,
wall transparent reentry, opaque audio, live-HLS-only behavior, typed failures,
cancellation, and missing isolation capabilities. `pkg/ytdlp/vk_public_product_test.go`
covers registered-client direct/HLS byte playback, API/page/manifest/segment/
media/subtitle/thumbnail isolation across all ambient credential classes,
signed-query retention across HLS/DASH child hops including DASH BaseURL/index/
initialization/representation ranges, hostile HLS/DASH reference
rejection before hostile network access, wall reentry, multipage playlist
consumption and same-client reuse, failure cleanup, typed status errors, and
entered in-flight cancellation with zero artifacts.

## Outside the current claim

Login/private/password/deleted/friends-only/age- or account-gated/geo-only/DRM
content, VK challenge solving, websocket/chat, unavailable rows beyond the
bounded server response, unvalidated redirects, VK Play live DASH, non-HLS
live fallbacks, and production host rotation are not claimed. No auth token,
cookie, signed cookie, or live credential is invented.
