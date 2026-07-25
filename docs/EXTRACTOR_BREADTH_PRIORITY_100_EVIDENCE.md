# Extractor breadth priority-100 evidence

Baseline program start: `172a718c5f7ab660836ef52967858ac2f817c5e9`
Pinned reference: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
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

Aliases (`www`/identical-host mirrors) are not double-counted as distinct shapes. Suitable-only routing is never counted.

## Keys added on this branch (beyond wave 1)

**Brightcove adapters (11):** `pgatour`, `ninenews`, `ninenow`, `netapp`, `netapp_collection`, `amcnetworks`, `craftsy`, `tvo`, `tva`, `tvanouvelles`, `tvanouvelles_article`

**Kaltura/JW adapters (6):** `unitednationswebtv`, `azmedien`, `heise`, `inc`, `spiegel`, `onefootball`

**Arc POWA expansion (9):** `actionnewsjax`, `elcomercio`, `lateja`, `fifthdomain`, `vlno`, `fourteennews`, `globeandmail`, `pilotonline`, `uppermichigansource`

**Podcast feeder family (11):** `acast`, `acast_channel`, `simplecast`, `simplecast_episode`, `simplecast_podcast`, `megaphone`, `art19`, `art19_show`, `libsyn`, `spreaker`, `spreaker_show`

**Additional families (7):** `nowness`, `nowness_playlist`, `nowness_series`, `dacast`, `dacast_playlist`, `panopto`, `panopto_playlist`

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
This PR adds **Podcast / feeder APIs**, **NOWNESS**, and **Dacast**.

## Compatibility claims

Manifest/`SUPPORTED_SITES` entries for these keys claim **deterministic-corpus / fixture-backed** behavior only. Live-service compatibility is not claimed from synthetic fixtures. Routing-only wrappers are not counted unless URLResult re-entry succeeds in the inventory.
