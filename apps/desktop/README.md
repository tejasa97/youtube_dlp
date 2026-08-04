# YTDLP Go Desktop — Maintainer Guide

This directory contains the Wails/Svelte desktop interface for `ytdlp-go`. It
is intentionally limited to
**single, public YouTube videos**: no playlists, channels, search, live, Shorts,
or other sites. The desktop app is a thin front end over the existing Go
downloader; the extraction engine is unchanged and reusable from any Go
program.

## What you get in V0

- **Home** — paste a YouTube URL, see metadata, pick a quality (Best / 4K /
  1440p / 1080p / 720p / Audio only), choose a destination folder, then
  download now or add to queue.
- **Queue** — one active download at a time (FIFO), progress bar with rolling
  speed/ETA, cancel/retry/remove per job, clear-completed.
- **Downloads** — persisted history with search, open, reveal in finder.
- **Settings** — default folder, ffmpeg detection, custom ffmpeg path, copy
  diagnostics.
- **Modals** — FFmpeg missing, unsupported URL, generic error.

All state (settings + download history) lives in the OS user-config directory
(`~/Library/Application Support/ytdlp-desktop/state.json` on macOS, the
XDG/AppData equivalent on Linux/Windows). The `pkg/ytdlp` library is used
unchanged; persistence is owned by `apps/desktop/internal/store`.

## Quality presets

Exactly six presets, mapped to `pkg/ytdlp` format selectors:

| Preset     | Format selector                              |
|------------|----------------------------------------------|
| Best       | `bv*+ba/b`                                   |
| 4K         | `bv*[height<=2160]+ba/b[height<=2160]`       |
| 1440p      | `bv*[height<=1440]+ba/b[height<=1440]`       |
| 1080p      | `bv*[height<=1080]+ba/b[height<=1080]`       |
| 720p       | `bv*[height<=720]+ba/b[height<=720]`         |
| Audio only | `ba/b`                                       |

ETA and speed are computed locally from `pkg/ytdlp` `Bytes/Total` events; the
core library does not emit them, so the UI derives a rolling estimate and
hides the value when it can't be computed.

## Prerequisites

- **Go** ≥ 1.25 (matches the parent module).
- **Node.js** ≥ 20 and **npm** ≥ 10.
- **Wails CLI v2**:
  `go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0`
- **ffmpeg** on `PATH` (or a path you configure in Settings). Most YouTube
  downloads need ffmpeg to merge separate video and audio tracks.

## Run / build

From `apps/desktop`:

```bash
# Install JS dependencies once.
cd frontend && npm install && cd ..

# Live-reload dev build (requires Wails CLI).
wails dev

# Production build for the host platform.
wails build
# Resulting binary: apps/desktop/build/bin/ytdlp-desktop
```

`wails dev` opens a native window pointing at the Vite dev server. `wails build`
embeds the built frontend in the binary so the app runs standalone.

## Project layout

```
apps/desktop/
├── main.go            Wails entry point
├── app.go             Bound methods exposed to the JS side
├── wails.json         Wails project metadata
├── go.mod             Isolated Go module; replace ../.. → parent
├── frontend/
│   ├── src/
│   │   ├── App.svelte         App shell + routing
│   │   ├── main.ts            Mount + global styles
│   │   ├── lib/
│   │   │   ├── api.ts         Wails-runtime call helpers
│   │   │   ├── stores.ts      Reactive Svelte stores
│   │   │   ├── types.ts       JS shapes mirroring the Go JSON
│   │   │   ├── format.ts      Pure formatting helpers
│   │   │   └── components/    Sidebar, Modal, Banner, ProgressRow, StatusBadge
│   │   ├── pages/             Home, Queue, Downloads, Settings
│   │   └── styles/global.css  Design tokens (dark graphite/navy + cobalt)
│   └── package.json
└── internal/
    ├── urlcheck/      Single-video URL validation
    ├── store/         Settings + history persistence (JSON)
    ├── jobs/          FIFO queue + ytdlp.Client lifecycle
    └── ffmpegdetect/  Path-aware ffmpeg probe
```

## How the pieces talk

```
                  Wails JS bridge
Svelte  ─────────────────────────►  App.* (app.go)
  │                                       │
  │                                       ├─► jobs.Manager ─► ytdlp.Client.Run
  │                                       │                 (Simulate or Download)
  │                                       │
  │                                       ├─► ffmpegdetect.Probe
  │                                       │
  │                                       └─► store.Open
  │
  └── EventsEmit("job:update" / "queue:update" / "history:update" / ...)
```

`pkg/ytdlp` is the only library import from the parent; nothing else reaches
into `internal/`. The desktop app never embeds the extractor package
directly.

## Validation

Run the Desktop Go tests after building the ignored frontend output:

```sh
cd frontend
npm ci
npm run check
npm run test:ui
npm run build
cd ..

go test -count=1 ./...
go build ./...
```

When observable behavior crosses into the parent engine, run its relevant Go
package tests as well.

## Release packaging boundary

`wails build` creates a host-platform developer artifact. It is not yet a
complete public release process.

The current `darwin/*` post-build hook packages and verifies
`ytdlp-js-helper` for macOS output. Equivalent reviewed Windows and Linux
helper packaging is still required. Public releases also need explicit
FFmpeg/FFprobe installation or bundling behavior, platform signing,
notarization where applicable, declared Linux WebKitGTK dependencies,
checksums, notices, and clean-machine install/download verification.
