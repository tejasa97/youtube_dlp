# yt-dlp Go Port Evaluation

Date: 2026-07-17  
Reference snapshot: [`yt-dlp/yt-dlp@aefce1ee`](https://github.com/yt-dlp/yt-dlp/tree/aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8), dated 2026-07-14

## Verdict

A pure-Go implementation is feasible when the objective is **capability parity**, rather than compatibility with yt-dlp's Python implementation or ABI.

The target is a Go implementation capable of achieving everything yt-dlp achieves. It may provide:

- A native Go API instead of `import yt_dlp`.
- A new cross-platform plugin ABI instead of loading Python wheels.
- Different regex, HTTP, JavaScript, cryptography, and cookie implementations.
- Better structured events, cancellation, concurrency, security, and packaging.
- No Python runtime in the final generally available product.

The CLI syntax, configuration files, output templates, info JSON, download archives, and common filesystem behavior should remain compatible where practical. These are user-facing workflows and persisted data, unlike the Python ABI.

This should be approached as a staged new implementation, not a line-by-line translation or big-bang rewrite.

## Current Project Scale

Measured at the reference snapshot:

- 225,471 lines of product Python across 1,045 files.
- 195,805 lines, or 86.8%, are extractors across 971 files.
- 1,751 registered built-in extractors; 1,615 are marked working and 136 broken.
- 295 option registrations exposing 363 distinct CLI switches and aliases.
- 144 documented `YoutubeDL` API parameters.
- 18,368 lines of tests, including 4,137 dynamically generated extractor cases and 773 static test functions.
- During the preceding year: 623 commits, 431 touching extractors, approximately 54,000 lines of extractor churn, 145 contributor emails, and 101 commits touching YouTube extraction.

The project warns that listed sites are not guaranteed to work because websites continuously change. Its nightly channel builds on changed days and its `master` channel builds after every push.

- [Supported-sites caveat](https://github.com/yt-dlp/yt-dlp/blob/aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8/supportedsites.md#L1-L6)
- [Release-channel policy](https://github.com/yt-dlp/yt-dlp/blob/aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8/README.md#L163-L169)

## Capability-Parity Scope

The Go implementation should cover:

1. All built-in sites and URL routing.
2. Metadata, formats, playlists, searches, comments, subtitles, thumbnails, live streams, and scheduled streams.
3. Authentication, proxies, geo-routing, browser cookies, TLS impersonation, and client certificates.
4. HTTP, HLS, DASH, WebSocket, RTMP, and external downloaders.
5. Format selection, filtering, output templates, archives, retries, resume, and partial downloads.
6. ffmpeg postprocessing, metadata, thumbnails, chapters, and SponsorBlock.
7. Extensibility equivalent to extractors, postprocessors, transports, and challenge/token providers.
8. A native Go embedding API with structured progress and cancellation.
9. Current Windows, macOS, and Linux architecture coverage, updates, and release channels.

Existing Python plugins do not need to execute unchanged. Every category of extension they support must have an equivalent mechanism in the new system.

## Proposed Architecture

| Component | Recommended implementation |
|---|---|
| Core | Native Go library; the CLI is a thin client over it |
| Data model | Extensible ordered values rather than rigid structs; preserve unknown extractor fields |
| Extractors | Compiled Go modules sharing an `InfoExtractor`-like helper library |
| Plugins | Versioned subprocess/RPC or WASM ABI; avoid Go's fragile native `plugin` mechanism |
| Networking | Pluggable transports: native Go for ordinary traffic and impersonating transports for protected sites |
| Regex | Dual engine: RE2 for safe patterns and a bounded Python-compatible backtracking engine where necessary |
| JavaScript | Initially run EJS through a managed JS runtime; replace selected challenge logic with Go later |
| Media | Continue using ffmpeg/ffprobe; rewriting media codecs is outside the useful scope |
| Postprocessors | Native Go orchestration around ffmpeg, with native implementations where beneficial |
| Updates | Signed core binary and independently updateable signed extractor packs |
| Compatibility | Existing CLI, configuration, template, archive, and info-JSON dialects implemented as a compatibility layer |

Independent extractor packs would improve on the current architecture: a broken site could be fixed without replacing the whole executable. Packs should be signed, versioned against the core API, and capable of carrying compiled Go or WASM logic. Purely declarative scraping rules are insufficient for complex extractors.

## Principal Engineering Risks

### Extractor Scale and Churn

Extractors account for nearly 87% of the implementation. General Python-to-Go transpilation is not credible because the code uses inheritance, generators, lambdas, dynamic dictionaries, exceptions as control flow, callable traversal operations, and site-specific parsing.

The common extractor helper surface should be ported first. Mirroring familiar helper semantics will make individual extractor ports more mechanical and reduce divergence.

### Regular-Expression Semantics

The current tree contains approximately:

- 276 lookaheads.
- 30 lookbehinds.
- 68 named backreferences.
- 20 conditional groups.

Go's RE2-based standard `regexp` package deliberately excludes look-around and backreferences. A compatibility layer will therefore need a bounded backtracking engine or PCRE2-like implementation, syntax translation, timeouts, and differential tests.

- [Go `regexp`](https://pkg.go.dev/regexp)
- [RE2 limitations](https://github.com/google/re2#readme)

### Networking and Impersonation

Ordinary HTTP maps well to Go. Exact proxy, redirect, cookie, TLS, header-casing, WebSocket, FTP, and legacy-server behavior is harder. Browser TLS and HTTP fingerprint impersonation is required for some sites; a `net/http`-only implementation would not be sufficient.

The transport layer should select implementations by declared capability and permit native, impersonating, and browser-backed transports.

### Browser Cookies

Feature parity requires Firefox, Chromium, and Safari formats plus Windows DPAPI, macOS Keychain, GNOME Secret Service/KWallet, SQLite locking, containers/profiles, and changing browser schemas. This is feasible but requires separate platform implementations and fixtures.

### User-Facing Languages

Output templates implement Python-style formatting, object traversal, slicing, arithmetic, date formatting, alternatives, conversions, and Unicode normalization. Format selection and filtering are additional domain-specific languages. Each requires a real parser and differential tests.

- [Output-template grammar](https://github.com/yt-dlp/yt-dlp/blob/aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8/README.md#L1277-L1312)

### JavaScript and Media Dependencies

A Go core does not require every dependency to be rewritten. Full YouTube support currently relies on `yt-dlp-ejs` plus a supported JavaScript runtime, while media merging and postprocessing rely on ffmpeg and ffprobe. These should initially remain external, managed dependencies.

- [Runtime dependencies](https://github.com/yt-dlp/yt-dlp/blob/aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8/README.md#L195-L249)

### Plugin System

Go's native plugin mechanism cannot provide the required portable extension system: it excludes Windows and is sensitive to exact toolchain and dependency versions. Prefer a stable process/RPC or WASM contract supporting extractors, postprocessors, transports, cookie sources, and challenge/token providers.

- [Go plugin warnings](https://pkg.go.dev/plugin)

## Migration Strategy

Python should be used as the development oracle, not as the final runtime.

### Phase 0: High-Risk Prototype — 12 to 16 Weeks

Build:

- Go value model and extractor interface.
- HTTP/HLS/DASH download path.
- Output-template and format-selection parser prototypes.
- Pluggable TLS impersonation.
- One browser-cookie implementation.
- ffmpeg orchestration.
- YouTube/EJS proof of concept.
- Differential runner comparing Go against a pinned Python release.

This phase should prove the hard semantics rather than maximize site count.

### Phase 1: Production Foundation — 6 to 12 Months

Implement:

- Full CLI and configuration compatibility.
- Logging, progress, retries, cancellation, and archive behavior.
- Plugin ABI and extractor-pack updater.
- Core postprocessors and platform packaging.
- Generic extraction plus a representative set of simple, authenticated, live, and anti-bot sites.
- Record/replay HTTP test infrastructure.

### Phase 2: High-Value Coverage — 12 to 24 Months

Port sites based on actual usage while retaining Python fallback during development. YouTube should be a dedicated workstream: its extractor subtree is roughly 11,500 lines and received approximately 101 commits during the measured year.

### Phase 3: Complete Built-In Parity — Approximately 3 to 5 Years

Port the long tail, remove runtime fallback, and demonstrate that the Go implementation can absorb site fixes at a speed comparable to the reference project.

## Planning Estimates

These ranges assume experienced Go/Python/media/network engineers and that ffmpeg and a JavaScript runtime remain dependencies.

| Milestone | Estimated effort |
|---|---:|
| High-risk pilot | 1.5–2 engineer-years |
| Useful top-site implementation | 6–15 engineer-years |
| Frozen snapshot capability parity | 25–45 engineer-years |
| Current-at-launch full parity | 40–70 engineer-years |
| Ongoing parity maintenance | 5–8 engineers |

The capability-parity definition resolves the Python-ABI incompatibility, but most cost remains in the 1,751 extractors, transport behavior, test infrastructure, and continuous upstream churn.

## Parity and Acceptance Gates

Before a native capability is enabled:

- Pin the Python reference to an exact commit and identify that parity base in every Go release.
- Match CLI flags, aliases, help, configuration precedence, exit codes, stdout/stderr behavior, and filesystem effects where compatibility is promised.
- Match URL suitability, extractor routing, and supported extractor names.
- Compare normalized metadata, formats, selected streams, fragments, filenames, archives, postprocessor order, and media metadata.
- Run both implementations with the same request fixtures, IP, credentials, cookies, time source, and dependency versions.
- Maintain recorded HTTP fixtures with secrets and personal data removed.
- Run nightly live canaries for high-volume sites and broader scheduled long-tail tests.
- Test Windows, macOS, glibc Linux, musl Linux, x86, ARM, and updater behavior.
- Shadow native execution before cutover.
- Demonstrate that urgent high-volume site fixes can be reproduced in Go within 24–48 hours.

Python fallback must reach zero before declaring the final implementation independent of Python.

## Opportunities to Improve on yt-dlp

The strongest improvements are architectural rather than raw download speed:

- `context.Context` cancellation throughout.
- Typed, structured progress and error APIs.
- Bounded per-host concurrency and rate limiting.
- Fuzz-tested parsers and deterministic transport fixtures.
- Signed, hot-swappable extractor packs.
- Sandboxed and permissioned plugins.
- Reproducible cross-platform builds.
- Managed ffmpeg and JavaScript-runtime discovery.
- Transport fallback based on declared capabilities.
- Streaming playlists rather than materializing large collections.
- Stable public Go interfaces separated from internal extractor details.
- Structured telemetry for site breakage, fallback, throttling, and patch latency.

## Recommendation

Proceed only as a staged new implementation. Set the end-state requirement to zero Python runtime, use Python solely as a differential oracle during migration, and require native Go capabilities to earn traffic incrementally.

The next artifact should be a formal parity specification enumerating every CLI capability, extractor, protocol, result field, postprocessor, plugin capability, and platform behavior. That specification becomes the acceptance contract for the port.

## Governance Note

The current upstream repository prohibits AI/LLM-assisted issues, patches, pull requests, reviews, and other contributions. This evaluation is for local planning and must not be submitted upstream.

- [Upstream policy](https://github.com/yt-dlp/yt-dlp/blob/aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8/.NO_AI/README.md#L1-L17)
