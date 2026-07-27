# JW Platform adapters wave 1 evidence

Baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
Go branch: `codex/jwplatform-adapters-wave1`

This wave adds nine public-site JW Platform adapters that discover validated
8-character media ids and hand off to the existing `jwplatform` extractor. No JW
playback logic was duplicated.

## Frozen scope

| Key | Reference class | Success evidence |
| --- | --- | --- |
| `bundesliga` | `BundesligaIE` | `vid` query → JW re-entry; zero network on invalid routes |
| `businessinsider` | `BusinessInsiderIE` | Page fixture → JW re-entry with slug display id (product test) |
| `dbtv` | `DBTVIE` | 8-character JW and 11-character YouTube transparent handoffs |
| `hollywoodreporter` | `HollywoodReporterIE` | Showcase card fixture → JW or YouTube re-entry (synthetic `fixture0001`) |
| `iltalehti` | `IltalehtiIE` | Ordered playlist fixture + canonical title |
| `lefigarovideoembed` | `LeFigaroVideoEmbedIE` | `__NEXT_DATA__` fixture → transparent JW re-entry with title + HTTPS poster; HTTP posters omitted |
| `mirrorcouk` | `MirrorCoUKIE` | `json-placeholder` fixture → JW re-entry preserving media id |
| `outsidetv` | `OutsideTVIE` | Play URL segment → JW re-entry; zero network |
| `theintercept` | `TheInterceptIE` | `initialStoreTree` fixture → transparent JW re-entry with id/title/timestamp; accepts >128 unique posts |

**Counted keys: 9.** Each key has Suitable coverage, adapter→JW Platform handoff,
product registry selection before `jwplatform`/`generic`, and negative routing in
`internal/extractor/jwplatform_adapters_wave1_test.go`. Cancellation, bounds, and
secret-safe failure coverage is family-level across the nine adapters in that
same file (not duplicated per key).

## Parsing evidence

The bounded `jwWave1JSToJSON` JS-to-JSON subset uses `skipJSNoise` for both
trailing-comma lookahead and top-level noise skipping. Coverage:

- `TestJWWave1JSToJSONSemantics`: unquoted keys, mixed quotes, escapes
  (`\'`, `\\`, `\x41`, `A`, control chars), line continuations, block
  comments, malformed inputs.
- `TestJWWave1JSToJSONCombinesCommentsWithTrailingCommas`: line and block
  comments separating a comma from `}`/`]`, nested trailing commas, and
  block comments interspersed with content.
- `TestJWWave1JSToJSONBounds` and `TestJWWave1JSToJSONOutputBound` exercise
  the input-cap rejection and the per-write output-cap rejection via the new
  `boundJSONOutput` guard in `internal/extractor/jwplatform_adapters_wave1_relaxed.go`.

The Hollywood Reporter synthetic YouTube fixture uses the repository's
`fixture0001` convention in lieu of any real YouTube identifier.

## Transparent metadata evidence

- `TestOperationMergesTransparentEntryMetadata` asserts producer
  `id`, `title`, `thumbnail`, `duration`, `timestamp`, `view_count`,
  `availability`, and `language` win across the final recursion step.
- `TestOperationTransparentOverlayDoesNotEraseBackendMetadata` confirms an
  overlay with only an id/title never erases child fields the backend
  supplied.
- `TestOperationTransparentEntryAcrossTwoHopURLResults` exercises
  transparent metadata across two consecutive URL-result handoffs while
  preserving a middle-stage `description`.
- `TestOperationMergesTransparentParentInfoFromURLResult` and
  `TestOperationAmaraNestedTransparentPreservesChildID` continue to pass
  with the helper refactor (no regressions in Amara or generic parent
  propagation).

JW-backed product coverage:

- `TestProductMirrorCoUKReentersJWPlatformMediaID` (unchanged): media id
  round-trip through the JW backend.
- `TestProductBusinessInsiderArticleIDSurvivesJWReentry`: article slug
  preserved on the handoff.
- `TestProductLeFigaroPosterAndTitleSurviveJWReentry`: producer title and
  HTTPS poster override backend metadata.
- `TestProductTheInterceptProducerMetadataSurvivesJWReentry`: producer id,
  title, and timestamp survive recursion.

## Deliberate hardening vs pinned reference

