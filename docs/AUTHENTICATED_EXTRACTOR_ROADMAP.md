# Authenticated Extractor Roadmap

Research-and-design document for the remaining authenticated extractor work.
**No production code is proposed for immediate merge here** — this document is the
implementation-ready design that the follow-up PRs consume.

## 1. Purpose & Scope

This roadmap produces implementation-ready designs for five features:

1. Twitch VOD chat replay
2. Twitch subscriber / authenticated content
3. Vimeo password-protected videos
4. Vimeo private / authenticated videos
5. Relevant Vimeo anti-bot behavior

Each feature section documents: exact pinned-reference behavior and request
sequence; required credentials/cookies/tokens/scopes; reusable Go abstractions;
missing abstractions; credential-origin and redirect boundaries; secret-redaction
requirements; cancellation and retry behavior; expected metadata/output;
categorized errors; deterministic fixture strategy; success/failure/leakage/fuzz
tests; recommended PR split; and explicit non-goals and unresolved questions.

The document closes with an ordered series of bounded implementation PRs
(§10), each with exact file ownership, dependencies, acceptance criteria,
verification commands, risk level, and a Composer-safe flag.

## 2. Method & Verification Basis

- **Branch:** `codex/authenticated-extractor-design`, based on latest `origin/main`.
- **Pinned reference (read-only):** `/Users/tejas/projects/yt-dlp-reference` at
  commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`.
- **Go code reviewed against current tree**, principally:
  - `internal/extractor/extractor.go` (transport interfaces, error sentinels, `Request`, `Credential`)
  - `internal/extractor/twitch.go` (GQL client, kinds, headers, error mapping)
  - `internal/extractor/vimeo.go` and `internal/extractor/vimeo_album.go` (video + album flows)
  - `internal/extractor/vimeo_album_test.go` (fixture pattern)
  - `internal/extractor/youtube_auth.go` (authenticated-transport precedent)
  - `internal/extractor/json.go` (`RequestJSON`, `RequestJSONWithScopedAuthorizationNoRedirect`, `HTTPStatusError`)
  - `internal/network/client.go`, `client_scoped_auth.go` (isolation + redaction primitives)
  - `internal/network/impersonate/profile.go` (chrome-133 / firefox-120)
  - `internal/credentials/netrc/netrc.go` (bounded parser, redacted `Credential`)
  - `internal/differential/shadow.go` (`SanitizeObservation`, sensitive-name set)
  - `internal/cli/run.go` (flag surface: `--netrc`, `--netrc-location`; **no** `--video-password`)

Every reference claim below was checked against the pinned tree; every Go claim
was checked against the current source. Load-bearing negative findings
(feature absent, sentinel absent, flag absent) are called out explicitly.

## 3. Shared Foundations

### 3.1 Existing Go abstractions that are directly reusable

**Transport capability interfaces** (`internal/extractor/extractor.go`) — the
extractor negotiates capabilities by type-asserting the ambient `Transport`:

| Interface | Method | Purpose |
|---|---|---|
| `Transport` | `Do`, `ReadPage` | ambient (cookies + credentials allowed) |
| `CookieIsolatedTransport` | `DoWithoutCookies` | drop cookie jar |
| `CredentialIsolatedNoRedirectTransport` | `DoWithoutCredentialsNoRedirect` | strip `Authorization`/`Cookie`/`Proxy-Authorization`, refuse redirects |
| `ScopedAuthorizationNoRedirectTransport` | `DoWithScopedAuthorizationNoRedirect` | keep only an explicit short-lived `Authorization`, refuse redirects |
| `CredentialIsolatedProfilePageTransport` | `ReadPageProfileWithoutCredentialsNoRedirect` | isolated impersonating page fetch |
| `ProfileTransport` | `DoProfile`, `ReadPageProfile` | impersonating request/page fetch |

**Network primitives** (`internal/network/client.go`, `client_scoped_auth.go`):
- `DoWithoutCredentials` deletes `Authorization`, `Cookie`, `Proxy-Authorization`
  and uses a nil jar.
- `DoNoRedirect` sets `CheckRedirect = ErrUseLastResponse`.
- `DoWithScopedAuthorizationNoRedirect` preserves only an explicit `Authorization`,
  refuses redirects, and redacts errors. Backed by tests in `client_scoped_auth_test.go`.
- `RedactURL` / `RedactRawURL` strip sensitive query keys
  (`auth`, `authorization`, `key`, `sig`, `signature`, `token`).
- `RedactHeaders` masks `Authorization`, `Cookie`, `Proxy-Authorization`, `Set-Cookie`.
- `RetryableStatus(code)` → true for 408, 429, and ≥500.

**JSON helpers** (`internal/extractor/json.go`):
- `RequestJSON(ctx, transport, method, url, body, headers, &target)` — ambient.
- `RequestJSONWithScopedAuthorizationNoRedirect(...)` — routes through the
  scoped-auth transport; used today by the Vimeo album flow.
- `HTTPStatusError{Code int}` — the canonical typed status error the categorizers
  match with `errors.As`.

**Impersonation** (`internal/network/impersonate/profile.go`): fully implemented
`chrome-133` (`HelloChrome_133`) and `firefox-120` (`HelloFirefox_120`) with
`Lookup`/`Supported` and fingerprint metadata. Vimeo already pins
`vimeoImpersonationProfile = "chrome-133"`.

**Credential model** (`internal/extractor/extractor.go`,
`internal/credentials/netrc/netrc.go`):
- `Request.Credentials CredentialProvider` exists on the extractor `Request`.
- `CredentialProvider.Lookup(ctx, authority) (Credential, bool, error)`.
- `extractor.Credential{Username, Password}` and `netrc.Credential{Login, Account, Password}`
  both implement redacted `String()`/`GoString()`.
- netrc is wired only through `--netrc` / `--netrc-location`; the extractor
  `authority` corresponds to the upstream `_NETRC_MACHINE` name.

**Reference authenticated-flow precedents already in-tree:**
- **YouTube** (`youtube_auth.go`): `youtubeAuthenticatedTransport` narrows
  `Transport` to also require `Cookies(string)` and `DoNoRedirect`; builds a
  SAPISIDHASH `Authorization` from `LOGIN_INFO` + SID cookies; `youtubeSafeHeaderValue`
  bounds header values (≤512, no `\r\n\x00`). Note the exact signature is
  `Cookies(rawURL string) ([]*http.Cookie, error)` (`internal/network/client.go`):
  it takes a **complete URL** (scheme + host required), not a bare hostname, and
  returns the jar snapshot applicable to that URL.
- **Vimeo album/showcase** (`vimeo_album.go`): a viewer-JWT + scoped-authorization
  API pattern is implemented, and its **structure** is a useful template for
  Vimeo features 3–5. **Two caveats that this roadmap depends on:**
  1. the album JWT is **anonymous** — `fetchVimeoAlbumJWT` fetches
     `https://vimeo.com/_next/viewer` via `DoWithoutCredentialsNoRedirect`
     (cookies stripped), so it yields a logged-out viewer token. It **must not**
     be reused unchanged for authenticated/private videos, which require a
     **cookie-bearing** viewer fetch (see §7);
  2. the album flow **does not submit an album password** — for
     `privacy.view == "password"` it returns `ErrAuthentication` and stops. It is
     **not** a working album-password implementation (see §6.13 correction).

  The reusable structure is:
  - requires both `CredentialIsolatedNoRedirectTransport` **and**
    `ScopedAuthorizationNoRedirectTransport`, else `ErrTransportIsolation`;
  - `resolveVimeoAlbumSlug` GETs `https://vimeo.com/showcase/{slug}/auth` via
    `DoWithoutCredentialsNoRedirect` with `X-Requested-With: XMLHttpRequest`
    (slug→id resolution only — **not** the password POST);
  - `vimeoAlbumTokenProvider` caches the (anonymous) viewer JWT with a refresh
    lead (`vimeoAlbumJWTRefreshLead = 2m`) and `invalidate()`s on 401/403;
  - `fetchVimeoAlbumJWT` GETs `https://vimeo.com/_next/viewer`, validates the JWT
    shape (`vimeoAlbumJWTPattern`), decodes the `exp` claim, and bounds payload
    sizes (`vimeoAlbumMaxJWTBytes = 8 KiB`, `vimeoAlbumMaxJWTPayload = 4 KiB`);
  - `fetchVimeoAlbumMetadata` / `fetchVimeoAlbumVideoPage` call
    `api.vimeo.com/albums/{id}` with `Authorization: jwt <token>` through
    `RequestJSONWithScopedAuthorizationNoRedirect`;
  - `withVimeoAlbumToken` retries exactly once on 401/403 after invalidating the token;
  - privacy gate: `anybody` → OK; `password`/`nobody`/`contacts`/`users`/`unlisted`
    → `ErrAuthentication` (i.e. password albums are refused, not unlocked).

