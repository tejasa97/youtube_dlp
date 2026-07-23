# YouTube extractor pilot fixture

This corpus is a synthetic, offline fixture for the Phase 1 YouTube video
extractor. It contains only structural data needed to exercise player-response
parsing, direct and signature-cipher formats, `n` transformation, signature
deciphering, HLS/DASH manifest exposure, and normalized metadata.

The player program is the pinned synthetic EJS fixture at
`conformance/javascript/ejs-0.8.0/synthetic-player.js`. Its provenance and the
exact upstream `yt-dlp-ejs` version are documented alongside that fixture.

The domains use the reserved `.example` namespace, and the in-memory test
transport rejects every unlisted URL. No live YouTube request is made. The
expected document is intentionally checked in so field presence, ordering, and
challenge-transformed URLs remain reviewable.

The playlist corpus is also synthetic and follows the renderer and continuation
shapes consumed by `YoutubeTabIE` in the pinned reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`: ordered
`playlistVideoRenderer` URL results, `continuationItemRenderer` cursor lookup,
and `youtubei/v1/browse` continuation requests. The client version is the
pinned reference's web client value. The live fixture follows the pinned
reference's `isLive`/`isLiveContent` to `live_status=is_live` classification and
HLS manifest exposure. All identifiers, metadata, cursors, keys, visitor data,
and domains are artificial; no captured account or production response is
stored.

The comments corpus (`comments-watch.html`, `comments-header.json`,
`comments-page.json`, `comments-page-2.json`, and `comments-disabled.json`) is
also wholly synthetic. Its field and traversal expectations were derived from
the read-only pinned reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- modern entity-backed comment fields:
  `yt_dlp/extractor/youtube/_video.py:2367-2394`;
- legacy `commentRenderer` fields:
  `yt_dlp/extractor/youtube/_video.py:2396-2443`;
- header sorting, duplicate detection, reply/subthread traversal, and the five
  `max_comments` dimensions:
  `yt_dlp/extractor/youtube/_video.py:2445-2577`;
- `/youtubei/v1/next` continuation requests, disabled-comment handling, and
  visitor refresh:
  `yt_dlp/extractor/youtube/_video.py:2577-2659`;
- the generated initial continuation:
  `yt_dlp/extractor/youtube/_video.py:2661-2667`;
- initial comment-section discovery:
  `yt_dlp/extractor/youtube/_video.py:2669-2678`;
- the pre-fetch approximate count:
  `yt_dlp/extractor/youtube/_video.py:4369-4376`;
- deferred post-extraction integration:
  `yt_dlp/extractor/youtube/_video.py:4572` and
  `yt_dlp/extractor/common.py:3882-3908`; and
- the public CLI aliases:
  `yt_dlp/options.py:1524-1533`.

The synthetic data and inline test documents include legacy parents and
replies, modern entity mutations, wrapped sort commands, root and reply
continuations, nested subthreads, click-tracking parameters, rotating visitor
identities, transient/incomplete retry responses, pinned duplicate behavior,
and a disabled message. All video/comment IDs, authors, text, thumbnails,
counts, API keys, client versions, continuation tokens, visitor data,
click-tracking values, and URLs are artificial. No production comment,
identity, account credential, cookie, tracking value, or response is retained.

The Go capability intentionally remains narrower than those reference paths:
it is opt-in and bounded; it does not authenticate, synthesize an estimated
timestamp, expose the approximate count before retrieval, reproduce upstream's
configurable ignore-error policy, or claim untested renderer families.

`playlist-modern.html` and `playlist-modern-continuation.json` extend that
synthetic corpus with the `lockupViewModel` video and
`continuationItemViewModel` shapes, including an executor-wrapped continuation
command and repeated video occurrences, handled by the pinned reference's
`YoutubeTabBaseInfoExtractor._extract_lockup_view_model` and
`YoutubeBaseInfoExtractor._extract_continuation` at commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. The minimal field layout was also
checked against the anonymous public single-video playlist page on 2026-07-22.
All identifiers, text, endpoints, and continuation tokens in these fixtures are
artificial; no production visitor data, tracking values, signed URLs, or media
metadata is retained.

The watch offset expectations follow the pinned `YoutubeIE._real_extract`
fragment-then-query traversal and its `start`/`t` to `start_time` and `end` to
`end_time` mapping. Tests use only synthetic IDs plus the pinned public Big Buck
Bunny URL for an optional live acceptance check.

Channel live-alias matching follows the `/channel/.../live`, `/user/.../live`,
`/c/.../live`, and handle routes exercised by `YoutubeTabIE` in the pinned
reference. The deterministic resolver reuses the synthetic `live-watch.html`
player response and resolves only a validated video ID through the existing
video extractor. It does not retain a production channel page or redirect.

`sabr-watch.html`, `android-player.json`, and `android-vr-player.json` are synthetic regression fixtures
for URL-less `serverAbrStreamingUrl` webpage responses and native-client format
recovery. Their response fields and client-request expectations are derived
from `YoutubeIE._extract_player_responses`, `_DEFAULT_CLIENTS`, and the Android
client table in the same pinned reference checkout. They also pin propagation
of the webpage visitor identity required to keep multi-client player requests
in one anonymous session. The cookie-isolation and authenticated-page policy is
derived from `_get_requested_clients` in
`yt_dlp/extractor/youtube/_video.py`, which excludes clients without
`SUPPORTS_COOKIES` when authenticated. The synthetic watch page uses the
bounded `ytcfg.set({...})` shape observed by the pinned implementation; player
URLs are accepted only from structured configuration or the player response's
`assets.js`, then constrained to HTTPS YouTube `/s/player/` paths. No production
response, media URL, cookie, visitor identifier, or account data is retained.

The authenticated WEB player recovery tests are synthetic expectations derived
from the pinned reference's SID-cookie selection, SHA-1 authorization
construction, authenticated-session predicate, and Innertube header generation
in `yt_dlp/extractor/youtube/_base.py:724-799` and `:921-961`, plus the WEB
player request body in `yt_dlp/extractor/youtube/_video.py:2903-2956` and
`:2685-2710`. They cover the `SAPISID` fallback, 1P/3P schemes, delegated and
user session identifiers, account index, visitor identity, fixed WEB origin,
and HTML5 playback context. All cookie values, hashes, account/session
identifiers, client versions, visitor data, URLs, and responses are artificial.
No production cookie, account, request, or response was captured. The Go slice
is intentionally limited to format recovery from the exact WEB player endpoint;
authenticated comments, browse/search/Music clients, multi-client rotation,
and direct SABR/UMP remain outside this evidence.

The protected-playback token fixture is derived from the `player`, `gvs`, and
`subs` context definitions and token placement behavior in the pinned
reference's `yt_dlp/extractor/youtube/pot/provider.py`,
`yt_dlp/extractor/youtube/pot/_director.py`,
`yt_dlp/extractor/youtube/_base.py`, and
`yt_dlp/extractor/youtube/_video.py`. Its tokens are inert base64url strings;
its identities and URLs are synthetic. The fixture proves the Go provider
boundary and placement rules, not interoperability with a production token
generator.

`captions-watch.html` is a synthetic caption renderer derived from
`YoutubeIE._real_extract`'s `playerCaptionsTracklistRenderer` traversal and
`YoutubeIE._SUBTITLE_FORMATS` in the same pinned checkout. It covers manual and
ASR tracks, original/translated language naming, stable caption URL formats,
and the `subs` token query fields `pot`, `potc`, and `c`. The `xpe`/`xpv`
required-token expectation follows the pinned caption experiment check. All
names, language entries, visitor data, media URLs, and base64url tokens are
artificial; the fixture is never used to request YouTube caption content.

## Privacy-Enhanced Embed URL Support (youtube-nocookie.com)

Behavioral reference: `YoutubeIE._VALID_URL` and `YoutubeIE._EMBED_REGEX` in
`yt_dlp/extractor/youtube/_video.py` at pinned commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.

The pinned reference accepts youtube-nocookie.com via the broad
`(?:\w+\.)?[yY][oO][uU][tT][uU][bB][eE](?:-nocookie|kids)?\.com` host pattern
combined with the multi-path alternation (`v/`, `embed/`, `e/`, `shorts/`,
`live/`, `watch?v=`). The Go implementation intentionally deviates with a
narrower, security-conscious policy:

Accepted URL forms:

- `https://www.youtube-nocookie.com/embed/<11-char-video-id>`
- `https://youtube-nocookie.com/embed/<11-char-video-id>`
- `http://www.youtube-nocookie.com/embed/<11-char-video-id>`
- `http://youtube-nocookie.com/embed/<11-char-video-id>`
- `//www.youtube-nocookie.com/embed/<11-char-video-id>` (protocol-relative)

