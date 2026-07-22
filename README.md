# ytdlp-go

ytdlp-go is an experimental, Python-free audio and video downloader written in
Go. It is an independent implementation informed by the observable behavior of
[yt-dlp](https://github.com/yt-dlp/yt-dlp), with compatibility proven against
small, attributable conformance corpora.

This project is not affiliated with, endorsed by, or sponsored by yt-dlp,
GitHub, Google/YouTube, or the operators of supported services. Product and
service names identify compatibility targets only.

> [!WARNING]
> This is alpha software, not a drop-in replacement for yt-dlp. The repository
> has 28 representative native extractors and broad infrastructure coverage,
> but it does not support thousands of sites or every yt-dlp option. Treat the
> [capability manifest](conformance/parity_manifest.yaml) and
> [supported-extractor list](docs/SUPPORTED_SITES.md) as the source of truth.

## Contents

- [Current status](#current-status)
- [Quick start](#quick-start)
- [Build from source](#build-from-source)
- [Runtime dependencies](#runtime-dependencies)
- [Usage](#usage)
- [Configuration](#configuration)
- [Supported extractors and protocols](#supported-extractors-and-protocols)
- [YouTube JavaScript helper](#youtube-javascript-helper)
- [Cookies and authentication](#cookies-and-authentication)
- [Embedding in Go](#embedding-in-go)
- [Plugins, signed packs, and updates](#plugins-signed-packs-and-updates)
- [Compatibility and Python-free policy](#compatibility-and-python-free-policy)
- [Development and verification](#development-and-verification)
- [Getting help and contributing](#getting-help-and-contributing)
- [Security, legal use, and license](#security-legal-use-and-license)
- [Documentation](#documentation)

## Current status

Phase 0, Phase 1, Phase 2, and the repository-controlled Phase 3 implementation
are complete. Gate G3 is blocked because the required traffic window,
deployment semantic-shadow review, operational regression drill, live canary
observations, native Windows evidence, and production distribution authority do
not yet exist. These external evidence gaps do not block source visibility and
are not replaced with synthetic claims.

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

## Quick start

Go 1.25.12 or newer is required to build the project.

    git clone https://github.com/tejasa97/youtube_dlp.git
    cd youtube_dlp
    mkdir -p bin
    go build -trimpath -o bin/ytdlp-go ./cmd/ytdlp-go
    go build -trimpath -o bin/ytdlp-js-helper ./cmd/ytdlp-js-helper
    ./bin/ytdlp-go --version
    ./bin/ytdlp-go --help

Download a supported URL:

    ./bin/ytdlp-go URL

Write selected subtitle sidecars without downloading the media:

    ./bin/ytdlp-go --skip-download --write-subs --write-auto-subs \
        --sub-langs "en.*,ja" --sub-format "srt/vtt/best" URL

Extract metadata without downloading:

    ./bin/ytdlp-go --skip-download --print-json URL

Select a format and output name:

    ./bin/ytdlp-go -f "bestvideo+bestaudio/best" -o "%(title)s.%(ext)s" URL

Extract audio with ffmpeg:

    ./bin/ytdlp-go -x --audio-format mp3 URL

There are no endorsed public binary releases yet. Build from a reviewed source
revision and keep production signing or updater trust separate from the
deterministic test keys in this repository.

## Build from source

Build the main executable and the optional YouTube challenge helper:

    CGO_ENABLED=0 go build -trimpath -o bin/ytdlp-go ./cmd/ytdlp-go
    CGO_ENABLED=0 go build -trimpath -o bin/ytdlp-js-helper ./cmd/ytdlp-js-helper

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

    docker build -f .github/python-free.Dockerfile -t ytdlp-go .
    docker run --rm ytdlp-go --version

For adaptive media merging and post-processing, build the separate non-root
runtime image. It remains Python-free but includes ffmpeg and ffprobe:

    docker build -f .github/runtime.Dockerfile -t ytdlp-go-runtime .
    docker volume create ytdlp-downloads
    docker run --rm --read-only --tmpfs /tmp \
        -v ytdlp-downloads:/downloads ytdlp-go-runtime URL

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

    ./bin/ytdlp-go --help

Common operations:

    # Confine output to a directory
    ./bin/ytdlp-go -P home:downloads URL

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

Supported output-template, format-selector, sorting, metadata, and match-filter
syntax is intentionally bounded. See the compatibility-language and downloader
documents in the [documentation index](docs/README.md) before assuming an
upstream expression is accepted.

## Configuration

ytdlp-go reads yt-dlp-style option files using bounded quoting, comments,
encoding detection, aliases, and nested explicit locations. Command-line values
have the highest precedence.

Example yt-dlp.conf:

    # Keep downloads in one directory
    --output-dir "downloads"
    --output "%(title)s.%(ext)s"
    --retries 3
    --concurrent-fragments 4

Load a specific file:

    ./bin/ytdlp-go --config-location /path/to/yt-dlp.conf URL

Skip discovered configuration:

    ./bin/ytdlp-go --ignore-config URL

See [Configuration](docs/CONFIGURATION.md) for discovery paths, precedence,
encodings, aliases, limits, and security behavior.

## Supported extractors and protocols

The registered representative extractors are:

- generic direct media, YouTube, Vimeo, Twitch, SoundCloud, Streamable,
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

Native media transfer covers direct HTTP/HTTPS, HLS, DASH, and ISM. Protected
formats may require selected headers, cookies, impersonation, or JavaScript.
DRM decryption is not implemented; --allow-unplayable-formats only permits
DRM-marked formats to participate in selection.

## YouTube JavaScript helper

The helper must be beside ytdlp-go or selected through --js-helper. PATH is
deliberately not searched for executable helper code:

    ./bin/ytdlp-go --js-helper ./bin/ytdlp-js-helper URL

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

    ./bin/ytdlp-go --cookies-from-browser chrome:Default URL

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

    client := ytdlp.NewClient(
        ytdlp.WithEventHandler(func(ctx context.Context, event ytdlp.Event) error {
            log.Printf("%s: %d/%d", event.Kind, event.Bytes, event.Total)
            return nil
        }),
    )

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

## Development and verification

Run the normal local gate:

    test -z "$(gofmt -l .)"
    go mod tidy -diff
    go vet ./...
    go test ./...
    go test -race ./...
    go run ./cmd/paritycheck

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