**Result & event model:**
- `internal/extractor/result.go`: `Extraction{Info, Entries, Redirect, Enrich}`,
  `Media()`, `Playlist()`, `URLResult()`; `Entry` carries `Availability`
  (already supports `subscriber_only`, `private`, `premium`, `unlisted`).
- `internal/events/events.go`: deterministic sink (no timestamps).
- `internal/downloader/downloader.go`: retry/backoff, `RedactRawURL` in events.

### 3.2 Missing abstractions (cross-feature)

1. **Twitch authenticated transport.** `twitchHeaders()` sets only `Client-ID`
   and `Content-Type`. There is **no** reading of the `auth-token` cookie and
   **no** `Authorization: OAuth …` header path. Twitch has zero cookie/credential
   wiring today (verified: no matches for `auth-token`/`Authorization`/`Cookie`/
   `videopassword` in `twitch.go`).
2. **Video-scoped password input.** There is **no** `--video-password` CLI option
   and no plumbed field on `Request` for a per-video password. netrc supplies
   only username/password machine credentials.
3. **Credential wiring into Twitch/Vimeo *video* extraction.** `Request.Credentials`
   exists but is not consumed by `twitch.go` or `vimeo.go` (only the Vimeo album
   flow uses scoped auth, and it uses an anonymous viewer JWT, not user login).
4. **Error taxonomy for auth outcomes.** No `ErrWrongPassword` and no
   `ErrSubscriberOnly` / login-required-vs-forbidden distinction exists
   (verified: no matches). Today both collapse into `ErrAuthentication`.
5. **Twitch VOD chat replay has no model at all** — neither in this codebase nor
   in the pinned reference (see §4).

### 3.3 Security model — credential-origin & redirect boundaries (applies to all features)

- **Extractor-scoped auth only.** A credential/token acquired for one host must
  never be attached to a request for another host. Enforced structurally by
  choosing the narrowest transport interface per request (see §3.1) rather than
  the ambient `Transport`.
- **Redirects disabled on credential-bearing requests.** Every request that
  carries a cookie, user credential, or bearer/OAuth token MUST route through a
  `*NoRedirect` method so a 30x cannot replay the secret to a `Location` origin.
- **Cross-origin proof.** Each feature's leakage test asserts that isolated and
  scoped requests carry no `Authorization`/`Cookie`/`Proxy-Authorization` except
  the single intended scoped header to the single intended host (mirrors
  `vimeo_album_test.go` lines 72–111).
- **No anonymous fallback after authenticated state.** Once a request has been
  made with credentials (login cookie present, password accepted, or OAuth token
  minted), a failure must surface a categorized auth error — never silently retry
  anonymously. This mirrors upstream `raise_login_required` semantics and the
  album flow's `withVimeoAlbumToken` (which fails closed to `ErrAuthentication`).
- **No embedded upstream secrets.** The reference hard-codes base64 OAuth Basic
  secrets for Vimeo android/ios/macos clients and a Twitch web `Client-ID`. The
  `Client-ID` is a public web constant already present in-tree and is retained.
  The Vimeo private OAuth *Basic secrets* MUST NOT be copied; the Go design uses
  the web viewer-JWT path exclusively (anonymous or authenticated according to
  the operation jar).
- **No Python dependencies** introduced by any feature.

### 3.4 Secret-redaction requirements (applies to all features)

- Any URL logged/evented passes through `RedactRawURL` (Twitch usher `sig`/`token`
  query params and Vimeo signed CDN URLs must be masked).
- Any error that may embed a token or password is wrapped so `.Error()` cannot
  print the secret. The album flow's negative test asserts
  `!strings.Contains(err.Error(), "fixture-token")` — every new feature adds an
  equivalent assertion for its own secret material.
- `differential/shadow.go`'s `sensitiveName` set already covers
  `auth`/`authorization`/`cookie`/`password`/`token`/`sig`/`signature`; new field
  names introduced (e.g. a password form field) must fall under an existing
  sensitive key or be added to redaction there **only if a differential
  observation can capture them** (call out in the owning PR).
- Header value guards (`youtubeSafeHeaderValue` pattern: ≤512 bytes, no
  `\r\n\x00`) MUST wrap any credential/token before it becomes a header value.

---

## 4. Feature 1 — Twitch VOD chat replay

> **Critical finding (verified):** Twitch VOD chat replay **does not exist in the
> pinned reference**. A repository-wide search for `chat|comment|rechat` in
> `/Users/tejas/projects/yt-dlp-reference` matched only YouTube live chat
> (`youtube_live_chat.py`). `twitch.py` has no chat/comment download path.
> Therefore this feature has **no "exact pinned-reference behavior"** to port.
> It must be designed as net-new, and this roadmap recommends it be the **lowest
> priority / optional** item, gated behind an explicit opt-in.

### 4.1 Reference behavior & request sequence

None in the pinned reference. The real-world mechanism (documented here only so
the design is grounded, **not** copied from upstream) is Twitch's public GQL
`VideoCommentsByVideoID` persisted query, paginated by a `cursor` from each
response's `pageInfo`, with per-comment `contentOffsetSeconds` relative to VOD start.
Because there is no upstream reference, the design MUST NOT claim parity and MUST
be validated purely against deterministic fixtures.

### 4.2 Credentials / cookies / tokens / scopes

- Public VOD chat is retrievable **anonymously** with the existing web
  `Client-ID` (no user token). This keeps Feature 1 independent of Feature 2.
- Subscriber-gated chat segments would require the same `auth-token` cookie as
  Feature 2; **out of scope** for the first cut (non-goal §4.13).

### 4.3 Reusable Go abstractions

- `RequestJSON` + `twitchHeaders()` + `twitchOperationHashes` map (extend with the
  chat persisted-query hash) + `categorizeTwitchHTTP`.
- `Entry`/`Extraction` model is **not** the right output shape for chat; chat is a
  sidecar artifact, not a media/playlist entry. This is an unresolved output-model
  question (§4.14).

### 4.4 Missing abstractions

- A chat-replay output/serialization surface (there is no chat model in-tree).
- A cursor-pagination loop bounded like the album iterator
  (`vimeoAlbumMaxPages`/`MaxEntries` analogues: `twitchChatMaxPages`, `twitchChatMaxComments`).
- An explicit opt-in flag/`Request` field (`Request` has `YouTubeComments`/
  `SoundCloudComments` precedents; a `TwitchChat` boolean fits the same pattern).

### 4.5 Credential-origin & redirect boundaries

- Anonymous: use `Transport.Do`/`RequestJSON` to `gql.twitch.tv` only.
- If ever extended to subscriber chat, reuse Feature 2's scoped OAuth transport
  and `*NoRedirect`. No cross-origin: all requests target `gql.twitch.tv`.

### 4.6 Secret redaction

