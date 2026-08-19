# Troubleshooting

Start by recording:

- the exact application or CLI version/revision;
- operating system and architecture;
- whether you used VidStow, the CLI, or the Go API;
- the smallest public URL that reproduces the problem;
- whether FFmpeg, FFprobe, browser cookies, or the JavaScript helper were used;
- expected and actual behavior; and
- the complete plain-text diagnostic message.

Remove cookies, tokens, authorization headers, signed media URLs, browser
profiles, private-media titles, usernames, and sensitive local paths before
sharing diagnostics.

## A URL is rejected by VidStow

VidStow currently accepts public, on-demand YouTube video, Short, and playlist
URLs. Channels, search pages, live streams, authenticated videos, and other
sites are outside its current scope. VidStow is independently versioned; check
the [VidStow repository](https://github.com/vidstow/vidstow) for its current
product documentation and issue tracker.

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

## YouTube media returns HTTP 403

Adaptive googlevideo 403s are often request-rate limits or expired signed URLs,
not a missing JavaScript helper. Current builds refresh the source, rotate off
rejected URL/client pairs, and resume only when the refreshed server accepts
the saved range.

Retry the operation once on the same revision. If it still fails:

1. Confirm `ytdlp-js-helper` is the version built from the same revision and
   sits beside the main executable, or is selected explicitly.
2. Report the public video URL, revision, platform, and sanitized HTTP status.
   Do not include signed query parameters.

Challenge timeouts remain the helper path above. Do not treat a 403 as “helper
missing” by default.

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

VidStow users can select an explicit FFmpeg location in Settings. CLI users
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

## VidStow application or local-state problems

VidStow owns its platform requirements, Wails/WebKit guidance, settings and
history format, and application packaging. Use the
[VidStow issue tracker](https://github.com/vidstow/vidstow/issues) for those
problems. Engine or provider defects reproducible through this repository's CLI
or public Go packages belong here.

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
