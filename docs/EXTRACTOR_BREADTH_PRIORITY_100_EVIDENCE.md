# Extractor breadth priority-100 evidence

Baseline program start: `172a718c5f7ab660836ef52967858ac2f817c5e9`
Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
Rebase base / current `origin/main`: `73f992c489f7e964e3324af32953da182ee64aa1` (Wave 1 `#87` plus later main, including YouTube production-parity `#90`).
This PR continues the same program after wave 1 (`#87`).

## Automated inventories (authoritative)

Do **not** trust prose arithmetic. Counts are enforced by:

- `TestBreadthPriority100AuditableInventory` in `internal/extractor/breadth_priority_100_ledger_test.go`
- Shape runners in `internal/extractor/breadth_priority_100_shapes_test.go`
- Negative/security matrix in `internal/extractor/breadth_priority_100_negatives_test.go`

Current automated floors:

| Metric | Floor | Source |
|--------|------:|--------|
| Success URL shapes (Extract / URLResult re-entry / playlist CollectEntries) | **≥100** | `breadthMinSuccessShapes` |
| Playlist/feed behaviors added by this program since baseline | **≥20** | `breadthMinPlaylists` |

Shape entries carry a **canonical identity** (`key|route-syntax`). Duplicate IDs, fixture cardinality (`-single`/`-multi` on the same path), hostname aliases with identical behavior, and payload-only variants are rejected by the ledger.

Aliases (`www`/identical-host mirrors), optional query order, and result cardinality are not counted as distinct shapes. Suitable-only routing is never counted.

## Keys added on this branch (beyond wave 1)

**Brightcove adapters (11):** `pgatour`, `ninenews`, `ninenow`, `netapp`, `netapp_collection`, `amcnetworks`, `craftsy`, `tvo`, `tva`, `tvanouvelles`, `tvanouvelles_article`

**Kaltura/JW adapters (6):** `unitednationswebtv`, `azmedien`, `heise`, `inc`, `spiegel`, `onefootball`

**Arc POWA expansion (9):** `actionnewsjax`, `elcomercio`, `lateja`, `fifthdomain`, `vlno`, `fourteennews`, `globeandmail`, `pilotonline`, `uppermichigansource`

**Podcast feeder family (11):** `acast`, `acast_channel`, `simplecast`, `simplecast_episode`, `simplecast_podcast`, `megaphone`, `art19`, `art19_show`, `libsyn`, `spreaker`, `spreaker_show`

Podcast media normalization uses one Python-free implementation of the pinned
`clean_podcast_url` tracking-prefix and double-scheme corpus. Acast and
Simplecast fixtures exercise nested analytics prefixes; final URLs are then
subject to the port's strict public-host URL policy. Simplecast podcast pages
use the attributable `sites/search` response envelope before the bounded
podcast episode list request.

**Additional families (7):** `nowness`, `nowness_playlist`, `nowness_series`, `dacast`, `dacast_playlist`, `panopto`, `panopto_playlist`

**Second-review honest restorations (12):** `teachingchannel`, `nowcanal`, `democracynow`, `buzzfeed`, `mediastream`, `winsports`, `abcotvs`, `abcotvs_clips`, `vidsio`, `laracasts`, `laracasts_series` (+ existing-route expansions: NOWNESS `/category/`, Panopto playlist `Embed.aspx?pid=`, AZ Medien telem1/tvo-online hosts)

## Playlist contract

Every program playlist in the automated inventory uses `LazyFirstPageEntries` or `OnDemandEntries` (not eager `StaticEntries`):

- lazy first-page/network (no fetch before first `Next`)
- ordered + independently reusable iteration
- bounded pages/entries
- duplicate skip where applicable
- hostile continuation rejection (`spreaker_show` `next_url`)
- cancellation checked on Extract and between pages via `OnDemandEntries` / CollectEntries

## Families (≥8)

Wave 1 families remain: Cloudflare Stream, Arc Publishing, Anvato, ThePlatform, plus pre-existing Brightcove/Kaltura/JW/Wistia/SproutVideo backends.
This PR adds **Podcast / feeder APIs**, **NOWNESS**, **Dacast**, **Panopto**, and **MediaStream**.

## Compatibility claims

Manifest/`SUPPORTED_SITES` entries for these keys claim **deterministic-corpus / fixture-backed** behavior only. Live-service compatibility is not claimed from synthetic fixtures. Routing-only wrappers are not counted unless URLResult re-entry succeeds in the inventory.

## Explicit deviations (do not overclaim)

- **Laracasts → Vimeo referrer:** Upstream smuggles a `laracasts.com` Referer onto the Vimeo player URL. This implementation emits `https://player.vimeo.com/video/{id}` and the registered Vimeo extractor fetches `https://vimeo.com/{id}` with Vimeo's own profile/Referer policy. Subscriber-gated Vimeo responses that require the Laracasts referrer are out of scope.
- **ABCOTVS `publishedKey`:** Reference may fall back to `publishedKey` when selecting the media id. This adapter uses the story/video `id` from the OTV API payload (and the URL id as fallback) and does not implement `publishedKey` selection or full metadata parity (timestamps, thumbnails, captions).
- **BuzzFeed Facebook embeds:** Facebook bucket URLs are retained as bare playlist entries with an empty `ExtractorKey`. There is no registered Facebook extractor, so an explicit `facebook` key is never emitted (would invent a guaranteed-bad `SelectFor` route). YouTube bucket URLs keep `ExtractorKey=youtube` with verified fixture re-entry. The BuzzFeed provenance fixture includes one YouTube child and one bare Facebook child (distinct URLs; no same-URL dedupe collapse).

- **Oversized inputs:** Page-backed new adapters reject pages `> maxExtractorJSONBytes` as `ErrInvalidMetadata`. API-backed `abcotvs` / `abcotvs_clips` reject bounded JSON overflow as `ErrJSONResponseTooLarge` via `hostedRequestJSON`.