- No secrets in the anonymous path. Log URLs via `RedactRawURL` for consistency.

### 4.7 Cancellation & retry

- `contextError(ctx)` checked before each page; honor `ctx.Done()` mid-pagination
  (album iterator pattern). Retry only `RetryableStatus` codes; bounded pages.

### 4.8 Expected metadata / output

- Unresolved (§4.14). Candidate: a JSON sidecar of
  `{offset_seconds, author, message}` ordered by offset, written next to the VOD,
  behind opt-in. No change to media selection.

### 4.9 Categorized errors

- Reuse `categorizeTwitchHTTP`: 404/410 → `ErrUnavailable` (chat disabled/absent),
  429 → `ErrTwitchRateLimited`, else `ErrTwitchNetwork`. Malformed →
  `ErrInvalidMetadata`. Bound breach → a new `ErrTwitchChatLimit` (or reuse a
  playlist-limit-style sentinel).

### 4.10 Deterministic fixture strategy

- `conformance/extractors/twitch/vod-chat-page1.json`, `…-page2.json`,
  `…-empty.json`, `…-malformed.json`. A fixture transport (per §9) returns pages
  by decoding the request cursor; no live network.

### 4.11 Tests

- **Success:** two-page fixture → ordered comments, correct offsets, terminates on
  empty `pageInfo`.
- **Failure:** 404 → `ErrUnavailable`; malformed → `ErrInvalidMetadata`; page bound
  → limit error.
- **Leakage:** assert no `Authorization`/`Cookie` header on any request.
- **Fuzz:** cursor parser and offset parser fed arbitrary bytes never panic and
  never allocate beyond bounds.

### 4.12 Recommended PR split

- Single optional PR **after** Features 2–5 land, or drop entirely if product
  priority is low. See §10 (PR-7, optional).

### 4.13 Non-goals

- Subscriber-only chat; live chat; chat rendering/burn-in; emote/badge asset
  download; real-time streaming.

### 4.14 Unresolved questions

1. Is chat replay in product scope at all given zero upstream precedent?
2. Output model: sidecar JSON vs. an in-tree chat type vs. reusing an existing
   subtitle/sidecar writer?
3. Which persisted-query hash to pin, and how to keep it deterministic without
   embedding an upstream secret (the hash is public but volatile)?

---

## 5. Feature 2 — Twitch subscriber / authenticated content

### 5.1 Exact pinned-reference behavior & request sequence

From `yt-dlp-reference/yt_dlp/extractor/twitch.py`:

1. `TwitchBaseIE._download_base_gql` reads the `auth-token` cookie from
   `https://gql.twitch.tv`; if present it sets `headers['Authorization'] = 'OAuth ' + auth_token`.
   `_CLIENT_ID = 'ue6666qo983tsx6so1t0vnawi233wa'` is always sent as `Client-ID`.
2. `_download_access_token` issues the `{token_kind}PlaybackAccessToken` GQL
   persisted query and receives `{value, signature}`.
3. The usher manifest (`usher.ttvnw.net/vod/{id}.m3u8` or channel equivalent) is
   fetched with `sig`/`token` query params.
4. `_extract_twitch_m3u8_formats`: on a 403 whose JSON body contains
   `vod_manifest_restricted` or `unauthorized_entitlements`:
   - if the `auth-token` cookie **is** present → raise
     "account does not have access to subscriber-only content";
   - else → `raise_login_required`.
5. `_perform_login` (form login via `passport.twitch.tv`, TFA-aware) is the
   credential-origin path when no cookie is supplied.

### 5.2 Credentials / cookies / tokens / scopes

- **Primary:** `auth-token` cookie scoped to `gql.twitch.tv`. It is sourced from
  the **operation cookie jar**, which is already populated by **both**
  `--cookies` (Netscape file) and `--cookies-from-browser` (verified:
  `pkg/ytdlp/client.go` calls `transport.AddCookies(...)` for both paths). No new
  cookie ingestion is required. This cookie becomes the `OAuth <token>` bearer.
- **Public constant:** the web `Client-ID` (already in `twitch.go`, retained).
- **Out of scope:** replicating `_perform_login`/passport form login and TFA
  (non-goal §5.13). The Go design consumes an already-obtained `auth-token`
  cookie rather than performing interactive login.

### 5.3 Reusable Go abstractions

- `twitchOperationHashes`, `RequestJSON`, `twitchManifestURL`/`twitchVODManifestURL`,
  `categorizeTwitchHTTP`.
- **YouTube auth precedent** (`youtube_auth.go`): the pattern of a *narrowed*
  authenticated transport (`Transport` + `Cookies(rawURL string)` + `DoNoRedirect`)
  is the template for a `twitchAuthenticatedTransport`. `Cookies` takes a complete
  URL (`https://gql.twitch.tv`), not a hostname.
- `youtubeSafeHeaderValue`-style guard for the `OAuth <token>` header value.
- The operation cookie jar (populated by `--cookies` / `--cookies-from-browser`)
  for reading the `auth-token` cookie.

### 5.4 Missing abstractions

- `twitchAuthenticatedTransport interface { Transport; Cookies(string) ([]*http.Cookie, error); DoNoRedirect(...) }`.
- `twitchOAuthHeaders(token)` layered over `twitchHeaders()` with a bounded,
  guarded `Authorization: OAuth <token>` value.
- Subscriber-only outcome sentinels: `ErrTwitchSubscriberOnly` (cookie present but
  no entitlement) vs. login-required (`ErrAuthentication`).
- Body-sniffing of the 403 JSON for `vod_manifest_restricted` /
  `unauthorized_entitlements` (bounded read).

### 5.5 Credential-origin & redirect boundaries

- **Chosen behavior (matches the pinned reference):** the `auth-token` cookie is
  sent by the jar to `gql.twitch.tv` **and** the derived `Authorization: OAuth
  <token>` header is added on the *same* `gql.twitch.tv` request. yt-dlp does not
  strip the cookie when it sets the header (`_download_base_gql` just reads the
  cookie and adds the header; the request still carries jar cookies). Both travel
  to the **same origin**, so this does not cross an origin boundary. This behavior
  MUST be asserted by a test (§5.11), rather than left implicit.
- The cookie/header attach **only** to `gql.twitch.tv`. Usher manifest requests
  (`usher.ttvnw.net`) carry the `sig`/`token` **query** signature but **no**
  cookie and **no** `Authorization` header (different origin) — assert this.
- All credential-bearing GQL requests use `DoNoRedirect`.
- No anonymous fallback: if the `auth-token` cookie is present and the manifest is
  restricted, surface `ErrTwitchSubscriberOnly` — do **not** retry without the token.

### 5.6 Secret redaction

- `auth-token` value and `OAuth` header never logged; wrap through
  `RedactHeaders`. Usher URL `sig`/`token` masked via `RedactRawURL`
  (`token`/`sig`/`signature` already in the sensitive query set).
- Error strings from GQL/usher wrapped so the token cannot appear.

### 5.7 Cancellation & retry

- `contextError(ctx)` before each GQL/usher call. Retry only 408/429/≥500 via
  `RetryableStatus`, with the existing downloader backoff. Cancellation propagates
  immediately (fixture transports must honor `ctx.Done()`).

### 5.8 Expected metadata / output

- Same media/format output as the current anonymous VOD/stream path, but now
  succeeding for subscriber-only VODs when a valid cookie is present. Set
  `Availability = subscriber_only` on the resulting media when the entitlement
  gate was crossed.

### 5.9 Categorized errors

| Condition | Sentinel |
|---|---|
| 403 restricted + cookie present | `ErrTwitchSubscriberOnly` (new) |
| 403 restricted, no cookie | `ErrAuthentication` (login required) |
| 401/403 generic | `ErrAuthentication` |
| 404/410 | `ErrUnavailable` |
| 429 | `ErrTwitchRateLimited` |
| 451 | `ErrRegionRestricted` |
| other/transport | `ErrTwitchNetwork` |
| malformed JSON | `ErrInvalidMetadata` |

