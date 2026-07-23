# YouTube subtitle conversion evidence

Behavioral reference inspected read-only: `yt-dlp` commit
`aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`, specifically
`yt_dlp/options.py` (`--convert-subs`, `--convert-sub`, and
`--convert-subtitles`) and `FFmpegSubtitlesConvertorPP` in
`yt_dlp/postprocessor/ffmpeg.py`.

This CLI accepts the same three option spellings and treats `none` as disabled.
It intentionally supports the bounded sidecar formats `srt`, `ass`, and `vtt`.
`vtt` maps to ffmpeg's `webvtt` muxer. `mov_text` is excluded: it is a codec
whose conventional output is a media container rather than a standalone
subtitle sidecar, so this implementation has no safe, unambiguous extension
contract for it.

Conversion is run only after the client has already produced subtitle
artifacts; it never enables subtitle writing or media downloading. Results are
walked recursively for playlist entries. Sources must be regular, non-symlink
files below the output root. Destination replacement is atomic through the
typed internal ffmpeg toolset; source removal follows only success. Existing
destinations obey `--force-overwrites`.

`--list-subs` uses its existing simulation behavior, thus produces no artifacts
to convert. `--skip-download` remains compatible with explicit subtitle writes,
and conversion only sees those sidecars. After each successful conversion,
matching `requested_subtitles[*].filepath` and `ext` InfoJSON fields are
updated so `--print-json` names the converted sidecar rather than the removed
source.
