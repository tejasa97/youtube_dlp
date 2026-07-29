# HDS protocol

The `internal/protocol/hds` package implements the bounded, unencrypted
subset of Adobe HTTP Dynamic Streaming (HDS) / Flash Media Manifest (F4M)
VOD content derived from yt-dlp's `f4m.py` downloader. It is intentionally
narrow: live streams, encrypted variants, and DRM-protected media are
rejected with categorized errors rather than partially supported.

## Components

`internal/protocol/hds` is split into focused files:

| File | Role |
| --- | --- |
| `box.go` | ISO box framing (32-bit and 64-bit size headers). |
| `errors.go` | Categorized error sentinels. |
| `manifest.go` | F4M XML parsing, DRM filtering, namespace validation, baseURL resolution. |
| `bootstrap.go` | ABST/ASRT/AFRT binary parsing with strict bounds. |
| `flv.go` | FLV header, optional metadata tag, mdat extraction. |
| `plan.go` | `(segment, fragment)` pair derivation and signed-URL-respecting URL construction. |
| `downloader.go` | End-to-end bounded GET + retry + redirect walker + atomic output commit. |

## Supported subset

- F4M 1.0 and 2.0 XML manifests with a single root element in the Adobe
  namespace, optional `<baseURL>` (absolute or relative), `<media>`
  selection by bitrate, and inline or external `<bootstrapInfo>`.
- ABST bootstrap with one ASRT (segment run table) and one AFRT (fragment
  run table), `flags & 0x20` not set (VOD only).
- `Seg<segment>-Frag<number>` URL construction with verbatim path
  concatenation, preserving existing query bytes (including signed
  duplicates), and appending `pv-2.0` plus caller extra query params.
- FLV output: 13-byte header + optional 24-bit-bounded metadata tag +
  concatenated first-mdat payloads.

## Rejected subset

- Live ABST (bootstrap flag 0x20): `ErrUnsupportedLive`.
- Encrypted / DRM-attached media: `ErrUnsupportedDRM`.
- Unscoped `<drmAdditionalHeader>` / `<drmAdditionalHeaderSet>`.
- Non-http(s) schemes, userinfo in any URL, malformed box sizes, oversized
  bootstrap or media bodies, redirect chains beyond 5 hops, redirect to a
  disallowed scheme.

## Security guarantees

- `Authorization`, `Cookie`, `Proxy-Authorization`, `Referer` are stripped
  from `Config.Headers` on construction and re-applied before every fetch.
- Bounded byte limits on manifest, bootstrap, fragment, and final output
  prevent unbounded allocation from hostile input.
- Temporary output is created with `O_CREATE|O_EXCL` in a unique filename
  inside the validated output root; commit uses backup-then-rename with
  rollback to preserve the previous destination on rename failure.
- All transport errors are passed through `redactAllURLs` so signed URLs
  cannot reach logs or telemetry.

## Configuration

```go
dl, err := hds.NewDownloader(transport, hds.Config{
    Headers:           someHeaders,
    Attempts:          3,                 // bounded retries
    RetryBaseDelay:    250 * time.Millisecond,
    RetryMaxDelay:     4 * time.Second,
    MaxFragmentSize:   64 << 20,          // 64 MiB
    MaxOutputBytes:    8 << 30,           // 8 GiB
    ExtraSegmentQuery: "injected=1",
    RequestedBitrate:  0,                 // 0 = highest available
})
```

Negative values are rejected by `validateConfig` before defaults are
applied.

## Validation

- `go build ./internal/protocol/hds/...`
- `go test ./internal/protocol/hds/ -count=1 -race`
- `go test ./internal/protocol/hds/ -fuzz=FuzzParseManifest -fuzztime=10s`
- `go test ./internal/protocol/hds/ -fuzz=FuzzParseBootstrap -fuzztime=10s`
- `go test ./internal/protocol/hds/ -fuzz=FuzzFixBareAmpersands -fuzztime=10s`