### 5.10 Deterministic fixture strategy

- `conformance/extractors/twitch/`: `vod-access-token.json`,
  `vod-manifest-restricted-403.json` (body with `vod_manifest_restricted`),
  `vod-unauthorized-entitlements-403.json`, plus a synthetic (fake) `auth-token`
  cookie value that is obviously non-real (e.g. `synthetic-auth-token`).
- Fixture transport implements `Cookies(rawURL string)` returning the synthetic
  cookie for `https://gql.twitch.tv` and `DoNoRedirect`; it asserts the
  `gql.twitch.tv` request carries **both** the jar `auth-token` cookie and the
  `OAuth` header, and that no cookie/`Authorization` reaches `usher.ttvnw.net`.

### 5.11 Tests

- **Success:** cookie present + valid entitlement fixture → media extracted,
  `Availability = subscriber_only`.
- **Failure:** restricted+cookie → `ErrTwitchSubscriberOnly`;
  restricted+no-cookie → `ErrAuthentication`; 429 → `ErrTwitchRateLimited`.
- **Leakage:** GQL request carries `OAuth`+`Client-ID`+`auth-token` cookie only to
  `gql.twitch.tv` (asserting the chosen cookie-accompanies-header behavior of
  §5.5); usher request carries neither cookie nor `Authorization`; no redirect
  followed; `err.Error()` never contains `synthetic-auth-token`.
- **Fuzz:** 403-body sniffer fed arbitrary bytes never panics; header guard
  rejects values with `\r\n\x00`/over length.

### 5.12 Recommended PR split

- PR-1: transport + header + cookie sourcing + sentinels (§10 PR-1).
- PR-2: subscriber-only categorization + fixtures + tests (§10 PR-2).

### 5.13 Non-goals

- Passport form login / interactive credential entry / TFA.
- Subscriber-only **chat** (Feature 1 territory).
- Ad-tier / turbo-specific manifest variations.

### 5.14 Unresolved questions

1. **Resolved:** the `auth-token` cookie enters via the operation jar, which both
   `--cookies` and `--cookies-from-browser` already populate; no `internal/cookies`
   change is needed. (Remaining sub-question: should a dedicated hint be emitted
   when the jar has no `auth-token` for a gated VOD?)
2. Should `Availability = subscriber_only` be set unconditionally on gated VODs or
   only when the gate was actually crossed with a token?


---

## 6. Feature 3 — Vimeo password-protected videos

### 6.1 Exact pinned-reference behavior & request sequence

From `yt-dlp-reference/yt_dlp/extractor/vimeo.py`:

- **Video password (`_verify_video_password`, line 211):** POST to
  `https://vimeo.com/{path}/{video_id}/password` with a **JSON** body
  (`json.dumps({'password': ..., 'token': ...}, separators=(',', ':'))`), i.e.
  compact JSON, sent with headers `Accept: */*`,
  `Content-Type: application/json`, and `Referer:` set to the **exact**
  `https://vimeo.com/{path}/{video_id}` URL (no `/password` suffix). Request uses
  `impersonate=True`. HTTP **418** = wrong password. `token` is the `xsrft` value
  from `_fetch_viewer_info`.
- **Player password (`_verify_player_video_password`, line 1092):** POST to
  `{player-url-without-query}/check-password` with **URL-encoded form data**
  (`Content-Type: application/x-www-form-urlencoded`) whose single field
  `password` is the **base64-encoded** password (`base64.b64encode`). Used when
  the player `config['view'] == 4`. A JSON response of `false` = wrong password
  (not an HTTP 418).
- **API path (`_extract_from_api`, ~line 1193):** a 400 whose body flags
  `invalid_parameter` on `password` triggers `_verify_video_password`.
- The password itself originates from `--video-password` (yt-dlp `videopassword`,
  read by `_get_video_password`).

> **Encoding summary (do not conflate):** the *normal video* password is **JSON**
> with `Content-Type: application/json`; **only** the *player* `/check-password`
> path uses `application/x-www-form-urlencoded` with a base64 password.

### 6.2 Credentials / cookies / tokens / scopes

- **Video password** (per-video secret) — **no current input channel exists** in
  the Go CLI (§3.2). Requires a new `--video-password` flag → `Request` field.
- **`xsrft` token** from the viewer config (anonymous), used as the form `token`.
- No user login required for the password-only case.

### 6.3 Reusable Go abstractions

- `fetchVimeoAlbumJWT`'s **viewer-fetch shape** (bounds, JWT/`exp` parsing) is a
  useful template for reading `xsrft` from `https://vimeo.com/_next/viewer`. For
  the password-only case the *anonymous* isolated fetch is acceptable (no login
  needed), so the existing `DoWithoutCredentialsNoRedirect` path is reusable for
  the `xsrft` GET — but the JSON schema differs (`xsrft`, not just `jwt`), so a
  new field parse is required.
- `vimeoImpersonationProfile = "chrome-133"` + `ProfileTransport` cover
  `impersonate=True`; `CredentialIsolatedProfilePageTransport` covers the
  credential-stripped page fetch.
- `RequestJSON`/`HTTPStatusError` for the 418/400 mapping.
- Header/value guards and `RedactRawURL` for redaction.

### 6.4 Missing abstractions

- **Profiled, no-redirect POST transport for the password submission itself.**
  None of the existing primitives is suitable on its own:
  - `ProfileTransport.DoProfile` impersonates but **may follow redirects**
    (no `CheckRedirect = ErrUseLastResponse`), so a 3xx from the password POST
    could carry credentials off-origin;
  - `CredentialIsolatedProfilePageTransport.ReadPageProfileWithoutCredentialsNoRedirect`
    is **GET-only** (`http.NewRequest(http.MethodGet, …)`) and **strips cookies**
    (`includeCookies=false`), so it cannot carry the ambient jar or post a body;
  - `Client.DoNoRedirect` refuses redirects but is **not impersonated** (no
    profile name; the request goes out with the native TLS fingerprint, defeating
    the `impersonate=True` semantics of the reference password POST).

  A new narrow interface is required, e.g.
  `ProfiledNoRedirectTransport.DoProfiledNoRedirect(ctx, *http.Request, profileName string)
  (*http.Response, error)`, combining impersonation + `CheckRedirect =
  ErrUseLastResponse` + the operation jar. Same-origin `Set-Cookie` behavior:
  because the password POST and the `Set-Cookie` target share the origin
  (`vimeo.com/{path}/{id}/password` for the video path, `player.vimeo.com/check-
  password` for the player path), the response's `Set-Cookie` is **accepted and
  merged into the operation jar** for subsequent same-origin requests; the
  fixture transport asserts that no redirect is followed off the password origin
  and that the returned `Set-Cookie` (if any) is retained only for that origin.
- `--video-password` CLI flag plus public `pkg/ytdlp.Request.VideoPassword`
  plumbed through to the extractor (see PR-3 in §10 for the full plumbing).
  Redaction is **proven by leakage tests**, not merely by a stringer.
- `verifyVimeoVideoPassword(ctx, transport, path, videoID, password, xsrft)`
  posting the **compact JSON** body to `vimeo.com/{path}/{id}/password` via the
  profiled-no-redirect transport with `Accept: */*`,
  `Content-Type: application/json`, exact `Referer`, impersonation, and no
  redirect.
- Optional `verifyVimeoPlayerPassword` posting **form-urlencoded** data with a
  base64 `password` to `{player-url}/check-password` (the `view == 4` path) via
  the same profiled-no-redirect transport; wrong password is a JSON `false`, not
  an HTTP 418.
- `ErrWrongPassword` sentinel (418 / `false` / invalid password) distinct from
  `ErrAuthentication` (login required).
