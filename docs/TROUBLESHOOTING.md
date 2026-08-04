# Troubleshooting

Start by recording:

- the exact application or CLI version/revision;
- operating system and architecture;
- whether you used Desktop, CLI, or the Go API;
- the smallest public URL that reproduces the problem;
- whether FFmpeg, FFprobe, browser cookies, or the JavaScript helper were used;
- expected and actual behavior; and
- the complete plain-text diagnostic message.

Remove cookies, tokens, authorization headers, signed media URLs, browser
profiles, private-media titles, usernames, and sensitive local paths before
sharing diagnostics.

## A URL is rejected by Desktop

Desktop V0 accepts only one public, on-demand YouTube video. Playlists,
channels, search pages, Shorts, live streams, authenticated videos, and other
sites are outside its current scope.

If the engine supports the input, try the CLI instead and consult
[Supported sites](SUPPORTED_SITES.md).

## Video analysis failed

Analysis can fail when:

- the video is private, unavailable, age/account restricted, or regional;
- YouTube changed the page, API response, or player behavior;
- the network is unavailable;
- the JavaScript helper is missing; or
- the URL shape is not supported by the selected interface.

Retry once after confirming the URL works in a normal browser. Repeated generic
failures should include the public URL and diagnostic output in a bug report.

## “YouTube challenge timed out — retry”

Some YouTube media URLs require the helper to process the current player
JavaScript before media transfer begins. The preprocessing phase has a bounded
execution time. A particular player can exceed it even while other videos work.

Retry the operation once. If it repeats:

1. Confirm `ytdlp-js-helper` is the version built from the same revision as the
   main application.
2. Confirm it is a regular executable beside the main executable, or selected
   explicitly by the CLI.
3. Report the public video URL, revision, platform, and exact error.

The failure is not necessarily caused by the selected resolution or the queue.

## JavaScript helper is missing

For a source build, compile both programs:

```sh
CGO_ENABLED=0 go build -trimpath -o bin/ytdlp-go ./cmd/ytdlp-go
CGO_ENABLED=0 go build -trimpath -o bin/ytdlp-js-helper ./cmd/ytdlp-js-helper
```

Keep them in the same directory, or pass the helper explicitly:

```sh
./bin/ytdlp-go --js-helper ./bin/ytdlp-js-helper URL
```

The downloader intentionally does not search `PATH` for executable helper code.

## FFmpeg is missing

Check the toolchain:

```sh
ffmpeg -version
ffprobe -version
```

Desktop users can select an explicit FFmpeg location in Settings. CLI users
should ensure the toolchain is available through the configured media/filesystem
environment.

Many high-resolution downloads use separate video and audio tracks. Without
FFmpeg, those tracks cannot be merged into one output file.

## Download completed but no combined file appeared

Check for:

- missing or incompatible FFmpeg/FFprobe;
- insufficient disk space;
- a destination file that already exists;
- output-folder permission failures;
- unsupported stream/container combinations; or
- cancellation during finalization.

Preserve the full post-processing diagnostic. Do not delete temporary files
until the report is understood if they do not contain sensitive material.

## Output folder cannot be used

Choose a folder owned by the current user and verify that it exists or can be
created. Avoid system directories, read-only volumes, and locations controlled
by another application.

On macOS, grant the application access when the normal folder picker or privacy
prompt requests it. On managed systems, organization policy may prevent access
even when ordinary file permissions appear correct.

## A download is slow or repeatedly retries

Service throttling, network loss, proxies, VPNs, and temporary CDN failures can
affect transfers. Try without a proxy/VPN where appropriate and reduce fragment
concurrency if the connection or service does not tolerate parallel requests.

CLI users can tune `--limit-rate`, `--retries`, `--concurrent-fragments`, and
`--per-host-fragments`. Avoid treating one transient live URL as a stable test.

## Authentication or browser-cookie import failed

Browser import is platform-specific and may trigger normal credential-store
prompts. Confirm the browser and profile were explicitly selected and are
supported on the current operating system.

Never export or upload a complete browser profile. See
[Browser cookie import](CHROMIUM_COOKIE_IMPORT.md) and report only sanitized
diagnostics.

## Desktop will not launch

Development builds are not signed public releases and may be blocked by normal
platform protections. Verify that the bundle was built for the current
operating system and architecture and that its helper binaries are present.

Future public packages will document supported OS versions, signing, and
verification. Do not bypass organizational security policy to run an unknown
artifact.

## Linux WebKitGTK problems

Wails uses the platform webview. Linux distributions ship different WebKitGTK
ABIs and package names. A binary built on one distribution may not run on an
unverified distribution without the expected GTK/WebKit runtime.

Until supported Linux release targets are published, use `wails doctor` and
the Wails prerequisites for the host distribution when building from source.

## Corrupt Desktop settings or history

Desktop stores settings and history in `state.json` under the user's config
directory. If the file cannot be decoded, the application preserves it with a
`.bak` suffix and creates default state.

Removing `state.json` resets settings and history but does not delete downloaded
media. Preserve a copy first if it is needed for a bug report, and sanitize all
local paths and history details before sharing it.

## Before opening an issue

1. Confirm the behavior is claimed for the selected interface and URL family.
2. Reproduce on the current supported revision or release channel.
3. Search existing issues.
4. Reduce the report to one independently actionable problem.
5. Remove secrets and personal information.

Use [Support](../SUPPORT.md) for issue routing. Suspected credential disclosure,
path escape, arbitrary code execution, update/signature bypass, or another
security boundary failure must be reported privately under
[Security](../SECURITY.md).
