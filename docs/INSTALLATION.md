# Installation

`ytdlp-go` is pre-release software. Public DMG, Windows installer, AppImage,
Linux package, and standalone CLI releases are not published yet. Do not treat
CI artifacts, test keys, or locally built bundles as endorsed releases.

## Choose what to install

| Interface | Intended use | Installation today |
| --- | --- | --- |
| YTDLP Go Desktop | Graphical single-video YouTube downloads | Development source build |
| `ytdlp-go` CLI | Terminal and automation workflows | Source build |
| `pkg/ytdlp` | Embedding in another Go application | Clone with a temporary local module replacement |

The repository/module identity must be reconciled before normal `go install`
or `go get` instructions are advertised. The repository is currently hosted at
`github.com/tejasa97/youtube_dlp`, while `go.mod` declares
`github.com/ytdlp-go/ytdlp`.

## CLI source build

### CLI requirements

- Go 1.25.12 or newer.
- Git.
- FFmpeg for adaptive video/audio merging and requested post-processing.
- FFprobe for operations that inspect existing media streams.

Clone and build the CLI and JavaScript helper:

```sh
git clone https://github.com/tejasa97/youtube_dlp.git
cd youtube_dlp
mkdir -p bin

CGO_ENABLED=0 go build -trimpath -o bin/ytdlp-go ./cmd/ytdlp-go
CGO_ENABLED=0 go build -trimpath -o bin/ytdlp-js-helper ./cmd/ytdlp-js-helper
```

On Windows, use executable suffixes:

```powershell
New-Item -ItemType Directory -Force bin
$env:CGO_ENABLED = "0"
go build -trimpath -o bin/ytdlp-go.exe ./cmd/ytdlp-go
go build -trimpath -o bin/ytdlp-js-helper.exe ./cmd/ytdlp-js-helper
```

Verify the resulting programs:

```sh
./bin/ytdlp-go --version
./bin/ytdlp-go --help
./bin/ytdlp-js-helper --version
```

Keep the helper beside the main executable. The downloader does not search
`PATH` for executable helper code. A different absolute helper path can be
selected explicitly:

```sh
./bin/ytdlp-go --js-helper ./bin/ytdlp-js-helper URL
```

## Desktop development build

### Desktop requirements

- Go 1.25.12 or newer.
- Node.js 20 or newer and npm 10 or newer.
- Wails CLI v2.13.0.
- Platform build dependencies reported by `wails doctor`.
- FFmpeg/FFprobe for media merging and post-processing.

Install the pinned Wails CLI:

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0
```

From `apps/desktop`:

```sh
cd frontend
npm ci
cd ..

wails doctor
wails dev
```

Create a production-mode build for the current host platform:

```sh
wails build
```

This is a developer build, not a signed or notarized public release. The
current macOS post-build hook packages and verifies `ytdlp-js-helper` in the
application bundle. Equivalent Windows and Linux helper packaging, FFmpeg
redistribution decisions, platform signing, and clean-machine verification
must be completed before publishing artifacts to end users.

See the [Desktop maintainer guide](../apps/desktop/README.md) for project layout
and validation commands.

## FFmpeg and FFprobe

The application can download some combined formats without FFmpeg, but many
high-quality YouTube formats provide video and audio as separate tracks.
FFmpeg is then required to produce one playable output file.

The Desktop app checks `PATH` and also accepts an explicit FFmpeg location in
Settings. The CLI uses its filesystem/media configuration and normal tool
discovery. Run the following to inspect an installed system toolchain:

```sh
ffmpeg -version
ffprobe -version
```

Future binary releases may bundle a reviewed FFmpeg distribution. Until the
redistribution source, configuration, notices, and licensing process are
defined, this documentation does not promise a bundled copy.

## Docker

Build the strict Python-free scratch image:

```sh
docker build -f .github/python-free.Dockerfile -t ytdlp-go .
docker run --rm ytdlp-go --version
```

Build the practical non-root image with FFmpeg and FFprobe:

```sh
docker build -f .github/runtime.Dockerfile -t ytdlp-go-runtime .
docker volume create ytdlp-downloads
docker run --rm --read-only --tmpfs /tmp \
  -v ytdlp-downloads:/downloads \
  ytdlp-go-runtime URL
```

See [Python-free runtime image](PYTHON_FREE_RUNTIME_IMAGE.md) for its exact
verification boundary.

## Updating and uninstalling

There is no endorsed binary update channel yet. Update a source checkout only
after reviewing the target revision, then rebuild both the main binary and the
JavaScript helper.

Development Desktop builds can be removed like ordinary local application
bundles. User settings and history are stored separately; see
[Desktop data and privacy](DESKTOP.md#data-and-privacy) before deleting them.

## Future release installation

Once public packages exist, this page will document:

- signed and notarized macOS DMGs;
- signed Windows installers;
- Linux AppImage and distribution packages;
- supported operating-system and architecture versions;
- checksums, signatures, and provenance verification;
- upgrades, rollback, and uninstall behavior; and
- the difference between stable, beta, and development channels.

No command or link will be added here until the corresponding artifact and
verification process are operational.