- `xsrft` extraction helper from the `_next/viewer` payload.

### 6.5 Credential-origin & redirect boundaries

- The password POST targets `vimeo.com` (JSON `/password`) or `player.vimeo.com`
  (form `/check-password`) **only**; any resulting session cookie/redirect MUST
  NOT be followed (`*NoRedirect`). The password is never attached to CDN/media or
  `api.vimeo.com` requests.
- Cross-origin proof: leakage test asserts the password (and its base64 form)
  never appears in any request to `api.vimeo.com` or CDN hosts, and that no
  redirect is followed off `vimeo.com`/`player.vimeo.com`.

### 6.6 Secret redaction

- The password must **not** be provable-safe by a stringer alone. Redaction is
  demonstrated by leakage tests asserting the plaintext password and its base64
  form never appear in emitted events, logs, request bodies to other origins, or
  `err.Error()` (`!strings.Contains(err.Error(), <fixturePassword>)` and the
  base64 variant).
- `xsrft` and any session material are masked via `RedactHeaders`/`RedactRawURL`;
  error strings from the password POST are wrapped so no secret can surface.

### 6.7 Cancellation & retry

- `contextError(ctx)` before the POST. Password verification is **not** retried on
  418 (deterministic wrong-password) — only transient `RetryableStatus` codes
  retry. Cancellation honored by fixture transport.

### 6.8 Expected metadata / output

- On correct password: full media/formats identical to the public path.
- Source/original formats only when logged in (Feature 4) — password alone does
  not unlock source downloads.

### 6.9 Categorized errors

| Condition | Sentinel |
|---|---|
| 418 / invalid password | `ErrWrongPassword` (new) |
| password required but none supplied | `ErrAuthentication` |
| 404/410 | `ErrUnavailable` |
| 429/403 anti-bot | see §8 |
| malformed | `ErrInvalidMetadata` |
| transport | `ErrVimeoNetwork` / existing playlist-network sentinel analogue |

### 6.10 Deterministic fixture strategy

- `conformance/extractors/vimeo/`: `video-viewer.json` (with `xsrft`),
  `password-ok.json`, `password-418.json`, `player-check-password-ok.json`,
  `player-check-password-false.json`. The `/password` fixture asserts a **JSON**
  request body with `Content-Type: application/json`, `Accept: */*`, and the exact
  `Referer`; the `/check-password` fixture asserts a **form-urlencoded** body with
  a base64 `password`. Synthetic password (`synthetic-password`) and synthetic
  `xsrft`; fixture transport validates the POST body/headers and rejects any redirect.

### 6.11 Tests

- **Success:** correct password fixture → media extracted.
- **Failure:** 418 (`/password`) or JSON `false` (`/check-password`) →
  `ErrWrongPassword`; missing password → `ErrAuthentication`.
- **Leakage:** password (plaintext + base64) and `xsrft` absent from API/CDN
  requests and from `err.Error()`; JSON vs. form encoding asserted per path;
  no redirect followed.
- **Fuzz:** `xsrft` parser and 418/400 body sniffer never panic on arbitrary bytes.

### 6.12 Recommended PR split

- PR-3: `--video-password` plumbing + `ErrWrongPassword` + `xsrft` helper.
- PR-4: video/player password verification + fixtures + tests.

### 6.13 Non-goals

- **Album/showcase password submission.** *(Correction: this is **not** already
  handled.)* The current `vimeo_album.go` returns `ErrAuthentication` for
  `privacy.view == "password"` and never submits a password. The pinned reference
  (`_get_album_data_and_hashed_pass`, lines 527–555) POSTs
  `https://vimeo.com/showcase/{album_id}/auth` with form data
  `{password, token: xsrft}` and `X-Requested-With: XMLHttpRequest`, reads
  `hashed_pass`, and treats HTTP **401** as wrong password. Implementing album
  password submission is a **separate future item**, explicitly out of scope for
  Features 3–4 here (tracked as an unresolved item, §6.14).
- Brute-forcing or storing passwords; interactive prompts.

### 6.14 Unresolved questions

1. Should `--video-password` be global or Vimeo-scoped? (netrc has no password-only
   channel; a flag is required regardless.)
2. Is the `player.vimeo.com/check-password` (`view == 4`) path in initial scope, or
   deferred until a fixture proves it is reachable in the Go video flow?
3. Album/showcase password submission (§6.13): should the `showcase/{id}/auth`
   POST + `hashed_pass` flow be added to `vimeo_album.go`, and if so is it part of
   the video-password PRs or a dedicated album PR?

---

## 7. Feature 4 — Vimeo private / authenticated videos

### 7.1 Exact pinned-reference behavior & request sequence

From `vimeo.py`:

- `_is_logged_in`: `'vimeo' in self._get_cookies('https://vimeo.com')`.
- `_fetch_viewer_info` (line 135): plain `_download_json` GET to
  `https://vimeo.com/_next/viewer` with `Accept: application/json`. **It uses the
  ambient cookie jar** — so when the `vimeo` session cookie is present the returned
  `jwt` is an **authenticated** web JWT; when absent it is anonymous. This is the
  crux of Feature 4: the authenticated JWT is obtained by a **cookie-bearing**
  viewer fetch, not by the album's credential-isolated (anonymous) fetch.
- `_perform_login`: POST to the `log_in` endpoint with
  `{action, email, password, service, token}` where `token` is `xsrft` from the
  viewer (out of scope — non-goal §7.13).
- `_fetch_oauth_token` (line 369): for the **web** client (`VIEWER_JWT`), returns
  `jwt {viewer['jwt']}` and sends only that to `api.vimeo.com`; other clients use
  OAuth `client_credentials` with scope
  `private public create edit delete interact upload purchased stats video_files`
  (the latter rely on **embedded base64 Basic secrets** — see security note).
- `_call_videos_api` (~line 416): `api.vimeo.com/videos/{id}:{unlisted_hash}`;
  `REQUIRES_AUTH` clients need a logged-in session.
- `_extract_original_format` (~line 438): source formats returned only when logged in.
- `_extract_from_api` 404 with `error_code 5460` → `raise_login_required`.

### 7.2 Credentials / cookies / tokens / scopes

- **`vimeo` session cookie** = logged-in state, sourced from the operation jar
  (`--cookies` / `--cookies-from-browser`, both already populate it).
- **Authenticated viewer JWT** obtained from a **cookie-bearing** GET of
  `https://vimeo.com/_next/viewer` (exact origin, no redirect). This is **not**
  the anonymous album JWT: reusing `fetchVimeoAlbumJWT`
  (`DoWithoutCredentialsNoRedirect`) unchanged would strip the `vimeo` cookie and
  yield a logged-out token that cannot see private videos.
- Only that scoped `Authorization: jwt <token>` is sent to `api.vimeo.com`.
- **`unlisted_hash`** appended to the video id for unlisted videos.
- **Security note:** the embedded android/ios/macos OAuth **Basic secrets** in the
  reference MUST NOT be copied. The Go design uses only the web viewer-JWT path.

### 7.3 Reusable Go abstractions

- **Structure** of the album flow (`vimeoAlbumTokenProvider`, `withVimeoAlbumToken`,
  `vimeoAlbumHeaders`, `RequestJSONWithScopedAuthorizationNoRedirect`, the
  `exp`/refresh-lead caching, the single 401/403 retry) is the template — but the
  **JWT acquisition step must differ**: an authenticated, cookie-bearing viewer
  fetch replaces `fetchVimeoAlbumJWT`'s anonymous fetch.
- `ScopedAuthorizationNoRedirectTransport` for the `api.vimeo.com/videos/{id}` call
  (unchanged — only the JWT itself carries authority; no cookie goes to the API).
- The operation cookie jar for the `vimeo` session cookie.
- `youtube_auth.go`'s narrowed `Transport + Cookies(rawURL) + DoNoRedirect` shape
  is the correct precedent for the cookie-bearing viewer fetch.

