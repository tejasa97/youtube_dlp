# YTDLP Go Desktop

YTDLP Go Desktop is the graphical interface for the `ytdlp-go` engine. It is
designed for people who want to download a video without learning command-line
options.

The application is currently a **Preview**. Public installers are not yet
published.

## V0 scope

Desktop V0 accepts one public, on-demand YouTube video at a time.

Supported input forms include ordinary YouTube watch links, `youtu.be` links,
and supported YouTube embed links that resolve to an 11-character video ID.

Not supported in Desktop V0:

- playlists;
- channels or handles;
- search pages;
- Shorts;
- live streams;
- videos requiring sign-in;
- other websites; or
- DRM decryption.

The CLI and Go library have broader extractor support. A feature supported by
the engine is not automatically available in the Desktop interface.

## Download workflow

1. Paste a supported YouTube video URL on Home.
2. Let the application analyze its public metadata and available formats.
3. Confirm the title, channel, duration, and thumbnail.
4. Choose a quality preset.
5. Choose the destination folder.
6. Select **Download** or **Add to Queue**.

The queue processes one active job at a time in first-in, first-out order.

## Quality presets

| Preset | Intended result |
| --- | --- |
| Best | Highest available result selected by the engine |
| 4K | Best result up to 2160p |
| 1440p | Best result up to 1440p |
| 1080p | Best result up to 1080p |
| 720p | Best result up to 720p |
| Audio only | Best available audio track |

A label such as 4K or 1080p is a maximum, not a promise that the source offers
that resolution. Many high-resolution choices use separate video and audio
tracks and therefore require FFmpeg to produce one final file.

## Queue

Queue shows:

- the active job;
- jobs waiting to start;
- total, downloading, and completed summary counts;
- current progress, transfer speed, and estimated time remaining when known;
- recently completed or failed jobs; and
- actions to cancel, retry, or remove eligible jobs.

Canceling the active job waits for its worker to exit before the next queued
job starts. Retrying creates a new attempt using the retained request details.

## Downloads history

Successful downloads are added to local history. Downloads lets you:

- search completed entries;
- open a completed file;
- reveal it in the operating-system file manager.

## Settings

Settings controls:

- default download folder;
- automatic FFmpeg detection;
- an explicit FFmpeg location;
- copying diagnostics for support.

The default destination is a `ytdlp-desktop` folder inside the current user's
Downloads directory.

## FFmpeg

FFmpeg is required for downloads that merge separate video and audio tracks.
The underlying engine also uses FFmpeg and FFprobe for selected media
operations, although Desktop V0 does not expose advanced conversion controls.
The **Audio only** preset selects the best available audio track; it does not
promise MP3 conversion.

If FFmpeg is unavailable, the application explains which operations need it.
Install a trusted FFmpeg distribution or select its location in Settings.
Future public packages may include a reviewed copy, but that is not promised
until redistribution and release procedures are complete.

## YouTube JavaScript challenges

Some YouTube media URLs require a transformation derived from the current
player JavaScript. The app uses the separate `ytdlp-js-helper` executable for
that work.

If the current player takes too long to process, the application reports:

> YouTube challenge timed out — retry

This happens before media transfer begins. It does not mean that every video
or quality is unavailable, but a particular video may remain unavailable until
the helper can process its current player challenge.

## Data and privacy

Settings and a bounded local download history are stored in `state.json` under
the operating system's per-user configuration directory:

- macOS: `~/Library/Application Support/ytdlp-desktop/state.json`
- Windows: the current user's configuration/AppData location
- Linux: the current user's configuration directory, normally under
  `$XDG_CONFIG_HOME` or `~/.config`

History is capped at 200 entries. The state file is written with user-only file
permissions where supported. If it becomes unreadable or malformed, the app
preserves a `.bak` copy and starts with default state.

The current application does not advertise automatic telemetry. Diagnostics
should be reviewed before sharing and must not include cookies, tokens, private
media details, usernames, or sensitive local paths.

## Current limitations

- Public installers and automatic updates are not available.
- Desktop supports fewer inputs and features than the CLI.
- YouTube can change its pages, clients, and player challenges without notice.
- Some videos require authentication, a supported region, or behavior outside
  Desktop V0.
- Transfer speed and ETA are local estimates and may be hidden when the total
  size is unknown.
- FFmpeg must currently be installed or configured separately.

See [Troubleshooting](TROUBLESHOOTING.md) for common failures and
[Support](../SUPPORT.md) before filing an issue.

## Development

Developers should use the [Desktop maintainer guide](../apps/desktop/README.md)
for Wails prerequisites, project layout, tests, and native build commands.
