# CLI Usage

This guide is organized around common tasks. The executable's help output is
the authoritative option list:

```sh
ytdlp-go --help
```

Examples use `ytdlp-go` as though it is on `PATH`. For a local source build,
replace it with `./bin/ytdlp-go` or the appropriate Windows path.

## Basic downloads

Download a supported URL using the default selection:

```sh
ytdlp-go URL
```

Choose an output template:

```sh
ytdlp-go -o "%(title)s [%(id)s].%(ext)s" URL
```

Inspect metadata without downloading media:

```sh
ytdlp-go --skip-download --print-json URL
```

## Format selection

Select separate video and audio tracks with a combined-format fallback:

```sh
ytdlp-go -f "bestvideo+bestaudio/best" URL
```

Prefer AVC video up to 1080p, combine it with audio, and fall back to the best
combined format:

```sh
ytdlp-go -f 'bestvideo[height<=1080][vcodec^=avc]+bestaudio/best' URL
```

Apply ordered format-sort preferences:

```sh
ytdlp-go -S 'res:1080' -S 'fps' --prefer-free-formats URL
```

See [Format selector behavior](FORMAT_SELECTOR_PARITY.md) for supported atoms,
fallbacks, merges, filters, indexing, sorting, and resource limits.

## Audio extraction

Extract and convert audio with FFmpeg:

```sh
ytdlp-go -x --audio-format mp3 URL
```

FFmpeg must be available for conversion and for merging separate tracks. See
[Installation](INSTALLATION.md#ffmpeg-and-ffprobe).

## Playlists

Prefer a single video when a supported URL also contains playlist context:

```sh
ytdlp-go --no-playlist URL
```

Select sparse items and a stepped range:

```sh
ytdlp-go -I "1,3,8:20:2" URL
```

List playlist entries without recursively extracting them:

```sh
ytdlp-go --flat-playlist -J URL
```

Playlist support depends on the selected extractor and URL family. See the
[playlist model](PLAYLIST_MODEL.md) and [supported-site catalog](SUPPORTED_SITES.md).

## Metadata and structured output

Print one JSON object per video:

```sh
ytdlp-go -j URL
```

Print one complete result object, including playlists:

```sh
ytdlp-go -J URL
```

Print selected fields:

```sh
ytdlp-go -O "title,id,duration" URL
```

Write selected metadata sidecars:

```sh
ytdlp-go \
  --skip-download \
  --write-info-json \
  --write-description \
  --write-thumbnail \
  URL
```

`--simulate` creates no media, subtitle, archive, or post-processing artifacts.
`--skip-download` skips media transfer but still permits explicitly requested
metadata, subtitle, and thumbnail sidecars.

## Subtitles and captions

List available manual and automatic tracks:

```sh
ytdlp-go --list-subs URL
```

Write selected tracks without downloading media:

```sh
ytdlp-go \
  --skip-download \
  --write-subs \
  --write-auto-subs \
  --sub-langs "en.*,ja" \
  --sub-format "srt/vtt/best" \
  URL
```

Convert or embed selected tracks:

```sh
ytdlp-go --write-subs --convert-subs vtt URL
ytdlp-go --embed-subs --sub-langs "en,fr" URL
```

Conversion and embedding require a compatible FFmpeg toolchain.

## SponsorBlock and chapter removal

SponsorBlock is opt-in for supported YouTube watch URLs.

```sh
ytdlp-go \
  --sponsorblock-mark "all,-preview" \
  --skip-download \
  --print-json \
  "https://www.youtube.com/watch?v=VIDEO_ID"
```

Remove selected ranges from downloaded media:

```sh
ytdlp-go \
  --sponsorblock-remove "sponsor,selfpromo" \
  --force-keyframes-at-cuts \
  "https://www.youtube.com/watch?v=VIDEO_ID"
```

See [SponsorBlock metadata](SPONSORBLOCK_METADATA.md) for categories, chapter
titles, mirrors, and removal behavior.

## Output paths

Repeat `--paths [TYPES:]PATH` and `--output [TYPES:]TEMPLATE` to route supported
artifact types beneath the configured home directory:

```sh
ytdlp-go \
  --paths "subtitle:captions" \
  --paths "thumbnail:images" \
  --output "default:media/%(title)s.%(ext)s" \
  --output "subtitle:%(title)s.%(ext)s" \
  URL
```

Output paths and templates are confined and resource-bounded. See
[Configuration](CONFIGURATION.md) and the executable help for accepted types.

## Transfer controls

```sh
ytdlp-go \
  --limit-rate 5M \
  --retries 3 \
  --concurrent-fragments 8 \
  --per-host-fragments 4 \
  --download-archive archive.txt \
  URL
```

Use `--progress-json` for newline-delimited structured progress events.

## Configuration

Load an explicit configuration file:

```sh
ytdlp-go --config-location /path/to/yt-dlp.conf URL
```

Skip discovered configuration:

```sh
ytdlp-go --ignore-config URL
```

Command-line values have highest precedence. See [Configuration](CONFIGURATION.md)
for locations, encoding, quoting, aliases, and nested explicit files.

## Cookies and authentication

Import a Netscape cookie file:

```sh
ytdlp-go --cookies cookies.txt URL
```

Import an explicitly selected browser profile:

```sh
ytdlp-go --cookies-from-browser chrome:Default URL
```

Use an explicitly selected native netrc file:

```sh
ytdlp-go --netrc --netrc-location /path/to/.netrc URL
```

Never paste real cookies, authorization headers, signed media URLs, browser
profiles, or tokens into public issues. Platform and extractor limitations are
documented in [browser cookie import](CHROMIUM_COOKIE_IMPORT.md) and the
[supported-site catalog](SUPPORTED_SITES.md).

## Extractor discovery and routing

Inspect the native extractor catalog without extraction or network access:

```sh
ytdlp-go --list-extractors
ytdlp-go --extractor-descriptions
```

Constrain automatic extractor routing:

```sh
ytdlp-go --use-extractors 'youtube.*,end' URL
```

See [Extractor selection](EXTRACTOR_SELECTION_EVIDENCE.md) and
[routing controls](EXTRACTOR_ROUTING_CONTROLS_EVIDENCE.md) for the exact
current contract.

## Automation and embedding

Prefer JSON and structured progress output over parsing human-readable text.
For in-process Go integration, use the [embedding API](EMBEDDING.md) instead of
spawning the CLI.

## Errors and support

The CLI categorizes invalid input, authentication, unsupported behavior,
network failures, security checks, cancellation, and internal failures. Keep
the complete diagnostic context when reporting a problem, but remove secrets
and personal data.

Start with [Troubleshooting](TROUBLESHOOTING.md), then read
[Support](../SUPPORT.md) before opening an issue.