### 7.4 Missing abstractions

- A **cookie-bearing, exact-origin, no-redirect** `_next/viewer` fetch that returns
  the **authenticated** web JWT. This is a new capability distinct from
  `fetchVimeoAlbumJWT`; it requires a `vimeoAuthenticatedTransport` narrowing
  (`Transport` + `Cookies(rawURL string)` + `DoNoRedirect`) so the `vimeo` cookie
  reaches only `https://vimeo.com/_next/viewer` and no redirect is followed.
- An authenticated viewer-JWT provider (caching + 401/403 refresh) that wraps the
  cookie-bearing fetch; the anonymous album provider stays as-is for albums.
- `unlisted_hash` handling in the video id path (URL parse + bounded validation).
- Mapping `error_code 5460` → login-required.

### 7.5 Credential-origin & redirect boundaries

- The `vimeo` session cookie attaches **only** to `https://vimeo.com/_next/viewer`
  (the authenticated JWT fetch) and never to `api.vimeo.com` or CDN/media hosts.
- The authenticated JWT attaches **only** as `Authorization: jwt <token>` to
  `api.vimeo.com` and never to `vimeo.com` or CDN hosts.
- Every credential-bearing call is `*NoRedirect`.
- No anonymous fallback: once logged-in state is detected, a private-video failure
  surfaces `ErrAuthentication`/`ErrUnavailable`, never an anonymous retry.

### 7.6 Secret redaction

- `vimeo` cookie and `jwt` token masked via `RedactHeaders`; JWT never in error
  strings (album flow already proves this with `fixture-token`). Signed CDN URLs
  via `RedactRawURL`.

### 7.7 Cancellation & retry

- Reuse `withVimeoAlbumToken`'s single 401/403 refresh-and-retry semantics.
- `contextError(ctx)` before each API call; `RetryableStatus` for transient.

### 7.8 Expected metadata / output

- Private/unlisted videos extract full formats when authenticated; source/original
  formats appear only in the logged-in case (parity with `_extract_original_format`).

### 7.9 Categorized errors

| Condition | Sentinel |
|---|---|
| 404 `error_code 5460` | `ErrAuthentication` (login required) |
| 401/403 | `ErrAuthentication` |
| 404/410 | `ErrUnavailable` |
| privacy = password | `ErrWrongPassword`/`ErrAuthentication` (defer to Feature 3) |
| malformed | `ErrInvalidMetadata` |

### 7.10 Deterministic fixture strategy

- `conformance/extractors/vimeo/`: `video-api-private.json`,
  `video-api-5460.json`, `video-viewer.json` (synthetic JWT
  `eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjQxMDI0NDQ4MDB9.c3ludGhldGlj`, exp = 4102444800),
  plus a synthetic `vimeo` cookie. Reuse the `vimeoAlbumFixtureTransport` shape.

### 7.11 Tests

- **Success:** logged-in fixture (synthetic `vimeo` cookie) → authenticated JWT
  from the cookie-bearing viewer fetch → private video formats; unlisted hash resolves.
- **Failure:** `5460` → `ErrAuthentication`; 404 → `ErrUnavailable`; anonymous
  viewer (no cookie) against a private video → `ErrAuthentication` (no silent success).
- **Cookie/JWT origin (explicit):** assert the `vimeo` cookie is sent **only** to
  `https://vimeo.com/_next/viewer`; assert the JWT is sent **only** to
  `api.vimeo.com`; assert **no** cookie reaches `api.vimeo.com` and **no** JWT
  reaches `vimeo.com` or CDN hosts.
- **Leakage:** no redirect followed on any credential-bearing request; `err.Error()`
  free of the synthetic token/cookie.
- **Fuzz:** `unlisted_hash` and `error_code` parsers never panic.

### 7.12 Recommended PR split

- PR-5: add a **cookie-bearing authenticated viewer-JWT provider** (new
  `vimeoAuthenticatedTransport` narrowing + provider). The anonymous album
  provider is left untouched; album tests must still pass.
- PR-6: private/unlisted single-video authenticated path (consuming PR-5's
  provider) + fixtures + tests.

### 7.13 Non-goals

- `_perform_login` (interactive email/password login) and OAuth
  `client_credentials` clients with embedded secrets.
- Upload/edit/delete scopes.

### 7.14 Unresolved questions

1. Should the authenticated provider be a fresh sibling of
   `vimeoAlbumTokenProvider` (preferred, keeps the working anonymous album flow
   untouched) or a shared abstraction parameterized by fetch strategy?
2. Does the Go video flow ever need the `REQUIRES_AUTH` web client cookie, or is
   the anonymous viewer-JWT sufficient for unlisted (non-private) videos?

---

## 8. Feature 5 — Relevant Vimeo anti-bot behavior

### 8.1 Exact pinned-reference behavior

From `vimeo.py` `_real_extract` (lines 1263–1288):

- Requests use `impersonate=is_secure` (TLS fingerprint impersonation; `is_secure`
  is true for `https:` input URLs).
- The 403/429 handler is a **single block** (`error.cause.status not in (403, 429)`
  re-raises). Both statuses are treated as the **same class of failure** — a
  TLS-fingerprint / datacenter-IP block — **not** ordinary rate limiting. The
  in-source comment states: *"403 == vimeo.com TLS fingerprint or DC IP block;
  429 == player.vimeo.com TLS FP block"*.
- The outcome is distinguished by **response context**, not by status alone:
  - if the body contains *"Because of its privacy settings, this video cannot be
    played here"* → `_REFERER_HINT` (embed-only; needs a matching `Referer`);
  - else if the response carries an `impersonate` **target extension** (an
    impersonation target *was* used) → error naming that target + `dcip_msg`;
  - else if the request was **not** secure → plain `dcip_msg`;
  - else → the "blocked due to its TLS fingerprint" guidance.
- **`error_code 5460`** (from `_extract_from_api`) → geo / EU-login-required.
- There is **no** `Retry-After` handling and **no** generic rate-limit path in this
  block; the 429 is a fingerprint signal, not a throttle.

