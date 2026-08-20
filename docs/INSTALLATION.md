# Installation

`ytdlp-go` is pre-release software. Standalone CLI archives and operating-system
packages are not published yet. Do not treat CI artifacts, test keys, or locally
built binaries as endorsed releases.

## Choose what to install

| Interface | Intended use | Installation today |
| --- | --- | --- |
| [VidStow](https://github.com/vidstow/vidstow) | Graphical public YouTube video, Short, and playlist downloads | See the separate VidStow repository |
| `ytdlp-go` CLI | Terminal and automation workflows | Source build |
| `pkg/ytdlp` | Embedding in another Go application | Canonical Go module dependency |

The GitHub repository and Go module path are both
[`github.com/tejasa97/ytdlp-go`](https://github.com/tejasa97/ytdlp-go).
Add the embeddable package with:

```sh
go get github.com/tejasa97/ytdlp-go/pkg/ytdlp
```

Tagged `v0.2.x` releases remain available as `github.com/tejasa97/youtube_dlp`.
New versions use the `ytdlp-go` module path; update `go.mod` and imports
accordingly.

The API is pre-release, so review dependency upgrades before adopting them.

## CLI source build

### CLI requirements

- Go 1.25.12 or newer.
- Git.
- FFmpeg for adaptive video/audio merging and requested post-processing.
- FFprobe for operations that inspect existing media streams.

Clone and build the CLI and JavaScript helper:

```sh
git clone https://github.com/tejasa97/ytdlp-go.git
cd ytdlp-go
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

## Desktop application

VidStow is developed and released independently from this engine. Follow the
[VidStow build instructions](https://github.com/vidstow/vidstow#run-locally)
for its Go, Node.js, Wails, FFmpeg, and platform requirements. The historical
`apps/desktop` snapshot in this repository is not the current desktop source.

## FFmpeg and FFprobe

The CLI and downstream applications can download some combined formats without
FFmpeg, but many high-quality formats provide video and audio as separate tracks.
FFmpeg is then required to produce one playable output file.

VidStow checks `PATH` and also accepts an explicit FFmpeg location in Settings.
The CLI uses its filesystem/media configuration and normal tool discovery. Run
the following to inspect an installed system toolchain:

```sh
ffmpeg -version
ffprobe -version
```

Current source builds and repository images do not promise a bundled FFmpeg
distribution. FFmpeg and FFprobe remain explicit external dependencies where an
operation requires them.

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

VidStow installation, local-data, update, and removal behavior is documented in
the separate VidStow repository.
