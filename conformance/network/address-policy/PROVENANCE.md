# Network address policy and extractor retry provenance

Reference: pinned `yt-dlp` commit `aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`,
primarily `yt_dlp/options.py` (`--source-address`, `--force-ipv4`,
`--force-ipv6`, and `--extractor-retries`) and the shared networking/retry
contracts in `yt_dlp/networking/` and `yt_dlp/extractor/common.py`.

This change implements the bounded native-Go subset of that behavior:

- `--source-address` binds native HTTP and browser-profile TCP dials to a
  validated IP address; `--force-ipv4`/`-4` and `--force-ipv6`/`-6` select
  wildcard family bindings.
- Address options are resolved in command-line order. A later source address
  clears a previous family choice, and a later family choice clears a prior
  source address or opposite family. The programmatic `Request` rejects
  conflicting fields instead of guessing.
- HTTP CONNECT, SOCKS5, and SOCKS5H proxy connections inherit the selected
  local family. SOCKS5H retains a domain-form target so DNS remains proxy-side.
- `--extractor-retries` is an opt-in replay of the entered extractor call
  boundary, before media selection, downloads, sidecars, postprocessors, or
  archive commits. The selected extractor must explicitly implement the
  internal `RetrySafeExtractor` capability; ordinary extractors run once even
  when a retry count is configured because whole-Extract replay is not assumed
  to be side-effect free. For an opted-in extractor, only transient typed
  network failures and extractor/native HTTP 408/429/5xx statuses are eligible.
  Cancellation, unsupported/invalid/authentication/geo/unavailable outcomes,
  and permanent 4xx statuses stop immediately.
- DNS not-found and non-temporary resolver failures are permanent. DNS
  timeout/temporary signals and concrete connection refusal/reset/abort,
  unreachable-network/host, timeout, and broken-pipe failures remain eligible.
- Retry events contain a redacted URL (including case-insensitive
  `X-Amz-Signature` query keys) and categorized, URL-free diagnostics.
  Retry count and exponential delays are bounded by the existing downloader
  retry limits; tests inject the wait hook for deterministic sequencing.
- Custom `RoundTripper` values must implement the explicit policy capability
  before a non-default source or family is accepted. Rejections are generic
  and never echo the requested address or transport error details.

Evidence is synthetic and offline. It proves address-family selection,
loopback source binding, proxy handshake behavior, replay-safe retry
classification, cancellation preservation, bounds, last-wins CLI plumbing, and
secret-safe diagnostics. The registered production extractors do not currently
claim the replay-safety capability, so this lane does not claim automatic
whole-Extract retries for them. It also does not claim external-downloader
interface binding, geo verification/XFF, file URLs, or retrying post-download/
postprocess operations.