Query and fragment `t`/`start`/`end` offsets are preserved using the existing
`parseYouTubeOffset` machinery. Extraction canonicalizes to
`https://www.youtube.com/watch?v=<id>` and never fetches the nocookie URL.

Deliberate deviations from the pinned reference:

1. Host allowlist is exact apex (`youtube-nocookie.com`) and `www` only;
   wildcard subdomains (e.g. `m.youtube-nocookie.com`) are rejected.
2. Only the `/embed/<id>` path shape is accepted on nocookie hosts; `v/`,
   `e/`, `shorts/`, `live/`, `watch?v=`, and all other routes are rejected
   with the categorized `ErrUnsupported` boundary.
3. Userinfo (`user@host`, `user:pass@host`), explicit ports (`:443`, `:8080`,
   and the empty-port form `host:` where Go's `Port()` returns ""), and
   non-HTTP(S) schemes (`ftp:`, `file:`, `data:`) are unconditionally
   rejected on all YouTube hosts.
4. Encoded path separators (`%2f`, `%5c`) and NUL bytes (`%00`) are rejected
   as defense-in-depth, even though `net/url` does not reject them.
5. Lookalike and suffix-confusion hosts (`evil-youtube-nocookie.com`,
   `youtube-nocookie.com.evil.example`, `attacker.youtube-nocookie.com`) are
   rejected by exact string comparison rather than regex wildcard.

These deviations are documented as intentional hardening; full upstream
URL-regex parity is not claimed.

### Shared URL-policy enforcement

The URL-security gates (scheme, userinfo, port, encoded separators, host
classification) are enforced by a single `validateYouTubeURLPolicy` helper
called at the top of `YouTube.Extract` before any route dispatch. This ensures
video, playlist, and channel-live-alias routing all reject hostile URL forms
consistently; no route can bypass the policy by dispatching before validation.
Playlist dispatch additionally requires `hostStandard` classification.

### Context cancellation propagation

Context cancellation (`context.Canceled`) and deadline expiry
(`context.DeadlineExceeded`) from the JavaScript challenge solver are returned
directly without recategorization as `ErrChallengeSolver`, so callers can
observe them with `errors.Is`. This guarantee applies to the `SolvePlayer`
call in `resolveYouTubeURLs`; the `recoverYouTubeFormats` path already
propagated context errors prior to this change.

## Finite post-live DVR reconstruction

The synthetic post-live fixture and protocol tests derive their status,
`targetDurationSec`, live timestamp, `X-Head-Seqnum`, `sq`, two-sequence tail,
and 120-hour retained-window semantics from
`YoutubeIE._needs_live_processing`, `_prepare_live_from_start_formats`,
`_live_adaptive_fragments`, and the live metadata assembly in
`yt_dlp/extractor/youtube/_video.py` at the pinned commit above. Media bodies
are locally generated and split into artificial sequence chunks; all signed
query values and headers are inert test data.

## Active live-from-start reconstruction

The opt-in active fixtures derive their format eligibility, inclusive active
head, five-second polling model, five-hour normal refresh, accelerated refresh
after repeated misses, exact `(itag, client)` refreshed-format identity, and
active-to-ended final probe from `YoutubeIE._needs_live_processing`,
`_prepare_live_from_start_formats`, and `_live_adaptive_fragments` at the
pinned commit above. The Go implementation adds explicit poll, segment,
refresh-failure, response-size, cancellation, and filesystem bounds. All
player responses, signing values, clocks, waits, and media are synthetic.
