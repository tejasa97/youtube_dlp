<h1 align="center">ytdlp-go</h1>

<p align="center">
  <strong>A native, Python-free media downloader and embeddable Go library.</strong>
</p>

<p align="center">
  <a href="#project-status"><img src="https://img.shields.io/badge/status-alpha-f59e0b" alt="Project status: alpha"></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.25.12-00ADD8?logo=go&amp;logoColor=white" alt="Go 1.25.12"></a>
  <a href="#python-free-by-design"><img src="https://img.shields.io/badge/Python-free-16a34a" alt="Python-free"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-2563eb" alt="Apache License 2.0"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="docs/SUPPORTED_SITES.md">Supported sites</a> ·
  <a href="docs/README.md">Documentation</a> ·
  <a href="docs/EMBEDDING.md">Go API</a> ·
  <a href="CONTRIBUTING.md">Contributing</a>
</p>

---

`ytdlp-go` is an independent Go implementation informed by the observable
behavior of [yt-dlp](https://github.com/yt-dlp/yt-dlp). Extraction,
networking, playlists, media protocols, compatibility languages, plugins, and
the public API are implemented in Go. Python is not used as a runtime, build,
test, plugin, or fallback dependency.

The project aims for practical yt-dlp feature parity through native,
evidence-backed implementations. It does not claim blanket parity: supported
behavior is bounded by checked-in conformance fixtures, and every known gap
remains explicit.

> [!CAUTION]
> **This is alpha software, not yet a drop-in replacement for yt-dlp.**
> The repository currently has 66 representative native extractors and broad
> downloader infrastructure, but not yt-dlp's thousands of sites or complete
> option language. Check the [supported-site catalog](docs/SUPPORTED_SITES.md)
> and [capability manifest](conformance/parity_manifest.yaml) before relying on
> a particular workflow.

This project is not affiliated with, endorsed by, or sponsored by yt-dlp,
GitHub, Google/YouTube, or any supported service. Product and service names
identify compatibility targets only.

## Project status

| Area | Evidence-backed scope today |
| --- | --- |
| Runtime | Native Go binaries; no Python execution or interpreter fallback |
| Extractors | 66 representative extractors across simple, shared-backend, playlist, live, authenticated, regional, anti-bot, manifest, and JavaScript-heavy families |
| Media | Direct HTTP(S), HLS, DASH, and ISM/Smooth Streaming |
| Playlists | Lazy reusable sequences, bounded continuations, item/range selection, reverse selection, and flat-playlist mode |
| Formats | Bounded selector AST, sorting and filtering, video+audio merging, fallbacks, and multi-output plans |
| Post-processing | Typed ffmpeg/ffprobe operations, subtitle conversion/embedding, metadata, chapters, remuxing, audio extraction, concat, and safe moves |
| Compatibility | Output/progress templates, match filters, metadata transforms, configuration files, aliases, cache, and download archive |
| Extensions | Versioned native RPC and constrained WASM plugins, signed packs, catalogs, and updater transactions |
| Public API | Versioned v1alpha1 Go API with context cancellation, categorized errors, events, playlists, metadata, and artifacts |

The capability manifest records **83 capabilities**: **76 compatible** within
their declared corpora, **6 partial**, and **1 intentional deviation**.
“Compatible” means the linked deterministic evidence passes; it does not mean
unbounded equivalence with every upstream behavior.

The repository implementation for Phases 0–3 exists, while Gate G3 remains
blocked on real deployment evidence. G3 is currently backburnered rather than
an active development target; synthetic fixtures are not presented as
production traffic, canary, regional, account, or Windows evidence.

## Why ytdlp-go?

| Principle | What it provides |
| --- | --- |
| Native deployment | A portable Go program and library without a Python environment |
| Explicit compatibility | Claims point to fixtures, tests, provenance, and known deviations |
| Safe composition | Bounded resources, confined output, cancellation, categorized errors, and structured events |
| Auditable boundaries | JavaScript, cookies, credentials, ffmpeg, plugins, and update trust are visible interfaces |
| No hidden fallback | Unsupported behavior fails explicitly instead of invoking Python |

## Quick start

Go 1.25.12 or newer is required.

```sh
git clone https://github.com/tejasa97/youtube_dlp.git
cd youtube_dlp
mkdir -p bin

CGO_ENABLED=0 go build -trimpath -o bin/ytdlp-go ./cmd/ytdlp-go
CGO_ENABLED=0 go build -trimpath -o bin/ytdlp-js-helper ./cmd/ytdlp-js-helper

./bin/ytdlp-go --version
./bin/ytdlp-go --help
```

Download a supported URL:

```sh
./bin/ytdlp-go URL
```

Select and merge adaptive video and audio:

```sh
./bin/ytdlp-go \
  -f "bestvideo+bestaudio/best" \
  -o "%(title)s.%(ext)s" \
  URL
```

Extract metadata without downloading:

```sh
./bin/ytdlp-go --skip-download --print-json URL
```

Extract audio with ffmpeg:

```sh
./bin/ytdlp-go -x --audio-format mp3 URL
```

There are no endorsed public binary releases yet. Build from a reviewed source
revision; repository test keys are not production updater trust.

## Format selection and sorting

Format selectors support bounded comma outputs, slash fallbacks, plus merges,
groups, direct IDs, quality atoms, extensions, filters, and `.N` indexing. For
example, prefer AVC video up to 1080p with a separate audio stream and fall
back to the best combined format:

```sh
./bin/ytdlp-go \
  -f 'bestvideo[height<=1080][vcodec^=avc]+bestaudio/best' \
  URL
```

String filters support `=`, `^=`, `$=`, `*=`, `~=` and their negated forms;
numeric filters support `<`, `<=`, `>`, `>=`, `=` and `!=`. Append `?` to an
operator to include formats whose field is missing. `~=` uses bounded
Python-compatible regular-expression search semantics:

```sh
./bin/ytdlp-go -f 'best[format_id~="(?i)source|original"]' URL
```

`-S`/`--format-sort` is repeatable. Each occurrence supplies one pinned
FormatSorter field, alias, or limit:

```sh
./bin/ytdlp-go \
  -S 'res:1080' \
  -S 'fps' \
  --prefer-free-formats \
  URL
```

`--allow-unplayable-formats` permits DRM-marked formats to participate in
selection; it does not decrypt DRM. Selector and regular-expression inputs are
resource-bounded and fail explicitly when a limit is exceeded. Current product
execution supports bounded arbitrary N-track merges per output; broader
multi-output lifecycle handling remains in the active parity plan.

Use `--video-multistreams` and `--audio-multistreams` to retain additional
same-kind merge tracks, or their `--no-*` forms to clear inherited settings.
`--format-sort-force` (`--S-force`) and `--format-sort-reset` control ordered
sort preferences. `--merge-output-format mp4/mkv` supplies merge-container
preferences. `--check-formats`, `--check-all-formats`, and
`--no-check-formats` control bounded availability probing; the default Auto
mode checks only formats marked DRM or `__needs_testing`. `-f -` prompts for a
selector after extraction; prompts are written to stderr and cannot be used
with `--progress-json`.

See [format-selector behavior and limits](docs/FORMAT_SELECTOR_PARITY.md) and
the [active implementation plan](docs/FORMAT_SELECTOR_PARITY_IMPLEMENTATION_PLAN.md).

## Common workflows

### Metadata and printing

```sh
# One quiet JSON object per video (simulates by default)
./bin/ytdlp-go -j URL

# One JSON object for the complete result, including playlists
./bin/ytdlp-go -J URL

# Print selected fields
./bin/ytdlp-go -O "title,id,duration" URL

# Append a lifecycle template to a confined file
./bin/ytdlp-go \
  --print-to-file "after_move:%(filepath)s" \
  "%(uploader)s-downloads.txt" \
  URL

# Write metadata sidecars and a platform-native internet shortcut
./bin/ytdlp-go \
  --skip-download \
  --write-info-json \
  --write-description \
  --write-thumbnail \
  --convert-thumbnails "webp>png/jpg" \
  --embed-thumbnail \
  --write-link \
  --paths "subtitle:captions" \
  --paths "thumbnail:images" \
  --paths "infojson:metadata" \
  --output "default:media/%(title)s.%(ext)s" \
  --output "subtitle:%(title)s.%(ext)s" \
  --output "thumbnail:%(title)s.%(ext)s" \
  --output "infojson:%(title)s.%(ext)s" \
  URL
```

`--simulate` creates no media, subtitle, archive, or postprocessor artifacts.
`--skip-download` skips media transfer but still permits explicitly requested
metadata, subtitle, and thumbnail sidecars. Add `--no-simulate` to listing commands when
you also want the normal download. Repeat `--output` to give `default`,
`subtitle`, `thumbnail`, `description`, `infojson`, `link`,
`pl_description`, `pl_infojson`, or `pl_thumbnail` its own confined template; unspecified types fall back to
`default`.

`--convert-thumbnails FORMAT` converts written images to `jpg`, `png`, or
`webp`. Conditional mappings such as `webp>png/jpg` use the first matching
rule and fall back to the first unconditional format; `none` disables
conversion.
`--embed-thumbnail` downloads the best image when needed and embeds it as
cover art in MP3, MP4-family, Matroska, FLAC, Ogg, or Opus media. A merged
WebM audio/video pair is published as Matroska when cover art is requested.
Add `--write-thumbnail` when the standalone image should be retained too.

Repeat `--paths [TYPES:]PATH` to place those produced artifact types in
separate directories beneath `home`. Supported types are `home`, `subtitle`,
`thumbnail`, `description`, `infojson`, `link`, `pl_description`,
`pl_infojson`, and `pl_thumbnail`. Untyped paths select `home`; later values
for the same type win.

### Playlists

```sh
# Select sparse items and a stepped range
./bin/ytdlp-go -I "1,3,8:20:2" URL

# Process an inclusive range in reverse
./bin/ytdlp-go \
  --playlist-start 3 \
  --playlist-end 8 \
  --playlist-reverse \
  URL

# List entries without recursively extracting them
./bin/ytdlp-go --flat-playlist -J URL
```

### Subtitles and captions

```sh
# List available manual and automatic tracks
./bin/ytdlp-go --list-subs URL

# Write selected tracks without downloading media
./bin/ytdlp-go \
  --skip-download \
  --write-subs \
  --write-auto-subs \
  --sub-langs "en.*,ja" \
  --sub-format "srt/vtt/best" \
  URL

# Convert written sidecars
./bin/ytdlp-go --write-subs --convert-subs vtt URL

# Embed selected text tracks in a supported container
./bin/ytdlp-go --embed-subs --sub-langs "en,fr" URL
```

Add `--write-subs` when embedded subtitle sidecars should also be retained.

### SponsorBlock

SponsorBlock is opt-in for YouTube watch URLs. It can expose normalized
chapters, mark selected ranges, remove selected ranges with ffmpeg, or combine
marking and removal on the post-cut timeline.

```sh
# Mark ranges as chapters while inspecting metadata
./bin/ytdlp-go \
  --sponsorblock-mark "all,-preview" \
  --skip-download \
  --print-json \
  "https://www.youtube.com/watch?v=VIDEO_ID"

# Remove selected ranges from downloaded media
./bin/ytdlp-go \
  --sponsorblock-remove "sponsor,selfpromo" \
  --force-keyframes-at-cuts \
  "https://www.youtube.com/watch?v=VIDEO_ID"

# Remove ordinary chapters by title and a manual time range
./bin/ytdlp-go \
  --remove-chapters "(?i)^(intro|credits)$" \
  --remove-chapters "*1:30-2:00" \
  URL
```

`--sponsorblock-api URL` selects a compatible mirror.
`--sponsorblock-chapter-title TEMPLATE` customizes marked chapter titles with
the bounded shared output-template syntax; the pinned default is
`[SponsorBlock]: %(category_names)l`.
`--no-sponsorblock` disables inherited mark and remove options without clearing
the API base or stored chapter-title template. Categories are repeatable,
comma-separated, and support
`all`/`default` plus `-category` exclusions.
`--remove-chapters` is repeatable: regular expressions use bounded
Python-compatible title-search semantics, while values beginning with `*` contain comma-separated
`START-END` ranges with open bounds. `--no-remove-chapters` clears inherited
rules. Ordinary, manual, and SponsorBlock cuts share one transactional ffmpeg
and subtitle-retiming pass.

### YouTube comments and live-from-start

```sh
# Bounded public or signed-in WEB comments
./bin/ytdlp-go \
  --write-comments \
  --youtube-max-comments 100 \
  --youtube-comment-sort top \
  --skip-download \
  --print-json \
  "https://www.youtube.com/watch?v=VIDEO_ID"

# Reconstruct a supported adaptive live stream from its retained beginning
./bin/ytdlp-go \
  --live-from-start \
  "https://www.youtube.com/watch?v=VIDEO_ID"
```

Comment retrieval includes bounded root/reply continuations and nested
subthreads. It does not claim arbitrary authenticated Innertube clients.
Current-edge live downloading remains the default.

### Transfer controls

```sh
./bin/ytdlp-go \
  --limit-rate 5M \
  --retries 3 \
  --concurrent-fragments 8 \
  --per-host-fragments 4 \
  --download-archive archive.txt \
  URL
```

Use `--progress-json` for newline-delimited structured progress events or
`--telemetry-json --skip-download` for one privacy-safe aggregate operation
snapshot.

## Extractor coverage

The representative catalog includes YouTube, Vimeo, Twitch, SoundCloud, Apple
Podcasts, Streamable, PeerTube, Internet Archive, TikTok, Bluesky, Imgur,
Flickr, Dailymotion, Reddit, Twitter/X, Bandcamp, Mixcloud, Rumble, Bilibili,
Instagram, Kick, Niconico, BBC iPlayer, ARD Mediathek, NRK, SVT Play, and
deterministic authenticated coverage.

Shared-backend families add Brightcove, Kaltura, JW Platform, Wistia,
SproutVideo, Cloudflare Stream, Arc Publishing, Anvato, ThePlatform, podcast
providers, Dacast, Panopto, and site-specific adapters.

YouTube coverage includes bounded watch/embed/Shorts extraction, playlists,
channels and handles, public and authorized membership/custom tabs, channel
search, hashtag playlists, general and Music search/browse, captions,
comments, live aliases, post-live reconstruction, live-from-start, multiple
native player clients, and an explicit PO-token provider boundary.

Twitch coverage includes the seven reviewed public classes from the pinned
reference: anonymous live/rerun channels, VODs, direct clips and collections,
and bounded channel videos, clips, and collections playlists. The registered
keys are `twitch_stream`, `twitch_vod`, `twitch_clips`, `twitch_collection`,
`twitch_videos`, `twitch_videos_clips`, and `twitch_videos_collections`.
Credential-isolated no-redirect GraphQL/Usher/HLS/direct-clip-media/thumbnail handling is
fixture-backed; login/private, subscriber-only or restricted entitlement,
mature/geo-restricted, chat/comments, and unsupported credential flows remain
deferred.

SoundCloud coverage includes tracks, sets, bare profiles, the pinned public
`tracks`, `albums`, `sets`, `reposts`, `likes`, `spotlight`, and `comments`
profile tabs, tokenized private-set hydration, stations, related resources,
bounded public track search, and the full pinned artwork/avatar thumbnail
matrix.

ARD Mediathek coverage has a dedicated `ard_mediathek_collection` key for
bounded public `sendung`, `serie`, and `sammlung` pages, including season,
original-version, and audio-description variants. Collection children re-enter
the registered ARD item extractor; authentication, geo-only, DRM, and
production-service interoperability beyond the deterministic corpus are not
claimed.

Microsoft public-media coverage includes six exact fixture-backed keys for
videoplayer embeds, Medius embeds, Learn shows/events and child pages, and
Build sessions, with credential-isolated native ISM/HLS/DASH/direct downloads
and validated transparent Medius re-entry. Authenticated or DRM playback and
live-production interoperability are not claimed.

TED public-media coverage includes four exact fixture-backed keys for talks,
series, playlists, and `embed`/`embed-ssl` transparent routes. Anonymous public
Next metadata, direct/HLS/audio formats, HLS subtitles, thumbnails, chapters,
strict TED-origin isolation, season filtering, and playlist child reuse are
covered. Login/private, unavailable, DRM, geo, and arbitrary external media
handoffs are not claimed.

The full URL matrix and per-extractor boundaries live in
[Supported sites](docs/SUPPORTED_SITES.md). A listed extractor means its
checked-in corpus is supported—not every page, account state, region,
historical API, or future service response.

## Media and post-processing

| Component | Current native behavior |
| --- | --- |
| Direct HTTP(S) | Bounded retries, resume, throttling, size limits, and safe publication |
| HLS | VOD/live polling, byte ranges, maps, AES-128, low-latency parts, delta skips, SCTE-35/DATERANGE and cue-based ad suppression |
| DASH | SegmentTemplate, SegmentList, timelines, SegmentBase/SIDX, hierarchical indexes, dynamic polling, and bounded compatible multi-period composition |
| ISM | Smooth Streaming addressing and fragment download |
| ffmpeg/ffprobe | Separate-track merge, audio extraction, remux, conversion, metadata, chapters, subtitles, concat, fixups, and SponsorBlock cuts |

Native paths propagate selected format headers. DRM decryption is not
implemented. `--allow-unplayable-formats` only allows DRM-marked formats to
participate in selection.

The retained finite-VOD SABR/UMP implementation is experimental and outside
the compatibility target. Expanding live, post-live, stream-protection, or
full-client SABR parity is not a project goal or roadmap requirement.

## Architecture

```text
URL
 └─► native extractor registry
      └─► metadata, formats, subtitles, or lazy playlist entries
           └─► bounded compatibility and selection rules
                └─► direct / HLS / DASH / ISM transfer
                     └─► optional typed ffmpeg operations
                          └─► confined artifacts + structured events
```

The CLI and public Go API use the same operation pipeline. Network state,
cookies, credential access, challenge solving, cancellation, output policy,
archive state, and nested playlist extraction stay within that operation.

## Runtime and Docker

The main downloader does not require Python.

- `ffmpeg` is required for adaptive track merging, requested post-processing,
  and automatic clear-key HLS SAMPLE-AES delegation. `ffprobe` is required
  only for operations that inspect media streams.
- `ytdlp-js-helper` is required only for supported YouTube flows that need a
  JavaScript challenge. It is a separate pure-Go executable with bounded IPC.
- Browser credential services are consulted only after explicit cookie-import
  selection.
- External downloaders are optional, explicitly selected, shell-free, and
  reject interpreter/script trampolines.

Build the strict Python-free scratch image:

```sh
docker build -f .github/python-free.Dockerfile -t ytdlp-go .
docker run --rm ytdlp-go --version
```

Build the practical non-root image with ffmpeg and ffprobe:

```sh
docker build -f .github/runtime.Dockerfile -t ytdlp-go-runtime .
docker volume create ytdlp-downloads
docker run --rm --read-only --tmpfs /tmp \
  -v ytdlp-downloads:/downloads \
  ytdlp-go-runtime URL
```

See [Python-free runtime image](docs/PYTHON_FREE_RUNTIME_IMAGE.md) for the
verification boundary.

## Configuration, cookies, and authentication

The CLI reads bounded yt-dlp-style option files with comments, quoting,
encoding detection, aliases, and nested explicit locations. Command-line
values have highest precedence.

```text
# yt-dlp.conf
--output-dir "downloads"
--output "%(title)s.%(ext)s"
--retries 3
--concurrent-fragments 4
```

```sh
# Load an explicit configuration file
./bin/ytdlp-go --config-location /path/to/yt-dlp.conf URL

# Skip discovered configuration
./bin/ytdlp-go --ignore-config URL

# Import a Netscape cookie file
./bin/ytdlp-go --cookies cookies.txt URL

# Import an explicitly selected browser profile
./bin/ytdlp-go --cookies-from-browser chrome:Default URL

# Use an explicitly selected native netrc file
./bin/ytdlp-go --netrc --netrc-location /path/to/.netrc URL

# Select a supported browser network profile
./bin/ytdlp-go --impersonate firefox-120 URL
```

Browser import supports Firefox, Safari on macOS, and declared
Chromium-family sources with platform-specific limitations. Cookie,
authorization, signed-query, netrc, and browser-secret values are excluded
from public diagnostics and events.

On macOS, Chrome import may trigger the normal Keychain prompt. Never attach
real cookies or tokens to a public issue. See [cookie import
documentation](docs/CHROMIUM_COOKIE_IMPORT.md), [native netrc
evidence](docs/P3_NETRC_EVIDENCE.md), and the [security policy](SECURITY.md).

## YouTube JavaScript helper

The helper must be placed beside `ytdlp-go` or selected explicitly. `PATH` is
not searched for executable helper code.

```sh
./bin/ytdlp-go --js-helper ./bin/ytdlp-js-helper URL
```

The process uses bounded messages, cancellation, timeouts, and a scrubbed
credential environment. It is started only when the selected flow requires a
challenge. See the [helper protocol](docs/JAVASCRIPT_HELPER_PROTOCOL.md).

## Embedding in Go

The supported package contract is
`github.com/ytdlp-go/ytdlp/pkg/ytdlp`. The v1alpha1 API exposes
context-aware requests, events, categorized errors, normalized metadata, lazy
playlists, and produced artifacts.

> [!IMPORTANT]
> The repository is hosted at `github.com/tejasa97/youtube_dlp`, while
> `go.mod` declares `github.com/ytdlp-go/ytdlp`. Until that identity is
> reconciled, normal `go get` installation is not advertised. Build inside a
> clone or use a temporary local `replace` directive for evaluation.

```go
client := ytdlp.NewClient(
    ytdlp.WithEventHandler(func(ctx context.Context, event ytdlp.Event) error {
        log.Printf("%s: %d/%d", event.Kind, event.Bytes, event.Total)
        return nil
    }),
)
defer client.Close()

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
```

See [Embedding ytdlp-go](docs/EMBEDDING.md) and the [API compatibility
policy](docs/P2_API_COMPATIBILITY_POLICY.md).

## Plugins, packs, and updates

Plugins do not automatically claim URLs and are not discovered from arbitrary
`PATH` entries. Production use requires trusted package verification, explicit
permission approval, ABI negotiation, and `PluginID` selection.

Supported extension boundaries are versioned length-prefixed native JSON RPC
and a constrained extractor-only WASM ABI. Python and interpreter-backed
plugins are rejected. Signed packs, offline catalogs, revocation, atomic
installation, rollback, and updater health checks have deterministic evidence;
the repository does not choose production signers or publishing credentials.

Start with [Plugin ABI v1](docs/P2_PLUGIN_ABI_V1.md), [signed
packs](docs/P2_SIGNED_PACKS.md), and [updater and
releases](docs/P2_UPDATER_RELEASES.md).

## Python-free by design

The behavioral reference is pinned at
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`. It is read-only and
used solely to derive attributable expectations. Production code, builds,
tests, releases, plugins, and runtime operation do not read or execute the
reference checkout.

A capability becomes compatible only when its manifest entry links passing
automated evidence. Unknown behavior remains a documented deviation or a
categorized unsupported result. There is no silent Python fallback.

## Roadmap

Active work follows practical parity gaps rather than blanket claims:

1. deepen high-value extractors and reusable playlist behavior;
2. close remaining format-selector, match-filter, template, and metadata
   language gaps;
3. close attributable HLS, DASH, downloader, and post-processing gaps;
4. replay relevant upstream changes and keep conformance claims current; and
5. choose a permanent repository identity and reconcile the Go module path
   before advertising `go get`.

SABR expansion is not part of the parity goal. Gate G3 deployment evidence is
backburnered and is not an active implementation track. Historical plans and
exit reviews remain available as engineering evidence, not as the current
near-term roadmap.

The authoritative backlog is the set of known deviations in the
[capability manifest](conformance/parity_manifest.yaml). Extractor breadth is
tracked separately against every registered class in the pinned reference by
the [extractor master checklist](docs/EXTRACTOR_MASTER_CHECKLIST.md).

## Development and verification

GitHub Actions is temporarily disabled. Contributors must run and report the
relevant local checks:

```sh
test -z "$(gofmt -l .)"
go mod tidy -diff
go vet ./...
go test ./...
go test -race ./...
go run ./cmd/paritycheck
```

New compatibility claims require deterministic success and failure evidence,
cancellation and resource-bound tests where applicable, security review, and
fixture provenance. Real credentials, private captures, and production signed
URLs do not belong in fixtures.

Specialist repository tools include:

| Command | Purpose |
| --- | --- |
| `cmd/paritycheck` | Validate capability evidence and fallback claims |
| `cmd/deltareplay` | Classify attributable upstream changes |
| `cmd/jscheck` | Verify the isolated JavaScript/EJS boundary |
| `cmd/ytdlp-pack` | Build and inspect signed plugin packs |
| `cmd/ytdlp-release` | Assemble reproducible alpha archives |
| `cmd/ytdlp-update` | Exercise signed update and rollback transactions |

See [Contributing](CONTRIBUTING.md), the [fixture
policy](docs/FIXTURE_POLICY.md), and the [documentation index](docs/README.md).

## Security, support, and legal use

- Read [Support](SUPPORT.md) before filing a site or feature issue.
- Report vulnerabilities privately through [Security](SECURITY.md).
- Remove cookies, tokens, signed URLs, private media details, and personal
  information from reproductions.
- Use the software only for content you are authorized to access and in
  accordance with applicable law and service terms.
- DRM circumvention is not provided.

Project code is licensed under the [Apache License 2.0](LICENSE). Embedded
assets and dependencies retain their own licenses; see
[Third-party notices](THIRD_PARTY_NOTICES.md).
