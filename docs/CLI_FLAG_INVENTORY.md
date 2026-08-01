# CLI Flag Inventory

**Purpose**: Complete classification of every yt-dlp CLI option against the Go port, anchored to the pinned behavioral reference.

**Reference**: `yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8` (`yt_dlp/options.py`)

**Go baseline**: `origin/main` at `be41e0d` (close queue selection and stopping parity)

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
| `--source-address` | present | Validated native/profile TCP source binding; programmatic source/family conflicts fail closed |
| `--impersonate` | present | `flags.String("impersonate", ...)` |
| `--list-impersonate-targets` | defer | Impersonation listing |
| `--force-ipv4` / `-4` | present | Native/profile TCP family policy; CLI options are last-wins |
| `--force-ipv6` / `-6` | present | Native/profile TCP family policy; CLI options are last-wins |
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
| `--match-title` | present | Hidden parity flag. `SimpleFilters.MatchTitle` → `internal/compat/simplefilter` (case-insensitive regex search on title) |
| `--reject-title` | present | Hidden parity flag. `SimpleFilters.RejectTitle` → `internal/compat/simplefilter` |
| `--min-filesize` / `--max-filesize` | present | `DownloaderOptions.MinFilesize/MaxFilesize` → direct HTTP downloader abort using Content-Length + resume offset and bounded streaming enforcement for unknown/misleading lengths (reference `parse_bytes` SIZE grammar) |
| `--date` / `--datebefore` / `--dateafter` | present | `SimpleFilters.Date/DateBefore/DateAfter` → strict `date_from_str` grammar; `--date` wins over the range bounds with a warning |
| `--min-views` / `--max-views` | present | Hidden parity flags. `SimpleFilters.MinViews/MaxViews` |
| `--match-filters` / `--no-match-filters` | present | `flags.Var(&matchFilters, "match-filter", ...)` |
| `--break-match-filters` / `--no-break-match-filters` | present | `flags.Var(&breakMatchFilters, "break-match-filter", ...)` |
| `--no-playlist` / `--yes-playlist` | present | `Playlist.Disabled` field. YouTube implements `_yes_playlist`-style choice for ambiguous video+playlist URLs |
| `--age-limit` | present | `SimpleFilters.AgeLimit` → `age_restricted` semantics |
| `--download-archive` / `--no-download-archive` | present | `flags.String("download-archive", ...)` |
| `--force-write-archive` / `--force-write-download-archive` / `--force-download-archive` | present | `Request.ForceWriteArchive`; records successful simulate/skip-download entries only |
| `--max-downloads` | present | Per-Run `Request.MaxDownloads`; `Result.Downloads` aggregates qualifying attempts, including errored runs; CLI carries the remaining budget across batch inputs |
| `--break-on-existing` / `--no-break-on-existing` | present | `Request.BreakOnExisting` → `StopBreakOnExisting` |
| `--break-on-reject` | present | Hidden parity flag. `Request.BreakOnReject` → `StopBreakOnReject` |
| `--break-per-input` / `--no-break-per-input` | present | `Request.BreakPerInput`; resets budget/stop scope per input and consumes stops without the global abort diagnostic |
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
| `--ffmpeg-location` | **present** | `Request.Filesystem.FfmpegLocation` + ffmpeg discovery propagation |
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
| `--force-write-archive` / aliases | present | Records successful simulate/skip-download entries after selection |
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
| `--batch-file` / `-a` / `--no-batch-file` | **present** | Bounded URL reading pipeline; repeatable files and stdin via `-` |
| `--id` | defer | Use video ID as filename |
| `--paths` / `-P` | present | `flags.Var(paths, "paths", ...)` |
| `--output` / `-o` | present | `flags.Var(&outputTemplates, "output", ...)` |
| `--output-na-placeholder` | defer | NA placeholder |
| `--autonumber-size` / `--autonumber-start` | defer | Autonumbering |
| `--restrict-filenames` / `--no-restrict-filenames` | **present** | `Filesystem.RestrictFilenames` wired to template filename sanitization |
| `--windows-filenames` / `--no-windows-filenames` | **present** | `Filesystem.WindowsFilenames` wired to Windows-compatible path parts |
| `--trim-filenames` / `--trim-file-names` | **present** | `Filesystem.TrimFilenames` wired to basename length limit |
| `--no-overwrites` / `-w` | present | Existing bool-based no-overwrite behavior |
| `--force-overwrites` / `--yes-overwrites` | present | `--yes-overwrites` is an exact alias of `--force-overwrites` |
| `--no-force-overwrites` | defer | Current bool plumbing cannot distinguish yt-dlp's tri-state default from explicit no-overwrites |
| `--continue` / `-c` / `--no-continue` | **present** | `Filesystem.NoContinue` wired to direct downloader resume control |
| `--part` / `--no-part` | **present** | `Filesystem.NoPart` wired to `.part` temporary file control |
| `--mtime` / `--no-mtime` | **present** | `Filesystem.NoMtime` wired to output file mtime from metadata |
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
| `--extractor-retries` | present | Bounded transient network retries around entered extractor calls only; default 3 in CLI |
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
| **present** | ~116 |
| **wire-only** | 6 Go-only |
| **needs-core** | ~2 (misc) |
| **parked** | ~4 (verbosity) |
| **defer** | ~100+ |
| **Total yt-dlp options** | ~292 |

