# Extractor routing-controls evidence

This lane implements the bounded routing controls attributable to
`yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` (`YoutubeDL.py` and
`extractor/generic.py`). Routing is deterministic and performs no network
lookup.

## Force-generic routing

`Request.ForceGenericExtractor` and the hidden CLI flag
`--force-generic-extractor` select the registered `generic` extractor for
automatic URL routing. The policy does not override an explicit playlist
`ExtractorKey` or an explicit `Request.PluginID`. The registered generic
extractor's HTTP(S), host, and userinfo checks still run; unsupported schemes,
malformed URLs, and credential-bearing generic targets fail before transport
construction.

## Default-search routing

`Request.DefaultSearch` and `--default-search` support the pinned modes:

- empty or `fixup_error`: repair an unqualified public DNS host when its shape
  is unambiguous, otherwise report an unsupported input;
- `error`: report an unsupported unqualified input;
- `auto`: route an unqualified term to the registered `ytsearch` pseudo-
  extractor;
- `auto_warning`: the same route with a privacy-safe metadata warning;
- a prefix owned by a registered search extractor: `ytsearch*`, `scsearch*`,
  `nicosearch`, `prxstories`, or `prxseries`.

Search terms are bounded by the selected extractor's existing query policy.
The generated routing URL contains only a fixed placeholder. The original
validated term is carried in typed internal request plumbing and escaped by
the selected search extractor when it constructs its service request. This
keeps query syntax, credentials, and control characters out of routing
targets and diagnostics.

Protocol-less localhost and IP-literal inputs, protocol-less userinfo, path
inputs, oversized or invalid-UTF-8 inputs, unsupported prefixes, and invalid
prefix syntax fail closed. Explicit HTTP(S) URLs remain URL inputs regardless
of `DefaultSearch`; no URL-vs-search lookup or probing is performed.

## Evidence

- `pkg/ytdlp/routing_controls_test.go`
- `engine.TestClientForceGenericUsesRegisteredGenericExtractor`
- `internal/cli/routing_controls_test.go`
- `internal/extractor/TestYouTubeSearchUsesBoundedProductQueryOverride`
- `internal/extractor.TestRegistryHonorsExplicitExtractorKey`
- `docs/CLI_FLAG_INVENTORY.md`
- `conformance/routing/PROVENANCE.md`

The tests cover URL ambiguity, HTTP(S) and pseudo schemes, localhost/IP and
userinfo rejection, registered search prefixes, generic forcing, batch input
plumbing, cancellation, categorized errors, concurrent routing, explicit
plugin precedence, and credential-safe diagnostics. They do not claim
extractor discovery/listing, `--use-extractors`, extractor arguments, new
search providers, plugin discovery, broad URL fixup, or unbounded extractor
width.
