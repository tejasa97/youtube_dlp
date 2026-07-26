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
| `businessinsider` | `BusinessInsiderIE` | Page fixture → JW re-entry with slug display id |
| `dbtv` | `DBTVIE` | 8-character JW and 11-character YouTube transparent handoffs |
| `hollywoodreporter` | `HollywoodReporterIE` | Showcase card fixture → JW or YouTube re-entry |
| `iltalehti` | `IltalehtiIE` | Ordered playlist fixture + canonical title |
| `lefigarovideoembed` | `LeFigaroVideoEmbedIE` | `__NEXT_DATA__` fixture → JW re-entry with title/poster |
| `mirrorcouk` | `MirrorCoUKIE` | `json-placeholder` fixture → JW re-entry preserving media id |
| `outsidetv` | `OutsideTVIE` | Play URL segment → JW re-entry; zero network |
| `theintercept` | `TheInterceptIE` | `initialStoreTree` fixture → transparent JW re-entry with metadata |

**Counted keys: 9.** Each key has Suitable coverage, adapter→JW Platform handoff,
product registry selection before `jwplatform`/`generic`, and negative routing in
`internal/extractor/jwplatform_adapters_wave1_test.go`. Cancellation, bounds, and
secret-safe failure coverage is family-level across the nine adapters in that
same file (not duplicated per key).

## Deliberate hardening vs pinned reference

- Exact hostname/path routing only under `hostedRejectUnsafeURL`.
- HTTPS canonical page URLs for fetched adapters.
- Bundesliga, DBTV JW, and Outside TV perform no webpage request.
- Hollywood Reporter never echoes unsupported showcase types in errors.
- Iltalehti uses balanced `window.App` extraction with the bounded `jwWave1JSToJSON` subset (unquoted keys, single/double quotes, trailing commas, comments, `undefined`/`void 0`, and pinned string escapes) without executing page JavaScript.
- Mirror.co.uk omits `display_id` until Entry supports it; the JW Platform media id is preserved through handoff and product re-entry.
- Le Figaro uses balanced `__NEXT_DATA__` extraction.
- The Intercept matches posts by slug in sorted, bounded map iteration and rejects duplicate slugs.
- The Intercept matches the bare `theintercept.com` host only.

## Checklist promotion

`go run ./cmd/extractorinventory` promotes these rows to `already_supported`:

- `bundesliga`, `businessinsider`, `dbtv`, `hollywoodreporter`, `iltalehti`,
  `lefigarovideoembed`, `mirrorcouk`, `outsidetv`, `theintercept`

Post-wave inventory counts: `already_supported=106`, `uses_existing_shared_backend=52`.

## Verification commands

```sh
gofmt -w internal/extractor/jwplatform_adapters_wave1.go internal/extractor/jwplatform_adapters_wave1_relaxed.go internal/extractor/jwplatform_adapters_wave1_test.go internal/extractor/jwplatform_adapters_wave1_relaxed_test.go pkg/ytdlp/client.go pkg/ytdlp/client_test.go pkg/ytdlp/jwplatform_adapters_wave1_test.go
go test ./internal/extractor -run 'JWPlatformAdaptersWave1|JWWave1' -count=1
go test ./pkg/ytdlp -run 'JWPlatformAdaptersWave1|ProductRegistryIncludesIntegratedExtractors' -count=1
go test -race ./internal/extractor -run 'JWPlatformAdaptersWave1|JWWave1' -count=1
go test -p 4 ./... -count=1
go vet ./...
go run ./cmd/paritycheck
docker build -f .github/python-free.Dockerfile .
```

Provenance: `conformance/extractors/shared/jwplatform-adapters-wave1/PROVENANCE.md`
