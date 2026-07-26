# Brightcove adapters wave 2 evidence

Baseline: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`
Go branch: `codex/brightcove-adapters-wave-2`

This wave adds eight public-site Brightcove adapters that discover validated
account/player/video identities and hand off to the existing `brightcove`
extractor. No Brightcove playback logic was duplicated.

## Frozen scope

| Key | Reference class | Success evidence |
| --- | --- | --- |
| `formula1` | `Formula1IE` | Direct URL → Brightcove re-entry; zero network on invalid routes |
| `europeantour` | `EuropeanTourIE` | Page fixture → Brightcove re-entry |
| `maoritv` | `MaoriTVIE` | Page fixture → Brightcove re-entry |
| `thestar` | `TheStarIE` | Page fixture → Brightcove re-entry |
| `thesun` | `TheSunIE` | Ordered playlist fixture + `og:title`; malformed id rejection |
| `wimbledon` | `WimbledonIE` | Metadata API fixture → transparent Brightcove URL with title/duration |
| `usatoday` | `USATodayIE` | `ajax=true` page fixture → transparent Brightcove URL with metadata |
| `skynewsau` | `SkyNewsAUIE` | Page + News API fixture → transparent Brightcove URL with title/timestamp |

**Counted keys: 8.** Each key has Suitable coverage, adapter→Brightcove handoff,
product registry selection before `brightcove`/`generic`, negative routing,
cancellation, bounds, and secret-safe failure tests in
`internal/extractor/brightcove_adapters_wave2_test.go`.

## Deliberate hardening vs pinned reference

- Exact hostname/path routing only under `hostedRejectUnsafeURL`.
- HTTPS canonical page/API and Brightcove player URLs.
- Formula1 performs no webpage request.
- The Sun rejects non-digit `data-video-id-pending` values instead of emitting unsafe entries.
- USA Today uses bracket-balanced `ui-video-data` JSON extraction instead of naive regex.
- Sky News AU validates `embedcode` as `account-video` before API fetch; API key is never returned in errors.
- Description metadata from Wimbledon/USA Today is not copied because `Entry` does not support it.

## Checklist promotion

`go run ./cmd/extractorinventory` promotes these rows to `already_supported`:

- `formula1`, `europeantour`, `maoritv`, `thestar`, `thesun`, `wimbledon`, `usatoday`, `skynewsau`

Post-wave inventory counts: `already_supported=94`, `uses_existing_shared_backend=61`.

## Verification commands

```sh
gofmt -w internal/extractor/brightcove_adapters_wave2.go internal/extractor/brightcove_adapters_wave2_test.go pkg/ytdlp/client.go pkg/ytdlp/client_test.go
go test ./internal/extractor -run 'Formula1|EuropeanTour|MaoriTV|TheStar|TheSun|Wimbledon|USAToday|SkyNewsAU|BrightcoveAdaptersWave2' -count=1
go test -race ./internal/extractor -run 'Formula1|EuropeanTour|MaoriTV|TheStar|TheSun|Wimbledon|USAToday|SkyNewsAU|BrightcoveAdaptersWave2' -count=1
go test -p 4 ./... -count=1
go vet ./...
go run ./cmd/paritycheck
docker build -f .github/python-free.Dockerfile .
```

Provenance: `conformance/extractors/shared/brightcove-adapters-wave2/PROVENANCE.md`