> Note: a *separate* comment at line 479 ("Most web client API requests are
> subject to rate-limiting (429) when logged-in") concerns `api.vimeo.com`
> logged-in requests. The existing `withVimeoAlbumToken` retry loop covers only
> **401/403** (verified: `status.Code == http.StatusUnauthorized ||
> status.Code == http.StatusForbidden`); it does **not** retry 429. Whether and
> how to handle an `api.vimeo.com` 429 is left **unresolved** (§8.14). Do not
> conflate the line-479 comment with this anti-bot block.

### 8.2 Credentials / cookies / tokens / scopes

- None beyond impersonation; anti-bot handling is transport-shaping, not auth.
  Interacts with Features 3–4 only in error categorization.

### 8.3 Reusable Go abstractions

- Impersonation is **fully implemented** (`chrome-133`/`firefox-120`);
  `vimeoImpersonationProfile` already pins chrome-133.
- `categorizeVimeoPlaylistTransportError` already maps
  `network.ErrImpersonationUnavailable → ErrTransportProfile` — extend the same
  pattern to the video path.
- `ErrTransportProfile` is the correct sentinel for a fingerprint/DC-IP block
  (both 403 and player 429). No new rate-limit sentinel is needed.

### 8.4 Missing abstractions

- A Vimeo anti-bot classifier for the video path that distinguishes outcomes by
  **origin, impersonation state, and response context** — mirroring the reference
  block, **not** by status code alone:
  - body says privacy/embed-only → set/require `Referer` (a `_REFERER_HINT`
    analogue) and surface an embed-only error;
  - **403 or 429** while an impersonation target was in use → `ErrTransportProfile`
    (TLS-fingerprint / DC-IP block) with a redacted, actionable message;
  - `error_code 5460` → `ErrAuthentication` (geo login).
- **Do not** introduce `ErrVimeoRateLimited` or `Retry-After` handling for this
  block; 429 here is a fingerprint block, not a throttle.
- Referer plumbing for embed-only videos.

### 8.5 Credential-origin & redirect boundaries

- Anti-bot handling must not weaken isolation: impersonation applies to the
  transport, credentials still route through `*NoRedirect`. No credential is added
  to satisfy an anti-bot check.

### 8.6 Secret redaction

- Anti-bot error messages must not echo request URLs verbatim — use `RedactRawURL`.
  No secrets involved, but signed URLs may appear in 403/429 contexts.

### 8.7 Cancellation & retry

- A 403/429 fingerprint block is **not** retried (a blind retry with the same
  fingerprint would loop); it is surfaced as `ErrTransportProfile`. There is no
  `Retry-After` handling for this block. `contextError(ctx)` is checked throughout.

### 8.8 Expected metadata / output

- No new metadata; correct categorization so callers can act (switch profile,
  supply cookies, retry later).

### 8.9 Categorized errors

| Condition | Sentinel |
|---|---|
| 403 (vimeo.com) fingerprint/DC-IP block | `ErrTransportProfile` |
| 429 (player.vimeo.com) fingerprint block | `ErrTransportProfile` |
| impersonation unavailable | `ErrTransportProfile` |
| error_code 5460 | `ErrAuthentication` |
| embed-only privacy (post-Referer) | `ErrAuthentication`/`ErrUnavailable` |

### 8.10 Deterministic fixture strategy

- `conformance/extractors/vimeo/`: `antibot-403.json`, `antibot-429.json`,
  `error-5460.json`, `embed-only.json`. Fixture transport returns these statuses
  and asserts impersonation profile + `Referer` on the retried request.

### 8.11 Tests

- **Success:** embed-only + correct `Referer` → extraction proceeds.
- **Failure:** 403 (vimeo.com) → `ErrTransportProfile`; 429 (player.vimeo.com) →
  `ErrTransportProfile` (fingerprint block, **not** a rate-limit sentinel);
  5460 → `ErrAuthentication`.
- **Context distinction:** assert the classifier keys on origin + impersonation
  state + body, so an identical status maps to the correct outcome.
- **Leakage:** anti-bot error strings pass `RedactRawURL` (no signed token in message).
- **Fuzz:** `error_code`/body-sniffer parsing never panics.

### 8.12 Recommended PR split

- PR-8: shared Vimeo anti-bot classifier + Referer plumbing + fixtures + tests.

### 8.13 Non-goals

- CAPTCHA solving; proxy rotation; residential-IP acquisition; new impersonation
  profiles beyond the two implemented.

### 8.14 Unresolved questions

1. Should the classifier ever attempt a profile fallback (chrome-133 →
   firefox-120) on a fingerprint block, or always surface `ErrTransportProfile`
   for the caller to decide? (Reference does not auto-fallback.)
2. How should a separate `api.vimeo.com` logged-in 429 (line 479) be treated if
   it surfaces? The existing `withVimeoAlbumToken` retry loop covers only
   **401/403** (verified: `status.Code == http.StatusUnauthorized ||
   status.Code == http.StatusForbidden`); it does **not** retry 429. Options:
   add explicit backoff, extend the retry loop to 429 on the API origin only,
   or surface `ErrTransportProfile`/a new API-rate-limit sentinel for the caller
   to decide. Left **unresolved** until a fixture proves the path is reachable.

---

## 9. Cross-Cutting: fixtures, redaction, and test taxonomy

### 9.1 Deterministic fixture pattern (shared)

Follow `vimeo_album_test.go`:

- A per-feature `*FixtureTransport` implements the exact narrowed interface the
  feature requires (`Do`/`ReadPage` return "must not be used" errors so the ambient
  path can never be exercised by accident).
- Fixtures are JSON files under `conformance/extractors/{twitch,vimeo}/`.
- All secrets in fixtures are **synthetic and obviously fake**
  (`synthetic-*`), and JWTs use a far-future `exp` (`4102444800`).
- Fixture transports validate request shape (method, host, headers) and reject any
  request bearing an unexpected `Authorization`/`Cookie`/`Proxy-Authorization`.
- Cancellation is testable via `ctx.Done()` blocking hooks (album's
  `blockPage`/`blockSlug` pattern).

### 9.2 Leakage test contract (shared)

Every feature adds a test that asserts, for every request the extractor makes:
1. credential/token headers appear only on the single intended host;
2. no redirect is ever followed on a credential-bearing request;
3. `err.Error()` for every categorized error contains none of the synthetic secret
   values.

### 9.3 Fuzz test contract (shared)

Every parser introduced (JWT/`xsrft`/cursor/`unlisted_hash`/`error_code`/403-body
sniffer) gets a `Fuzz*` test asserting no panic and bounded allocation on
arbitrary input.

---

## 10. Ordered Implementation PRs

Dependency order is top-to-bottom. Each PR is bounded, independently reviewable,
and lists exact file ownership. "Composer-safe" = mechanical, well-specified,
low-ambiguity change suitable for autonomous implementation; "No" = needs human
design judgment or touches shared/security-critical surfaces.

> **Ownership guard:** none of these PRs may touch `parity_manifest.yaml`, CI, or
> module config, and each must keep `git diff --check` clean. PRs that add error
> sentinels or CLI flags are ordered first because later PRs depend on them.

### PR-1 — Twitch authenticated transport + OAuth header + sentinels
- **Owns:** `internal/extractor/twitch.go` (add `twitchAuthenticatedTransport`,
  `twitchOAuthHeaders`, `ErrTwitchSubscriberOnly`), `internal/extractor/extractor.go`
  (only if a shared sentinel is preferred). Cookie sourcing via `internal/cookies`
  (read-only use).
- **Depends on:** none.
- **Acceptance:** OAuth header built only from a present `auth-token` cookie,
  guarded (≤512, no `\r\n\x00`); attaches only to `gql.twitch.tv`; no redirects;
  compiles; existing Twitch tests pass.
- **Verify:** `go build ./...`; `go test ./internal/extractor/ -run Twitch`;
  `git diff --check`.
- **Risk:** Medium (security-critical header/cookie path).
- **Composer-safe:** No (security boundary; human review of leakage tests).

### PR-2 — Twitch subscriber-only categorization + fixtures + tests
- **Owns:** `internal/extractor/twitch.go` (403-body sniffer, categorization),
  `internal/extractor/twitch_test.go`, `conformance/extractors/twitch/*.json`
  (new fixtures only).
- **Depends on:** PR-1.
- **Acceptance:** restricted+cookie → `ErrTwitchSubscriberOnly`;
  restricted+no-cookie → `ErrAuthentication`; leakage + fuzz tests pass.
- **Verify:** `go test ./internal/extractor/ -run Twitch`; `git diff --check`.
- **Risk:** Low–Medium.
- **Composer-safe:** Yes (spec is deterministic once PR-1 lands).

### PR-3 — `--video-password` plumbing + `ErrWrongPassword` + `xsrft` helper
- **Owns (full plumbing, public API → CLI → extractor):**
  - `pkg/ytdlp/client.go`: add the **public** `Request.VideoPassword` field, its
    input validation/cloning where the `Request` is copied/normalized, and
    propagation into the internal `extractor.Request` (so the value actually
    reaches the extractor);
  - `internal/extractor/extractor.go`: internal `Request.VideoPassword` +
    `ErrWrongPassword` sentinel + redacted stringers;
  - `internal/cli/run.go`: new `--video-password` flag wired into the public
    `Request`;
  - `internal/extractor/vimeo.go`: `xsrft` extraction helper.
- **Depends on:** none (but sequence before PR-4).
- **Acceptance:** flag parsed and threaded through `pkg/ytdlp.Request` →
  `pkg/ytdlp/client.go` → `extractor.Request`; the value **never appears** in
  emitted events, logs, or `err.Error()`. **Redaction is proven by leakage/round-
  trip tests, not by the existence of a stringer** — a `String()`/`GoString()`
  override alone is *not* accepted as proof.
- **Verify:** `go build ./...`; `go test ./pkg/ytdlp/ ./internal/cli/ ./internal/extractor/`
  (API test asserting the field propagates; CLI test asserting the flag binds and
  is redacted); `git diff --check`.
- **Risk:** Medium (touches public API + CLI + Request; must not log secret).
- **Composer-safe:** No (secret-handling surface).

### PR-4 — Vimeo video/player password verification + fixtures + tests
- **Owns:** `internal/extractor/vimeo.go` (`verifyVimeoVideoPassword`,
  optional player path), `internal/extractor/vimeo_test.go`,
  `conformance/extractors/vimeo/password-*.json` (new fixtures).
- **Depends on:** PR-3 **and** the profiled-no-redirect transport foundation
  (§6.4) — without it the password POST cannot be both impersonated and
  redirect-safe.
- **Acceptance:** correct password → media; 418 → `ErrWrongPassword`; no redirect
  followed off the password origin; `Set-Cookie` from the password origin merged
  into the operation jar and never forwarded to a different origin; leakage +
  fuzz tests pass.
- **Verify:** `go test ./internal/extractor/ -run Vimeo`; `git diff --check`.
- **Risk:** Medium.
- **Composer-safe:** **No** — depends on the new profiled-no-redirect transport
  primitive; the password POST is a security boundary (impersonation + no
  redirect + same-origin cookie acceptance) that requires human review.

### PR-5 — New cookie-bearing authenticated Vimeo viewer-JWT provider
- **Owns:** new `internal/extractor/vimeo_viewer.go` (a
  `vimeoAuthenticatedTransport` narrowing — `Transport` + `Cookies(rawURL string)`
  + `DoNoRedirect` — plus a cookie-bearing, exact-origin `_next/viewer` fetch that
  returns the **authenticated** web JWT, with caching + 401/403 refresh). The
  anonymous album provider in `vimeo_album.go` is **left untouched** (this is a new
  sibling, **not** a refactor of the working album flow).
- **Depends on:** none.
- **Acceptance:** authenticated JWT obtained only via a cookie-bearing fetch of
  `https://vimeo.com/_next/viewer` (no redirect); the `vimeo` cookie reaches only
  that origin and never `api.vimeo.com`; existing album tests remain unchanged and
  passing; cookie/JWT origin + leakage tests included (§7.11).
- **Verify:** `go test ./internal/extractor/ -run 'VimeoAlbum|VimeoViewer'`;
  `git diff --check`.
- **Risk:** Medium (new live auth path; must not regress album flow).
- **Composer-safe:** No (security boundary; human review of cookie/JWT origin tests).

### PR-6 — Vimeo private/unlisted authenticated single-video path + tests
- **Owns:** `internal/extractor/vimeo.go` (`videos/{id}` API path,
  `unlisted_hash`, `5460` mapping), `internal/extractor/vimeo_test.go`,
  `conformance/extractors/vimeo/video-api-*.json` (new fixtures).
- **Depends on:** PR-5.
- **Acceptance:** private/unlisted extraction with synthetic JWT/cookie; `5460` →
  `ErrAuthentication`; leakage + fuzz pass.
- **Verify:** `go test ./internal/extractor/ -run Vimeo`; `git diff --check`.
- **Risk:** Medium.
- **Composer-safe:** Yes (once PR-5 provider exists).

### PR-8 — Vimeo anti-bot classifier + Referer plumbing + tests
- **Owns:** `internal/extractor/vimeo.go` (anti-bot classifier keyed on origin +
  impersonation state + response body, Referer plumbing),
  `internal/extractor/vimeo_test.go`,
  `conformance/extractors/vimeo/antibot-*.json` (new fixtures).
- **Depends on:** PR-6 (shares video-path error mapping).
- **Acceptance:** both **403 (vimeo.com)** and **429 (player.vimeo.com)** →
  `ErrTransportProfile` (a TLS-fingerprint / DC-IP block, **not** a rate limit —
  **no** `ErrVimeoRateLimited`, **no** `Retry-After`); `5460` → `ErrAuthentication`;
  embed-only Referer path proven; classifier distinguishes outcomes by context, not
  status alone.
- **Verify:** `go test ./internal/extractor/ -run Vimeo`; `git diff --check`.
- **Risk:** Low–Medium.
- **Composer-safe:** Yes.

### PR-7 (optional) — Twitch VOD chat replay
- **Owns:** new `internal/extractor/twitch_chat.go` + tests +
  `conformance/extractors/twitch/vod-chat-*.json`; `Request.TwitchChat` opt-in.
- **Depends on:** PR-1/PR-2 (transport + fixtures pattern).
- **Acceptance:** deterministic pagination over fixtures; opt-in only; leakage +
  fuzz pass. **Blocked on resolving §4.14 output-model question.**
- **Verify:** `go test ./internal/extractor/ -run TwitchChat`; `git diff --check`.
- **Risk:** High (no upstream precedent; output model undecided).
- **Composer-safe:** No (requires product/design decisions first).

### Recommended first implementation PR

**PR-1 (Twitch authenticated transport + OAuth header + sentinels).** It has no
dependencies, unblocks PR-2 and PR-7, and establishes the credential-boundary and
redaction test scaffolding the other Twitch work reuses. Because it is a security
boundary it is **not** Composer-safe and should be human-authored/reviewed.

---

## 11. Global Non-Goals & Unresolved Design Decisions

### 11.1 Non-goals (all features)
- Interactive login flows (`_perform_login`) for Twitch or Vimeo, and TFA.
- Copying any embedded upstream OAuth Basic secrets (Vimeo android/ios/macos).
- Adding Python dependencies of any kind.
- CAPTCHA solving, proxy rotation, or residential IP acquisition.
- Chat rendering, emote/badge assets, or live (non-replay) chat.

### 11.2 Unresolved design decisions (roll-up)
1. **Twitch chat scope** — is Feature 1 in product scope given zero upstream
   precedent, and what is its output model? (§4.14)
2. **Password input channel** — global vs. site-scoped `--video-password`; player
   (`view == 4`) path in first cut or deferred? (§6.14)
3. **Vimeo/Twitch cookie source** — *resolved:* both `--cookies` and
   `--cookies-from-browser` already populate the operation jar, from which the
   Twitch `auth-token` and Vimeo `vimeo` session cookies are read; no
   `internal/cookies` change is needed. (Open sub-question: emit a hint when the
   jar lacks the expected cookie for a gated resource.) (§5.14, §7.14)
4. **Provider refactor vs. sibling** — reuse `vimeoAlbumTokenProvider` directly or
   fork it to protect the album flow. (§7.14; PR-5 isolates the risk)
5. **`api.vimeo.com` logged-in 429** — the *only* remaining Vimeo rate-limit
   question is the separate logged-in API 429 (§8.1 line-479 note): rely on the
   existing token retry-once or add explicit backoff. The anti-bot 403/429 is a
   fingerprint block (`ErrTransportProfile`), **not** a rate limit. (§8.14)
6. **Availability semantics** — when to stamp `subscriber_only`/`private`/`unlisted`
   on results. (§5.14)

### 11.3 Verification performed for this document
- `git diff --check` clean; no source/test/fixture/manifest/CI file modified.
- Only `docs/AUTHENTICATED_EXTRACTOR_ROADMAP.md` added.
- All reference claims checked against pinned commit
  `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`; all Go claims checked against the
  current tree (transport interfaces, `vimeo_album.go` reuse, absent
  `--video-password`, absent Twitch cookie/OAuth path, absent
  `ErrWrongPassword`/`ErrTwitchSubscriberOnly`).
