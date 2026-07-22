# Python-free runtime container

The repository intentionally provides two container boundaries:

- `.github/python-free.Dockerfile` produces a `scratch` audit image containing
  only static Go executables, CA certificates, and license material.
- `.github/runtime.Dockerfile` produces a non-root Alpine runtime containing
  the same Go downloader and JavaScript helper plus ffmpeg and ffprobe for
  adaptive merging and requested post-processing.

Neither image contains or invokes Python. ffmpeg is an accepted media runtime
dependency under ADR 0005; it is not a fallback implementation of extractor
behavior. The runtime distribution pins its Alpine 3.21 base by manifest digest
and installs packages without retaining the APK cache.

## Local verification

These checks do not depend on GitHub Actions:

```sh
docker build -f .github/python-free.Dockerfile -t ytdlp-go-scratch .
docker build -f .github/runtime.Dockerfile -t ytdlp-go-runtime .

docker run --rm ytdlp-go-scratch --version
docker run --rm --entrypoint /bin/sh ytdlp-go-runtime -eu -c \
  '! command -v python; ! command -v python3; ffmpeg -version; ffprobe -version'

mkdir -p downloads
docker run --rm --read-only --tmpfs /tmp \
  -v "$PWD/downloads:/downloads" ytdlp-go-runtime --version
```

Deterministic ffmpeg operation and cancellation coverage remains in
`internal/media/ffmpeg` and `internal/media/postprocess`. A public adaptive
download can be run only as an explicit, non-gating live canary. Do not commit
downloaded media or signed media URLs.

The runtime process uses UID/GID 65532 and writes from `/downloads`. A bind
mount must therefore be writable by that identity, or callers may use a Docker
named volume.
