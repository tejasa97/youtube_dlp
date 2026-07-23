<h1 align="center">ytdlp-go</h1>

<p align="center"><strong>A native, Python-free audio and video downloader written in Go.</strong></p>

<p align="center">
  <a href="#current-status"><img src="https://img.shields.io/badge/status-alpha-orange.svg" alt="Project status: alpha"></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.25.12-00ADD8.svg?logo=go&amp;logoColor=white" alt="Go 1.25.12"></a>
  <a href="#compatibility-and-python-free-policy"><img src="https://img.shields.io/badge/runtime-Python--free-2ea44f.svg" alt="Python-free"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue.svg" alt="License: Apache-2.0"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="docs/SUPPORTED_SITES.md">Supported extractors</a> ·
  <a href="docs/README.md">Documentation</a> ·
  <a href="CONTRIBUTING.md">Contributing</a>
</p>

ytdlp-go is an independent Go implementation informed by the observable
behavior of [yt-dlp](https://github.com/yt-dlp/yt-dlp). It is not a Python
wrapper: extraction, networking, playlist handling, media protocols,
configuration, plugins, and compatibility logic are implemented natively in
Go and tested against small, attributable conformance corpora.

This project is not affiliated with, endorsed by, or sponsored by yt-dlp,
GitHub, Google/YouTube, or the operators of supported services. Product and
service names identify compatibility targets only.

> [!CAUTION]
> **This is alpha software, not a drop-in replacement for yt-dlp.** The project
> has 28 representative native extractors and broad infrastructure coverage,
> but it does not yet support thousands of sites or every yt-dlp option. Use the
> [capability manifest](conformance/parity_manifest.yaml) and
> [supported-extractor catalog](docs/SUPPORTED_SITES.md) as the source of truth.

## Contents

- [Current status](#current-status)
- [Why ytdlp-go?](#why-ytdlp-go)
- [Quick start](#quick-start)
- [Build from source](#build-from-source)
- [Runtime dependencies](#runtime-dependencies)
- [Usage](#usage)
- [Configuration](#configuration)
- [Supported extractors and protocols](#supported-extractors-and-protocols)
- [How it fits together](#how-it-fits-together)
- [YouTube JavaScript helper](#youtube-javascript-helper)
- [Cookies and authentication](#cookies-and-authentication)
- [Embedding in Go](#embedding-in-go)
- [Plugins, signed packs, and updates](#plugins-signed-packs-and-updates)
- [Compatibility and Python-free policy](#compatibility-and-python-free-policy)
- [Roadmap](#roadmap)
- [Development and verification](#development-and-verification)
- [Getting help and contributing](#getting-help-and-contributing)
- [Security, legal use, and license](#security-legal-use-and-license)
- [Documentation](#documentation)

## Current status

The repository-controlled work for Phases 0 through 3 is implemented. Phase 3
itself is **not complete**: Gate G3 still requires a measured traffic window,
deployment semantic-shadow review, an operational regression drill, approved
live canary observations, native Windows evidence, and production distribution
decisions. Synthetic fixtures are not presented as substitutes for those facts.

| Area | Current evidence-backed scope |
| --- | --- |
| Runtime | Go binaries with no Python runtime, library, helper, plugin, build, or test dependency |
| Public Go API | Versioned v1alpha1 API with context cancellation, categorized errors, and structured events |
| Extractors | 28 representative native extractors across direct, shared-backend, playlist/API, live, authenticated, manifest, anti-bot, regional, and JavaScript risks |
| Downloads | Direct HTTP, HLS, DASH, and ISM with bounded retries, resume, fragments, cancellation, and output confinement |
| Post-processing | Typed ffmpeg/ffprobe operations including audio extraction, remuxing, conversion, embedding, chapters, concat, fixups, and safe moves |
| Compatibility languages | Scoped format selection/sorting, output and progress templates, metadata transforms, match filters, configuration, archive, and cache behavior |
| Extensions | Explicitly trusted native RPC v1.0/v1.1 and constrained WASM plugins; deterministic signed packs and offline catalogs |
| Operations | Opt-in privacy-safe telemetry, semantic-shadow comparison, bounded canaries, and local diagnosis reports |
| Releases | Reproducible no-cgo alpha assembly, SPDX/license output, signed updater metadata, rollback, and Python-free container evidence |

The capability manifest currently records 55 capabilities: 54 compatible
within their declared corpora and one intentional deviation. A compatible
entry is not a claim of complete yt-dlp parity. See the
[Phase 3 exit review](docs/PHASE_3_EXIT_REVIEW.md), [Phase 2 security
review](docs/P2_SECURITY_REVIEW.md), and [Phase 3
plan](PHASE_3_IMPLEMENTATION_PLAN.md) for exact boundaries.

## Why ytdlp-go?

| Goal | What it means here |
| --- | --- |
| Native Go deployment | A static downloader and embeddable API, with no Python interpreter or package environment to ship |
| Explicit compatibility | Every compatibility claim points to deterministic automated evidence and records its known deviations |
| Safe composition | Context cancellation, structured events, categorized errors, bounded resources, and output confinement are part of the public design |
| Replaceable boundaries | JavaScript challenges, ffmpeg operations, credentials, browser profiles, plugins, and update trust are explicit rather than hidden fallbacks |

The project is an engineering exploration as well as a downloader: it asks how
far yt-dlp-style behavior can be reproduced in a portable Go system without
silently delegating difficult cases back to Python.

## Quick start

Go 1.25.12 or newer is required to build the project.

```sh
git clone https://github.com/tejasa97/youtube_dlp.git
cd youtube_dlp
mkdir -p bin
go build -trimpath -o bin/ytdlp-go ./cmd/ytdlp-go
go build -trimpath -o bin/ytdlp-js-helper ./cmd/ytdlp-js-helper
./bin/ytdlp-go --version
./bin/ytdlp-go --help
```

Download a supported URL:

```sh
./bin/ytdlp-go URL
```

Write selected subtitle sidecars without downloading the media:

```sh
./bin/ytdlp-go --skip-download --write-subs --write-auto-subs \
    --sub-langs "en.*,ja" --sub-format "srt/vtt/best" URL
```

Convert written subtitle sidecars to a supported format:

```sh
./bin/ytdlp-go --skip-download --write-subs --convert-subs vtt URL
```

Embed selected subtitles in a supported media container:

```sh
./bin/ytdlp-go --embed-subs --sub-langs 'en,fr' URL
```

Add `--write-subs` to retain the sidecar files after successful embedding.

List available manual and automatic subtitles without writing files:

```sh
./bin/ytdlp-go --list-subs URL
```

Extract metadata without downloading:

```sh
./bin/ytdlp-go --skip-download --print-json URL
```

Select a format and output name:

```sh
./bin/ytdlp-go -f "bestvideo+bestaudio/best" -o "%(title)s.%(ext)s" URL
```

Extract audio with ffmpeg:

```sh
./bin/ytdlp-go -x --audio-format mp3 URL
```

There are no endorsed public binary releases yet. Build from a reviewed source
revision and keep production signing or updater trust separate from the
deterministic test keys in this repository.

## Build from source

Build the main executable and the optional YouTube challenge helper:

```sh
CGO_ENABLED=0 go build -trimpath -o bin/ytdlp-go ./cmd/ytdlp-go
CGO_ENABLED=0 go build -trimpath -o bin/ytdlp-js-helper ./cmd/ytdlp-js-helper
```

The repository also contains specialist tools:

| Command | Purpose |
| --- | --- |
| cmd/ytdlp-pack | Build and inspect deterministic signed plugin packs |
| cmd/ytdlp-update | Exercise the signed updater and rollback API |
| cmd/ytdlp-release | Assemble reproducible cross-platform alpha archives |
| cmd/jscheck | Verify isolated JavaScript and embedded EJS execution |
| cmd/paritycheck | Validate capability and fallback claims |
| cmd/deltareplay | Classify attributable upstream changes |

The Python-free Docker target builds and tests in an Alpine Go stage without
Python and copies static executables into a scratch image:

```sh
docker build -f .github/python-free.Dockerfile -t ytdlp-go .
docker run --rm ytdlp-go --version
```

For adaptive media merging and post-processing, build the separate non-root
runtime image. It remains Python-free but includes ffmpeg and ffprobe:

```sh
docker build -f .github/runtime.Dockerfile -t ytdlp-go-runtime .
docker volume create ytdlp-downloads
docker run --rm --read-only --tmpfs /tmp \
    -v ytdlp-downloads:/downloads ytdlp-go-runtime URL
```

The scratch image is the strict dependency-audit artifact; the runtime image is
the practical downloader distribution. See
[`docs/PYTHON_FREE_RUNTIME_IMAGE.md`](docs/PYTHON_FREE_RUNTIME_IMAGE.md) for the
local verification boundary.

## Runtime dependencies

The main downloader is a static Go program and does not require Python.

- ffmpeg and ffprobe are required only for merging or requested
  post-processing such as audio extraction and remuxing. They are executed
  directly with argument vectors, never through a shell.
- ytdlp-js-helper is required for supported YouTube flows that need a
  JavaScript challenge. It is a separate pure-Go executable using the embedded,
  hash-pinned EJS bundle.
- Browser cookie import may use the operating system credential service after
  the user explicitly selects a browser profile.
- An external downloader is optional and must be selected explicitly with
  --downloader. Interpreter and script trampolines are rejected.

## Usage

The checked-in CLI help is the authoritative option list:

```sh
./bin/ytdlp-go --help
```

Common operations:

```sh
# Confine output to a directory
./bin/ytdlp-go -P home:downloads URL

# Select an inclusive playlist range and process it in reverse
./bin/ytdlp-go --playlist-start 3 --playlist-end 8 --playlist-reverse URL

# Print machine-readable metadata while keeping progress on stderr
./bin/ytdlp-go --skip-download --print-json URL

# Emit newline-delimited structured progress events
./bin/ytdlp-go --progress-json URL

# Emit one privacy-safe aggregate operation snapshot
./bin/ytdlp-go --telemetry-json --skip-download URL

# Limit transfer rate and retry transient failures
./bin/ytdlp-go --limit-rate 5M --retries 3 URL

# Download HLS/DASH fragments concurrently within bounded limits
./bin/ytdlp-go --concurrent-fragments 8 --per-host-fragments 4 URL

# Record and skip previously downloaded extractor IDs
./bin/ytdlp-go --download-archive archive.txt URL

# Import a Netscape cookie file
./bin/ytdlp-go --cookies cookies.txt URL

# Import an explicitly selected browser profile
./bin/ytdlp-go --cookies-from-browser chrome:Default URL

# Use an explicitly selected native netrc file
./bin/ytdlp-go --netrc --netrc-location /path/to/.netrc URL

# Select a pinned browser impersonation profile
./bin/ytdlp-go --impersonate firefox-120 URL
```

Supported output-template, format-selector, sorting, metadata, and match-filter
syntax is intentionally bounded. See the compatibility-language and downloader
documents in the [documentation index](docs/README.md) before assuming an
upstream expression is accepted.

## Configuration

ytdlp-go reads yt-dlp-style option files using bounded quoting, comments,
encoding detection, aliases, and nested explicit locations. Command-line values
have the highest precedence.

Example yt-dlp.conf:

```text
# Keep downloads in one directory
--output-dir "downloads"
--output "%(title)s.%(ext)s"
--retries 3
--concurrent-fragments 4
```

Load a specific file:

```sh
./bin/ytdlp-go --config-location /path/to/yt-dlp.conf URL
```

Skip discovered configuration:

```sh
./bin/ytdlp-go --ignore-config URL
```

See [Configuration](docs/CONFIGURATION.md) for discovery paths, precedence,
encodings, aliases, limits, and security behavior.

## Supported extractors and protocols

The registered representative extractors are:

- generic direct media and bounded native-provider embeds, YouTube, Vimeo, Twitch, SoundCloud, Streamable,
  PeerTube, Internet Archive, TikTok, and the deterministic authenticated fixture;
- SVT Play, Brightcove, Kaltura, JW Platform, Wistia, and SproutVideo;
- Dailymotion, Reddit, Twitter/X, Bandcamp, Mixcloud, Rumble, and Bilibili;
- Instagram, Kick, BBC iPlayer, ARD Mediathek, and NRK.

This list means the checked-in routing and conformance corpus exists; it does
not promise every page, account state, region, playlist type, or current live
site response. External services change independently of this repository.

See [Supported extractors](docs/SUPPORTED_SITES.md) for URL families, risk
classes, limitations, and the distinction between deterministic conformance and
opt-in live canaries.

Public YouTube comments are opt-in and bounded:

```sh
./bin/ytdlp-go --write-comments --youtube-max-comments 100 \
  --skip-download --print-json 'https://www.youtube.com/watch?v=VIDEO_ID'
```

Use `--youtube-comment-sort new|top` to choose the order. The current scope
covers anonymous parent comments, click-tracked reply continuations, and
bounded nested subthreads; authenticated comments are not yet supported.

Native media transfer covers direct HTTP/HTTPS, HLS, DASH, and ISM. Protected
formats may require selected headers, cookies, impersonation, or JavaScript.
DRM decryption is not implemented; --allow-unplayable-formats only permits
DRM-marked formats to participate in selection.

## How it fits together

```text
URL
 └─► native extractor registry
      └─► normalized metadata, formats, subtitles, or playlist entries
           └─► bounded selection and compatibility rules
                └─► direct / HLS / DASH / ISM downloader
                     └─► optional ffmpeg post-processing
                          └─► confined output and structured events
```

The same pipeline powers the CLI and the public Go API. Network transport,
cookies, challenge solving, cancellation, archive state, and output policy are
owned by one operation so nested playlists do not silently escape the caller's
security or resource settings.

## YouTube JavaScript helper

The helper must be beside ytdlp-go or selected through --js-helper. PATH is
deliberately not searched for executable helper code:

```sh
./bin/ytdlp-go --js-helper ./bin/ytdlp-js-helper URL
```

The helper is supervised as a separate process with bounded messages,
cancellation, timeouts, and no inherited credential environment. Pages whose
selected formats do not need a challenge do not start it. See [JavaScript
helper protocol](docs/JAVASCRIPT_HELPER_PROTOCOL.md).

## Cookies and authentication

Netscape cookie files and native netrc credentials are opt-in. Browser import
supports Firefox and declared Chromium-family profiles on macOS, Linux, and
Windows, with explicit platform/store limitations. Cookie values,
authorization fields, signed query parameters, netrc values, and browser
secrets are redacted from diagnostics and events.

On macOS, importing Chrome may trigger the normal Keychain prompt:

```sh
./bin/ytdlp-go --cookies-from-browser chrome:Default URL
```

Never attach real cookies or tokens to a public issue. See [Chromium cookie
import](docs/CHROMIUM_COOKIE_IMPORT.md), [native netrc
evidence](docs/P3_NETRC_EVIDENCE.md), and [Security policy](SECURITY.md).

## Embedding in Go

The supported package contract is `github.com/ytdlp-go/ytdlp/pkg/ytdlp`. Its
v1alpha1 API accepts context-aware requests and returns categorized errors,
structured events, normalized metadata, playlist entries, and produced
artifacts.

> [!IMPORTANT]
> The current repository is hosted at `github.com/tejasa97/youtube_dlp`, while
> `go.mod` declares the intended canonical module path
> `github.com/ytdlp-go/ytdlp`. Until those locations are reconciled, the module
> is not advertised as directly installable with `go get`. Build within a
> cloned checkout, or use a temporary local `replace` directive for embedding
> evaluation. Do not publish a dependency on an unreconciled path.

```go
client := ytdlp.NewClient(
    ytdlp.WithEventHandler(func(ctx context.Context, event ytdlp.Event) error {
        log.Printf("%s: %d/%d", event.Kind, event.Bytes, event.Total)
        return nil
    }),
)
defer client.Close()

result, err := client.Run(ctx, ytdlp.Request{
    URL:          rawURL,
    OutputDir:    "downloads",
    SkipDownload: true,
})
if err != nil {
    if ytdlp.IsCategory(err, ytdlp.ErrorUnsupported) {
        // Handle unsupported input without matching diagnostic text.
    }
    return err
}
fmt.Println(string(result.InfoJSON))
```

See [Embedding ytdlp-go](docs/EMBEDDING.md) and the [API compatibility
policy](docs/P2_API_COMPATIBILITY_POLICY.md).

## Plugins, signed packs, and updates

Plugins are never discovered from arbitrary PATH entries and never
automatically claim URLs. Production use requires a trusted package, exact
permission approval, ABI negotiation, and explicit PluginID selection.
Supported extension boundaries are native length-prefixed JSON RPC and a
constrained extractor-only WASM ABI. Python and interpreter-backed plugins are
rejected.

The updater verifies caller-supplied threshold trust, product/channel/platform
scope, version monotonicity, hashes, health checks, rollback state, and
revocation. The repository does not choose production signers or publishing
credentials.

Start with [Plugin ABI v1](docs/P2_PLUGIN_ABI_V1.md), [signed
packs](docs/P2_SIGNED_PACKS.md), and [updater and
releases](docs/P2_UPDATER_RELEASES.md).

## Compatibility and Python-free policy

The pinned behavioral reference is
yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8. It is used only as a
read-only source of attributable behavioral expectations. Production code,
builds, tests, releases, plugins, and runtime operation do not read or execute
that checkout.

A capability may be marked compatible only when the manifest points to passing
automated evidence. Unknown or incomplete behavior must remain an explicit
deviation or return a categorized unsupported error. There is no silent Python
fallback.

Privacy-safe telemetry is disabled by default. When selected, it records only
fixed extractor/capability/outcome dimensions with overflow accounting; it
does not record URLs, paths, titles, usernames, credentials, or error strings.
See [Phase 3 telemetry](docs/P3_TELEMETRY.md).

## Roadmap

Near-term work follows the explicit gaps in the capability manifest rather
than a promise of blanket parity:

1. deepen high-value extractor behavior and replay relevant upstream changes;
2. close reusable playlist, format-selection, post-processing, and media
   protocol gaps with attributable conformance evidence;
3. harden the isolated JavaScript, plugin, credential, and update boundaries;
4. reconcile the public repository and canonical Go module paths before
   advertising normal `go get` installation; and
5. collect the real deployment evidence required by Gate G3 only through
   approved, privacy-preserving observation and canary workflows.

The exact backlog is recorded as known deviations in
[`conformance/parity_manifest.yaml`](conformance/parity_manifest.yaml). Phase
plans explain sequencing; the manifest describes what the current code proves.

## Development and verification

Run the normal local gate:

```sh
test -z "$(gofmt -l .)"
go mod tidy -diff
go vet ./...
go test ./...
go test -race ./...
go run ./cmd/paritycheck
```

The project uses deterministic fixtures rather than real account data or
captured private responses. New claims require success, failure, cancellation,
security, and provenance evidence appropriate to their risk.

See [Contributing](CONTRIBUTING.md), the [fixture
policy](docs/FIXTURE_POLICY.md), and the [Phase 3
plan](PHASE_3_IMPLEMENTATION_PLAN.md).

## Getting help and contributing

Read [Support](SUPPORT.md) before filing a bug, site request, or feature
request. Reports need a current revision, a minimal safe reproduction, and
plain-text diagnostics with credentials and personal data removed. Security
reports use the private process in [SECURITY.md](SECURITY.md).

[Contributing](CONTRIBUTING.md) covers local verification, extractor evidence,
fixture provenance, compatibility claims, pull-request scope, licensing, and
the separation from upstream yt-dlp. GitHub Actions is temporarily disabled;
contributors must report the local checks they ran.

## Security, legal use, and license

Report vulnerabilities privately as described in [SECURITY.md](SECURITY.md).
Do not publish real cookies, access tokens, signed media URLs, private media,
personal information, or production signing keys.

Use this software only for content you are authorized to access and download,
and comply with applicable law and service terms. The project does not provide
DRM circumvention.

Project code is licensed under the [Apache License 2.0](LICENSE). Embedded
assets and dependencies retain their own licenses; see [third-party
notices](THIRD_PARTY_NOTICES.md).

## Documentation

The [documentation index](docs/README.md) separates user guidance, embedding
and extension contracts, security/release material, architecture decisions,
phase plans, and historical evidence. This README intentionally documents the
public entry points; detailed conformance mechanics remain in focused files.
