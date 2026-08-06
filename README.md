<h1 align="center">ytdlp-go</h1>

<p align="center">
  <strong>A native, Python-free media engine for command-line and Go applications.</strong>
</p>

<p align="center">
  <a href="#project-status"><img src="https://img.shields.io/badge/status-alpha-f59e0b" alt="Project status: alpha"></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.25.12-00ADD8?logo=go&amp;logoColor=white" alt="Go 1.25.12"></a>
  <a href="#python-free-by-design"><img src="https://img.shields.io/badge/Python-free-16a34a" alt="Python-free"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-2563eb" alt="Apache License 2.0"></a>
</p>

<p align="center">
  <a href="docs/INSTALLATION.md">Installation</a> ·
  <a href="https://github.com/tejasa97/vidstow">VidStow</a> ·
  <a href="docs/CLI_USAGE.md">CLI</a> ·
  <a href="docs/EMBEDDING.md">Go API</a> ·
  <a href="docs/README.md">Documentation</a> ·
  <a href="CONTRIBUTING.md">Contributing</a>
</p>

---

`ytdlp-go` is an independent Go implementation informed by the observable
behavior of [yt-dlp](https://github.com/yt-dlp/yt-dlp). It provides one native
media engine through a CLI, an embeddable Go API, and explicit provider
compositions. The separate [VidStow](https://github.com/tejasa97/vidstow)
project provides a focused desktop experience powered by this engine.

Python is not used as a runtime, build, test, plugin, fallback, or JavaScript
execution dependency. Unsupported behavior fails explicitly instead of
silently invoking another implementation.

> [!CAUTION]
> **This is alpha software, not yet a drop-in replacement for yt-dlp.**
> There are no endorsed public binary releases or installers. Supported
> behavior is bounded by checked-in evidence, and the native extractor catalog
> is much smaller than yt-dlp's. Review [Project status](docs/PROJECT_STATUS.md)
> and [Supported sites](docs/SUPPORTED_SITES.md) before relying on a workflow.

This project is not affiliated with, endorsed by, or sponsored by yt-dlp,
GitHub, Google/YouTube, or any supported service. Product and service names
identify compatibility targets only.

## Choose an interface

| Interface | Maturity | Best for | Start here |
| --- | --- | --- | --- |
| [VidStow](https://github.com/tejasa97/vidstow) | Early stage | A focused graphical workflow for non-technical users | [VidStow repository](https://github.com/tejasa97/vidstow) |
| `ytdlp-go` CLI | Alpha | Terminal use, scripting, and advanced controls | [CLI guide](docs/CLI_USAGE.md) |
| `pkg/ytdlp` | `v1alpha1` | Embedding the engine in another Go application | [Go API guide](docs/EMBEDDING.md) |

VidStow currently accepts one public, on-demand YouTube video at a time. It is
versioned and released independently. The CLI and Go API expose a broader
evidence-backed feature set.

## Why ytdlp-go?

| Principle | What it provides |
| --- | --- |
| Native deployment | Go programs and downstream applications without a Python environment |
| Explicit compatibility | Claims linked to fixtures, tests, provenance, and known deviations |
| Safe composition | Bounded resources, cancellation, categorized errors, and confined output |
| Auditable boundaries | Visible interfaces for JavaScript, credentials, FFmpeg, plugins, and updates |
| No hidden fallback | Unsupported behavior is reported instead of delegated to Python |

## Quick start

Go 1.25.12 or newer is required.

### Build the CLI from source

The following commands use a Unix-like shell. Windows source-build commands
are documented under [Installation](docs/INSTALLATION.md#cli-source-build).

```sh
git clone https://github.com/tejasa97/youtube_dlp.git
cd youtube_dlp
mkdir -p bin

CGO_ENABLED=0 go build -trimpath -o bin/ytdlp-go ./cmd/ytdlp-go
CGO_ENABLED=0 go build -trimpath -o bin/ytdlp-js-helper ./cmd/ytdlp-js-helper
```

Keep `ytdlp-js-helper` beside the main executable, then verify both programs:

```sh
./bin/ytdlp-go --version
./bin/ytdlp-js-helper --version
./bin/ytdlp-go --help
```

Download a supported URL:

```sh
./bin/ytdlp-go URL
```

Choose and merge adaptive video and audio:

```sh
./bin/ytdlp-go -f "bestvideo+bestaudio/best" URL
```

Extract MP3 audio with FFmpeg:

```sh
./bin/ytdlp-go -x --audio-format mp3 URL
```

Inspect metadata without downloading media:

```sh
./bin/ytdlp-go --skip-download --print-json URL
```

See [CLI usage](docs/CLI_USAGE.md) for playlists, subtitles, output paths,
cookies, SponsorBlock, routing, automation, and transfer controls.

### Use the focused desktop application

[VidStow](https://github.com/tejasa97/vidstow) is the independently versioned
Wails/Svelte desktop application built on root `engine` plus
`providers/youtube`. Its repository contains the current source-build,
development, packaging, and product-scope documentation.

### Embed the Go API

`pkg/ytdlp` exposes context-aware requests, events, categorized errors,
playlists, metadata, and produced artifacts. Add the canonical package with:

```sh
go get github.com/tejasa97/youtube_dlp/pkg/ytdlp
```

The API remains pre-release; review upgrades and follow the
[embedding guide](docs/EMBEDDING.md).

## Project status

| Area | Evidence-backed scope today |
| --- | --- |
| Runtime | Native Go programs; no Python interpreter or fallback |
| Downstream | VidStow provides a separately versioned, focused YouTube desktop workflow |
| Extractors | Representative simple, shared-backend, playlist, live, authenticated, regional, manifest, and JavaScript-heavy families |
| Media | Direct HTTP(S), HLS, DASH, and ISM/Smooth Streaming |
| Playlists | Lazy sequences, bounded continuations, item/range selection, reverse, and flat modes |
| Formats | Bounded selection, sorting, filtering, fallbacks, merges, and multi-output plans |
| Post-processing | Typed FFmpeg/FFprobe operations for merging, audio, metadata, subtitles, chapters, remuxing, concat, and safe moves |
| Extensions | Versioned native RPC and constrained WASM plugins, signed packs, catalogs, and updater transactions |

The capability manifest records **94 capabilities**: **86 compatible** within
their declared corpora, **7 partial**, and **1 intentional deviation**.
This summary is enforced against the manifest by repository tests.
“Compatible” means the linked deterministic evidence passes within its
declared corpus; it does not imply unbounded equivalence with every upstream
behavior, account state, region, or future service response.

DRM decryption is not implemented. Experimental SABR/UMP work is outside the
general compatibility target. See [Project status](docs/PROJECT_STATUS.md) for
the evidence model and principal limitations.

## Architecture

```text
VidStow (separate repo)       ytdlp-go CLI       Go application
 engine + providers/youtube       │             engine composition
              │                pkg/ytdlp        or pkg/ytdlp
              └──────────────┬─────┴─────────────────┘
                             │
                           engine
                             │
          extraction → selection → transfer → post-processing
                             │
                  confined artifacts + events
```

Network state, credentials, JavaScript challenge solving, output policy,
archive state, and nested extraction stay within the current operation
boundary. Read the [architecture overview](docs/ARCHITECTURE.md) and
[architecture decisions](docs/adr/README.md) for the detailed contracts.

## Requirements and releases

- **Go 1.25.12+** for source builds.
- **FFmpeg and FFprobe** for adaptive-track merging and requested media
  operations.
- **`ytdlp-js-helper`** beside the main executable for supported JavaScript
  challenge flows.
- **Node.js** only when rebuilding the JavaScript challenge helper from its
  upstream source inputs.

No standalone CLI binary release or production updater channel is currently
endorsed. Build from a reviewed source revision. See
[Installation](docs/INSTALLATION.md) and the
[Roadmap](ROADMAP.md) for the release boundary and planned packaging work.

## Documentation

| Document | Purpose |
| --- | --- |
| [Installation](docs/INSTALLATION.md) | Current source-build paths and future release boundary |
| [VidStow](https://github.com/tejasa97/vidstow) | Separate focused desktop application powered by this engine |
| [CLI usage](docs/CLI_USAGE.md) | Task-oriented command-line workflows |
| [Go embedding](docs/EMBEDDING.md) | Public library usage and lifecycle |
| [Supported sites](docs/SUPPORTED_SITES.md) | Public URL families and known boundaries |
| [Configuration](docs/CONFIGURATION.md) | Configuration files, precedence, aliases, and paths |
| [Troubleshooting](docs/TROUBLESHOOTING.md) | CLI, helper, FFmpeg, and engine failures |
| [Project status](docs/PROJECT_STATUS.md) | Maturity labels, evidence model, and limitations |
| [Architecture](docs/ARCHITECTURE.md) | Engine boundaries and operation lifecycle |
| [Documentation index](docs/README.md) | Complete guide, evidence, plan, and audit index |
| [Changelog](CHANGELOG.md) | User-visible changes |
| [Roadmap](ROADMAP.md) | Current priorities without release-date promises |

## Python-free by design

The main program, extractor registry, network stack, media protocols,
compatibility languages, plugin boundaries, updater logic, and JavaScript
helper are implemented in Go. FFmpeg and FFprobe remain explicit external
media-tool boundaries; they do not introduce a Python runtime.

The repository includes Python-sentinel checks and a strict scratch runtime
image. See [Python-free runtime evidence](docs/PYTHON_FREE_RUNTIME_IMAGE.md).

## Development and verification

Run the main repository tests:

```sh
go test ./...
go run ./cmd/paritycheck
```

Contributions should keep claims bounded, preserve fixture provenance, add
success and failure tests, and document known deviations. Read
[Contributing](CONTRIBUTING.md), the [fixture policy](docs/FIXTURE_POLICY.md),
and the [documentation index](docs/README.md) before opening a pull request.

## Security, support, and legal use

- Use [Support](SUPPORT.md) for issue routing and diagnostic hygiene.
- Report security-sensitive findings privately under [Security](SECURITY.md).
- Review [Third-party notices](THIRD_PARTY_NOTICES.md) before redistributing
  dependencies or external tools.

Use the project only for content you are authorized to access and download.
Respect service terms, copyright, privacy, access controls, and applicable law.
The project does not decrypt DRM or grant rights to third-party media.

Licensed under the [Apache License 2.0](LICENSE).