## Wave Plan

### Wave 1 (this PR)
| Flag | Classification | Work |
|------|---------------|------|
| `--no-overwrites` | present | Inverse of `--force-overwrites`, pure CLI |
| `--no-playlist` / `--yes-playlist` | present | `Playlist.Disabled` field. YouTube implements `_yes_playlist`-style choice for ambiguous video+playlist URLs |

### Wave 1b (playlist-disable consume)
| `--no-playlist` / `--yes-playlist` | present | YouTube implements `_yes_playlist`-style choice via `NoPlaylist`. Other extractors deferred |

### Wave 2 (this PR)
| Flag | Classification | Work |
|------|---------------|------|
| `--restrict-filenames` / `--no-restrict-filenames` | present | `Filesystem.RestrictFilenames` → template filename sanitization |
| `--windows-filenames` / `--no-windows-filenames` | present | `Filesystem.WindowsFilenames` → Windows-compatible path parts |
| `--trim-filenames` / `--trim-file-names` | present | `Filesystem.TrimFilenames` → basename length limit |
| `--continue` / `-c` / `--no-continue` | present | `Filesystem.NoContinue` → direct downloader resume control |
| `--part` / `--no-part` | present | `Filesystem.NoPart` → `.part` temporary file control |
| `--mtime` / `--no-mtime` | present | `Filesystem.NoMtime` → output file mtime from metadata |
| `--ffmpeg-location` | present | `Filesystem.FfmpegLocation` → ffmpeg/ffprobe discovery |

### Wave 3
| Flag | Classification | Work |
|------|---------------|------|
| `--batch-file` / `-a` / `--no-batch-file` | present | Bounded URL reading pipeline; repeatable files, comments/blank lines, and stdin via `-` |
| `--list-formats` / `-F` | present | Existing format-table renderer at the pre-process stage; simulation is implied unless `--no-simulate` is explicit |

### Wave 4
| Flag | Classification | Work |
|------|---------------|------|
| `--match-title` / `--reject-title` | present | Hidden parity flags; `internal/compat/simplefilter` case-insensitive regex title checks |
| `--date` / `--dateafter` / `--datebefore` | present | Strict `date_from_str` grammar + inclusive `DateRange`; `--date` wins with a warning |
| `--min-views` / `--max-views` / `--age-limit` | present | Hidden parity flags for views; `age_restricted` semantics |
| `--min-filesize` / `--max-filesize` | present | `DownloaderOptions` bounds enforced in the direct HTTP downloader (known-length preflight plus bounded streaming for unknown/misleading lengths; Content-Encoding remains exempt) |
| `--max-downloads` | present | Per-Run cap with partial `Result.Downloads` accounting on categorized errors; CLI carries the remaining budget across batch inputs |
| `--break-on-existing` / `--no-break-on-existing` | present | Archive matches stop the run (`StopBreakOnExisting`) |
| `--break-on-reject` | present | Hidden parity flag; any filter rejection stops the run (`StopBreakOnReject`) |
| `--break-per-input` / `--no-break-per-input` | present | Resets budget/stop scope per input without `Aborting remaining downloads` or exit 101 |
| `--force-write-archive` / aliases | present | Records successful simulate/skip-download entries after selection; rejects and size aborts are not recorded |
| Stopping exit contract | present | Queue-wide stops emit `Aborting remaining downloads` and exit 101; per-input stops continue silently; cancellation remains 130 |

### Parked
`--verbose` / `-v` / `--no-verbose`, `--no-warnings` / `--warnings`, `--progress` / `--no-progress`

### Go-only (Wave 4)
`--preferred-extensions`, `--youtube-translated-captions`, live poll flags
| `--alias` / `-t` / `--preset-alias` | present | Config alias system (pre-parse) |
