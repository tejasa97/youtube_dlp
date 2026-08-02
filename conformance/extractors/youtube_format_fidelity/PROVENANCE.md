# YouTube format-fidelity fixture

This corpus is a synthetic, offline fixture for single-video format
normalization (format-fidelity wave). It exercises the fields the pinned
reference's `_extract_formats_and_subtitles` derives per format: codec
classification with `none` defaults and dynamic range, format notes and
quality ranks, source preferences, DRC and super-resolution suffixes, audio
track identity with original/default/descriptive language preferences, DASH
container markers, sample rates and channel counts, damaged-format
deprioritization, filesize approximation, fps=1 omission, and same-itag
format-ID collisions across audio tracks. Storyboards, DRM playback, new
downloader protocols, and SABR expansion are explicitly out of scope.

The format assembly follows the pinned reference
`yt-dlp/yt-dlp@aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8`:

- quality ladder, itag/quality mapping, and the 3gp (17) tiny override:
  `yt_dlp/extractor/youtube/_video.py:3366-3384`;
- language and preference from `audioTrack` (original 10, default 5,
  descriptive -10 with `-desc` suffix, plain -1):
  `yt_dlp/extractor/youtube/_video.py:3247-3265`;
- the format dict: asr, format_id (`-drc`/`-sr` suffixes), format_note,
  source_preference, fps > 1, audio_channels, quality (rank minus 0.5 for
  DRC), has_drm, tbr (averageBitrate first), filesize_approx, language and
  language_preference, preference (`-10` damaged, `-2` for itag 17):
  `yt_dlp/extractor/youtube/_video.py:3435-3481`;
- `parse_codecs` (families, `none` defaults, DV/HDR10 detection, two-unknown
  raw fallback): `yt_dlp/utils/_utils.py:3057-3093`;
- `filesize_from_tbr`: `yt_dlp/utils/_utils.py:5669-5675`;
- the `_dash` container marker for single-stream formats:
  `yt_dlp/extractor/youtube/_video.py:3483-3486`;
- super-resolution detection via the `xtags=sr=1` marker:
  `yt_dlp/extractor/youtube/_video.py:3240-3243`;
- same-itag audio tracks survive with colliding format IDs, distinguished by
  language (the stream identity is `(itag, audioTrack.id, isDrc)`):
  `yt_dlp/extractor/youtube/_video.py:3388-3390` and 3507-3510.

The `resolution` field is intentionally NOT emitted by the extractor: the
pinned reference derives it at the product layer
(`YoutubeDL.py:1283-1284`), and the Go product already does the same.

All identifiers, URLs, codecs, and track names are artificial; no production
response, cookie, token, signed URL, or account data is retained. The video ID
`fixture0003` is reserved for this surface. The expected document is checked
in so field presence, ordering, and deterministic format normalization remain
reviewable; the watch page contains plain-URL formats, so extraction performs
exactly one transport read.