- Exact hostname/path routing only under `hostedRejectUnsafeURL`.
- HTTPS canonical page URLs for fetched adapters.
- Bundesliga, DBTV JW, and Outside TV perform no webpage request.
- Hollywood Reporter never echoes unsupported showcase types in errors.
- Iltalehti uses balanced `window.App` extraction with the bounded `jwWave1JSToJSON` subset (unquoted keys, single/double quotes, trailing commas — including trailing commas separated from `}`/`]` by line or block comments — `undefined`/`void 0`, and pinned string escapes) without executing page JavaScript.
- The Intercept matches posts by slug in single-pass map iteration and rejects duplicate slugs; the page and balanced-JSON byte caps already bound the post map so no separate post-count cap is required, and 129+ unique non-matching posts with one match succeed.
- Le Figaro accepts only HTTPS posters through `strictValidHostedHTTPURL` + scheme check; HTTP and non-conforming posters are omitted without failing the extraction.
- All JW-backed site adapters share the validated transparent helper
  `jwPlatformURLEntry` defined alongside `jwPlatformURLResult` in
  `internal/extractor/kaltura_jw_adapters.go`; the wave-1 adapter
  handoff no longer duplicates that contract.
- Hollywood Reporter's YouTube showcase fixture uses the synthetic
  `fixture0001` id to avoid real-identifier fixtures.

## Deliberate, currently unrepresentable deviations

The following upstream-only fields are not represented by `extractor.Entry`
and are explicitly recorded as deviations rather than parity:

- DBTV `display_id` — DBTV's adapter discards the article slug and uses the
  validated 8-character JW Platform media id (or 11-character YouTube id)
  as the entry id; the upstream `display_id` distinction is lost because
  `extractor.Entry` has no dedicated display slot. Until `Entry` is
  expanded, this is a deliberate simplification.
- Mirror.co.uk `display_id` — Mirror's adapter discards the article slug
  and preserves only the validated JW Platform media id as the entry id;
  the upstream `display_id` distinction is lost for the same reason.
- The Intercept `display_id`, `description`, and `comment_count` — The
  Intercept's adapter surfaces the numeric post id, producer title, and
  release timestamp; the upstream `display_id` (URL slug), post
  `description`, and `comment_count` are not representable because
  `extractor.Entry` lacks dedicated slots for them. Recording them would
  require widening the schema, which is out of scope for this PR.
- Le Figaro `description` — not on `extractor.Entry`; producer title and
  HTTPS poster are preserved, description is unavailable.

The plan deliberately keeps these as deviations; widening `extractor.Entry`
is not part of this PR.

## Synthetic-fixture caveats

Fixture pages embed synthesised identifiers because real third-party media
ids cannot be checked into the repository. Routing, host matching, slug
bounds, and parsing invariants are the real signal of correctness here;
playback correctness is deferred to the existing `jwplatform` and
`youtube` extractors.

## Checklist promotion

`go run ./cmd/extractorinventory` promotes these rows to `already_supported`:

- `bundesliga`, `businessinsider`, `dbtv`, `hollywoodreporter`, `iltalehti`,
  `lefigarovideoembed`, `mirrorcouk`, `outsidetv`, `theintercept`

Post-wave inventory counts are regenerated from
`/Users/tejas/projects/yt-dlp-reference` at the end of this branch and
captured in `conformance/extractors/upstream_master_checklist.csv` plus
`docs/EXTRACTOR_MASTER_CHECKLIST.md`.

## Verification commands

```sh
gofmt -w internal/extractor/jwplatform_adapters_wave1.go internal/extractor/jwplatform_adapters_wave1_relaxed.go internal/extractor/jwplatform_adapters_wave1_test.go internal/extractor/jwplatform_adapters_wave1_relaxed_test.go internal/extractor/kaltura_jw_adapters.go pkg/ytdlp/client.go pkg/ytdlp/client_test.go pkg/ytdlp/jwplatform_adapters_wave1_test.go
go test ./internal/extractor -run 'JWPlatformAdaptersWave1|JWWave1|Iltalehti|LeFigaro|TheIntercept' -count=1
go test ./pkg/ytdlp -run 'JWPlatformAdaptersWave1|ProductBusinessInsider|ProductLeFigaro|ProductTheIntercept|ProductMirrorCoUK|TransparentEntryMetadata|TransparentMetadataAcrossURLResults|ProductRegistryIncludesIntegratedExtractors' -count=1
go test -race ./internal/extractor -run 'JWPlatformAdaptersWave1|JWWave1|Iltalehti|LeFigaro|TheIntercept' -count=1
go test -race ./pkg/ytdlp -run 'JWPlatformAdaptersWave1|ProductBusinessInsider|ProductLeFigaro|ProductTheIntercept|ProductMirrorCoUK|TransparentEntryMetadata|TransparentMetadataAcrossURLResults|ProductRegistryIncludesIntegratedExtractors' -count=1
go run ./cmd/extractorinventory -reference /absolute/path/to/yt-dlp-reference -repository . -output conformance/extractors/upstream_master_checklist.csv
go test -p 4 ./... -count=1
go test ./internal/upstreamdelta -count=1
go vet ./...
go mod tidy -diff
go run ./cmd/paritycheck
docker build -f .github/python-free.Dockerfile -t ytdlp-go:jwplatform-pr131 .
```

Provenance: `conformance/extractors/shared/jwplatform-adapters-wave1/PROVENANCE.md`
