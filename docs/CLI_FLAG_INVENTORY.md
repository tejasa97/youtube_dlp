# CLI Flag Inventory

**Purpose**: Complete classification of every yt-dlp CLI option against the Go port, anchored to the pinned behavioral reference.

**Reference**: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` (`yt_dlp/options.py`)

**Go baseline**: `origin/main` at `05b2282` (feat: add VK public ecosystem)

**Classification key**:

| Status | Meaning |
|--------|---------|
| **present** | Flag is registered on the Go FlagSet and the behavior is wired |
| **wire-only** | `Request`/options field exists, flag is missing from the CLI |
| **needs-core** | Flag needs a new `Request` field or downstream behavior change |
| **defer** | Needs a real subsystem (not a switch) — complex, deferred |
| **go-only** | Go-specific extension, not a yt-dlp flag |
| **parked** | Needs a behavior contract before registration |

---

## General Options

| Option | Status | Notes |
|--------|--------|-------|
| `-h`, `--help` | present | Handled by `flag.ErrHelp`; returns 0 |
| `--version` | present | `flags.Bool("version", ...)` |
| `--update` / `-U` | defer | Update subsystem |
| `--no-update` | defer | Update subsystem |
| `--update-to` | defer | Update subsystem |
| `--ignore-errors` / `-i` | present | `flags.BoolFunc("ignore-errors", ...)` |
| `--no-abort-on-error` | present | `flags.BoolFunc("no-abort-on-error", ...)` |
| `--abort-on-error` | present | `flags.BoolFunc("abort-on-error", ...)` |
| `--list-extractors` | defer | Extractor listing |
| `--extractor-descriptions` | defer | Extractor listing |
| `--use-extractors` / `--ies` | defer | Extractor selection |
| `--force-generic-extractor` | defer | Extractor routing |
| `--default-search` | defer | Search prefix |
| `--ignore-config` / `--no-config` | present | Config loader |
| `--no-config-locations` | present | Config loader |
| `--config-locations` | present | `flags.Var(&configLocations, "config-location", ...)` |
| `--plugin-dirs` / `--no-plugin-dirs` | defer | Plugin subsystem |
## Network Options

| Option | Status | Notes |
|--------|--------|-------|
| `--proxy` | present | `flags.String("proxy", ...)` |
| `--socket-timeout` | present | `flags.Duration("socket-timeout", ...)` |
| `--source-address` | defer | Network binding |
| `--impersonate` | present | `flags.String("impersonate", ...)` |
| `--list-impersonate-targets` | defer | Impersonation listing |
| `--force-ipv4` / `-4` | defer | Network preference |
| `--force-ipv6` / `-6` | defer | Network preference |
| `--enable-file-urls` | defer | File URL support |

## Geo-restriction

| Option | Status | Notes |
|--------|--------|-------|
| `--geo-verification-proxy` | defer | Geo subsystem |
| `--xff` | defer | Geo subsystem |
| `--geo-bypass` / `--no-geo-bypass` | defer | Geo subsystem |
| `--geo-bypass-country` | defer | Geo subsystem |
| `--geo-bypass-ip-block` | defer | Geo subsystem |

## Video Selection

| Option | Status | Notes |
|--------|--------|-------|
| `--playlist-start` | present | `flags.Int("playlist-start", ...)` |
| `--playlist-end` | present | `flags.Int("playlist-end", ...)` |
| `--playlist-items` / `-I` | present | `flags.String("playlist-items", ...)` |
| `--match-title` | defer | Regex filter |
| `--reject-title` | defer | Regex filter |
| `--min-filesize` / `--max-filesize` | defer | Download filter |
| `--date` / `--datebefore` / `--dateafter` | defer | Date filter |
| `--min-views` / `--max-views` | defer | View count filter |
| `--match-filters` / `--no-match-filters` | present | `flags.Var(&matchFilters, "match-filter", ...)` |
| `--break-match-filters` / `--no-break-match-filters` | present | `flags.Var(&breakMatchFilters, "break-match-filter", ...)` |
| `--no-playlist` / `--yes-playlist` | **needs-core** | New `Playlist.Disabled` field. NOT `RelatedFiles.NoPlaylist` |
| `--age-limit` | defer | Age restriction filter |
| `--download-archive` / `--no-download-archive` | present | `flags.String("download-archive", ...)` |
| `--max-downloads` | defer | Download count limit |
| `--break-on-existing` / `--no-break-on-existing` | defer | Archive-based stop |
| `--break-on-reject` | defer | Reject-based stop |
| `--break-per-input` / `--no-break-per-input` | defer | Per-input break |
| `--skip-playlist-after-errors` | present | `flags.Int("skip-playlist-after-errors", ...)` |
| `--js-runtimes` / `--no-js-runtimes` | defer | JS runtime subsystem |
| `--remote-components` / `--no-remote-components` | defer | Remote component subsystem |
| `--flat-playlist` / `--no-flat-playlist` | present | `flags.Bool("flat-playlist", ...)` |
| `--live-from-start` / `--no-live-from-start` | present | `flags.Bool("live-from-start", ...)` |
| `--wait-for-video` / `--no-wait-for-video` | defer | Live wait behavior |
| `--mark-watched` / `--no-mark-watched` | defer | YouTube-specific |
| `--no-colors` / `--no-colours` | defer | Terminal output |
| `--color` | defer | Terminal output |
| `--compat-options` | defer | Compatibility shims |
## Post-Processing Options

| Option | Status | Notes |
|--------|--------|-------|
| `--extract-audio` / `-x` | present | `flags.Bool("extract-audio", ...)` |
| `--audio-format` | present | `flags.String("audio-format", ...)` |
| `--audio-quality` | present | `flags.Int("audio-quality", ...)` |
| `--remux-video` | present | `flags.String("remux-video", ...)` |
| `--recode-video` | present | `flags.String("recode-video", ...)` |
| `--postprocessor-args` / `--ppa` | defer | Post-processor args |
| `--keep-video` / `-k` / `--no-keep-video` | defer | Post-process cleanup |
| `--post-overwrites` / `--no-post-overwrites` | defer | Post-process overwrite |
| `--embed-subs` / `--no-embed-subs` | present | `flags.Bool("embed-subs", ...)` |
| `--embed-thumbnail` / `--no-embed-thumbnail` | present | `flags.Bool("embed-thumbnail", ...)` |
| `--embed-metadata` / `--add-metadata` / `--no-embed-metadata` / `--no-add-metadata` | present | `flags.Bool("embed-metadata", ...)` + `Request.EmbedMetadata` |
| `--embed-chapters` / `--add-chapters` / `--no-embed-chapters` / `--no-add-chapters` | present | `flags.BoolFunc("embed-chapters", ...)` + `Request.EmbedChapters *bool` |
| `--embed-info-json` / `--no-embed-info-json` | defer | Info JSON embedding |
| `--metadata-from-title` | defer | Legacy metadata extraction |
| `--parse-metadata` | present | `flags.Var(metadataParseFlag{...}, "parse-metadata", ...)` |
| `--replace-in-metadata` | present | `flags.Var(metadataReplaceFlag{...}, "replace-in-metadata", ...)` |
| `--xattrs` / `--xattr` | defer | Extended attributes |
| `--concat-playlist` | defer | Playlist concatenation |
| `--fixup` | defer | Media fixup |
| `--ffmpeg-location` | **needs-core** | New `Request.FfmpegLocation` + toolset propagation |
| `--exec` / `--no-exec` | defer | External command execution |
| `--exec-before-download` / `--no-exec-before-download` | defer | External command execution |
| `--convert-subs` / `--convert-sub` / `--convert-subtitles` | present | `flags.String("convert-subs", ...)` |
| `--convert-thumbnails` | present | `flags.String("convert-thumbnails", ...)` |
| `--split-chapters` / `--no-split-chapters` | defer | Chapter splitting |
| `--remove-chapters` / `--no-remove-chapters` | present | `flags.Var(&removeChapters, "remove-chapters", ...)` |
| `--force-keyframes-at-cuts` / `--no-force-keyframes-at-cuts` | present | `flags.BoolFunc("force-keyframes-at-cuts", ...)` |
| `--use-postprocessor` | defer | Plugin post-processor |

## SponsorBlock Options

| Option | Status | Notes |
|--------|--------|-------|
| `--sponsorblock-mark` | present | `flags.Func("sponsorblock-mark", ...)` |
| `--sponsorblock-remove` | present | `flags.Func("sponsorblock-remove", ...)` |
| `--sponsorblock-chapter-title` | present | `flags.Func("sponsorblock-chapter-title", ...)` |
| `--no-sponsorblock` | present | `flags.BoolFunc("no-sponsorblock", ...)` |
| `--sponsorblock-api` | present | `flags.String("sponsorblock-api", ...)` |

## Verbosity and Simulation Options

| Option | Status | Notes |
|--------|--------|-------|
| `--quiet` / `--no-quiet` | present | `flags.BoolFunc("quiet", ...)` |
| `--no-warnings` / `--warnings` | **parked** | Needs a defined stderr/verbosity pipeline before registration |
| `--simulate` / `-s` / `--no-simulate` | present | `flags.BoolFunc("simulate", ...)` |
| `--ignore-no-formats-error` / `--no-ignore-no-formats-error` | defer | Error handling |
| `--skip-download` / `--no-download` | present | `flags.Bool("skip-download", ...)` |
| `--print` / `-O` | present | `flags.Var(&printTemplates, "print", ...)` |
| `--print-to-file` | present | `--print-to-file` handling |
| `--get-url` / `-g` | present | `flags.Bool("get-url", ...)` |
| `--get-title` / `-e` | present | `flags.Bool("get-title", ...)` |
| `--get-id` | present | `flags.Bool("get-id", ...)` |
| `--get-thumbnail` | present | `flags.Bool("get-thumbnail", ...)` |
| `--get-description` | present | `flags.Bool("get-description", ...)` |
| `--get-duration` | present | `flags.Bool("get-duration", ...)` |
| `--get-filename` | present | `flags.Bool("get-filename", ...)` |
| `--get-format` | present | `flags.Bool("get-format", ...)` |
| `--dump-json` / `-j` | present | `flags.Bool("dump-json", ...)` |
| `--dump-single-json` / `-J` | present | `flags.Bool("dump-single-json", ...)` |
| `--print-json` | present | `flags.Bool("print-json", ...)` |
| `--force-write-archive` | defer | Archive write behavior |
| `--newline` | defer | Output format |
| `--no-progress` / `--progress` | **parked** | Needs a defined stderr/verbosity pipeline before registration |
| `--console-title` | defer | Terminal title |
| `--progress-template` | present | `flags.String("progress-template", ...)` |
| `--progress-delta` | defer | Progress update interval |
| `--verbose` / `-v` / `--no-verbose` | **parked** | Needs a defined stderr/verbosity pipeline before registration |
| `--dump-pages` / `--write-pages` / `--load-pages` | defer | Debug page dumping |
| `--print-traffic` | defer | Network traffic debug |
## Filesystem Options

| Option | Status | Notes |
|--------|--------|-------|
| `--batch-file` / `-a` / `--no-batch-file` | **needs-core** | URL reading pipeline |
| `--id` | defer | Use video ID as filename |
| `--paths` / `-P` | present | `flags.Var(paths, "paths", ...)` |
| `--output` / `-o` | present | `flags.Var(&outputTemplates, "output", ...)` |
| `--output-na-placeholder` | defer | NA placeholder |
| `--autonumber-size` / `--autonumber-start` | defer | Autonumbering |
| `--restrict-filenames` / `--no-restrict-filenames` | **needs-core** | Template has `sanitizeFilename(..., restricted)`. Needs CLI + product option |
| `--windows-filenames` / `--no-windows-filenames` | **needs-core** | Filename sanitization |
| `--trim-filenames` / `--trim-file-names` | **needs-core** | Filename length limit |
| `--no-overwrites` / `-w` | present | Inverse of `--force-overwrites`; added in Wave 1 |
| `--force-overwrites` / `--yes-overwrites` | present | `flags.Bool("force-overwrites", ...)` |
| `--no-force-overwrites` | present | Inverse handled by default |
| `--continue` / `-c` / `--no-continue` | **needs-core** | Resume partial downloads |
| `--part` / `--no-part` | **needs-core** | `.part` file usage |
| `--mtime` / `--no-mtime` | **needs-core** | File modification time |
| `--write-description` / `--no-write-description` | present | `flags.Bool("write-description", ...)` |
| `--write-info-json` / `--no-write-info-json` | present | `flags.Bool("write-info-json", ...)` |
| `--write-playlist-metafiles` / `--no-write-playlist-metafiles` | present | `flags.Bool("no-write-playlist-metafiles", ...)` |
| `--clean-info-json` / `--no-clean-info-json` | defer | Info JSON cleaning |
| `--write-comments` / `--get-comments` / `--no-write-comments` / `--no-get-comments` | present | `flags.Bool("write-comments", ...)` |
| `--load-info-json` | defer | Load cached info |
| `--cookies` / `--no-cookies` | present | `flags.String("cookies", ...)` |
| `--cookies-from-browser` / `--no-cookies-from-browser` | present | `flags.String("cookies-from-browser", ...)` |
| `--cache-dir` / `--no-cache-dir` | present | `flags.String("cache-dir", ...)` |
| `--rm-cache-dir` | defer | Cache cleanup |

## Thumbnail Options

| Option | Status | Notes |
|--------|--------|-------|
| `--write-thumbnail` / `--no-write-thumbnail` | present | `flags.BoolFunc("write-thumbnail", ...)` |
| `--write-all-thumbnails` | present | `flags.BoolFunc("write-all-thumbnails", ...)` |
| `--list-thumbnails` | present | `flags.Bool("list-thumbnails", ...)` |

## Internet Shortcut Options

| Option | Status | Notes |
|--------|--------|-------|
| `--write-link` | present | `flags.Bool("write-link", ...)` |
| `--write-url-link` | present | `flags.Bool("write-url-link", ...)` |
| `--write-webloc-link` | present | `flags.Bool("write-webloc-link", ...)` |
| `--write-desktop-link` | present | `flags.Bool("write-desktop-link", ...)` |

## Extractor Options

| Option | Status | Notes |
|--------|--------|-------|
| `--extractor-retries` | defer | Extractor retry logic |
| `--allow-dynamic-mpd` / `--ignore-dynamic-mpd` | defer | DASH policy |
| `--hls-split-discontinuity` / `--no-hls-split-discontinuity` | defer | HLS policy |
| `--extractor-args` | defer | Extractor-specific arguments |

## Go-only Extensions (not in yt-dlp options.py)

These are `Request`/options fields that are Go-specific and have no counterpart in the pinned yt-dlp options:

| Option | Status | Notes |
|--------|--------|-------|
| `--preferred-extensions` | **wire-only** | `Request.PreferredExtensions []string` exists, no CLI flag |
| `--youtube-translated-captions` / `--no-youtube-translated-captions` | **wire-only** | `Request.YouTubeTranslatedCaptions bool` exists, no CLI flag |
| `--live-poll-interval` | **wire-only** | `DownloaderOptions.LivePollInterval` exists, no CLI flag |
| `--live-refresh-interval` | **wire-only** | `DownloaderOptions.LiveRefreshInterval` exists, no CLI flag |
| `--live-max-polls` | **wire-only** | `DownloaderOptions.LiveMaxPolls` exists, no CLI flag |
| `--live-max-no-progress-polls` | **wire-only** | `DownloaderOptions.LiveMaxNoProgressPolls` exists, no CLI flag |

---

## Summary

| Category | Count |
|----------|-------|
| **present** | ~106 |
| **wire-only** | 6 Go-only |
| **needs-core** | ~12 (Wave 1: 1, Wave 2: 7, Wave 3: 2, misc) |
| **parked** | ~4 (verbosity) |
| **defer** | ~100+ |
| **Total yt-dlp options** | ~292 |

## Wave Plan

### Wave 1 (this PR)
| Flag | Classification | Work |
|------|---------------|------|
| `--no-overwrites` | present | Inverse of `--force-overwrites`, pure CLI |
| `--no-playlist` / `--yes-playlist` | needs-core | New `Playlist.Disabled` field |

### Wave 1b (playlist-disable consume)
| `--no-playlist` / `--yes-playlist` | needs-core | Behavioral consumption of `Playlist.Disabled` in extraction routing |

### Wave 2
| Flag | Classification | Work |
|------|---------------|------|
| `--restrict-filenames` / `--no-restrict-filenames` | needs-core | Wire to existing `sanitizeFilename` |
| `--windows-filenames` / `--no-windows-filenames` | needs-core | Filename sanitization |
| `--trim-filenames` | needs-core | Filename length limit |
| `--continue` / `--no-continue` | needs-core | Resume support |
| `--part` / `--no-part` | needs-core | `.part` file support |
| `--mtime` / `--no-mtime` | needs-core | File mtime support |
| `--ffmpeg-location` | needs-core | Toolset discovery |

### Wave 3
| Flag | Classification | Work |
|------|---------------|------|
| `--batch-file` / `-a` / `--no-batch-file` | needs-core | URL reading pipeline |
| `--list-formats` / `-F` | needs-core | Sugar over simulate + `%(formats_table)s` |

### Parked
`--verbose` / `-v` / `--no-verbose`, `--no-warnings` / `--warnings`, `--progress` / `--no-progress`

### Go-only (Wave 4)
`--preferred-extensions`, `--youtube-translated-captions`, live poll flags
| `--alias` / `-t` / `--preset-alias` | present | Config alias system (pre-parse) |